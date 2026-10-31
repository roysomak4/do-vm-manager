package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"

	"github.com/digitalocean/godo"
)

// fakeAPI is an in-memory DigitalOcean account. Droplets start as "new" and
// become active with IPs after activateAfter polls via DropletsByTag.
type fakeAPI struct {
	mu            sync.Mutex
	nextID        int
	droplets      []*godo.Droplet
	polls         map[int]int
	activateAfter int

	// onCreate decides the outcome of the nth create call (1-based): whether
	// the droplets are actually created, and what error to return.
	onCreate    func(call int) (create bool, err error)
	createCalls int
	requests    []godo.DropletMultiCreateRequest

	neverActive  map[string]bool // names that stay "new" forever
	vanish       map[string]bool // names removed right after creation
	listByTagErr error
	deleted      []int
	deleteErr    map[int]error
	onPoll       func() // called on each DropletsByTag

	projects  map[string][]int // project ID -> droplet IDs
	assignErr error
	assigns   [][]int
}

func newFake() *fakeAPI {
	return &fakeAPI{nextID: 1000, polls: map[int]int{}, activateAfter: 1, neverActive: map[string]bool{}, vanish: map[string]bool{}}
}

func apiError(code int) error {
	req := httptest.NewRequest(http.MethodPost, "https://api.digitalocean.com/v2/droplets", nil)
	return &godo.ErrorResponse{Response: &http.Response{StatusCode: code, Request: req}, Message: http.StatusText(code)}
}

// seed adds a pre-existing droplet.
func (f *fakeAPI) seed(name, status string, tags ...string) *godo.Droplet {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	d := &godo.Droplet{ID: f.nextID, Name: name, Status: status, Tags: tags, SizeSlug: "s-1vcpu-1gb",
		Region: &godo.Region{Slug: "nyc3"}, Networks: networks(f.nextID)}
	f.droplets = append(f.droplets, d)
	return d
}

func networks(id int) *godo.Networks {
	return &godo.Networks{V4: []godo.NetworkV4{
		{IPAddress: fmt.Sprintf("203.0.113.%d", id%250), Type: "public"},
		{IPAddress: fmt.Sprintf("10.0.0.%d", id%250), Type: "private"},
	}}
}

func (f *fakeAPI) Droplets(ctx context.Context) ([]godo.Droplet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []godo.Droplet
	for _, d := range f.droplets {
		out = append(out, *d)
	}
	return out, nil
}

func (f *fakeAPI) DropletsByTag(ctx context.Context, tag string) ([]godo.Droplet, error) {
	if f.onPoll != nil {
		f.onPoll()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listByTagErr != nil {
		return nil, f.listByTagErr
	}
	var out []godo.Droplet
	for _, d := range f.droplets {
		if !slices.Contains(d.Tags, tag) {
			continue
		}
		f.polls[d.ID]++
		if d.Status == "new" && f.polls[d.ID] >= f.activateAfter && !f.neverActive[d.Name] {
			d.Status = "active"
			d.Networks = networks(d.ID)
		}
		out = append(out, *d)
	}
	return out, nil
}

func (f *fakeAPI) CreateDroplets(ctx context.Context, req *godo.DropletMultiCreateRequest) ([]godo.Droplet, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	f.requests = append(f.requests, *req)
	create, err := true, error(nil)
	if f.onCreate != nil {
		create, err = f.onCreate(f.createCalls)
	}
	var out []godo.Droplet
	if create {
		for _, name := range req.Names {
			f.nextID++
			d := &godo.Droplet{ID: f.nextID, Name: name, Status: "new", Tags: slices.Clone(req.Tags),
				SizeSlug: req.Size, Region: &godo.Region{Slug: req.Region}}
			if !f.vanish[name] {
				f.droplets = append(f.droplets, d)
			}
			out = append(out, *d)
		}
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (f *fakeAPI) DeleteDroplet(ctx context.Context, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.deleteErr[id]; err != nil {
		return err
	}
	f.deleted = append(f.deleted, id)
	f.droplets = slices.DeleteFunc(f.droplets, func(d *godo.Droplet) bool { return d.ID == id })
	return nil
}

func (f *fakeAPI) countNamed(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, d := range f.droplets {
		if d.Name == name {
			n++
		}
	}
	return n
}

func (f *fakeAPI) ProjectDropletIDs(ctx context.Context, projectID string) ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.projects[projectID]), nil
}

func (f *fakeAPI) AssignDroplets(ctx context.Context, projectID string, ids []int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assigns = append(f.assigns, slices.Clone(ids))
	if f.assignErr != nil {
		return f.assignErr
	}
	if f.projects == nil {
		f.projects = map[string][]int{}
	}
	for _, id := range ids {
		if !slices.Contains(f.projects[projectID], id) {
			f.projects[projectID] = append(f.projects[projectID], id)
		}
	}
	return nil
}
