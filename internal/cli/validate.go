package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/roysomak4/do-vm-manager/internal/spec"
)

func newValidateCmd(g *globalFlags) *cobra.Command {
	var offline bool
	cmd := &cobra.Command{
		Use:   "validate SPEC",
		Short: "Check a cluster spec for errors and estimate its cost",
		Long: "Validate parses the spec and checks it for errors. Unless --offline is set,\n" +
			"it also confirms the region, sizes, image and SSH keys exist in your\n" +
			"DigitalOcean account and estimates the cost.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			s, err := spec.Load(args[0])
			if err != nil {
				return validationFailed(err)
			}
			printHosts(out, s)
			if offline {
				fmt.Fprintln(out, "\nSpec is valid (offline check only).")
				return nil
			}

			client, err := g.client()
			if err != nil {
				return err
			}
			cat, err := client.Catalog(cmd.Context(), s.Image)
			if err != nil {
				return fmt.Errorf("fetch account data: %w", err)
			}
			est, err := s.Check(cat)
			if err != nil {
				return validationFailed(err)
			}
			fmt.Fprintf(out, "\nSpec is valid. %d droplets, estimated $%.2f/month ($%.4f/hour).\n",
				est.Droplets, est.MonthlyPrice, est.HourlyPrice)
			return nil
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "skip checks against the DigitalOcean API")
	return cmd
}

func printHosts(w io.Writer, s *spec.Spec) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tROLE\tSIZE\tTAGS")
	for _, h := range s.Hosts() {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", h.Name, h.Role, h.Size, strings.Join(h.Tags, ","))
	}
	tw.Flush()
}

// validationFailed formats joined errors as a bulleted list.
func validationFailed(err error) error {
	lines := strings.Split(err.Error(), "\n")
	return fmt.Errorf("spec is invalid:\n  - %s", strings.Join(lines, "\n  - "))
}
