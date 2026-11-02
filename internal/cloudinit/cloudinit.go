// Package cloudinit builds the user data dovm passes to new droplets.
package cloudinit

import (
	"errors"
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

// usernameRe is the portable Linux username format accepted by useradd.
var usernameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// ValidateUsername checks a login user name.
func ValidateUsername(name string) error {
	switch {
	case name == "root":
		return errors.New("login user cannot be root: root logs in with the spec's SSH keys")
	case !usernameRe.MatchString(name):
		return fmt.Errorf("login user %q is invalid: use up to 32 lowercase letters, digits, dashes or underscores, starting with a letter or underscore", name)
	}
	return nil
}

// sshdConfig turns on password logins for everyone but root. It sorts before
// the image's and cloud-init's own drop-ins, and sshd keeps the first value it
// reads for each setting.
const sshdConfig = `PasswordAuthentication yes
PermitRootLogin prohibit-password
`

type config struct {
	SSHPwauth  bool        `yaml:"ssh_pwauth"`
	Users      []user      `yaml:"users"`
	WriteFiles []writeFile `yaml:"write_files"`
	RunCmd     []string    `yaml:"runcmd"`
}

type user struct {
	Name       string `yaml:"name"`
	Shell      string `yaml:"shell"`
	LockPasswd bool   `yaml:"lock_passwd"`
	Password   string `yaml:"plain_text_passwd"`
}

type writeFile struct {
	Path        string `yaml:"path"`
	Permissions string `yaml:"permissions"`
	Content     string `yaml:"content"`
}

// LoginUser returns a cloud-config that adds an unprivileged user who can log
// in over SSH with password, while root stays key-only. The password is
// stored in plain text in the droplet's user data.
func LoginUser(name, password string) (string, error) {
	if err := ValidateUsername(name); err != nil {
		return "", err
	}
	if password == "" {
		return "", errors.New("login password is empty")
	}
	data, err := yaml.Marshal(config{
		SSHPwauth: true,
		// Listing only this user also stops cloud-init adding the image's
		// default user, which DigitalOcean images do not use.
		Users: []user{{Name: name, Shell: "/bin/bash", Password: password}},
		WriteFiles: []writeFile{{
			Path: "/etc/ssh/sshd_config.d/00-dovm.conf", Permissions: "0644", Content: sshdConfig,
		}},
		RunCmd: []string{"systemctl restart ssh || systemctl restart sshd"},
	})
	if err != nil {
		return "", err
	}
	return "#cloud-config\n" + string(data), nil
}
