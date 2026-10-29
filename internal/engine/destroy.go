package engine

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/digitalocean/godo"
	"golang.org/x/sync/errgroup"

	"github.com/roysomak4/do-vm-manager/internal/spec"
)

// DestroySelection narrows which of a cluster's droplets to destroy.
type DestroySelection struct {
	Hosts          []string // only these hostnames; empty means all
	IncludeOrphans bool     // also tagged droplets whose name is not in the spec
}

// DestroyTarget is a droplet selected for deletion.
type DestroyTarget struct {
	Droplet godo.Droplet
	Orphan  bool // carries the cluster tag but is not named in the spec
}

// SelectForDestroy picks the droplets to delete. Only droplets carrying the
// cluster tag are ever selected. With a spec, droplets named in it are
// selected, plus orphans if requested; without one (s == nil), every tagged
// droplet is. It also returns requested hosts that have no tagged droplet.
func SelectForDestroy(ctx context.Context, api API, tag string, s *spec.Spec, sel DestroySelection) ([]DestroyTarget, []string, error) {
	inSpec := map[string]bool{}
	if s != nil {
		for _, h := range s.Hosts() {
			inSpec[h.Name] = true
		}
		for _, h := range sel.Hosts {
			if !inSpec[h] {
				return nil, nil, fmt.Errorf("host %q is not in the spec", h)
			}
		}
	}

	tagged, err := api.DropletsByTag(ctx, tag)
	if err != nil {
		return nil, nil, fmt.Errorf("list droplets tagged %s: %w", tag, err)
	}

	var targets []DestroyTarget
	found := map[string]bool{}
	for _, d := range tagged {
		orphan := s != nil && !inSpec[d.Name]
		var selected bool
		if len(sel.Hosts) > 0 {
			selected = slices.Contains(sel.Hosts, d.Name)
		} else {
			selected = !orphan || sel.IncludeOrphans
		}
		if selected {
			targets = append(targets, DestroyTarget{Droplet: d, Orphan: orphan})
			found[d.Name] = true
		}
	}
	slices.SortFunc(targets, func(a, b DestroyTarget) int {
		return cmp.Or(cmp.Compare(a.Droplet.Name, b.Droplet.Name), cmp.Compare(a.Droplet.ID, b.Droplet.ID))
	})

	var notFound []string
	for _, h := range sel.Hosts {
		if !found[h] {
			notFound = append(notFound, h)
		}
	}
	return targets, notFound, nil
}

// DestroyResult is the outcome of deleting one droplet.
type DestroyResult struct {
	Target DestroyTarget
	Err    error
}

// Destroy deletes the targets concurrently and reports each outcome. Results
// are in the same order as targets.
func Destroy(ctx context.Context, api API, targets []DestroyTarget, concurrency int, report Reporter) []DestroyResult {
	if concurrency <= 0 {
		concurrency = 4
	}
	if report == nil {
		report = func(Event) {}
	}
	results := make([]DestroyResult, len(targets))
	index := map[int]int{}
	ids := make([]int, len(targets))
	for i, t := range targets {
		results[i].Target = t
		index[t.Droplet.ID] = i
		ids[i] = t.Droplet.ID
	}
	deleteAll(ctx, api, ids, concurrency, func(id int, err error) {
		i := index[id]
		results[i].Err = err
		name := targets[i].Droplet.Name
		if err != nil {
			report(Event{Kind: EventFailed, Hosts: []string{name}, DropletID: id, Err: err})
		} else {
			report(Event{Kind: EventDeleted, Hosts: []string{name}, DropletID: id})
		}
	})
	return results
}

// deleteAll deletes droplets with at most concurrency requests in flight.
// done is called once per droplet, never concurrently.
func deleteAll(ctx context.Context, api API, ids []int, concurrency int, done func(id int, err error)) {
	var mu sync.Mutex
	var g errgroup.Group
	g.SetLimit(concurrency)
	for _, id := range ids {
		g.Go(func() error {
			err := api.DeleteDroplet(ctx, id)
			mu.Lock()
			defer mu.Unlock()
			done(id, err)
			return nil
		})
	}
	_ = g.Wait()
}
