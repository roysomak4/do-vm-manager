package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/roysomak4/do-vm-manager/internal/engine"
)

func testModel() (*model, *bool) {
	canceled := false
	m := newModel(Config{
		Title:      "Creating cluster demo",
		CancelHint: "rolling back",
		Rows: []Row{
			{Name: "cp01", Role: "manager", State: Existing, DropletID: 7, IP: "203.0.113.7"},
			{Name: "vm01", Role: "worker", State: Queued},
			{Name: "vm02", Role: "worker", State: Queued},
		},
	}, func() { canceled = true })
	return m, &canceled
}

func send(m *model, msgs ...tea.Msg) {
	for _, msg := range msgs {
		m.Update(msg)
	}
}

func view(m *model) string { return m.View().Content }

func TestDashboardFollowsCreateEvents(t *testing.T) {
	m, _ := testModel()
	send(m,
		eventMsg{Kind: engine.EventCreateRequested, Hosts: []string{"vm01", "vm02"}, Detail: "attempt 1"},
		eventMsg{Kind: engine.EventCreated, Hosts: []string{"vm01"}, DropletID: 101},
		eventMsg{Kind: engine.EventCreated, Hosts: []string{"vm02"}, DropletID: 102},
		eventMsg{Kind: engine.EventReady, Hosts: []string{"vm01"}, DropletID: 101, Detail: "198.51.100.1"},
	)
	if got := m.rows[1]; got.State != Ready || got.IP != "198.51.100.1" || got.DropletID != 101 {
		t.Errorf("vm01 = %+v", got)
	}
	if got := m.rows[2]; got.State != Provisioning || got.DropletID != 102 {
		t.Errorf("vm02 = %+v", got)
	}

	out := view(m)
	for _, want := range []string{"Creating cluster demo", "2/3 done", "198.51.100.1", "provisioning", "ctrl+c to cancel", "vm01: ready at 198.51.100.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
}

func TestDashboardFailureAndRollback(t *testing.T) {
	m, _ := testModel()
	send(m,
		eventMsg{Kind: engine.EventCreated, Hosts: []string{"vm01"}, DropletID: 101},
		eventMsg{Kind: engine.EventFailed, Hosts: []string{"vm02"}, Err: errors.New("quota exceeded")},
		eventMsg{Kind: engine.EventRollback, Detail: "deleting droplets created by this run"},
		eventMsg{Kind: engine.EventDeleted, Hosts: []string{"vm01"}, DropletID: 101},
	)
	if m.rows[1].State != RolledBack || m.rows[2].State != Failed || m.rows[2].Detail != "quota exceeded" {
		t.Errorf("rows = %+v", m.rows)
	}
	if m.rows[0].State != Existing {
		t.Errorf("existing row changed: %+v", m.rows[0])
	}
	if out := view(m); !strings.Contains(out, "1 failed") || !strings.Contains(out, "quota exceeded") {
		t.Errorf("view:\n%s", out)
	}
}

func TestDashboardCtrlCCancelsOnce(t *testing.T) {
	m, canceled := testModel()
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !*canceled || !m.canceling {
		t.Fatal("ctrl+c did not cancel")
	}
	if cmd != nil {
		t.Error("ctrl+c should not quit before the work finishes")
	}
	if out := view(m); !strings.Contains(out, "canceling: rolling back") || !strings.Contains(out, "waiting for in-progress work") {
		t.Errorf("view:\n%s", out)
	}
}

func TestDashboardDoneAppliesFinalRows(t *testing.T) {
	m, _ := testModel()
	_, cmd := m.Update(doneMsg{
		{Name: "vm01", Role: "worker", State: Ready, DropletID: 101, IP: "198.51.100.1"},
		{Name: "vm02", Role: "worker", State: TimedOut, DropletID: 102, Detail: "not ready after 10m0s"},
	})
	if cmd == nil {
		t.Fatal("done should quit")
	}
	if m.rows[2].State != TimedOut || m.rows[0].State != Existing {
		t.Errorf("rows = %+v", m.rows)
	}
	out := view(m)
	if strings.Contains(out, "ctrl+c") {
		t.Errorf("finished view still shows the cancel hint:\n%s", out)
	}
	if !strings.Contains(out, "3/3 done") || !strings.Contains(out, "timed out") {
		t.Errorf("view:\n%s", out)
	}
}

func TestDashboardDestroyMatchesByDropletID(t *testing.T) {
	// Two droplets with the same name must be tracked separately.
	m := newModel(Config{Rows: []Row{
		{Name: "dup", State: Deleting, DropletID: 1},
		{Name: "dup", State: Deleting, DropletID: 2},
	}}, func() {})
	send(m, eventMsg{Kind: engine.EventDeleted, Hosts: []string{"dup"}, DropletID: 2})
	if m.rows[0].State != Deleting || m.rows[1].State != Deleted {
		t.Errorf("rows = %+v", m.rows)
	}
}

func TestRunWaitsForWork(t *testing.T) {
	var out bytes.Buffer
	workDone := false
	err := Run(context.Background(), Config{Title: "Test", Rows: []Row{{Name: "a", State: Queued}}},
		func(ctx context.Context, report engine.Reporter) []Row {
			report(engine.Event{Kind: engine.EventCreateRequested, Hosts: []string{"a"}})
			time.Sleep(50 * time.Millisecond)
			report(engine.Event{Kind: engine.EventReady, Hosts: []string{"a"}, Detail: "192.0.2.1"})
			workDone = true
			return []Row{{Name: "a", State: Ready, IP: "192.0.2.1"}}
		},
		tea.WithInput(nil), tea.WithOutput(&out), tea.WithWindowSize(120, 40))
	if err != nil {
		t.Fatal(err)
	}
	if !workDone {
		t.Fatal("Run returned before work finished")
	}
	if !strings.Contains(out.String(), "192.0.2.1") {
		t.Errorf("final frame missing IP:\n%s", out.String())
	}
}

func TestFormatHelpers(t *testing.T) {
	if got := formatDuration(75 * time.Second); got != "1:15" {
		t.Errorf("formatDuration = %q", got)
	}
	if got := truncate("abcdefghij", 5); got != "abcd…" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("a\nb", 10); got != "a; b" {
		t.Errorf("truncate newline = %q", got)
	}
}
