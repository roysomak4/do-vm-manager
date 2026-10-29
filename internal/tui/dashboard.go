// Package tui provides the interactive terminal UI: a live progress dashboard
// for create and destroy, and the spec wizard.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/roysomak4/do-vm-manager/internal/engine"
)

// State is a dashboard row's state.
type State int

const (
	Queued State = iota
	Requesting
	Retrying
	Provisioning
	Deleting
	Ready
	Existing
	Deleted
	RolledBack
	Failed
	TimedOut
	Canceled
)

var stateLabels = map[State]string{
	Queued: "queued", Requesting: "requesting", Retrying: "retrying", Provisioning: "provisioning",
	Deleting: "deleting", Ready: "ready", Existing: "existing", Deleted: "deleted",
	RolledBack: "rolled back", Failed: "failed", TimedOut: "timed out", Canceled: "canceled",
}

func (s State) String() string { return stateLabels[s] }

// Done reports whether the state is final.
func (s State) Done() bool { return s >= Ready }

func (s State) failed() bool { return s >= Failed }

// Row is one droplet on the dashboard.
type Row struct {
	Name      string
	Role      string
	State     State
	DropletID int
	IP        string
	Detail    string // error or note

	started, finished time.Time
}

// Config describes a dashboard.
type Config struct {
	Title      string
	Rows       []Row
	CancelHint string // shown after ctrl+c, e.g. what cancel will do
}

// Work does the dashboard's job, reporting progress through report, and
// returns the final rows, which replace the live ones by name.
type Work func(ctx context.Context, report engine.Reporter) []Row

type eventMsg engine.Event

type doneMsg []Row

// Run shows the dashboard while work runs and returns when work finishes.
// ctrl+c cancels the context passed to work; the dashboard stays up until work
// returns, so cleanup such as rollback is still shown.
func Run(ctx context.Context, cfg Config, work Work, opts ...tea.ProgramOption) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	p := tea.NewProgram(newModel(cfg, cancel), opts...)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		rows := work(ctx, func(e engine.Event) { p.Send(eventMsg(e)) })
		p.Send(doneMsg(rows))
	}()

	_, err := p.Run()
	// Never return while work is still running, even if the UI failed.
	<-finished
	return err
}

const logLines = 5

type model struct {
	cfg         Config
	rows        []Row
	byName      map[string]int
	byID        map[int]int
	log         []string
	spinner     spinner.Model
	bar         progress.Model
	start, now  time.Time
	cancel      context.CancelFunc
	canceling   bool
	rollingBack bool
	done        bool
	width       int
}

func newModel(cfg Config, cancel context.CancelFunc) *model {
	now := time.Now()
	m := &model{
		cfg:     cfg,
		byName:  map[string]int{},
		byID:    map[int]int{},
		spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(styleActive)),
		bar:     progress.New(progress.WithoutPercentage(), progress.WithWidth(30)),
		start:   now,
		now:     now,
		cancel:  cancel,
		width:   100,
	}
	for i, r := range cfg.Rows {
		if !r.State.Done() {
			r.started = now
		}
		m.rows = append(m.rows, r)
		m.byName[r.Name] = i
		if r.DropletID != 0 {
			m.byID[r.DropletID] = i
		}
	}
	return m
}

func (m *model) Init() tea.Cmd { return m.spinner.Tick }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		m.now = time.Now()
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" && !m.canceling && !m.done {
			m.canceling = true
			m.addLog("canceling: " + m.cfg.CancelHint)
			m.cancel()
		}
	case eventMsg:
		m.apply(engine.Event(msg))
	case doneMsg:
		for _, r := range msg {
			i, ok := m.byName[r.Name]
			if !ok {
				continue
			}
			r.started = m.rows[i].started
			if r.finished.IsZero() {
				r.finished = m.rows[i].finished
			}
			if r.finished.IsZero() && r.State.Done() {
				r.finished = time.Now()
			}
			m.rows[i] = r
		}
		m.done = true
		m.now = time.Now()
		return m, tea.Quit
	}
	return m, nil
}

// row finds the row an event refers to, by droplet ID first, then name.
func (m *model) row(e engine.Event) *Row {
	if i, ok := m.byID[e.DropletID]; ok && e.DropletID != 0 {
		return &m.rows[i]
	}
	if len(e.Hosts) == 1 {
		if i, ok := m.byName[e.Hosts[0]]; ok {
			return &m.rows[i]
		}
	}
	return nil
}

func (m *model) apply(e engine.Event) {
	m.addLog(e.String())
	now := time.Now()
	set := func(r *Row, s State) {
		r.State = s
		if s.Done() && r.finished.IsZero() {
			r.finished = now
		}
	}
	switch e.Kind {
	case engine.EventCreateRequested, engine.EventCreateRetry:
		state := Requesting
		if e.Kind == engine.EventCreateRetry {
			state = Retrying
		}
		for _, h := range e.Hosts {
			if i, ok := m.byName[h]; ok {
				set(&m.rows[i], state)
			}
		}
	case engine.EventCreated:
		if r := m.row(e); r != nil {
			r.DropletID = e.DropletID
			m.byID[e.DropletID] = m.byName[r.Name]
			set(r, Provisioning)
		}
	case engine.EventReady:
		if r := m.row(e); r != nil {
			r.IP = e.Detail
			set(r, Ready)
		}
	case engine.EventFailed:
		for _, h := range e.Hosts {
			ev := e
			ev.Hosts = []string{h}
			if r := m.row(ev); r != nil {
				r.Detail = fmt.Sprint(e.Err)
				set(r, Failed)
			}
		}
	case engine.EventDeleted:
		if r := m.row(e); r != nil {
			if m.rollingBack {
				set(r, RolledBack)
			} else {
				set(r, Deleted)
			}
		}
	case engine.EventRollback:
		m.rollingBack = true
	case engine.EventWarning:
		if r := m.row(e); r != nil && r.Detail == "" {
			r.Detail = e.Detail
		}
	}
}

func (m *model) addLog(line string) {
	m.log = append(m.log, fmt.Sprintf("%s  %s", time.Now().Format("15:04:05"), line))
	if len(m.log) > logLines {
		m.log = m.log[len(m.log)-logLines:]
	}
}

var (
	styleTitle  = lipgloss.NewStyle().Bold(true)
	styleDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleActive = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleWarn   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)

func (m *model) icon(s State) string {
	switch {
	case s.failed():
		return styleErr.Render("✗")
	case s == RolledBack || s == Deleted:
		return styleWarn.Render("–")
	case s.Done():
		return styleOK.Render("✓")
	case s == Queued:
		return styleDim.Render("·")
	case m.done:
		return styleDim.Render("?")
	}
	return m.spinner.View()
}

func stateStyle(s State) lipgloss.Style {
	switch {
	case s.failed():
		return styleErr
	case s == RolledBack || s == Deleted || s == Retrying:
		return styleWarn
	case s.Done():
		return styleOK
	case s == Queued:
		return styleDim
	}
	return styleActive
}

func (m *model) View() tea.View {
	var b strings.Builder

	elapsed := m.now.Sub(m.start).Round(time.Second)
	fmt.Fprintf(&b, "%s  %s\n\n", styleTitle.Render(m.cfg.Title), styleDim.Render(formatDuration(elapsed)))

	var finished, failed int
	for _, r := range m.rows {
		if r.State.Done() {
			finished++
		}
		if r.State.failed() {
			failed++
		}
	}
	pct := 1.0
	if len(m.rows) > 0 {
		pct = float64(finished) / float64(len(m.rows))
	}
	summary := fmt.Sprintf("%d/%d done", finished, len(m.rows))
	if failed > 0 {
		summary += styleErr.Render(fmt.Sprintf(", %d failed", failed))
	}
	fmt.Fprintf(&b, "%s  %s\n\n", m.bar.ViewAs(pct), summary)

	nameW, roleW := 4, 0
	for _, r := range m.rows {
		nameW = max(nameW, lipgloss.Width(r.Name))
		roleW = max(roleW, lipgloss.Width(r.Role))
	}
	const stateW, idW, ipW, timeW = 12, 11, 15, 6
	fixed := 2 + 2 + nameW + 2 + roleW + 2 + stateW + 2 + idW + 2 + ipW + 2 + timeW + 2
	detailW := max(20, m.width-fixed)

	for _, r := range m.rows {
		var id, took string
		if r.DropletID != 0 {
			id = fmt.Sprint(r.DropletID)
		}
		if !r.started.IsZero() {
			end := r.finished
			if end.IsZero() {
				end = m.now
			}
			took = formatDuration(end.Sub(r.started))
		}
		detail := r.Detail
		if r.State.failed() {
			detail = styleErr.Render(truncate(detail, detailW))
		} else {
			detail = styleDim.Render(truncate(detail, detailW))
		}
		fmt.Fprintf(&b, "  %s %s  %s  %s  %s  %s  %s  %s\n",
			m.icon(r.State),
			pad(r.Name, nameW),
			pad(r.Role, roleW),
			stateStyle(r.State).Render(pad(r.State.String(), stateW)),
			pad(id, idW),
			pad(r.IP, ipW),
			styleDim.Render(pad(took, timeW)),
			detail)
	}

	if len(m.log) > 0 {
		b.WriteString("\n")
		for _, l := range m.log {
			b.WriteString(styleDim.Render("  "+truncate(l, max(20, m.width-2))) + "\n")
		}
	}
	switch {
	case m.done:
	case m.canceling:
		b.WriteString("\n" + styleWarn.Render("  canceling, waiting for in-progress work to finish…") + "\n")
	default:
		b.WriteString("\n" + styleDim.Render("  ctrl+c to cancel") + "\n")
	}
	return tea.NewView(b.String())
}

func pad(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

func truncate(s string, w int) string {
	s = strings.ReplaceAll(s, "\n", "; ")
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	return string(r[:w-1]) + "…"
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}
