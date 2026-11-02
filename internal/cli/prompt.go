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

// promptPassword asks for a password without echoing it. Like prompt, it
// refuses when stdin is not a terminal, but accepts input set with cmd.SetIn.
func promptPassword(cmd *cobra.Command, question string) (string, error) {
	in := cmd.InOrStdin()
	if in == os.Stdin && !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errNotInteractive
	}
	fmt.Fprint(cmd.OutOrStdout(), question+" ")
	if in != os.Stdin {
		return readLine(in)
	}
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(cmd.OutOrStdout())
	return string(b), err
}

// readLine reads up to a newline one byte at a time, so that consecutive
// prompts on the same reader do not lose input to read-ahead buffering.
func readLine(r io.Reader) (string, error) {
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := r.Read(b)
		if n == 1 {
			if b[0] == '\n' {
				break
			}
			line = append(line, b[0])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return strings.TrimRight(string(line), "\r"), nil
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
