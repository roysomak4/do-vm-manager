package engine

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/roysomak4/do-vm-manager/internal/spec"
)

const tag = "dovm:demo"

func testSpec(roles map[string]spec.Role) *spec.Spec {
	return &spec.Spec{Name: "demo", Region: "nyc3", Image: "ubuntu-24-04-x64",
		SSHKeys: []string{"123", "aa:bb"}, Tags: []string{"example"}, Monitoring: true, Roles: roles}
}

func twoRoles() *spec.Spec {
	return testSpec(map[string]spec.Role{
		"manager": {Size: "s-2vcpu-4gb", Hosts: []string{"cp01", "cp02", "cp03"}},
		"worker":  {Size: "s-4vcpu-8gb", Hosts: []string{"vm01"}},
	})
}

func fastOpts() Options {
	return Options{
		WaitTimeout:     2 * time.Second,
		PollInterval:    time.Millisecond,
		CreateRetryWait: time.Millisecond,
	}
}

func run(t *testing.T, f *fakeAPI, s *spec.Spec, opts Options) *Result {
	t.Helper()
	plan, err := BuildPlan(context.Background(), f, s)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), f, plan, opts)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func statuses(res *Result) map[string]Status {
	out := map[string]Status{}
	for _, h := range res.Hosts {
		out[h.Host.Name] = h.Status
	}
	return out
}

func TestBuildPlan(t *testing.T) {
	f := newFake()
	f.seed("cp01", "active", tag)            // adopted
	f.seed("cp02", "active", "someone-else") // conflict: untagged
	f.seed("cp03", "active", tag)            // conflict: duplicate
	f.seed("cp03", "active", tag)
	f.seed("unrelated", "active")

	plan, err := BuildPlan(context.Background(), f, twoRoles())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Create) != 1 || plan.Create[0].Name != "vm01" {
		t.Errorf("Create = %+v", plan.Create)
	}
	if len(plan.Existing) != 1 || plan.Existing[0].Host.Name != "cp01" {
		t.Fatalf("Existing = %+v", plan.Existing)
	}
	if want := "size is s-1vcpu-1gb, spec says s-2vcpu-4gb"; !slices.Contains(plan.Existing[0].Drift, want) {
		t.Errorf("Drift = %v", plan.Existing[0].Drift)
	}
	err = plan.Err()
	if err == nil || !strings.Contains(err.Error(), `cp02: droplet`) || !strings.Contains(err.Error(), `cp03: 2 droplets`) {
		t.Errorf("conflicts = %v", err)
	}
	if _, err := Run(context.Background(), f, plan, fastOpts()); err == nil {
		t.Error("Run should refuse a plan with conflicts")
	}
}

func TestRunCreatesInBatchesByRole(t *testing.T) {
	f := newFake()
	opts := fastOpts()
	opts.BatchSize = 2
	res := run(t, f, twoRoles(), opts)

	if !res.OK() {
		t.Fatalf("not OK: %v", statuses(res))
	}
	var batches [][]string
	for _, r := range f.requests {
		batches = append(batches, r.Names)
	}
	want := [][]string{{"cp01", "cp02"}, {"cp03"}, {"vm01"}}
	if len(batches) != len(want) {
		t.Fatalf("batches = %v, want %v", batches, want)
	}
	for _, w := range want {
		if !slices.ContainsFunc(batches, func(b []string) bool { return slices.Equal(b, w) }) {
			t.Errorf("missing batch %v in %v", w, batches)
		}
	}

	for _, r := range f.requests {
		if r.Names[0] == "vm01" {
			if r.Size != "s-4vcpu-8gb" || r.Region != "nyc3" || r.Image.Slug != "ubuntu-24-04-x64" || !r.Monitoring {
				t.Errorf("worker request = %+v", r)
			}
			if want := []string{"worker", "example", tag}; !slices.Equal(r.Tags, want) {
				t.Errorf("tags = %v, want %v", r.Tags, want)
			}
			if len(r.SSHKeys) != 2 || r.SSHKeys[0].ID != 123 || r.SSHKeys[1].Fingerprint != "aa:bb" {
				t.Errorf("ssh keys = %+v", r.SSHKeys)
			}
		}
	}
	for _, h := range res.Hosts {
		if ip, _ := h.Droplet.PublicIPv4(); ip == "" || !h.Created {
			t.Errorf("%s: ip=%q created=%v", h.Host.Name, ip, h.Created)
		}
	}
}

func TestRunAdoptsExistingDroplets(t *testing.T) {
	f := newFake()
	f.seed("cp01", "off", tag)
	res := run(t, f, twoRoles(), fastOpts())

	if !res.OK() {
		t.Fatalf("not OK: %v", statuses(res))
	}
	for _, r := range f.requests {
		if slices.Contains(r.Names, "cp01") {
			t.Fatal("existing host cp01 was created again")
		}
	}
	if res.Hosts[0].Host.Name != "cp01" || res.Hosts[0].Created {
		t.Errorf("cp01 result = %+v", res.Hosts[0])
	}
}

func TestRunRecoversFromCreateThatFailedButSucceeded(t *testing.T) {
	f := newFake()
	// The first request creates the droplets but the response is a 500.
	f.onCreate = func(call int) (bool, error) {
		if call == 1 {
			return true, apiError(http.StatusInternalServerError)
		}
		return true, nil
	}
	s := testSpec(map[string]spec.Role{"worker": {Size: "s-1", Hosts: []string{"vm01", "vm02"}}})
	res := run(t, f, s, fastOpts())

	if !res.OK() {
		t.Fatalf("not OK: %v", statuses(res))
	}
	if f.createCalls != 1 {
		t.Errorf("create called %d times; should have found the droplets instead of retrying", f.createCalls)
	}
	for _, name := range []string{"vm01", "vm02"} {
		if n := f.countNamed(name); n != 1 {
			t.Errorf("%d droplets named %s, want 1", n, name)
		}
	}
}

func TestRunRetriesRateLimitedCreate(t *testing.T) {
	f := newFake()
	f.onCreate = func(call int) (bool, error) {
		if call == 1 {
			return false, apiError(http.StatusTooManyRequests)
		}
		return true, nil
	}
	s := testSpec(map[string]spec.Role{"worker": {Size: "s-1", Hosts: []string{"vm01"}}})
	res := run(t, f, s, fastOpts())

	if !res.OK() || f.createCalls != 2 {
		t.Fatalf("OK=%v after %d calls", res.OK(), f.createCalls)
	}
}

func TestRunDoesNotRetryPermanentErrors(t *testing.T) {
	f := newFake()
	f.onCreate = func(call int) (bool, error) { return false, apiError(http.StatusUnprocessableEntity) }
	s := testSpec(map[string]spec.Role{"worker": {Size: "s-1", Hosts: []string{"vm01"}}})
	res := run(t, f, s, fastOpts())

	if f.createCalls != 1 {
		t.Errorf("create called %d times", f.createCalls)
	}
	if h := res.Hosts[0]; h.Status != StatusFailed || h.Err == nil {
		t.Errorf("result = %+v", h)
	}
}

func TestRunGivesUpAfterMaxAttempts(t *testing.T) {
	f := newFake()
	f.onCreate = func(call int) (bool, error) { return false, apiError(http.StatusServiceUnavailable) }
	s := testSpec(map[string]spec.Role{"worker": {Size: "s-1", Hosts: []string{"vm01"}}})
	opts := fastOpts()
	opts.CreateAttempts = 3
	res := run(t, f, s, opts)

	if f.createCalls != 3 {
		t.Errorf("create called %d times, want 3", f.createCalls)
	}
	if h := res.Hosts[0]; h.Status != StatusFailed || !strings.Contains(h.Err.Error(), "giving up after 3 attempts") {
		t.Errorf("result = %+v", h)
	}
}

func TestRunStopsRetryingIfCheckFails(t *testing.T) {
	f := newFake()
	f.onCreate = func(call int) (bool, error) { return false, apiError(http.StatusInternalServerError) }
	f.listByTagErr = errors.New("api down")
	s := testSpec(map[string]spec.Role{"worker": {Size: "s-1", Hosts: []string{"vm01"}}})
	res := run(t, f, s, fastOpts())

	if f.createCalls != 1 {
		t.Errorf("create retried %d times without confirming what exists", f.createCalls-1)
	}
	if res.Hosts[0].Status != StatusFailed {
		t.Errorf("status = %s", res.Hosts[0].Status)
	}
}

func TestRunTimesOutSlowDroplets(t *testing.T) {
	f := newFake()
	f.neverActive["vm02"] = true
	s := testSpec(map[string]spec.Role{"worker": {Size: "s-1", Hosts: []string{"vm01", "vm02"}}})
	opts := fastOpts()
	opts.WaitTimeout = 50 * time.Millisecond
	res := run(t, f, s, opts)

	if got := statuses(res); got["vm01"] != StatusReady || got["vm02"] != StatusTimedOut {
		t.Fatalf("statuses = %v", got)
	}
	if len(f.deleted) != 0 {
		t.Error("droplets deleted without --rollback-on-failure")
	}
}

func TestRunDetectsVanishedDroplet(t *testing.T) {
	f := newFake()
	f.vanish["vm01"] = true
	s := testSpec(map[string]spec.Role{"worker": {Size: "s-1", Hosts: []string{"vm01"}}})
	res := run(t, f, s, fastOpts())

	if h := res.Hosts[0]; h.Status != StatusFailed || !strings.Contains(h.Err.Error(), "disappeared") {
		t.Fatalf("result = %+v", h)
	}
}

func TestRollbackDeletesOnlyThisRunsDroplets(t *testing.T) {
	f := newFake()
	existing := f.seed("cp01", "active", tag)
	f.neverActive["vm01"] = true
	opts := fastOpts()
	opts.WaitTimeout = 50 * time.Millisecond
	opts.Rollback = true
	res := run(t, f, twoRoles(), opts)

	if !res.RolledBack {
		t.Fatal("expected rollback")
	}
	if slices.Contains(f.deleted, existing.ID) {
		t.Error("rollback deleted a droplet that existed before the run")
	}
	if len(f.deleted) != 3 {
		t.Errorf("deleted %v, want the 3 created droplets", f.deleted)
	}
	got := statuses(res)
	if got["cp01"] != StatusReady || got["cp02"] != StatusRolledBack || got["vm01"] != StatusRolledBack {
		t.Errorf("statuses = %v", got)
	}
	if res.OK() {
		t.Error("rolled-back run reported OK")
	}
}

func TestRollbackFindsDropletsWithLostResponses(t *testing.T) {
	f := newFake()
	// vm01's create succeeds; vm02's request creates the droplet but returns a 422,
	// so the engine never learns its ID. Rollback must still find and delete it.
	f.onCreate = func(call int) (bool, error) {
		if call == 2 {
			return true, apiError(http.StatusUnprocessableEntity)
		}
		return true, nil
	}
	s := testSpec(map[string]spec.Role{"a": {Size: "s-1", Hosts: []string{"vm01"}}, "b": {Size: "s-1", Hosts: []string{"vm02"}}})
	opts := fastOpts()
	opts.Concurrency = 1
	opts.Rollback = true
	res := run(t, f, s, opts)

	if !res.RolledBack || f.countNamed("vm01") != 0 || f.countNamed("vm02") != 0 {
		t.Fatalf("rolledBack=%v, deleted=%v, statuses=%v", res.RolledBack, f.deleted, statuses(res))
	}
}

func TestCancelDuringWaitRollsBack(t *testing.T) {
	f := newFake()
	f.neverActive["vm01"] = true
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	f.onPoll = func() { once.Do(cancel) }

	s := testSpec(map[string]spec.Role{"worker": {Size: "s-1", Hosts: []string{"vm01"}}})
	plan, err := BuildPlan(context.Background(), f, s)
	if err != nil {
		t.Fatal(err)
	}
	opts := fastOpts()
	opts.Rollback = true
	res, err := Run(ctx, f, plan, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !res.RolledBack || f.countNamed("vm01") != 0 {
		t.Fatalf("rolledBack=%v deleted=%v status=%s", res.RolledBack, f.deleted, res.Hosts[0].Status)
	}
}

func TestCancelWithoutRollbackLeavesDroplets(t *testing.T) {
	f := newFake()
	f.neverActive["vm01"] = true
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	f.onPoll = func() { once.Do(cancel) }

	s := testSpec(map[string]spec.Role{"worker": {Size: "s-1", Hosts: []string{"vm01"}}})
	plan, _ := BuildPlan(context.Background(), f, s)
	res, _ := Run(ctx, f, plan, fastOpts())

	if res.Hosts[0].Status != StatusCanceled || f.countNamed("vm01") != 1 {
		t.Fatalf("status=%s count=%d", res.Hosts[0].Status, f.countNamed("vm01"))
	}

	// Re-running adopts the droplet instead of creating a duplicate.
	f.neverActive["vm01"] = false
	res = run(t, f, s, fastOpts())
	if !res.OK() || f.countNamed("vm01") != 1 || f.createCalls != 1 {
		t.Fatalf("resume: OK=%v count=%d creates=%d", res.OK(), f.countNamed("vm01"), f.createCalls)
	}
}

func TestReporterReceivesEvents(t *testing.T) {
	f := newFake()
	var mu sync.Mutex
	kinds := map[EventKind]int{}
	opts := fastOpts()
	opts.Reporter = func(e Event) { mu.Lock(); kinds[e.Kind]++; mu.Unlock() }
	run(t, f, testSpec(map[string]spec.Role{"w": {Size: "s-1", Hosts: []string{"a", "b"}}}), opts)

	if kinds[EventCreateRequested] != 1 || kinds[EventCreated] != 2 || kinds[EventReady] != 2 {
		t.Errorf("events = %v", kinds)
	}
}
