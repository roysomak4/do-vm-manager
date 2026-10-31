package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"golang.org/x/term"

	"github.com/roysomak4/do-vm-manager/internal/tui"
)

func newInitCmd(g *globalFlags) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init [FILE]",
		Short: "Write a cluster spec interactively",
		Long: "Init walks you through writing a cluster spec, offering the regions, images,\n" +
			"sizes and SSH keys available in your account. FILE defaults to cluster.yaml.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "cluster.yaml"
			if len(args) == 1 {
				path = args[0]
			}
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s already exists; pass --force to overwrite it", path)
			} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
				return errors.New("init is interactive and needs a terminal; see examples/cluster.yaml to write a spec by hand")
			}

			client, err := g.client()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			fmt.Fprintln(cmd.ErrOrStderr(), "Fetching regions, sizes, images, SSH keys and projects…")
			var cat tui.WizardCatalog
			eg, ectx := errgroup.WithContext(ctx)
			eg.Go(func() (err error) { cat.Regions, err = client.Regions(ectx); return })
			eg.Go(func() (err error) { cat.Sizes, err = client.Sizes(ectx); return })
			eg.Go(func() (err error) { cat.Images, err = client.DistributionImages(ectx); return })
			eg.Go(func() (err error) { cat.SSHKeys, err = client.SSHKeys(ectx); return })
			if err := eg.Wait(); err != nil {
				return fmt.Errorf("fetch account data: %w", err)
			}
			// Projects are optional: a token without project access still gets a spec,
			// with droplets in the default project.
			if projects, err := client.Projects(ctx); err == nil {
				cat.Projects = projects
			} else if ctx.Err() == nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Could not list projects (%v); droplets will go into the default project.\n", err)
			}

			s, err := tui.Wizard(ctx, cat, path)
			if err != nil {
				return err
			}
			data, err := s.Marshal(fmt.Sprintf("dovm cluster spec, written by `dovm init` on %s.\nCheck it with: dovm create %s --dry-run",
				time.Now().Format("2006-01-02"), path))
			if err != nil {
				return err
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s. Next: dovm create %s --dry-run\n", path, path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite FILE if it exists")
	return cmd
}
