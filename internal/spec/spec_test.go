package spec

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const validYAML = `
region: nyc3
image: ubuntu-24-04-x64
ssh_keys: [3871344, "aa:bb:cc"]
tags: [example]
roles:
  worker:
    size: s-2vcpu-4gb
    hosts: [vm01, vm02]
    tags: [example, pool-a]
  manager:
    size: s-2vcpu-2gb
    hosts: [cp01]
`

func TestParseValid(t *testing.T) {
	s, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if want := []string{"3871344", "aa:bb:cc"}; !slices.Equal(s.SSHKeys, want) {
		t.Errorf("SSHKeys = %v, want %v", s.SSHKeys, want)
	}

	hosts := s.Hosts()
	var names []string
	for _, h := range hosts {
		names = append(names, h.Name)
	}
	if want := []string{"cp01", "vm01", "vm02"}; !slices.Equal(names, want) {
		t.Errorf("host order = %v, want %v", names, want)
	}
	if want := []string{"worker", "example", "pool-a"}; !slices.Equal(hosts[1].Tags, want) {
		t.Errorf("worker tags = %v, want %v", hosts[1].Tags, want)
	}
	if hosts[0].Size != "s-2vcpu-2gb" || hosts[0].Role != "manager" {
		t.Errorf("cp01 = %+v", hosts[0])
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	_, err := Parse([]byte("region: nyc3\nimage: x\nsize: oops\nroles: {w: {size: s, hosts: [a]}}\n"))
	if err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestValidateReportsAllErrors(t *testing.T) {
	s := &Spec{
		Tags: []string{"bad tag"},
		Roles: map[string]Role{
			"worker":  {Size: "s-1", Hosts: []string{"vm01", "bad_name"}},
			"manager": {Hosts: []string{"vm01"}},
			"empty":   {Size: "s-1"},
		},
	}
	err := s.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{
		"region is required",
		"image is required",
		`tag "bad tag" is invalid`,
		`role "manager": size is required`,
		`role "empty": at least one host is required`,
		`hostname "bad_name" is invalid`,
		`hostname "vm01" is declared in both role "manager" and role "worker"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing error %q in:\n%v", want, err)
		}
	}
}

func TestValidateRequiresRoles(t *testing.T) {
	err := (&Spec{Region: "nyc3", Image: "x"}).Validate()
	if err == nil || !strings.Contains(err.Error(), "at least one role") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadExample(t *testing.T) {
	if _, err := Load(filepath.Join("..", "..", "examples", "cluster.yaml")); err != nil {
		t.Fatalf("example spec should be valid: %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if !os.IsNotExist(err) {
		t.Fatalf("expected not-exist error, got %v", err)
	}
}
