package cli

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/roysomak4/do-vm-manager/internal/output"
	"github.com/roysomak4/do-vm-manager/internal/spec"
)

type listEntry struct {
	Cluster string `json:"cluster"`
	output.VM
}

func newListCmd(g *globalFlags) *cobra.Command {
	var cluster, format string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List droplets managed by dovm, across all clusters",
		Long:  "List shows every droplet tagged dovm:<name>, grouped by cluster.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkFormat(format); err != nil {
				return err
			}
			client, err := g.client()
			if err != nil {
				return err
			}
			droplets, err := client.Droplets(cmd.Context())
			if err != nil {
				return fmt.Errorf("list droplets: %w", err)
			}

			entries := []listEntry{}
			for i := range droplets {
				for _, t := range droplets[i].Tags {
					name, ok := strings.CutPrefix(t, spec.ClusterTagPrefix)
					if !ok || (cluster != "" && name != cluster) {
						continue
					}
					entries = append(entries, listEntry{Cluster: name, VM: output.NewVM("", &droplets[i])})
				}
			}
			slices.SortFunc(entries, func(a, b listEntry) int {
				return cmp.Or(cmp.Compare(a.Cluster, b.Cluster), cmp.Compare(a.Hostname, b.Hostname), cmp.Compare(a.ID, b.ID))
			})

			out := cmd.OutOrStdout()
			if format == formatJSON {
				return writeJSON(out, entries)
			}
			if len(entries) == 0 {
				fmt.Fprintln(out, "No droplets managed by dovm.")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "CLUSTER\tHOST\tDROPLET\tSTATUS\tREGION\tSIZE\tPUBLIC IP\tPRIVATE IP")
			for _, e := range entries {
				fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
					e.Cluster, e.Hostname, e.ID, e.Status, e.Region, e.Size, e.Public, e.Private)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&cluster, "cluster", "", "only show this cluster")
	addFormatFlag(cmd, &format)
	return cmd
}
