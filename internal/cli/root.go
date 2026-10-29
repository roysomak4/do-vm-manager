// Package cli defines the dovm command tree.
package cli

import (
	"os"
	"runtime/debug"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/roysomak4/do-vm-manager/internal/cloud"
)

type globalFlags struct {
	token      string
	apiTimeout time.Duration
	apiURL     string
	noTUI      bool
}

// NewRootCmd builds the dovm command tree.
func NewRootCmd() *cobra.Command {
	g := &globalFlags{}
	root := &cobra.Command{
		Use:           "dovm",
		Short:         "Create and manage DigitalOcean droplets from a declarative spec",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       buildVersion(),
	}
	root.PersistentFlags().StringVar(&g.token, "token", "", "DigitalOcean API token (default: $DIGITALOCEAN_TOKEN, then doctl's config)")
	root.PersistentFlags().DurationVar(&g.apiTimeout, "api-timeout", 30*time.Second, "timeout for each DigitalOcean API request attempt")

	root.PersistentFlags().BoolVar(&g.noTUI, "no-tui", false, "plain text output instead of the interactive dashboard")
	root.PersistentFlags().StringVar(&g.apiURL, "api-url", "", "DigitalOcean API base URL")
	_ = root.PersistentFlags().MarkHidden("api-url") // for tests

	root.AddCommand(newInitCmd(g), newImportCmd(), newValidateCmd(g), newCreateCmd(g), newStatusCmd(g), newListCmd(g), newDestroyCmd(g))
	return root
}

// version is set at build time with -ldflags "-X .../internal/cli.version=v1.2.3".
var version = ""

func buildVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version // go install ...@version
	}
	return "dev"
}

// useTUI reports whether to show the interactive dashboard: only when output
// goes to a real terminal and it was not turned off.
func (g *globalFlags) useTUI(cmd *cobra.Command) bool {
	if g.noTUI || os.Getenv("TERM") == "dumb" || cmd.OutOrStdout() != os.Stdout {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

func (g *globalFlags) client() (*cloud.Client, error) {
	token, err := cloud.ResolveToken(g.token)
	if err != nil {
		return nil, err
	}
	return cloud.New(token, cloud.Options{Timeout: g.apiTimeout, BaseURL: g.apiURL})
}
