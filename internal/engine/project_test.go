package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digitalocean/godo"

	"github.com/roysomak4/do-vm-manager/internal/spec"
)

var lab = &godo.Project{ID: "proj-lab", Name: "Lab"}

func runInProject(t *testing.T, f *fakeAPI, s *spec.Spec, opts Options) (*Plan, *Result) {
	t.Helper()
	plan, err := BuildPlan(context.Background(), f, s)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.UseProject(context.Background(), f, lab); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), f, plan, opts)
	if err != nil {
		t.Fatal(err)
	}
	return plan, res
}

func TestProjectMovesCreatedAndOutsideDroplets(t *testing.T) {
	f := newFake()
	outside := f.seed("cp01", "active", tag)
	inside := f.seed("cp02", "active", tag)
	f.projects = map[string][]int{lab.ID: {inside.ID}}

	var mu sync.Mutex
	var events []Event
	opts := fastOpts()
	opts.Reporter = func(e Event) { mu.Lock(); events = append(events, e); mu.Unlock() }
	plan, res := runInProject(t, f, twoRoles(), opts)

	if moves := plan.ToMove(); len(moves) != 1 || moves[0].Host.Name != "cp01" {
		t.Errorf("ToMove = %+v, want only cp01", moves)
	}
	if !res.OK() || res.ProjectErr != nil {
		t.Fatalf("OK=%v ProjectErr=%v", res.OK(), res.ProjectErr)
	}
	if len(f.assigns) != 1 {
		t.Fatalf("assign calls = %v, want 1", f.assigns)
	}
	got := f.assigns[0]
	if !slices.Contains(got, outside.ID) || slices.Contains(got, inside.ID) || len(got) != 3 {
		t.Errorf("assigned %v: want cp01 (%d) and the 2 created droplets, not cp02 (%d)", got, outside.ID, inside.ID)
	}
	last := events[len(events)-1]
	if last.Kind != EventProject || !strings.Contains(last.String(), "into project Lab") {
		t.Errorf("last event = %v", last)
	}
}

func TestNoProjectAssignsNothing(t *testing.T) {
	f := newFake()
	f.seed("cp01", "active", tag)
	if res := run(t, f, twoRoles(), fastOpts()); !res.OK() {
		t.Fatal("not OK")
	}
	if len(f.assigns) != 0 {
		t.Errorf("assigned %v without a project", f.assigns)
	}
}

func TestProjectNothingToMove(t *testing.T) {
	f := newFake()
	d := f.seed("vm01", "active", tag)
	f.projects = map[string][]int{lab.ID: {d.ID}}
	s := testSpec(map[string]spec.Role{"worker": {Size: "s-1", Hosts: []string{"vm01"}}})
	if _, res := runInProject(t, f, s, fastOpts()); !res.OK() || len(f.assigns) != 0 {
		t.Errorf("OK=%v assigns=%v; want no assign call", res.OK(), f.assigns)
	}
}

func TestProjectFailureIsAWarning(t *testing.T) {
	f := newFake()
	f.assignErr = errors.New("forbidden")
	s := testSpec(map[string]spec.Role{"worker": {Size: "s-1", Hosts: []string{"vm01"}}})
	_, res := runInProject(t, f, s, fastOpts())
	if !res.OK() {
		t.Error("a failed project move should not fail the hosts")
	}
	if res.ProjectErr == nil || !strings.Contains(res.ProjectErr.Error(), `project "Lab"`) {
		t.Errorf("ProjectErr = %v", res.ProjectErr)
	}
}

func TestProjectSkippedAfterRollback(t *testing.T) {
	f := newFake()
	f.neverActive["vm01"] = true
	opts := fastOpts()
	opts.WaitTimeout = 20 * time.Millisecond
	opts.Rollback = true
	s := testSpec(map[string]spec.Role{"worker": {Size: "s-1", Hosts: []string{"vm01"}}})
	if _, res := runInProject(t, f, s, opts); !res.RolledBack || len(f.assigns) != 0 {
		t.Errorf("rolledBack=%v assigns=%v", res.RolledBack, f.assigns)
	}
}

func TestProjectAssignedAfterCancel(t *testing.T) {
	f := newFake()
	f.neverActive["vm01"] = true
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	f.onPoll = func() { once.Do(cancel) }
	s := testSpec(map[string]spec.Role{"worker": {Size: "s-1", Hosts: []string{"vm01"}}})
	plan, _ := BuildPlan(context.Background(), f, s)
	if err := plan.UseProject(context.Background(), f, lab); err != nil {
		t.Fatal(err)
	}
	res, _ := Run(ctx, f, plan, fastOpts())
	if !res.Canceled() || len(f.assigns) != 1 || res.ProjectErr != nil {
		t.Errorf("canceled=%v assigns=%v err=%v; the kept droplet should still be moved", res.Canceled(), f.assigns, res.ProjectErr)
	}
}
