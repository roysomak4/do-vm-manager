package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/roysomak4/do-vm-manager/internal/engine"
	"github.com/roysomak4/do-vm-manager/internal/output"
	"github.com/roysomak4/do-vm-manager/internal/spec"
)

// Host states reported by status.
const (
	stateExists   = "exists"
	stateMissing  = "missing"
	stateConflict = "conflict"
)

type statusHost struct {
	Hostname string     `json:"hostname"`
	Role     string     `json:"role"`
	State    string     `json:"state"`
	Droplet  *output.VM `json:"droplet,omitempty"`
	Note     string     `json:"note,omitempty"`
}

type statusReport struct {
	Cluster string       `json:"cluster"`
	Project string       `json:"project,omitempty"`
	Hosts   []statusHost `json:"hosts"`
	Orphans []output.VM  `json:"orphans"`
}

func newStatusCmd(g *globalFlags) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "status SPEC",
		Short: "Compare a cluster spec with the droplets that exist",
		Long: "Status shows, for each host in the spec, whether its droplet exists, its\n" +
			"state and IPs, and any difference from the spec. It also lists orphans:\n" +
			"droplets tagged for the cluster whose host is no longer in the spec.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkFormat(format); err != nil {
				return err
			}
			s, err := spec.Load(args[0])
			if err != nil {
				return validationFailed(err)
			}
			client, err := g.client()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			plan, err := engine.BuildPlan(ctx, client, s)
			if err != nil {
				return err
			}
			if s.Project != "" {
				projects, err := client.Projects(ctx)
				if err != nil {
					return fmt.Errorf("list projects: %w", err)
				}
				project, err := spec.FindProject(projects, s.Project)
				if err != nil {
					return err
				}
				if err := plan.UseProject(ctx, client, project); err != nil {
					return err
				}
			}
			report := buildStatus(plan)
			if format == formatJSON {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			printStatus(cmd.OutOrStdout(), report, s.ClusterTag())
			return nil
		},
	}
	addFormatFlag(cmd, &format)
	return cmd
}

func buildStatus(p *engine.Plan) statusReport {
	existing := map[string]engine.Existing{}
	for _, e := range p.Existing {
		existing[e.Host.Name] = e
	}
	conflicts := map[string]string{}
	for _, c := range p.Conflicts {
		conflicts[c.Host.Name] = c.Reason
	}

	r := statusReport{Cluster: p.Spec.Name, Orphans: []output.VM{}}
	if p.Project != nil {
		r.Project = p.Project.Name
	}
	for _, h := range p.Spec.Hosts() {
		sh := statusHost{Hostname: h.Name, Role: h.Role, State: stateMissing}
		if e, ok := existing[h.Name]; ok {
			vm := output.NewVM(h.Role, &e.Droplet)
			notes := e.Drift
			if e.OutsideProject {
				notes = append(slices.Clone(notes), "not in project "+p.Project.Name+"; create moves it")
			}
			sh.State, sh.Droplet, sh.Note = stateExists, &vm, strings.Join(notes, "; ")
		} else if reason, ok := conflicts[h.Name]; ok {
			sh.State, sh.Note = stateConflict, reason
		}
		r.Hosts = append(r.Hosts, sh)
	}
	for i := range p.Orphans {
		r.Orphans = append(r.Orphans, output.NewVM("", &p.Orphans[i]))
	}
	return r
}

func printStatus(w io.Writer, r statusReport, tag string) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tROLE\tSTATE\tDROPLET\tSTATUS\tPUBLIC IP\tPRIVATE IP\tNOTE")
	var active, inactive, missing, conflict int
	for _, h := range r.Hosts {
		var id, status, pub, priv string
		if d := h.Droplet; d != nil {
			id, status, pub, priv = fmt.Sprint(d.ID), d.Status, d.Public, d.Private
			if d.Status == "active" {
				active++
			} else {
				inactive++
			}
		}
		switch h.State {
		case stateMissing:
			missing++
		case stateConflict:
			conflict++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", h.Hostname, h.Role, h.State, id, status, pub, priv, h.Note)
	}
	tw.Flush()

	if len(r.Orphans) > 0 {
		fmt.Fprintf(w, "\nOrphans (tagged %s but not in the spec; `dovm destroy --include-orphans` removes them):\n", tag)
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "HOST\tDROPLET\tSTATUS\tPUBLIC IP")
		for _, o := range r.Orphans {
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", o.Hostname, o.ID, o.Status, o.Public)
		}
		tw.Flush()
	}

	fmt.Fprintf(w, "\n%d host(s): %d active, %d not active, %d missing, %d conflict; %d orphan(s).\n",
		len(r.Hosts), active, inactive, missing, conflict, len(r.Orphans))
}
