package tui

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
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
	session              *device.Session
	panes                [2]pane
	focus                int
	platform             device.Platform
	device               device.Device
	ready, busy          bool
	mtpIssue             error
	confirm              bool
	status, statusDetail string
	warning              bool
	width, height        int
	spinner              spinner.Model
	transfer             *transfer
	drag                 *fileDrag
	create               *createPrompt
	deleteName           string
	help                 bool
	helpOffset           int
}

type createPrompt struct {
	folder bool
	input  textinput.Model
}

func (p *createPrompt) title() string {
	if p.folder {
		return "New folder"
	}
	return "New empty file"
}

type transfer struct {
	name, destination string
	count, total      atomic.Int64
}

type pollMsg struct{}
type transferTick struct{ copy *transfer }

func New(session *device.Session, cwd string) *Model {
	spin := spinner.New(spinner.WithSpinner(spinner.Dot))
	spin.Style = foreground(coral, surfaceElevated)
	return &Model{
		session: session, panes: [2]pane{{path: "."}, {path: cwd}},
		width: 80, height: 24, spinner: spin,
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
	warning      error
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

func listExternal(session *device.Session, path string) listResult {
	result := listResult{index: 0, path: path}
	if path == "." {
		shares, err := session.Shares()
		if err != nil || shares != nil {
			result.err = err
			for _, name := range shares {
				result.entries = append(result.entries, fsops.Entry{Name: name, Folder: true})
			}
			return result
		}
	}
	root, relative, err := session.OpenPath(path)
	if err != nil {
		result.err = err
		return result
	}
	defer root.Close()
	result.entries, result.err = fsops.List(root, relative)
	return result
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
		result := scanResult{device: d}
		if d.Kind == "" {
			result.err = err
			return result
		}
		result.warning = err
		external := listExternal(session, ".")
		result.external, result.err = external.entries, external.err
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
		return listExternal(session, path)
	})
}

func (m *Model) refresh() tea.Cmd {
	m.busy = true
	session, external, host := m.session, m.panes[0].path, m.panes[1].path
	return m.work(func() tea.Msg {
		return refreshResult{listExternal(session, external), list(nil, 1, host)}
	})
}

type copyResult struct {
	download    bool
	name        string
	destination string
	target      listResult
	err         error
}

func (m *Model) startCopy(from int, entry fsops.Entry, target string) tea.Cmd {
	copy := &transfer{name: entry.Name, destination: destinationLabel(1-from, target)}
	copy.total.Store(-1)
	m.busy, m.transfer = true, copy
	to := 1 - from
	source, viewPath := filepath.Join(m.panes[from].path, entry.Name), m.panes[to].path
	session := m.session
	run := m.work(func() tea.Msg {
		result := copyResult{download: from == 0, name: copy.name, destination: copy.destination}
		devicePath := target
		if result.download {
			devicePath = source
		}
		root, relative, err := session.OpenPath(devicePath)
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
			ids, err := session.FilesystemIDs()
			if err != nil {
				result.err = err
				return result
			}
			result.err = fsops.Download(root, relative, target, report, ids...)
			result.target = list(nil, to, viewPath)
		} else {
			result.err = fsops.Upload(root, source, relative, report)
			result.target = listExternal(session, viewPath)
		}
		return result
	})
	return tea.Batch(run, watch(copy))
}

type createResult struct {
	name   string
	target listResult
	err    error
}

func (m *Model) createItem(name string, folder bool) tea.Cmd {
	m.busy = true
	index := m.focus
	path, session := m.panes[index].path, m.session
	return m.work(func() tea.Msg {
		result := createResult{name: name}
		var root *os.Root
		var err error
		relative := "."
		if index == 0 {
			root, relative, err = session.OpenPath(path)
		} else {
			root, err = session.OpenHostDirectory(path)
		}
		if err != nil {
			result.err = err
			return result
		}
		defer root.Close()
		result.err = fsops.Create(root, relative, name, folder)
		if result.err == nil {
			if index == 0 {
				result.target = listExternal(session, path)
			} else {
				result.target = list(nil, index, path)
			}
		}
		return result
	})
}

type deleteResult struct {
	name   string
	target listResult
	err    error
}

func (m *Model) deleteItem(name string) tea.Cmd {
	m.busy = true
	path, session := m.panes[1].path, m.session
	return m.work(func() tea.Msg {
		result := deleteResult{name: name}
		result.err = session.RemoveHostFile(path, name)
		if result.err == nil {
			result.target = list(nil, 1, path)
		}
		return result
	})
}
