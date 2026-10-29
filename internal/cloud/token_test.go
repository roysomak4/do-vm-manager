package cloud

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveTokenPrecedence(t *testing.T) {
	t.Setenv("DIGITALOCEAN_TOKEN", "env-token")
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "env-access-token")

	if got, _ := ResolveToken("flag-token"); got != "flag-token" {
		t.Errorf("flag should win, got %q", got)
	}
	if got, _ := ResolveToken(""); got != "env-token" {
		t.Errorf("DIGITALOCEAN_TOKEN should win over DIGITALOCEAN_ACCESS_TOKEN, got %q", got)
	}
	t.Setenv("DIGITALOCEAN_TOKEN", "")
	if got, _ := ResolveToken(""); got != "env-access-token" {
		t.Errorf("got %q", got)
	}
}

func TestDoctlToken(t *testing.T) {
	cases := []struct {
		name, config, want string
	}{
		{"default context", "access-token: default-tok\ncontext: default\n", "default-tok"},
		{"no context key", "access-token: default-tok\n", "default-tok"},
		{"named context", "access-token: default-tok\ncontext: work\nauth-contexts:\n  work: work-tok\n", "work-tok"},
		{"unknown context", "access-token: default-tok\ncontext: gone\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.config), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := doctlToken(path)
			if err != nil || got != tc.want {
				t.Fatalf("doctlToken = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestDoctlTokenMissingFile(t *testing.T) {
	got, err := doctlToken(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil || got != "" {
		t.Fatalf("doctlToken = %q, %v; want empty, nil", got, err)
	}
}
