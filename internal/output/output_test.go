package output

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digitalocean/godo"

	"github.com/roysomak4/do-vm-manager/internal/engine"
	"github.com/roysomak4/do-vm-manager/internal/spec"
)

func droplet(id int, pub, priv string) *godo.Droplet {
	return &godo.Droplet{ID: id, Status: "active", SizeSlug: "s-1", Region: &godo.Region{Slug: "nyc3"},
		Networks: &godo.Networks{V4: []godo.NetworkV4{
			{IPAddress: pub, Type: "public"},
			{IPAddress: priv, Type: "private"},
		}}}
}

func TestWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "output")
	res := &engine.Result{Hosts: []*engine.HostResult{
		{Host: spec.Host{Name: "cp01", Role: "manager"}, Status: engine.StatusReady, Droplet: droplet(1, "203.0.113.1", "10.0.0.1")},
		{Host: spec.Host{Name: "vm01", Role: "worker"}, Status: engine.StatusFailed},
		{Host: spec.Host{Name: "vm02", Role: "worker"}, Status: engine.StatusReady, Droplet: droplet(2, "203.0.113.2", "10.0.0.2")},
	}}

	n, err := Write(dir, res)
	if err != nil || n != 2 {
		t.Fatalf("Write = %d, %v", n, err)
	}

	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got, want := read(IPHostFile), "cp01\t203.0.113.1\nvm02\t203.0.113.2\n"; got != want {
		t.Errorf("%s = %q, want %q", IPHostFile, got, want)
	}
	if got, want := read(IPsFile), "203.0.113.1\n203.0.113.2\n"; got != want {
		t.Errorf("%s = %q, want %q", IPsFile, got, want)
	}

	var vms []map[string]any
	if err := json.Unmarshal([]byte(read(VMsJSON)), &vms); err != nil {
		t.Fatal(err)
	}
	if len(vms) != 2 || vms[0]["hostname"] != "cp01" || vms[0]["public"] != "203.0.113.1" ||
		vms[0]["private"] != "10.0.0.1" || vms[0]["role"] != "manager" {
		t.Errorf("vms.json = %v", vms)
	}

	wantInv := "# Ansible inventory written by dovm.\n[all:vars]\nansible_user=root\n" +
		"\n[manager]\ncp01 ansible_host=203.0.113.1 private_ip=10.0.0.1\n" +
		"\n[worker]\nvm02 ansible_host=203.0.113.2 private_ip=10.0.0.2\n"
	if got := read(Inventory); got != wantInv {
		t.Errorf("%s =\n%s\nwant\n%s", Inventory, got, wantInv)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 5 {
		t.Errorf("expected 5 files and no leftover temp files, got %d", len(entries))
	}
}

func TestWriteNothingReady(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	res := &engine.Result{Hosts: []*engine.HostResult{{Host: spec.Host{Name: "a"}, Status: engine.StatusFailed}}}
	if n, err := Write(dir, res); n != 0 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("output dir created with nothing to write")
	}
}

func TestInventoryGroupNames(t *testing.T) {
	got := string(ansibleInventory([]VM{
		{Hostname: "a", Role: "k8s-control:plane", Public: "192.0.2.1"},
		{Hostname: "b", Public: "192.0.2.2"},
	}))
	for _, want := range []string{"[k8s_control_plane]\na ansible_host=192.0.2.1\n", "[ungrouped]\nb ansible_host=192.0.2.2\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("inventory missing %q:\n%s", want, got)
		}
	}
}
