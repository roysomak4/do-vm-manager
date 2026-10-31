package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/roysomak4/do-vm-manager/internal/spec"
)

type importFlags struct {
	output     string
	force      bool
	name       string
	region     string
	image      string
	size       string
	roleSizes  map[string]string
	sshKeys    []string
	tags       []string
	monitoring bool
	ipv6       bool
	project    string
}

func newImportCmd() *cobra.Command {
	f := &importFlags{}
	cmd := &cobra.Command{
		Use:   "import HOSTFILE",
		Short: "Convert an old hostname.txt host list into a cluster spec",
		Long: "Import reads the host list used by the original Python script, one\n" +
			"\"hostname<TAB>role\" per line (role defaults to worker), and writes a\n" +
			"cluster spec. Flags fill in the settings the old script took on its\n" +
			"command line. No API calls are made; check the result with validate.",
		Example: "  dovm import hosts/hostname.txt --name k8s --region nyc3 \\\n" +
			"    --size s-4vcpu-8gb --role-size manager=s-2vcpu-4gb --ssh-keys 3871344",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImport(cmd, f, args[0])
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&f.output, "output", "o", "cluster.yaml", `spec file to write, or "-" for stdout`)
	fl.BoolVar(&f.force, "force", false, "overwrite the output file if it exists")
	fl.StringVar(&f.name, "name", "", "cluster name (required); droplets are tagged dovm:<name>")
	fl.StringVar(&f.region, "region", "nyc3", "region slug")
	fl.StringVar(&f.image, "image", "ubuntu-24-04-x64", "image slug or ID")
	fl.StringVar(&f.size, "size", "s-2vcpu-4gb", "droplet size for roles without --role-size")
	fl.StringToStringVar(&f.roleSizes, "role-size", nil, "size for one role, as ROLE=SIZE (repeatable)")
	fl.StringSliceVar(&f.sshKeys, "ssh-keys", nil, "SSH key IDs or fingerprints, comma-separated")
	fl.StringSliceVar(&f.tags, "tags", nil, "extra tags for every droplet, comma-separated")
	fl.BoolVar(&f.monitoring, "monitoring", true, "enable droplet monitoring")
	fl.BoolVar(&f.ipv6, "ipv6", false, "enable IPv6")
	fl.StringVar(&f.project, "project", "", "DigitalOcean project name or ID (default: the account's default project)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func runImport(cmd *cobra.Command, f *importFlags, hostFile string) error {
	data, err := os.ReadFile(hostFile)
	if err != nil {
		return err
	}
	hosts, err := spec.ParseHostList(data)
	if err != nil {
		return fmt.Errorf("%s: %w", hostFile, err)
	}
	for role := range f.roleSizes {
		if _, ok := hosts[role]; !ok {
			return fmt.Errorf("--role-size %s: no hosts in %s have role %q", role, hostFile, role)
		}
	}

	s := &spec.Spec{
		Name: f.name, Region: f.region, Image: f.image,
		SSHKeys: f.sshKeys, Tags: f.tags, Monitoring: f.monitoring, IPv6: f.ipv6, Project: f.project,
		Roles: map[string]spec.Role{},
	}
	for role, names := range hosts {
		size := f.size
		if rs, ok := f.roleSizes[role]; ok {
			size = rs
		}
		s.Roles[role] = spec.Role{Size: size, Hosts: names}
	}
	if err := s.Validate(); err != nil {
		return err
	}
	header := fmt.Sprintf("dovm cluster spec, imported from %s by `dovm import` on %s.",
		filepath.Base(hostFile), time.Now().Format("2006-01-02"))
	if f.output != "-" {
		header += "\nCheck it with: dovm create " + f.output + " --dry-run"
	}
	out, err := s.Marshal(header)
	if err != nil {
		return err
	}

	if f.output == "-" {
		_, err := cmd.OutOrStdout().Write(out)
		return err
	}
	if _, err := os.Stat(f.output); err == nil && !f.force {
		return fmt.Errorf("%s already exists; pass --force to overwrite it", f.output)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(f.output, out, 0o644); err != nil {
		return err
	}

	roles := make([]string, 0, len(hosts))
	for role, names := range hosts {
		roles = append(roles, fmt.Sprintf("%s: %d (%s)", role, len(names), s.Roles[role].Size))
	}
	slices.Sort(roles)
	fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s with %d host(s): %s\nNext: dovm validate %s\n",
		f.output, len(s.Hosts()), strings.Join(roles, ", "), f.output)
	return nil
}
