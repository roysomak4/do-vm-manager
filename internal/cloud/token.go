package cloud

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// tokenEnvVars are checked in order, matching doctl and the Terraform provider.
var tokenEnvVars = []string{"DIGITALOCEAN_TOKEN", "DIGITALOCEAN_ACCESS_TOKEN"}

// ResolveToken finds an API token: an explicit flag value first, then the
// environment, then doctl's config file for its current auth context.
func ResolveToken(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	for _, name := range tokenEnvVars {
		if v := os.Getenv(name); v != "" {
			return v, nil
		}
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", errNoToken
	}
	token, err := doctlToken(filepath.Join(configDir, "doctl", "config.yaml"))
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", errNoToken
	}
	return token, nil
}

var errNoToken = errors.New("no DigitalOcean API token found: pass --token, set DIGITALOCEAN_TOKEN, or run `doctl auth init`")

type doctlConfig struct {
	AccessToken  string            `yaml:"access-token"`
	Context      string            `yaml:"context"`
	AuthContexts map[string]string `yaml:"auth-contexts"`
}

// doctlToken reads the token for doctl's active context. A missing file is not an error.
func doctlToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var cfg doctlConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("read doctl config %s: %w", path, err)
	}
	if cfg.Context != "" && cfg.Context != "default" {
		return cfg.AuthContexts[cfg.Context], nil
	}
	return cfg.AccessToken, nil
}
