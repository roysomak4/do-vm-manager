package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/roysomak4/do-vm-manager/internal/spec"
)

// fakeDO serves the droplet list and delete endpoints from memory.
type fakeDO struct {
	mu       sync.Mutex
	droplets []map[string]any
	deleted  []int
}

func (f *fakeDO) add(id int, name string, tags ...string) {
	f.droplets = append(f.droplets, map[string]any{
		"id": id, "name": name, "status": "active", "tags": tags, "size_slug": "s-1vcpu-1gb",
		"region": map[string]any{"slug": "nyc3"},
		"networks": map[string]any{"v4": []map[string]any{
			{"ip_address": fmt.Sprintf("203.0.113.%d", id), "type": "public"},
			{"ip_address": fmt.Sprintf("10.0.0.%d", id), "type": "private"},
		}},
	})
}

func (f *fakeDO) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/droplets", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		tag := r.URL.Query().Get("tag_name")
		out := []map[string]any{}
		for _, d := range f.droplets {
			if tag == "" || slices.Contains(d["tags"].([]string), tag) {
				out = append(out, d)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"droplets": out})
	})
	mux.HandleFunc("DELETE /v2/droplets/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.PathValue("id"))
		f.mu.Lock()
		defer f.mu.Unlock()
		f.deleted = append(f.deleted, id)
		f.droplets = slices.DeleteFunc(f.droplets, func(d map[string]any) bool { return d["id"] == id })
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusNotImplemented)
	})
	return mux
}

// newCluster seeds: cp01, vm01 (cluster "demo"), old01 (demo orphan), vm02
// (same name as a spec host but untagged) and x01 (cluster "other").
func newCluster(t *testing.T) (*fakeDO, string) {
	f := &fakeDO{}
	f.add(1, "cp01", "manager", "dovm:demo")
	f.add(2, "vm01", "worker", "dovm:demo")
	f.add(3, "old01", "dovm:demo")
	f.add(4, "vm02", "worker")
	f.add(5, "x01", "dovm:other")
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	return f, srv.URL + "/"
}

func writeSpec(t *testing.T) string {
	path := filepath.Join(t.TempDir(), "cluster.yaml")
	data := `name: demo
region: nyc3
image: ubuntu-24-04-x64
roles:
  manager: {size: s-1vcpu-1gb, hosts: [cp01]}
  worker: {size: s-2vcpu-2gb, hosts: [vm01, vm02, vm03]}
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// execute runs dovm with args against apiURL, feeding stdin to prompts.
func execute(apiURL, stdin string, args ...string) (string, error) {
	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(append([]string{"--token", "test", "--api-url", apiURL}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestDestroyDryRunDeletesNothing(t *testing.T) {
	f, url := newCluster(t)
	out, err := execute(url, "", "destroy", writeSpec(t), "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 0 {
		t.Fatalf("dry run deleted %v", f.deleted)
	}
	for _, want := range []string{"cp01", "vm01", "Would delete 2 droplet(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "old01") {
		t.Errorf("orphan listed without --include-orphans:\n%s", out)
	}
}

func TestDestroyRequiresMatchingName(t *testing.T) {
	f, url := newCluster(t)
	_, err := execute(url, "yes\n", "destroy", writeSpec(t))
	if err == nil || !strings.Contains(err.Error(), "name did not match") {
		t.Fatalf("err = %v", err)
	}
	if len(f.deleted) != 0 {
		t.Fatalf("deleted %v after wrong confirmation", f.deleted)
	}
}

func TestDestroyDeletesOnlyTaggedSpecHosts(t *testing.T) {
	f, url := newCluster(t)
	out, err := execute(url, "demo\n", "destroy", writeSpec(t))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	slices.Sort(f.deleted)
	if !slices.Equal(f.deleted, []int{1, 2}) {
		t.Fatalf("deleted %v, want [1 2] (untagged vm02, orphan old01 and other cluster untouched)", f.deleted)
	}
	if !strings.Contains(out, "Deleted 2 of 2") {
		t.Errorf("output:\n%s", out)
	}
}

func TestDestroyIncludeOrphans(t *testing.T) {
	f, url := newCluster(t)
	if _, err := execute(url, "", "destroy", writeSpec(t), "--include-orphans", "--yes"); err != nil {
		t.Fatal(err)
	}
	slices.Sort(f.deleted)
	if !slices.Equal(f.deleted, []int{1, 2, 3}) {
		t.Fatalf("deleted %v, want [1 2 3]", f.deleted)
	}
}

func TestDestroyByName(t *testing.T) {
	f, url := newCluster(t)
	if _, err := execute(url, "", "destroy", "--name", "other", "--yes"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.deleted, []int{5}) {
		t.Fatalf("deleted %v, want [5]", f.deleted)
	}
}

func TestDestroyHostSubset(t *testing.T) {
	f, url := newCluster(t)
	out, err := execute(url, "", "destroy", writeSpec(t), "--host", "vm01,vm03", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.deleted, []int{2}) {
		t.Fatalf("deleted %v, want [2]", f.deleted)
	}
	if !strings.Contains(out, "vm03: no droplet tagged dovm:demo") {
		t.Errorf("output:\n%s", out)
	}
}

func TestDestroyArgumentErrors(t *testing.T) {
	_, url := newCluster(t)
	spec := writeSpec(t)
	cases := map[string][]string{
		"pass a SPEC or --name":          {"destroy"},
		"not both":                       {"destroy", spec, "--name", "demo"},
		"--include-orphans needs a SPEC": {"destroy", "--name", "demo", "--include-orphans"},
		`name "bad name" is invalid`:     {"destroy", "--name", "bad name"},
		`host "nope" is not in the spec`: {"destroy", spec, "--host", "nope"},
	}
	for want, args := range cases {
		if _, err := execute(url, "", args...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: err = %v, want %q", args, err, want)
		}
	}
}

func TestStatusJSON(t *testing.T) {
	_, url := newCluster(t)
	out, err := execute(url, "", "status", writeSpec(t), "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var r statusReport
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	states := map[string]string{}
	for _, h := range r.Hosts {
		states[h.Hostname] = h.State
	}
	want := map[string]string{"cp01": "exists", "vm01": "exists", "vm02": "conflict", "vm03": "missing"}
	for h, s := range want {
		if states[h] != s {
			t.Errorf("%s state = %q, want %q", h, states[h], s)
		}
	}
	if len(r.Orphans) != 1 || r.Orphans[0].Hostname != "old01" {
		t.Errorf("orphans = %+v", r.Orphans)
	}
	if r.Hosts[0].Droplet == nil || r.Hosts[0].Droplet.Public != "203.0.113.1" {
		t.Errorf("cp01 droplet = %+v", r.Hosts[0].Droplet)
	}
}

func TestStatusTable(t *testing.T) {
	_, url := newCluster(t)
	out, err := execute(url, "", "status", writeSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Orphans", "old01", "4 host(s): 2 active, 0 not active, 1 missing, 1 conflict; 1 orphan(s)."} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestList(t *testing.T) {
	_, url := newCluster(t)
	out, err := execute(url, "", "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []listEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Cluster+"/"+e.Hostname)
	}
	if want := []string{"demo/cp01", "demo/old01", "demo/vm01", "other/x01"}; !slices.Equal(got, want) {
		t.Errorf("list = %v, want %v", got, want)
	}

	out, err = execute(url, "", "list", "--cluster", "other")
	if err != nil || !strings.Contains(out, "x01") || strings.Contains(out, "cp01") {
		t.Errorf("filtered list: err=%v\n%s", err, out)
	}
	if _, err := execute(url, "", "list", "-o", "yaml"); err == nil {
		t.Error("expected error for unknown format")
	}
}

func TestCreateDryRunReportsOrphanAndConflict(t *testing.T) {
	// create validates against the catalog first, so serve those endpoints too.
	f := &fakeDO{}
	f.add(3, "old01", "dovm:demo")
	f.add(4, "vm02", "worker")
	mux := http.NewServeMux()
	mux.Handle("GET /v2/droplets", f.handler(t))
	mux.HandleFunc("GET /v2/regions", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"regions":[{"slug":"nyc3","available":true}]}`)
	})
	mux.HandleFunc("GET /v2/sizes", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"sizes":[{"slug":"s-1vcpu-1gb","available":true,"regions":["nyc3"]},{"slug":"s-2vcpu-2gb","available":true,"regions":["nyc3"]}]}`)
	})
	mux.HandleFunc("GET /v2/account/keys", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ssh_keys":[]}`)
	})
	mux.HandleFunc("GET /v2/images/ubuntu-24-04-x64", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"image":{"slug":"ubuntu-24-04-x64","status":"available"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, err := execute(srv.URL+"/", "", "create", writeSpec(t), "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "1 host(s) conflict") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if !strings.Contains(out, "droplet 3 (old01) is tagged dovm:demo but not in the spec") {
		t.Errorf("missing orphan warning:\n%s", out)
	}
}

func TestImport(t *testing.T) {
	dir := t.TempDir()
	hostFile := filepath.Join(dir, "hostname.txt")
	if err := os.WriteFile(hostFile, []byte("cp01\tmanager\nvm01\tworker\nvm02\tworker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	specFile := filepath.Join(dir, "cluster.yaml")
	args := []string{"import", hostFile, "-o", specFile, "--name", "k8s", "--size", "s-4vcpu-8gb",
		"--role-size", "manager=s-2vcpu-4gb", "--ssh-keys", "123,aa:bb", "--tags", "lab"}
	out, err := execute("", "", args...)
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if !strings.Contains(out, "3 host(s): manager: 1 (s-2vcpu-4gb), worker: 2 (s-4vcpu-8gb)") {
		t.Errorf("output:\n%s", out)
	}

	s, err := spec.Load(specFile)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "k8s" || s.Region != "nyc3" || !s.Monitoring || !slices.Equal(s.SSHKeys, []string{"123", "aa:bb"}) ||
		!slices.Equal(s.Roles["worker"].Hosts, []string{"vm01", "vm02"}) || s.Roles["manager"].Size != "s-2vcpu-4gb" {
		t.Errorf("spec = %+v", s)
	}

	if _, err := execute("", "", args...); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second import without --force: %v", err)
	}
	if _, err := execute("", "", "import", hostFile, "-o", "-", "--name", "k8s", "--role-size", "db=s-1"); err == nil ||
		!strings.Contains(err.Error(), `no hosts in`) {
		t.Errorf("unknown --role-size role: %v", err)
	}
	if _, err := execute("", "", "import", hostFile, "-o", "-"); err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("missing --name: %v", err)
	}
}
