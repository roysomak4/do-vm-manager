package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/roysomak4/do-vm-manager/internal/engine"
	"github.com/roysomak4/do-vm-manager/internal/output"
	"github.com/roysomak4/do-vm-manager/internal/spec"
)

type createFlags struct {
	dryRun      bool
	yes         bool
	rollback    bool
	waitTimeout time.Duration
	outputDir   string
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
			"creates what is still missing.",
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
	return cmd
}

func runCreate(cmd *cobra.Command, g *globalFlags, f *createFlags, path string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	s, err := spec.Load(path)
	if err != nil {
		return validationFailed(err)
	}
	client, err := g.client()
	if err != nil {
		return err
	}
	cat, err := client.Catalog(ctx, s.Image)
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

	printPlan(out, plan)
	if err := plan.Err(); err != nil {
		lines := strings.Split(err.Error(), "\n")
		return fmt.Errorf("cannot proceed, %d host(s) conflict with existing droplets:\n  - %s", len(lines), strings.Join(lines, "\n  - "))
	}
	fmt.Fprintf(out, "\n%d to create, %d already exist. Cluster total: $%.2f/month.\n",
		len(plan.Create), len(plan.Existing), est.MonthlyPrice)
	if f.dryRun {
		return nil
	}
	if len(plan.Create) > 0 && !f.yes {
		ok, err := confirm(cmd, fmt.Sprintf("Create %d droplet(s)?", len(plan.Create)))
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("aborted")
		}
	}

	res, err := engine.Run(ctx, client, plan, engine.Options{
		WaitTimeout: f.waitTimeout,
		Rollback:    f.rollback,
		Reporter:    newTextReporter(cmd.ErrOrStderr()),
	})
	if err != nil {
		return err
	}

	fmt.Fprintln(out)
	printResult(out, res)
	if !res.RolledBack {
		n, err := output.Write(f.outputDir, res)
		if err != nil {
			return fmt.Errorf("write output files: %w", err)
		}
		if n > 0 {
			fmt.Fprintf(out, "\nWrote %d host(s) to %s/{%s,%s,%s}\n", n, f.outputDir, output.VMsJSON, output.IPHostFile, output.IPsFile)
		}
	}

	switch {
	case res.OK():
		return nil
	case res.RolledBack:
		return errors.New("create failed; droplets created by this run were rolled back")
	case ctx.Err() != nil:
		return errors.New("interrupted; run create again to finish")
	default:
		return errors.New("some hosts are not ready; run create again to retry them")
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

// confirm asks a yes/no question. It refuses to guess when stdin is not a terminal.
func confirm(cmd *cobra.Command, question string) (bool, error) {
	if fi, err := os.Stdin.Stat(); cmd.InOrStdin() == os.Stdin && (err != nil || fi.Mode()&os.ModeCharDevice == 0) {
		return false, errors.New("refusing to create droplets without confirmation; pass --yes when running non-interactively")
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s [y/N] ", question)
	answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

// newTextReporter prints one timestamped line per progress event.
func newTextReporter(w io.Writer) engine.Reporter {
	var mu sync.Mutex
	return func(e engine.Event) {
		var msg string
		hosts := strings.Join(e.Hosts, ", ")
		switch e.Kind {
		case engine.EventCreateRequested:
			msg = fmt.Sprintf("creating %s (%s)", hosts, e.Detail)
		case engine.EventCreateRetry:
			msg = fmt.Sprintf("create failed for %s, checking for partial creation then retrying: %v", hosts, e.Err)
		case engine.EventCreated:
			msg = fmt.Sprintf("%s: droplet %d created, waiting for it to become active", hosts, e.DropletID)
		case engine.EventReady:
			msg = fmt.Sprintf("%s: ready at %s", hosts, e.Detail)
		case engine.EventFailed:
			msg = fmt.Sprintf("%s: FAILED: %v", hosts, e.Err)
		case engine.EventWarning:
			msg = "warning: " + e.Detail
			if hosts != "" {
				msg = fmt.Sprintf("warning: %s: %s", hosts, e.Detail)
			}
			if e.Err != nil {
				msg += fmt.Sprintf(": %v", e.Err)
			}
		case engine.EventRollback:
			msg = "rolling back: " + e.Detail
		case engine.EventDeleted:
			msg = fmt.Sprintf("%s: droplet %d deleted", hosts, e.DropletID)
		}
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "%s  %s\n", time.Now().Format("15:04:05"), msg)
	}
}
