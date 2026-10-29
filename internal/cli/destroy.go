package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/roysomak4/do-vm-manager/internal/engine"
	"github.com/roysomak4/do-vm-manager/internal/spec"
	"github.com/roysomak4/do-vm-manager/internal/tui"
)

const destroyTimeout = 5 * time.Minute

type destroyFlags struct {
	name           string
	hosts          []string
	includeOrphans bool
	dryRun         bool
	yes            bool
}

func newDestroyCmd(g *globalFlags) *cobra.Command {
	f := &destroyFlags{}
	cmd := &cobra.Command{
		Use:   "destroy [SPEC]",
		Short: "Delete a cluster's droplets",
		Long: "Destroy deletes droplets tagged dovm:<name>. Droplets without the tag are\n" +
			"never touched.\n\n" +
			"With a SPEC, it deletes the droplets of the hosts named in it; add\n" +
			"--include-orphans to also delete tagged droplets no longer in the spec.\n" +
			"Without a SPEC, --name deletes every droplet tagged for that cluster.\n\n" +
			"You are asked to type the cluster name to confirm unless --yes is given.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDestroy(cmd, g, f, args)
		},
	}
	cmd.Flags().StringVar(&f.name, "name", "", "cluster name, when destroying without a spec")
	cmd.Flags().StringSliceVar(&f.hosts, "host", nil, "only delete these hosts (repeatable or comma-separated)")
	cmd.Flags().BoolVar(&f.includeOrphans, "include-orphans", false, "also delete tagged droplets not named in the spec")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "show what would be deleted without deleting anything")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "delete without asking for confirmation")
	return cmd
}

func runDestroy(cmd *cobra.Command, g *globalFlags, f *destroyFlags, args []string) error {
	var s *spec.Spec
	var name string
	switch {
	case len(args) == 1 && f.name != "":
		return errors.New("pass either SPEC or --name, not both")
	case len(args) == 1:
		var err error
		if s, err = spec.Load(args[0]); err != nil {
			return validationFailed(err)
		}
		name = s.Name
	case f.name != "":
		if err := spec.ValidateName(f.name); err != nil {
			return err
		}
		if f.includeOrphans {
			return errors.New("--include-orphans needs a SPEC; with --name every tagged droplet is already included")
		}
		name = f.name
	default:
		return errors.New("pass a SPEC or --name")
	}
	tag := spec.ClusterTag(name)

	client, err := g.client()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	targets, notFound, err := engine.SelectForDestroy(ctx, client, tag, s,
		engine.DestroySelection{Hosts: f.hosts, IncludeOrphans: f.includeOrphans})
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	for _, h := range notFound {
		fmt.Fprintf(out, "%s: no droplet tagged %s, nothing to delete\n", h, tag)
	}
	if len(targets) == 0 {
		fmt.Fprintf(out, "No droplets tagged %s to delete.\n", tag)
		return nil
	}
	printTargets(out, targets)
	if f.dryRun {
		fmt.Fprintf(out, "\nWould delete %d droplet(s).\n", len(targets))
		return nil
	}

	if !f.yes {
		answer, err := prompt(cmd, fmt.Sprintf("\nThis permanently deletes %d droplet(s) and their data.\nType the cluster name (%s) to confirm:", len(targets), name))
		if errors.Is(err, errNotInteractive) {
			return errors.New("refusing to delete droplets without confirmation; pass --yes when running non-interactively")
		}
		if err != nil {
			return err
		}
		if answer != name {
			return errors.New("aborted: name did not match")
		}
	}

	// Finish deleting what was started even if interrupted, so the outcome is
	// fully reported rather than unknown.
	delCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), destroyTimeout)
	defer cancel()
	var results []engine.DestroyResult
	if g.useTUI(cmd) {
		rows := make([]tui.Row, len(targets))
		for i, t := range targets {
			rows[i] = tui.Row{Name: t.Droplet.Name, DropletID: t.Droplet.ID, State: tui.Deleting}
			rows[i].IP, _ = t.Droplet.PublicIPv4()
			if t.Orphan {
				rows[i].Role = "orphan"
			}
		}
		err = tui.Run(ctx, tui.Config{
			Title:      fmt.Sprintf("Destroying cluster %s", name),
			Rows:       rows,
			CancelHint: "deletions already requested will finish",
		}, func(_ context.Context, report engine.Reporter) []tui.Row {
			results = engine.Destroy(delCtx, client, targets, 0, report)
			return nil // the per-droplet events already set every row's final state
		})
		if err != nil {
			return err
		}
		for _, r := range results {
			if r.Err != nil {
				fmt.Fprintf(out, "%s (droplet %d): %v\n", r.Target.Droplet.Name, r.Target.Droplet.ID, r.Err)
			}
		}
	} else {
		results = engine.Destroy(delCtx, client, targets, 0, newTextReporter(cmd.ErrOrStderr()))
	}

	var failed int
	for _, r := range results {
		if r.Err != nil {
			failed++
		}
	}
	fmt.Fprintf(out, "\nDeleted %d of %d droplet(s).\n", len(results)-failed, len(results))
	if failed > 0 {
		return fmt.Errorf("%d droplet(s) could not be deleted; run destroy again to retry", failed)
	}
	return nil
}

func printTargets(w io.Writer, targets []engine.DestroyTarget) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tDROPLET\tSTATUS\tREGION\tSIZE\tPUBLIC IP\tNOTE")
	for _, t := range targets {
		d := t.Droplet
		pub, _ := d.PublicIPv4()
		var region, note string
		if d.Region != nil {
			region = d.Region.Slug
		}
		if t.Orphan {
			note = "orphan (not in spec)"
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\n", d.Name, d.ID, d.Status, region, d.SizeSlug, pub, note)
	}
	tw.Flush()
}
