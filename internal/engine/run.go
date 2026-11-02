package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/digitalocean/godo"
	"golang.org/x/sync/errgroup"

	"github.com/roysomak4/do-vm-manager/internal/cloud"
	"github.com/roysomak4/do-vm-manager/internal/spec"
)

// assignTimeout bounds moving droplets into a project at the end of a run.
const assignTimeout = time.Minute

// maxBatch is the most droplets DigitalOcean creates in one request.
const maxBatch = 10

// missingPollsBeforeFailure is how many consecutive polls a created droplet may
// be absent from listings before it is considered failed.
const missingPollsBeforeFailure = 3

// Options tunes a run. Zero values fall back to defaults.
type Options struct {
	WaitTimeout     time.Duration // how long to wait for all droplets to become ready
	PollInterval    time.Duration
	CreateAttempts  int           // attempts per batch on transient errors
	CreateRetryWait time.Duration // base backoff between attempts, multiplied by attempt number
	BatchSize       int
	Concurrency     int // create requests in flight at once
	Rollback        bool
	RollbackTimeout time.Duration
	UserData        string // cloud-init user data for new droplets; empty sends none
	Reporter        Reporter
}

func (o Options) withDefaults() Options {
	if o.WaitTimeout == 0 {
		o.WaitTimeout = 10 * time.Minute
	}
	if o.PollInterval == 0 {
		o.PollInterval = 5 * time.Second
	}
	if o.CreateAttempts == 0 {
		o.CreateAttempts = 4
	}
	if o.CreateRetryWait == 0 {
		o.CreateRetryWait = 5 * time.Second
	}
	if o.BatchSize <= 0 || o.BatchSize > maxBatch {
		o.BatchSize = maxBatch
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 4
	}
	if o.RollbackTimeout == 0 {
		o.RollbackTimeout = 2 * time.Minute
	}
	if o.Reporter == nil {
		o.Reporter = func(Event) {}
	}
	return o
}

// Status is the outcome for one host.
type Status string

const (
	StatusPending    Status = "pending"
	StatusReady      Status = "ready"
	StatusFailed     Status = "failed"
	StatusTimedOut   Status = "timed out"
	StatusCanceled   Status = "canceled"
	StatusRolledBack Status = "rolled back"
)

// HostResult is the outcome for one host.
type HostResult struct {
	Host    spec.Host
	Created bool // created by this run, as opposed to already existing
	Status  Status
	Droplet *godo.Droplet // as last seen; nil if no droplet was created
	Err     error
}

// Result is the outcome of a run, with hosts in spec order.
type Result struct {
	Hosts      []*HostResult
	RolledBack bool
	// ProjectErr is set if moving droplets into the plan's project failed. The
	// droplets still work; running create again retries the move.
	ProjectErr error
}

// Canceled reports whether the run was interrupted before every host finished.
func (r *Result) Canceled() bool {
	for _, h := range r.Hosts {
		if h.Status == StatusCanceled {
			return true
		}
	}
	return false
}

// OK reports whether every host is ready.
func (r *Result) OK() bool {
	for _, h := range r.Hosts {
		if h.Status != StatusReady {
			return false
		}
	}
	return true
}

// Run creates the plan's missing droplets and waits until every droplet is
// active with an IP. It returns an error only if the plan has conflicts; host
// failures are recorded in the Result. If opts.Rollback is set and any host
// fails or ctx is canceled, droplets created by this run are deleted.
func Run(ctx context.Context, api API, plan *Plan, opts Options) (*Result, error) {
	if err := plan.Err(); err != nil {
		return nil, err
	}
	r := &runner{api: api, plan: plan, opts: opts.withDefaults(), tag: plan.Spec.ClusterTag()}
	r.init()
	r.createAll(ctx)
	r.waitReady(ctx)
	if r.opts.Rollback && (ctx.Err() != nil || !r.result.OK()) {
		r.rollback(ctx)
	}
	if !r.result.RolledBack && plan.Project != nil {
		r.assignProject(ctx)
	}
	return r.result, nil
}

type runner struct {
	api    API
	plan   *Plan
	opts   Options
	tag    string
	result *Result
	byName map[string]*HostResult

	mu sync.Mutex // guards HostResult fields during concurrent creates
}

func (r *runner) emit(e Event) { r.opts.Reporter(e) }

func (r *runner) init() {
	existing := map[string]Existing{}
	for _, e := range r.plan.Existing {
		existing[e.Host.Name] = e
	}
	r.result = &Result{}
	r.byName = map[string]*HostResult{}
	for _, h := range r.plan.Spec.Hosts() {
		hr := &HostResult{Host: h, Status: StatusPending}
		if e, ok := existing[h.Name]; ok {
			d := e.Droplet
			hr.Droplet = &d
			for _, msg := range e.Drift {
				r.emit(Event{Kind: EventWarning, Hosts: []string{h.Name}, Detail: msg})
			}
			// A droplet that is not still provisioning is left as it is, even if powered off.
			if d.Status != "new" {
				hr.Status = StatusReady
			}
		} else {
			hr.Created = true
		}
		r.result.Hosts = append(r.result.Hosts, hr)
		r.byName[h.Name] = hr
	}
}

// createAll sends create requests in batches of hosts sharing a role.
func (r *runner) createAll(ctx context.Context) {
	var g errgroup.Group
	g.SetLimit(r.opts.Concurrency)
	for _, batch := range r.batches() {
		g.Go(func() error {
			r.createBatch(ctx, batch)
			return nil
		})
	}
	_ = g.Wait()
}

func (r *runner) batches() [][]spec.Host {
	var out [][]spec.Host
	var cur []spec.Host
	for _, h := range r.plan.Create {
		if len(cur) == r.opts.BatchSize || (len(cur) > 0 && cur[0].Role != h.Role) {
			out = append(out, cur)
			cur = nil
		}
		cur = append(cur, h)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// createBatch creates one batch. A transient failure may still have created
// some droplets, so before each retry it checks which exist and only asks for
// the rest. If that check fails it stops rather than risk duplicates.
func (r *runner) createBatch(ctx context.Context, hosts []spec.Host) {
	for attempt := 1; ; attempt++ {
		r.emit(Event{Kind: EventCreateRequested, Hosts: names(hosts), Detail: fmt.Sprintf("attempt %d", attempt)})
		droplets, err := r.api.CreateDroplets(ctx, r.request(hosts))
		if err == nil {
			r.recordCreated(droplets)
			return
		}
		if !cloud.IsTransient(err) {
			r.fail(ctx, hosts, err)
			return
		}
		if attempt >= r.opts.CreateAttempts {
			hosts = r.adoptCreated(ctx, hosts)
			r.fail(ctx, hosts, fmt.Errorf("giving up after %d attempts: %w", attempt, err))
			return
		}
		r.emit(Event{Kind: EventCreateRetry, Hosts: names(hosts), Err: err})
		if !sleep(ctx, time.Duration(attempt)*r.opts.CreateRetryWait) {
			r.fail(ctx, hosts, ctx.Err())
			return
		}
		var listErr error
		hosts, listErr = r.adoptCreatedErr(ctx, hosts)
		if listErr != nil {
			r.fail(ctx, hosts, fmt.Errorf("create failed (%v) and checking for partially created droplets also failed: %w", err, listErr))
			return
		}
		if len(hosts) == 0 {
			return
		}
	}
}

func (r *runner) request(hosts []spec.Host) *godo.DropletMultiCreateRequest {
	s := r.plan.Spec
	req := &godo.DropletMultiCreateRequest{
		Names:      names(hosts),
		Region:     s.Region,
		Size:       hosts[0].Size,
		Image:      imageRef(s.Image),
		Monitoring: s.Monitoring,
		IPv6:       s.IPv6,
		VPCUUID:    s.VPCUUID,
		Tags:       append(append([]string{}, hosts[0].Tags...), r.tag),
		UserData:   r.opts.UserData,
	}
	for _, k := range s.SSHKeys {
		req.SSHKeys = append(req.SSHKeys, sshKeyRef(k))
	}
	return req
}

func (r *runner) recordCreated(droplets []godo.Droplet) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range droplets {
		if hr, ok := r.byName[d.Name]; ok {
			hr.Droplet = &d
			r.emit(Event{Kind: EventCreated, Hosts: []string{d.Name}, DropletID: d.ID})
		}
	}
}

// adoptCreated records any of hosts that already have a tagged droplet and
// returns the rest. If listing fails, hosts is returned unchanged.
func (r *runner) adoptCreated(ctx context.Context, hosts []spec.Host) []spec.Host {
	rest, _ := r.adoptCreatedErr(ctx, hosts)
	return rest
}

func (r *runner) adoptCreatedErr(ctx context.Context, hosts []spec.Host) ([]spec.Host, error) {
	existing, err := r.api.DropletsByTag(ctx, r.tag)
	if err != nil {
		return hosts, err
	}
	found := map[string]godo.Droplet{}
	for _, d := range existing {
		found[d.Name] = d
	}
	var rest []spec.Host
	var adopted []godo.Droplet
	for _, h := range hosts {
		if d, ok := found[h.Name]; ok {
			adopted = append(adopted, d)
		} else {
			rest = append(rest, h)
		}
	}
	r.recordCreated(adopted)
	return rest, nil
}

func (r *runner) fail(ctx context.Context, hosts []spec.Host, err error) {
	if len(hosts) == 0 {
		return
	}
	status := StatusFailed
	if ctx.Err() != nil {
		status = StatusCanceled
	}
	r.mu.Lock()
	for _, h := range hosts {
		hr := r.byName[h.Name]
		hr.Status, hr.Err = status, err
	}
	r.mu.Unlock()
	r.emit(Event{Kind: EventFailed, Hosts: names(hosts), Err: err})
}

// waitReady polls the cluster's droplets with one list request per interval
// until every pending droplet is active with a public IPv4 address.
func (r *runner) waitReady(ctx context.Context) {
	pending := map[int]*HostResult{}
	for _, hr := range r.result.Hosts {
		if hr.Status == StatusPending && hr.Droplet != nil {
			pending[hr.Droplet.ID] = hr
		}
	}
	if len(pending) == 0 {
		return
	}

	waitCtx, cancel := context.WithTimeout(ctx, r.opts.WaitTimeout)
	defer cancel()
	ticker := time.NewTicker(r.opts.PollInterval)
	defer ticker.Stop()
	missing := map[int]int{}

	for len(pending) > 0 {
		select {
		case <-waitCtx.Done():
			status := StatusTimedOut
			if ctx.Err() != nil {
				status = StatusCanceled
			}
			for _, hr := range pending {
				hr.Status = status
				hr.Err = fmt.Errorf("droplet %d not ready after %s (status %q)", hr.Droplet.ID, r.opts.WaitTimeout, hr.Droplet.Status)
				r.emit(Event{Kind: EventFailed, Hosts: []string{hr.Host.Name}, DropletID: hr.Droplet.ID, Err: hr.Err})
			}
			return
		case <-ticker.C:
		}

		droplets, err := r.api.DropletsByTag(waitCtx, r.tag)
		if err != nil {
			if waitCtx.Err() == nil {
				r.emit(Event{Kind: EventWarning, Detail: "checking droplet status failed; will retry", Err: err})
			}
			continue
		}
		byID := map[int]godo.Droplet{}
		for _, d := range droplets {
			byID[d.ID] = d
		}
		for id, hr := range pending {
			d, ok := byID[id]
			if !ok {
				// A new droplet can briefly be missing from listings; one that stays
				// missing was destroyed, usually because provisioning failed.
				if missing[id]++; missing[id] >= missingPollsBeforeFailure {
					hr.Status = StatusFailed
					hr.Err = fmt.Errorf("droplet %d disappeared; provisioning likely failed", id)
					r.emit(Event{Kind: EventFailed, Hosts: []string{hr.Host.Name}, DropletID: id, Err: hr.Err})
					delete(pending, id)
				}
				continue
			}
			missing[id] = 0
			hr.Droplet = &d
			if ip, _ := d.PublicIPv4(); d.Status == "active" && ip != "" {
				hr.Status = StatusReady
				r.emit(Event{Kind: EventReady, Hosts: []string{hr.Host.Name}, DropletID: id, Detail: ip})
				delete(pending, id)
			}
		}
	}
}

// rollback deletes every droplet this run created. Droplets are found both by
// the IDs the run recorded and by listing the cluster tag, which catches
// droplets created by a request whose response was lost. Hosts that existed
// before the run are never touched. It runs even if ctx was canceled.
func (r *runner) rollback(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.opts.RollbackTimeout)
	defer cancel()
	r.emit(Event{Kind: EventRollback, Detail: "deleting droplets created by this run"})
	r.result.RolledBack = true

	toDelete := map[int]*HostResult{}
	for _, hr := range r.result.Hosts {
		if hr.Created && hr.Droplet != nil {
			toDelete[hr.Droplet.ID] = hr
		}
	}
	tagged, err := r.api.DropletsByTag(ctx, r.tag)
	if err != nil {
		r.emit(Event{Kind: EventWarning, Detail: "listing droplets for rollback failed; deleting only known droplets", Err: err})
	}
	for _, d := range tagged {
		if hr, ok := r.byName[d.Name]; ok && hr.Created {
			toDelete[d.ID] = hr
		}
	}

	deleteAll(ctx, r.api, slices.Collect(maps.Keys(toDelete)), r.opts.Concurrency, func(id int, err error) {
		hr := toDelete[id]
		if err != nil {
			hr.Err = errors.Join(hr.Err, fmt.Errorf("rollback: delete droplet %d: %w", id, err))
			r.emit(Event{Kind: EventFailed, Hosts: []string{hr.Host.Name}, DropletID: id, Err: err, Detail: "rollback delete failed"})
			return
		}
		hr.Status = StatusRolledBack
		r.emit(Event{Kind: EventDeleted, Hosts: []string{hr.Host.Name}, DropletID: id})
	})
}

// assignProject moves the droplets this run created, and existing ones outside
// the project, into the plan's project. Like rollback it runs even if ctx was
// canceled, so an interrupted run does not leave droplets in the wrong project.
func (r *runner) assignProject(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), assignTimeout)
	defer cancel()
	move := map[string]bool{}
	for _, e := range r.plan.Existing {
		move[e.Host.Name] = e.OutsideProject
	}
	var ids []int
	var hosts []string
	for _, hr := range r.result.Hosts {
		// Failed droplets with an ID have disappeared; assigning them would fail.
		if hr.Droplet == nil || hr.Status == StatusFailed || !(hr.Created || move[hr.Host.Name]) {
			continue
		}
		ids = append(ids, hr.Droplet.ID)
		hosts = append(hosts, hr.Host.Name)
	}
	if len(ids) == 0 {
		return
	}
	project := r.plan.Project
	if err := r.api.AssignDroplets(ctx, project.ID, ids); err != nil {
		r.result.ProjectErr = fmt.Errorf("move droplets into project %q: %w", project.Name, err)
		r.emit(Event{Kind: EventWarning, Hosts: hosts, Detail: fmt.Sprintf("could not move into project %q; run create again to retry", project.Name), Err: err})
		return
	}
	r.emit(Event{Kind: EventProject, Hosts: hosts, Detail: project.Name})
}

func names(hosts []spec.Host) []string {
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = h.Name
	}
	return out
}

func imageRef(ref string) godo.DropletCreateImage {
	if id, err := strconv.Atoi(ref); err == nil {
		return godo.DropletCreateImage{ID: id}
	}
	return godo.DropletCreateImage{Slug: ref}
}

func sshKeyRef(ref string) godo.DropletCreateSSHKey {
	if id, err := strconv.Atoi(ref); err == nil {
		return godo.DropletCreateSSHKey{ID: id}
	}
	return godo.DropletCreateSSHKey{Fingerprint: ref}
}

// sleep waits for d or until ctx is done, reporting whether the full wait elapsed.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
