// Package output writes inventory files for ready droplets.
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/digitalocean/godo"

	"github.com/roysomak4/do-vm-manager/internal/engine"
)

// VM is one entry in vms.json. hostname, public, private and role keep the
// names used by the original Python tool so downstream consumers still work.
type VM struct {
	Hostname   string   `json:"hostname"`
	Role       string   `json:"role,omitempty"`
	ID         int      `json:"id"`
	Status     string   `json:"status"`
	Public     string   `json:"public"`
	Private    string   `json:"private,omitempty"`
	PublicIPv6 string   `json:"public_v6,omitempty"`
	Region     string   `json:"region"`
	Size       string   `json:"size"`
	Tags       []string `json:"tags"`
}

// NewVM describes a droplet. role may be empty when it is not known.
func NewVM(role string, d *godo.Droplet) VM {
	vm := VM{Hostname: d.Name, Role: role, ID: d.ID, Status: d.Status, Size: d.SizeSlug, Tags: d.Tags}
	vm.Public, _ = d.PublicIPv4()
	vm.Private, _ = d.PrivateIPv4()
	vm.PublicIPv6, _ = d.PublicIPv6()
	if d.Region != nil {
		vm.Region = d.Region.Slug
	}
	return vm
}

// Files written by Write, relative to the output directory.
const (
	VMsJSON      = "vms.json"
	IPHostFile   = "vms_ip_host.txt"
	IPsFile      = "vm_ips.txt"
	DropletsJSON = "droplets.json" // raw API objects, for debugging
	Inventory    = "inventory.ini" // Ansible inventory, one group per role
)

// Write writes inventory files for the ready hosts in res to dir, creating it
// if needed. Each file is replaced atomically. It returns the number of hosts written.
func Write(dir string, res *engine.Result) (int, error) {
	var vms []VM
	var raw []godo.Droplet
	for _, h := range res.Hosts {
		if h.Status != engine.StatusReady || h.Droplet == nil {
			continue
		}
		vm := NewVM(h.Host.Role, h.Droplet)
		vm.Hostname = h.Host.Name
		vms = append(vms, vm)
		raw = append(raw, *h.Droplet)
	}
	if len(vms) == 0 {
		return 0, nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	var ipHost, ips bytes.Buffer
	for _, vm := range vms {
		fmt.Fprintf(&ipHost, "%s\t%s\n", vm.Hostname, vm.Public)
		fmt.Fprintf(&ips, "%s\n", vm.Public)
	}
	vmsJSON, err := json.MarshalIndent(vms, "", "  ")
	if err != nil {
		return 0, err
	}
	rawJSON, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return 0, err
	}
	for name, data := range map[string][]byte{
		VMsJSON:      append(vmsJSON, '\n'),
		IPHostFile:   ipHost.Bytes(),
		IPsFile:      ips.Bytes(),
		DropletsJSON: append(rawJSON, '\n'),
		Inventory:    ansibleInventory(vms),
	} {
		if err := writeAtomic(filepath.Join(dir, name), data); err != nil {
			return 0, err
		}
	}
	return len(vms), nil
}

var groupRe = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// ansibleInventory renders an INI inventory with one group per role. Role
// names are changed to valid Ansible group names (dashes and colons become _).
func ansibleInventory(vms []VM) []byte {
	groups := map[string][]VM{}
	for _, vm := range vms {
		g := "ungrouped"
		if vm.Role != "" {
			g = groupRe.ReplaceAllString(vm.Role, "_")
		}
		groups[g] = append(groups[g], vm)
	}
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	slices.Sort(names)

	var b bytes.Buffer
	b.WriteString("# Ansible inventory written by dovm.\n[all:vars]\nansible_user=root\n")
	for _, g := range names {
		fmt.Fprintf(&b, "\n[%s]\n", g)
		for _, vm := range groups[g] {
			fmt.Fprintf(&b, "%s ansible_host=%s", vm.Hostname, vm.Public)
			if vm.Private != "" {
				fmt.Fprintf(&b, " private_ip=%s", vm.Private)
			}
			b.WriteString("\n")
		}
	}
	return b.Bytes()
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
