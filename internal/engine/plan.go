// Package engine reconciles a spec against the droplets in an account and
// creates the ones that are missing.
package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/digitalocean/godo"

	"github.com/roysomak4/do-vm-manager/internal/spec"
)

// API is the subset of the DigitalOcean client the engine uses.
type API interface {
	Droplets(ctx context.Context) ([]godo.Droplet, error)
	DropletsByTag(ctx context.Context, tag string) ([]godo.Droplet, error)
	CreateDroplets(ctx context.Context, req *godo.DropletMultiCreateRequest) ([]godo.Droplet, error)
	DeleteDroplet(ctx context.Context, id int) error
}

// Plan says what a create run will do for each host in a spec.
type Plan struct {
	Spec      *spec.Spec
	Create    []spec.Host
	Existing  []Existing
	Conflicts []Conflict
	Orphans   []godo.Droplet // carry the cluster tag but are not named in the spec
}

// Existing is a host whose droplet already exists and carries the spec's cluster tag.
type Existing struct {
	Host    spec.Host
	Droplet godo.Droplet
	Drift   []string // differences from the spec; reported, never changed
}

// Conflict is a host that cannot be created or adopted safely.
type Conflict struct {
	Host   spec.Host
	Reason string
}

// BuildPlan lists every droplet once and classifies each host:
//   - no droplet with that name: create it
//   - exactly one, carrying the cluster tag: adopt it as existing
//   - any droplet with that name lacking the tag, or several tagged: conflict
//
// Tagged droplets whose name is not in the spec are reported as orphans.
func BuildPlan(ctx context.Context, api API, s *spec.Spec) (*Plan, error) {
	all, err := api.Droplets(ctx)
	if err != nil {
		return nil, fmt.Errorf("list droplets: %w", err)
	}
	byName := map[string][]godo.Droplet{}
	for _, d := range all {
		byName[d.Name] = append(byName[d.Name], d)
	}

	tag := s.ClusterTag()
	p := &Plan{Spec: s}
	for _, h := range s.Hosts() {
		var owned, foreign []godo.Droplet
		for _, d := range byName[h.Name] {
			if slices.Contains(d.Tags, tag) {
				owned = append(owned, d)
			} else {
				foreign = append(foreign, d)
			}
		}
		switch {
		case len(foreign) > 0:
			p.Conflicts = append(p.Conflicts, Conflict{Host: h, Reason: fmt.Sprintf(
				"droplet %d is named %q but is not tagged %q; tag it to adopt it, or rename the host",
				foreign[0].ID, h.Name, tag)})
		case len(owned) > 1:
			p.Conflicts = append(p.Conflicts, Conflict{Host: h, Reason: fmt.Sprintf(
				"%d droplets named %q are tagged %q; delete the extras", len(owned), h.Name, tag)})
		case len(owned) == 1:
			p.Existing = append(p.Existing, Existing{Host: h, Droplet: owned[0], Drift: drift(s, h, owned[0])})
		default:
			p.Create = append(p.Create, h)
		}
	}

	inSpec := map[string]bool{}
	for _, h := range s.Hosts() {
		inSpec[h.Name] = true
	}
	for _, d := range all {
		if !inSpec[d.Name] && slices.Contains(d.Tags, tag) {
			p.Orphans = append(p.Orphans, d)
		}
	}
	return p, nil
}

// Err returns the plan's conflicts as one error, or nil.
func (p *Plan) Err() error {
	var errs []error
	for _, c := range p.Conflicts {
		errs = append(errs, fmt.Errorf("%s: %s", c.Host.Name, c.Reason))
	}
	return errors.Join(errs...)
}

func drift(s *spec.Spec, h spec.Host, d godo.Droplet) []string {
	var out []string
	if d.Region != nil && d.Region.Slug != s.Region {
		out = append(out, fmt.Sprintf("region is %s, spec says %s", d.Region.Slug, s.Region))
	}
	if d.SizeSlug != "" && d.SizeSlug != h.Size {
		out = append(out, fmt.Sprintf("size is %s, spec says %s", d.SizeSlug, h.Size))
	}
	return out
}
