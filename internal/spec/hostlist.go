package spec

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// DefaultRole is the role given to hosts listed without one.
const DefaultRole = "worker"

// ParseHostList reads the host list format used by the original Python tool:
// one host per line as "hostname<TAB>role". Fields may be separated by any
// whitespace, the role defaults to DefaultRole, and blank lines and lines
// starting with # are skipped. It returns hostnames by role in file order.
func ParseHostList(data []byte) (map[string][]string, error) {
	roles := map[string][]string{}
	seen := map[string]int{}
	var errs []error
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		host, role := fields[0], DefaultRole
		switch {
		case len(fields) == 2:
			role = fields[1]
		case len(fields) > 2:
			errs = append(errs, fmt.Errorf("line %d: expected \"hostname role\", got %d fields", n, len(fields)))
			continue
		}
		if !ValidHostname(host) {
			errs = append(errs, fmt.Errorf("line %d: %q is not a valid hostname", n, host))
			continue
		}
		if !ValidTag(role) {
			errs = append(errs, fmt.Errorf("line %d: %q is not a valid role name", n, role))
			continue
		}
		if prev, dup := seen[host]; dup {
			errs = append(errs, fmt.Errorf("line %d: %q is already listed on line %d", n, host, prev))
			continue
		}
		seen[host] = n
		roles[role] = append(roles[role], host)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(errs) == 0 && len(roles) == 0 {
		errs = append(errs, errors.New("no hosts found"))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return roles, nil
}
