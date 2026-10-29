package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var errNotInteractive = errors.New("not running in a terminal")

// prompt asks a question and returns the trimmed answer. It refuses when
// stdin is not a terminal, so scripts never block on or guess an answer.
// Input set with cmd.SetIn (tests) is always accepted.
func prompt(cmd *cobra.Command, question string) (string, error) {
	if cmd.InOrStdin() == os.Stdin && !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errNotInteractive
	}
	fmt.Fprint(cmd.OutOrStdout(), question+" ")
	answer, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(answer), nil
}

const (
	formatTable = "table"
	formatJSON  = "json"
)

func addFormatFlag(cmd *cobra.Command, format *string) {
	cmd.Flags().StringVarP(format, "output", "o", formatTable, "output format: table or json")
}

func checkFormat(format string) error {
	if format != formatTable && format != formatJSON {
		return fmt.Errorf("unknown output format %q: use table or json", format)
	}
	return nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
