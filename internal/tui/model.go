package tui

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"usbfs/internal/device"
	"usbfs/internal/fsops"
)

type pane struct {
	path      string
	entries   []fsops.Entry
	cursor    int
	filtering bool
	query     string
	matches   []fsops.Entry
}

func (p *pane) replace(path string, entries []fsops.Entry) {
	previous := ""
	visible := p.visible()
	if p.path != path {
		p.filtering, p.query = false, ""
	} else if p.cursor < len(visible) {
		previous = visible[p.cursor].Name
	}
	p.path, p.entries = path, entries
	p.setFilter(p.filtering, p.query)
	for i, entry := range p.visible() {
		if entry.Name == previous {
			p.cursor = i
			break
		}
	}
}

func (p *pane) visible() []fsops.Entry {
	if p.filtering {
		return p.matches
	}
	return p.entries
}

func (p *pane) setFilter(active bool, query string) {
	p.filtering, p.query, p.cursor = active, query, 0
	if !active {
		p.matches = nil
		return
	}
	p.matches = p.matches[:0]
	needle := filterText(query)
	for _, entry := range p.entries {
		if strings.Contains(filterText(entry.Name), needle) {
			p.matches = append(p.matches, entry)
		}
	}
}

func filterText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, value)
}

type Model struct {
	session       *device.Session
	panes         [2]pane
	focus         int
	platform      device.Platform
	device        device.Device
	ready, busy   bool
	mtpIssue      error
	confirm       bool
	status        string
	warning       bool
	width, height int
	spinner       spinner.Model
	progress      progress.Model
	transfer      *transfer
}

type transfer struct {
	name         string
	count, total atomic.Int64
}

type pollMsg struct{}
type transferTick struct{ copy *transfer }

func New(session *device.Session, cwd string) *Model {
	spin := spinner.New(spinner.WithSpinner(spinner.Dot))
	spin.Style = foreground(coral, surfaceElevated)
	return &Model{
		session: session, panes: [2]pane{{path: "."}, {path: cwd}},
		width: 80, height: 24, spinner: spin,
		progress: progress.New(progress.WithSolidFill(coral), progress.WithoutPercentage()),
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(poll(), m.spinner.Tick)
}

func poll() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return pollMsg{} })
}

func watch(copy *transfer) tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return transferTick{copy} })
}

type scanResult struct {
	device       device.Device
	external     []fsops.Entry
	local        []fsops.Entry
	err, hostErr error
}

type listResult struct {
	index   int
	path    string
	entries []fsops.Entry
	err     error
}

type refreshResult [2]listResult

func list(root *os.Root, index int, path string) listResult {
	entries, err := fsops.List(root, path)
	return listResult{index, path, entries, err}
}

func (m *Model) work(fn func() tea.Msg) tea.Cmd {
	session := m.session
	return func() tea.Msg {
		var result tea.Msg
		session.Work(func() { result = fn() })
		return result
	}
}

func (m *Model) scan() tea.Cmd {
	m.busy = true
	session, cwd, platform := m.session, m.panes[1].path, m.platform
	return m.work(func() tea.Msg {
		d, err := session.DetectAndMount(platform)
		result := scanResult{device: d, err: err}
		if err != nil || d.Kind == "" {
			return result
		}
		root, err := session.OpenRoot()
		if err != nil {
			result.err = err
			return result
		}
		defer root.Close()
		result.external, result.err = fsops.List(root, ".")
		if result.err == nil {
			result.local, result.hostErr = fsops.List(nil, cwd)
		}
		return result
	})
}

func (m *Model) navigate(index int, path string) tea.Cmd {
	m.busy = true
	session := m.session
	return m.work(func() tea.Msg {
		if index == 1 {
			return list(nil, index, path)
		}
		root, err := session.OpenRoot()
		if err != nil {
			return listResult{index: index, path: path, err: err}
		}
		defer root.Close()
		return list(root, index, path)
	})
}

func (m *Model) refresh() tea.Cmd {
	m.busy = true
	session, external, host := m.session, m.panes[0].path, m.panes[1].path
	return m.work(func() tea.Msg {
		result := refreshResult{{index: 0, path: external}, list(nil, 1, host)}
		root, err := session.OpenRoot()
		if err != nil {
			result[0].err = err
			return result
		}
		defer root.Close()
		result[0] = list(root, 0, external)
		return result
	})
}

type copyResult struct {
	download bool
	name     string
	target   listResult
	err      error
}

func (m *Model) startCopy(entry fsops.Entry) tea.Cmd {
	copy := &transfer{name: entry.Name}
	copy.total.Store(-1)
	m.busy, m.transfer = true, copy
	from, to := m.focus, 1-m.focus
	source, target := filepath.Join(m.panes[from].path, entry.Name), m.panes[to].path
	session := m.session
	run := m.work(func() tea.Msg {
		result := copyResult{download: from == 0, name: copy.name}
		root, err := session.OpenRoot()
		if err != nil {
			result.err = err
			return result
		}
		defer root.Close()
		report := func(count, total int64) {
			copy.total.Store(total)
			copy.count.Store(count)
		}
		if result.download {
			result.err = fsops.Download(root, source, target, report)
			result.target = list(nil, to, target)
		} else {
			result.err = fsops.Upload(root, source, target, report)
			result.target = list(root, to, target)
		}
		return result
	})
	return tea.Batch(run, watch(copy))
}
