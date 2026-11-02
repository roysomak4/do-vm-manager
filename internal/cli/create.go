package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/roysomak4/do-vm-manager/internal/cloudinit"
	"github.com/roysomak4/do-vm-manager/internal/engine"
	"github.com/roysomak4/do-vm-manager/internal/output"
	"github.com/roysomak4/do-vm-manager/internal/spec"
	"github.com/roysomak4/do-vm-manager/internal/tui"
)

type createFlags struct {
	dryRun       bool
	yes          bool
	rollback     bool
	waitTimeout  time.Duration
	outputDir    string
	loginUser    string
	passwordFile string
}

func newCreateCmd(g *globalFlags) *cobra.Command {
	f := &createFlags{}
	cmd := &cobra.Command{
		Use:   "create SPEC",
		Short: "Create the droplets in a cluster spec that do not exist yet",
		Long: "Create validates the spec, compares it with the droplets in your account,\n" +
			"creates the missing ones and waits until each is active with an IP address.\n\n" +
			"Every droplet is tagged dovm:<name>. Droplets that already carry that tag are\n" +
			"left as they are, so re-running create after a failure or interruption only\n" +
			"creates what is still missing.\n\n" +
			"With --login-user, new droplets get an unprivileged user who can log in over\n" +
			"SSH with a password, shared by every droplet; root stays SSH-key only. The\n" +
			"password is read from --login-password-file, then $" + loginPasswordEnv + ", then\n" +
			"a prompt. Droplets that already exist are not changed.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCreate(cmd, g, f, args[0])
		},
	}
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "show what would be created without creating anything")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "create without asking for confirmation")
	cmd.Flags().BoolVar(&f.rollback, "rollback-on-failure", false, "delete the droplets this run created if any host fails or the run is interrupted")
	cmd.Flags().DurationVar(&f.waitTimeout, "wait-timeout", 10*time.Minute, "how long to wait for droplets to become active")
	cmd.Flags().StringVar(&f.outputDir, "output-dir", "output", "directory for inventory files")
	cmd.Flags().StringVar(&f.loginUser, "login-user", "", "add this user, who logs in over SSH with a password, to new droplets")
	cmd.Flags().StringVar(&f.passwordFile, "login-password-file", "", "file holding the --login-user password")
	return cmd
}

func runCreate(cmd *cobra.Command, g *globalFlags, f *createFlags, path string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	s, err := spec.Load(path)
	if err != nil {
		return validationFailed(err)
	}
	if f.loginUser != "" {
		if err := cloudinit.ValidateUsername(f.loginUser); err != nil {
			return err
		}
		if len(s.SSHKeys) == 0 {
			return validationFailed(errors.New("--login-user needs ssh_keys in the spec, so admins can log in as root"))
		}
	} else if f.passwordFile != "" {
		return errors.New("--login-password-file needs --login-user")
	}
	client, err := g.client()
	if err != nil {
		return err
	}
	cat, err := client.Catalog(ctx, s.Image, s.Project != "")
	if err != nil {
		return fmt.Errorf("fetch account data: %w", err)
	}
	est, err := s.Check(cat)
	if err != nil {
		return validationFailed(err)
	}
	plan, err := engine.BuildPlan(ctx, client, s)
	if err != nil {
		return err
	}
	if s.Project != "" {
		project, _ := spec.FindProject(cat.Projects, s.Project) // found, or Check would have failed
		if err := plan.UseProject(ctx, client, project); err != nil {
			return err
		}
	}

	printPlan(out, plan)
	if err := plan.Err(); err != nil {
		lines := strings.Split(err.Error(), "\n")
		return fmt.Errorf("cannot proceed, %d host(s) conflict with existing droplets:\n  - %s", len(lines), strings.Join(lines, "\n  - "))
	}
	fmt.Fprintf(out, "\n%d to create, %d already exist", len(plan.Create), len(plan.Existing))
	if plan.Project != nil {
		fmt.Fprintf(out, ", %d to move into project %s", len(plan.ToMove()), projectLabel(plan.Project))
	}
	fmt.Fprintf(out, ". Cluster total: $%.2f/month.\n", est.MonthlyPrice)
	if f.loginUser != "" {
		fmt.Fprintf(out, "New droplets get login user %s with a password; root stays SSH-key only.\n", f.loginUser)
	}
	if f.dryRun {
		return nil
	}

	opts := engine.Options{WaitTimeout: f.waitTimeout, Rollback: f.rollback, PollInterval: pollInterval}
	if f.loginUser != "" && len(plan.Create) > 0 {
		password, err := loginPassword(cmd, f)
		if err != nil {
			return err
		}
		if opts.UserData, err = cloudinit.LoginUser(f.loginUser, password); err != nil {
			return err
		}
	}
	if len(plan.Create) > 0 && !f.yes {
		answer, err := prompt(cmd, fmt.Sprintf("Create %d droplet(s)? [y/N]", len(plan.Create)))
		if errors.Is(err, errNotInteractive) {
			return errors.New("refusing to create droplets without confirmation; pass --yes when running non-interactively")
		}
		if err != nil {
			return err
		}
		if a := strings.ToLower(answer); a != "y" && a != "yes" {
			return errors.New("aborted")
		}
	}

	var res *engine.Result
	if g.useTUI(cmd) {
		cancelHint := "droplets already created are kept; run create again to finish"
		if f.rollback {
			cancelHint = "rolling back droplets created by this run"
		}
		err = tui.Run(ctx, tui.Config{
			Title:      createTitle(plan),
			Rows:       createRows(plan),
			CancelHint: cancelHint,
		}, func(ctx context.Context, report engine.Reporter) []tui.Row {
			opts.Reporter = report
			res, _ = engine.Run(ctx, client, plan, opts)
			return resultRows(res)
		})
		if err != nil {
			return err
		}
		printFailures(out, res)
	} else {
		opts.Reporter = newTextReporter(cmd.ErrOrStderr())
		if res, err = engine.Run(ctx, client, plan, opts); err != nil {
			return err
		}
		fmt.Fprintln(out)
		printResult(out, res)
	}
	if res.ProjectErr != nil {
		fmt.Fprintf(out, "\nwarning: %v\nThe droplets work; run create again to retry the move.\n", res.ProjectErr)
	}

	if !res.RolledBack {
		n, err := output.Write(f.outputDir, res)
		if err != nil {
			return fmt.Errorf("write output files: %w", err)
		}
		if n > 0 {
			fmt.Fprintf(out, "\nWrote %d host(s) to %s/{%s,%s,%s,%s}\n", n, f.outputDir,
				output.VMsJSON, output.IPHostFile, output.IPsFile, output.Inventory)
		}
	}

	switch {
	case res.OK():
		return nil
	case res.RolledBack:
		return errors.New("create failed; droplets created by this run were rolled back")
	case res.Canceled():
		return errors.New("interrupted; run create again to finish")
	default:
		return errors.New("some hosts are not ready; run create again to retry them")
	}
}

// createRows is the dashboard's starting state for a plan.
func createRows(p *engine.Plan) []tui.Row {
	existing := map[string]engine.Existing{}
	for _, e := range p.Existing {
		existing[e.Host.Name] = e
	}
	var rows []tui.Row
	for _, h := range p.Spec.Hosts() {
		r := tui.Row{Name: h.Name, Role: h.Role, State: tui.Queued}
		if e, ok := existing[h.Name]; ok {
			r.DropletID = e.Droplet.ID
			r.IP, _ = e.Droplet.PublicIPv4()
			r.Detail = strings.Join(e.Drift, "; ")
			r.State = tui.Existing
			if e.Droplet.Status == "new" {
				r.State = tui.Provisioning
			}
		}
		rows = append(rows, r)
	}
	return rows
}

// resultRows converts the engine's final result to dashboard rows.
func resultRows(res *engine.Result) []tui.Row {
	states := map[engine.Status]tui.State{
		engine.StatusPending:    tui.Queued,
		engine.StatusReady:      tui.Ready,
		engine.StatusFailed:     tui.Failed,
		engine.StatusTimedOut:   tui.TimedOut,
		engine.StatusCanceled:   tui.Canceled,
		engine.StatusRolledBack: tui.RolledBack,
	}
	var rows []tui.Row
	for _, h := range res.Hosts {
		r := tui.Row{Name: h.Host.Name, Role: h.Host.Role, State: states[h.Status]}
		if h.Status == engine.StatusReady && !h.Created {
			r.State = tui.Existing
		}
		if d := h.Droplet; d != nil {
			r.DropletID = d.ID
			r.IP, _ = d.PublicIPv4()
		}
		if h.Err != nil {
			r.Detail = h.Err.Error()
		}
		rows = append(rows, r)
	}
	return rows
}

// printFailures prints full error messages, which the dashboard truncates.
func printFailures(w io.Writer, res *engine.Result) {
	for _, h := range res.Hosts {
		if h.Err != nil {
			fmt.Fprintf(w, "%s: %v\n", h.Host.Name, h.Err)
		}
	}
}

func printPlan(w io.Writer, p *engine.Plan) {
	action := map[string]string{}
	for _, h := range p.Create {
		action[h.Name] = "create"
	}
	for _, e := range p.Existing {
		a := fmt.Sprintf("exists (droplet %d, %s)", e.Droplet.ID, e.Droplet.Status)
		if len(e.Drift) > 0 {
			a += "; " + strings.Join(e.Drift, "; ")
		}
		if e.OutsideProject {
			a += "; move to project " + p.Project.Name
		}
		action[e.Host.Name] = a
	}
	for _, c := range p.Conflicts {
		action[c.Host.Name] = "CONFLICT"
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tROLE\tSIZE\tACTION")
	for _, h := range p.Spec.Hosts() {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", h.Name, h.Role, h.Size, action[h.Name])
	}
	tw.Flush()
	for _, d := range p.Orphans {
		fmt.Fprintf(w, "warning: droplet %d (%s) is tagged %s but not in the spec; `dovm destroy --include-orphans` removes it\n",
			d.ID, d.Name, p.Spec.ClusterTag())
	}
}

func printResult(w io.Writer, res *engine.Result) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tROLE\tRESULT\tDROPLET\tPUBLIC IP\tPRIVATE IP\tERROR")
	for _, h := range res.Hosts {
		result := string(h.Status)
		if h.Status == engine.StatusReady && !h.Created {
			result = "existing"
		}
		var id, pub, priv, errMsg string
		if d := h.Droplet; d != nil {
			id = fmt.Sprint(d.ID)
			pub, _ = d.PublicIPv4()
			priv, _ = d.PrivateIPv4()
		}
		if h.Err != nil {
			errMsg = strings.ReplaceAll(h.Err.Error(), "\n", "; ")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", h.Host.Name, h.Host.Role, result, id, pub, priv, errMsg)
	}
	tw.Flush()
}

// newTextReporter prints one timestamped line per progress event.
func newTextReporter(w io.Writer) engine.Reporter {
	var mu sync.Mutex
	return func(e engine.Event) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "%s  %s\n", time.Now().Format("15:04:05"), e)
	}
}

func createTitle(p *engine.Plan) string {
	title := fmt.Sprintf("Creating cluster %s in %s", p.Spec.Name, p.Spec.Region)
	if p.Project != nil {
		title += ", project " + p.Project.Name
	}
	return title
}

// pollInterval overrides the engine's default status poll interval; for tests.
var pollInterval time.Duration

const loginPasswordEnv = "DOVM_LOGIN_PASSWORD"

// loginPassword reads the --login-user password from the password file, the
// environment or, failing both, a prompt that asks for it twice.
func loginPassword(cmd *cobra.Command, f *createFlags) (string, error) {
	if f.passwordFile != "" {
		data, err := os.ReadFile(f.passwordFile)
		if err != nil {
			return "", fmt.Errorf("read login password: %w", err)
		}
		password := strings.TrimRight(string(data), "\r\n")
		if password == "" {
			return "", fmt.Errorf("login password file %s is empty", f.passwordFile)
		}
		return password, nil
	}
	if password := os.Getenv(loginPasswordEnv); password != "" {
		return password, nil
	}
	password, err := promptPassword(cmd, fmt.Sprintf("Password for %s:", f.loginUser))
	if errors.Is(err, errNotInteractive) {
		return "", fmt.Errorf("no login password: set $%s or pass --login-password-file when running non-interactively", loginPasswordEnv)
	}
	if err != nil {
		return "", err
	}
	if password == "" {
		return "", errors.New("login password is empty")
	}
	again, err := promptPassword(cmd, "Repeat password:")
	if err != nil {
		return "", err
	}
	if again != password {
		return "", errors.New("passwords do not match")
	}
	return password, nil
}
