// Package spec loads and statically validates a cluster spec file.
package spec

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Spec is the desired set of droplets, as declared in a cluster YAML file.
type Spec struct {
	Name       string          `yaml:"name"`
	Region     string          `yaml:"region"`
	Image      string          `yaml:"image"`
	SSHKeys    []string        `yaml:"ssh_keys,omitempty"`
	Tags       []string        `yaml:"tags,omitempty"`
	Monitoring bool            `yaml:"monitoring,omitempty"`
	IPv6       bool            `yaml:"ipv6,omitempty"`
	VPCUUID    string          `yaml:"vpc_uuid,omitempty"`
	Project    string          `yaml:"project,omitempty"` // name or ID; empty means the account's default project
	Roles      map[string]Role `yaml:"roles"`
}

// Role groups hosts that share a droplet size and extra tags.
type Role struct {
	Size  string   `yaml:"size"`
	Hosts []string `yaml:"hosts,flow"`
	Tags  []string `yaml:"tags,omitempty,flow"`
}

// Host is one droplet to create, flattened from its role.
type Host struct {
	Name string
	Role string
	Size string
	Tags []string
}

var (
	// DigitalOcean droplet names: letters, digits, periods and dashes.
	hostnameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]{0,253}[a-zA-Z0-9])?$`)
	// DigitalOcean tag names: letters, digits, colons, dashes and underscores.
	tagRe = regexp.MustCompile(`^[a-zA-Z0-9_:-]{1,255}$`)
)

// Load reads and parses a spec file, rejecting unknown fields, then validates it.
func Load(path string) (*Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes YAML spec data and validates it.
func Parse(data []byte) (*Spec, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var s Spec
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// Validate checks the spec for errors that need no API access. All problems
// are reported together rather than stopping at the first.
func (s *Spec) Validate() error {
	var errs []error
	if err := ValidateName(s.Name); err != nil {
		errs = append(errs, err)
	}
	if s.Region == "" {
		errs = append(errs, errors.New("region is required"))
	}
	if s.Image == "" {
		errs = append(errs, errors.New("image is required"))
	}
	if len(s.Roles) == 0 {
		errs = append(errs, errors.New("at least one role is required"))
	}
	if s.Project != strings.TrimSpace(s.Project) {
		errs = append(errs, fmt.Errorf("project %q has leading or trailing spaces", s.Project))
	}
	for _, t := range s.Tags {
		if !tagRe.MatchString(t) {
			errs = append(errs, fmt.Errorf("tag %q is invalid", t))
		}
	}

	seen := map[string]string{}
	for _, name := range s.roleNames() {
		r := s.Roles[name]
		if !tagRe.MatchString(name) {
			errs = append(errs, fmt.Errorf("role %q is invalid: role names are used as tags", name))
		}
		if r.Size == "" {
			errs = append(errs, fmt.Errorf("role %q: size is required", name))
		}
		if len(r.Hosts) == 0 {
			errs = append(errs, fmt.Errorf("role %q: at least one host is required", name))
		}
		for _, t := range r.Tags {
			if !tagRe.MatchString(t) {
				errs = append(errs, fmt.Errorf("role %q: tag %q is invalid", name, t))
			}
		}
		for _, h := range r.Hosts {
			if !hostnameRe.MatchString(h) {
				errs = append(errs, fmt.Errorf("role %q: hostname %q is invalid", name, h))
			}
			if prev, dup := seen[h]; dup {
				errs = append(errs, fmt.Errorf("hostname %q is declared in both role %q and role %q", h, prev, name))
			}
			seen[h] = name
		}
	}
	return errors.Join(errs...)
}

// ValidHostname reports whether name is a valid droplet name.
func ValidHostname(name string) bool { return hostnameRe.MatchString(name) }

// ValidTag reports whether t is a valid DigitalOcean tag.
func ValidTag(t string) bool { return tagRe.MatchString(t) }

// Marshal renders the spec as YAML with a short header comment.
func (s *Spec) Marshal(header string) ([]byte, error) {
	var buf bytes.Buffer
	for _, line := range strings.Split(strings.TrimSpace(header), "\n") {
		if line != "" {
			fmt.Fprintf(&buf, "# %s\n", line)
		}
	}
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ValidateName checks a cluster name.
func ValidateName(name string) error {
	switch {
	case name == "":
		return errors.New("name is required")
	case !tagRe.MatchString(ClusterTag(name)):
		return fmt.Errorf("name %q is invalid: use letters, digits, dashes, underscores or colons", name)
	}
	return nil
}

// ClusterTag returns the tag dovm puts on droplets of the named cluster.
func ClusterTag(name string) string {
	return ClusterTagPrefix + name
}

// ClusterTagPrefix starts every cluster tag.
const ClusterTagPrefix = "dovm:"

// ClusterTag is the tag dovm puts on every droplet it creates for this spec.
// It marks ownership: only droplets carrying it are adopted or deleted.
func (s *Spec) ClusterTag() string {
	return ClusterTag(s.Name)
}

// Hosts flattens roles into droplets, ordered by role name then declaration order.
// Each host is tagged with its role, the spec-wide tags and the role's tags.
func (s *Spec) Hosts() []Host {
	var hosts []Host
	for _, name := range s.roleNames() {
		r := s.Roles[name]
		tags := dedupe(append(append([]string{name}, s.Tags...), r.Tags...))
		for _, h := range r.Hosts {
			hosts = append(hosts, Host{Name: h, Role: name, Size: r.Size, Tags: tags})
		}
	}
	return hosts
}

func (s *Spec) roleNames() []string {
	names := make([]string, 0, len(s.Roles))
	for n := range s.Roles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
