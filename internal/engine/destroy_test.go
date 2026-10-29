package engine

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/roysomak4/do-vm-manager/internal/spec"
)

// seedCluster creates: cp01 and vm01 (in spec, tagged), old01 (tagged orphan),
// vm02 (in spec but untagged) and other (another cluster).
func seedCluster(f *fakeAPI) {
	f.seed("cp01", "active", tag)
	f.seed("vm01", "active", tag, "worker")
	f.seed("old01", "active", tag)
	f.seed("vm02", "active", "worker")
	f.seed("other", "active", "dovm:other")
}

func clusterSpec() *spec.Spec {
	return testSpec(map[string]spec.Role{
		"manager": {Size: "s-1", Hosts: []string{"cp01"}},
		"worker":  {Size: "s-1", Hosts: []string{"vm01", "vm02"}},
	})
}

func targetNames(ts []DestroyTarget) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Droplet.Name)
	}
	return out
}

func TestSelectForDestroy(t *testing.T) {
	cases := []struct {
		name     string
		spec     *spec.Spec
		sel      DestroySelection
		want     []string
		notFound []string
		err      string
	}{
		{name: "spec hosts only", spec: clusterSpec(), want: []string{"cp01", "vm01"}},
		{name: "with orphans", spec: clusterSpec(), sel: DestroySelection{IncludeOrphans: true}, want: []string{"cp01", "old01", "vm01"}},
		{name: "host subset", spec: clusterSpec(), sel: DestroySelection{Hosts: []string{"vm01"}}, want: []string{"vm01"}},
		{name: "untagged host is not found", spec: clusterSpec(), sel: DestroySelection{Hosts: []string{"vm02"}}, notFound: []string{"vm02"}},
		{name: "host not in spec", spec: clusterSpec(), sel: DestroySelection{Hosts: []string{"nope"}}, err: `host "nope" is not in the spec`},
		{name: "no spec: every tagged droplet", want: []string{"cp01", "old01", "vm01"}},
		{name: "no spec, host subset", sel: DestroySelection{Hosts: []string{"old01", "gone"}}, want: []string{"old01"}, notFound: []string{"gone"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			seedCluster(f)
			targets, notFound, err := SelectForDestroy(context.Background(), f, tag, tc.spec, tc.sel)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := targetNames(targets); !slices.Equal(got, tc.want) {
				t.Errorf("targets = %v, want %v", got, tc.want)
			}
			if !slices.Equal(notFound, tc.notFound) {
				t.Errorf("notFound = %v, want %v", notFound, tc.notFound)
			}
			for _, tg := range targets {
				if !slices.Contains(tg.Droplet.Tags, tag) {
					t.Errorf("selected untagged droplet %s", tg.Droplet.Name)
				}
				if tg.Orphan != (tc.spec != nil && tg.Droplet.Name == "old01") {
					t.Errorf("%s: Orphan = %v", tg.Droplet.Name, tg.Orphan)
				}
			}
		})
	}
}

func TestDestroyReportsEachOutcome(t *testing.T) {
	f := newFake()
	a := f.seed("a", "active", tag)
	b := f.seed("b", "active", tag)
	f.deleteErr = map[int]error{b.ID: apiError(http.StatusInternalServerError)}
	targets, _, err := SelectForDestroy(context.Background(), f, tag, nil, DestroySelection{})
	if err != nil {
		t.Fatal(err)
	}

	var deletedEvents, failedEvents int
	results := Destroy(context.Background(), f, targets, 2, func(e Event) {
		switch e.Kind {
		case EventDeleted:
			deletedEvents++
		case EventFailed:
			failedEvents++
		}
	})
	if len(results) != 2 || results[0].Err != nil || results[1].Err == nil {
		t.Fatalf("results = %+v", results)
	}
	if !slices.Equal(f.deleted, []int{a.ID}) || deletedEvents != 1 || failedEvents != 1 {
		t.Errorf("deleted=%v events: deleted=%d failed=%d", f.deleted, deletedEvents, failedEvents)
	}
}

func TestSelectForDestroyListError(t *testing.T) {
	f := newFake()
	f.listByTagErr = errors.New("api down")
	if _, _, err := SelectForDestroy(context.Background(), f, tag, nil, DestroySelection{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildPlanReportsOrphans(t *testing.T) {
	f := newFake()
	seedCluster(f)
	plan, err := BuildPlan(context.Background(), f, clusterSpec())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Orphans) != 1 || plan.Orphans[0].Name != "old01" {
		t.Errorf("Orphans = %+v", plan.Orphans)
	}
}
