package cloudinit

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLoginUser(t *testing.T) {
	// Characters that would break hand-written YAML must round-trip.
	const password = `p@ss: "word" #1`
	data, err := LoginUser("student", password)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(data, "#cloud-config\n") {
		t.Fatalf("missing #cloud-config header:\n%s", data)
	}

	var got struct {
		SSHPwauth  bool `yaml:"ssh_pwauth"`
		Users      []map[string]any
		WriteFiles []map[string]string `yaml:"write_files"`
		RunCmd     []string            `yaml:"runcmd"`
	}
	if err := yaml.Unmarshal([]byte(data), &got); err != nil {
		t.Fatalf("not valid YAML: %v\n%s", err, data)
	}
	if !got.SSHPwauth {
		t.Error("ssh_pwauth is not set")
	}
	if len(got.Users) != 1 {
		t.Fatalf("users = %v", got.Users)
	}
	u := got.Users[0]
	if u["name"] != "student" || u["plain_text_passwd"] != password || u["lock_passwd"] != false || u["shell"] != "/bin/bash" {
		t.Errorf("user = %v", u)
	}
	if _, ok := u["sudo"]; ok {
		t.Error("login user must not get sudo")
	}
	if len(got.WriteFiles) != 1 || got.WriteFiles[0]["path"] != "/etc/ssh/sshd_config.d/00-dovm.conf" ||
		!strings.Contains(got.WriteFiles[0]["content"], "PasswordAuthentication yes") ||
		!strings.Contains(got.WriteFiles[0]["content"], "PermitRootLogin prohibit-password") {
		t.Errorf("write_files = %v", got.WriteFiles)
	}
	if len(got.RunCmd) != 1 || !strings.Contains(got.RunCmd[0], "restart ssh") {
		t.Errorf("runcmd = %v", got.RunCmd)
	}
}

func TestLoginUserRejectsBadInput(t *testing.T) {
	for _, tc := range []struct{ name, password string }{
		{"root", "pw"},
		{"Student", "pw"},
		{"1student", "pw"},
		{"stu dent", "pw"},
		{"", "pw"},
		{strings.Repeat("a", 33), "pw"},
		{"student", ""},
	} {
		if _, err := LoginUser(tc.name, tc.password); err == nil {
			t.Errorf("LoginUser(%q, %q) succeeded", tc.name, tc.password)
		}
	}
}

func TestValidateUsername(t *testing.T) {
	for _, ok := range []string{"student", "_svc", "amp-2026", "user_1"} {
		if err := ValidateUsername(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}
