// Package cli defines the dovm command tree.
package cli

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/roysomak4/do-vm-manager/internal/cloud"
)

type globalFlags struct {
	token      string
	apiTimeout time.Duration
}

// NewRootCmd builds the dovm command tree.
func NewRootCmd() *cobra.Command {
	g := &globalFlags{}
	root := &cobra.Command{
		Use:           "dovm",
		Short:         "Create and manage DigitalOcean droplets from a declarative spec",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&g.token, "token", "", "DigitalOcean API token (default: $DIGITALOCEAN_TOKEN, then doctl's config)")
	root.PersistentFlags().DurationVar(&g.apiTimeout, "api-timeout", 30*time.Second, "timeout for each DigitalOcean API request attempt")

	root.AddCommand(newValidateCmd(g), newCreateCmd(g))
	return root
}

func (g *globalFlags) client() (*cloud.Client, error) {
	token, err := cloud.ResolveToken(g.token)
	if err != nil {
		return nil, err
	}
	return cloud.New(token, cloud.Options{Timeout: g.apiTimeout})
}
