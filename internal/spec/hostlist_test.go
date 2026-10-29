package spec

import (
	"slices"
	"strings"
	"testing"
)

func TestParseHostList(t *testing.T) {
	roles, err := ParseHostList([]byte("# old hostname.txt\ncp01\tmanager\n\nvm01\tworker\nvm02   worker\r\nvm03\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := roles["manager"]; !slices.Equal(got, []string{"cp01"}) {
		t.Errorf("manager = %v", got)
	}
	if got := roles["worker"]; !slices.Equal(got, []string{"vm01", "vm02", "vm03"}) {
		t.Errorf("worker = %v", got)
	}
}

func TestParseHostListErrors(t *testing.T) {
	_, err := ParseHostList([]byte("vm01 worker extra\nbad_host\nvm02 bad/role\nvm03\nvm03\n"))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"line 1: expected", `line 2: "bad_host"`, `line 3: "bad/role"`, `line 5: "vm03" is already listed on line 4`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
	if _, err := ParseHostList([]byte("# only comments\n\n")); err == nil || !strings.Contains(err.Error(), "no hosts") {
		t.Errorf("empty list: %v", err)
	}
}
