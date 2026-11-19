package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"usbfs/internal/device"
	"usbfs/internal/fsops"
)

type openResult struct {
	done     <-chan error
	name     string
	external bool
	err      error
}

type openerExit struct{ err error }

func (m *Model) openFile(index int, path, name string) tea.Cmd {
	m.busy = true
	session := m.session
	return m.work(func() tea.Msg {
		result := openResult{name: name, external: index == 0}
		var root *os.Root
		if result.external {
			var err error
			root, err = session.OpenRoot()
			if err != nil {
				result.err = err
				return result
			}
			defer root.Close()
		}
		result.done, result.err = fsops.Launch(root, path)
		return result
	})
}

func (m *Model) message(message string, warning bool) {
	m.status, m.warning = message, warning
}

func (m *Model) applyList(result listResult) bool {
	if result.err != nil {
		if result.index == 0 && errors.Is(result.err, device.ErrUnmounted) {
			m.ready, m.device = false, device.Device{}
		}
		if result.index == 0 && errors.Is(result.err, os.ErrPermission) {
			m.message("Permission denied: external device restricts access to this folder", true)
		} else {
			m.message("Cannot read folder: "+result.err.Error(), true)
		}
		return false
	}
	m.panes[result.index].replace(result.path, result.entries)
	return true
}

func (m *Model) copyFinished(result copyResult) {
	m.busy, m.transfer = false, nil
	if result.target.path != "" && result.target.err == nil {
		m.panes[result.target.index].replace(result.target.path, result.target.entries)
	}
	if result.err == nil {
		destination := "external device"
		if result.download {
			destination = "Linux"
		}
		m.message("Copied "+result.name+" to "+destination, false)
		if result.target.err != nil {
			m.message("File copied. Cannot refresh destination: "+result.target.err.Error(), true)
		}
		return
	}
	var stage *fsops.StageError
	errors.As(result.err, &stage)
	switch {
	case !result.download && errors.Is(result.err, os.ErrExist):
		m.message("Overwrite blocked: file already exists on external device", true)
	case result.download && errors.Is(result.err, os.ErrExist):
		m.message("Overwrite blocked: file already exists on Linux", true)
	case !result.download && stage != nil && stage.Stage != "Cannot read file from Linux" &&
		(errors.Is(result.err, os.ErrPermission) || errors.Is(result.err, syscall.EROFS)):
		m.message("Upload blocked: external device folder is read-only. "+result.err.Error(), true)
	case result.download && errors.Is(result.err, os.ErrPermission) && stage != nil &&
		(stage.Stage == "Cannot read file from external device" || stage.Stage == "Cannot read source file"):
		m.message("Cannot read file: restricted by external device permissions", true)
	default:
		m.message("Copy failed: "+result.err.Error(), true)
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, msg.Width), max(1, msg.Height)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case pollMsg:
		if m.platform != "" && !m.ready && !m.busy && !m.confirm && m.mtpIssue == nil {
			return m, tea.Batch(m.scan(), poll())
		}
		return m, poll()
	case scanResult:
		m.busy, m.device = false, msg.device
		m.mtpIssue = nil
		switch {
		case errors.Is(msg.err, device.ErrMTPBusy):
			m.mtpIssue = device.ErrMTPBusy
			m.message("Android USB connection is in use. Press r after releasing it.", true)
		case errors.Is(msg.err, device.ErrMTPSession):
			m.mtpIssue = device.ErrMTPSession
			m.message("Android storage did not open. Press r after reselecting File transfer.", true)
		case msg.err != nil:
			m.message(msg.err.Error(), true)
		case msg.device.Kind != "":
			m.ready = true
			m.panes[0].replace(".", msg.external)
			m.message("Device ready.", false)
			m.applyList(listResult{1, m.panes[1].path, msg.local, msg.hostErr})
		default:
			m.message("", false)
		}
	case listResult:
		m.busy = false
		if m.applyList(msg) {
			m.message("", false)
		}
	case refreshResult:
		m.busy = false
		host := m.applyList(msg[1])
		external := m.applyList(msg[0])
		if host && external {
			m.message("Directory listings refreshed.", false)
		}
	case transferTick:
		if msg.copy == m.transfer {
			return m, watch(msg.copy)
		}
	case copyResult:
		m.copyFinished(msg)
	case openResult:
		m.busy = false
		if msg.err != nil {
			if msg.external && errors.Is(msg.err, os.ErrPermission) {
				m.message("Cannot read file: restricted by external device permissions", true)
			} else {
				m.message("Cannot open file: "+msg.err.Error(), true)
			}
		} else {
			m.message("Opening "+msg.name+" on the Linux desktop…", false)
			return m, func() tea.Msg { return openerExit{<-msg.done} }
		}
	case openerExit:
		if msg.err != nil {
			m.message("xdg-open could not launch the file: "+msg.err.Error(), true)
		}
	case tea.KeyMsg:
		return m, m.key(msg)
	}
	return m, nil
}

func (m *Model) key(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	if key == "ctrl+c" {
		return tea.Quit
	}
	if m.confirm {
		switch key {
		case "n", "esc":
			m.confirm = false
		case "y":
			if m.transfer == nil {
				return tea.Quit
			}
		}
		return nil
	}
	if m.ready && m.focus == 1 {
		p := &m.panes[1]
		switch {
		case p.filtering && key == "ctrl+u":
			p.setFilter(false, "")
			return nil
		case p.filtering && key == "backspace":
			_, size := utf8.DecodeLastRuneInString(p.query)
			p.setFilter(len(p.query) > size, p.query[:len(p.query)-size])
			return nil
		case !p.filtering && key == "/":
			p.setFilter(true, "")
			return nil
		case p.filtering && key == "alt+c":
			key = "c"
		case p.filtering && key == "alt+r":
			key = "r"
		case !msg.Alt && (msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace):
			if p.filtering || msg.Paste || len(msg.Runes) != 1 || !strings.ContainsRune("chjklqr", msg.Runes[0]) {
				input := strings.Map(func(r rune) rune {
					if unicode.IsPrint(r) {
						return r
					}
					return -1
				}, string(msg.Runes))
				if msg.Type == tea.KeySpace {
					input = " "
				}
				if input != "" {
					p.setFilter(true, p.query+input)
				}
				return nil
			}
		}
	}
	if key == "q" || key == "esc" {
		if !m.ready && m.device.Kind == "" && !m.session.HasMount() {
			return tea.Quit
		}
		m.confirm = true
		return nil
	}
	if !m.ready {
		if m.busy {
			return nil
		}
		if m.platform == "" {
			switch key {
			case "a", "A":
				m.platform = device.Android
			case "i", "I":
				m.platform = device.Apple
			default:
				return nil
			}
			return m.scan()
		}
		switch key {
		case "r":
			return m.scan()
		case "b":
			if !m.session.HasMount() {
				m.platform = ""
				m.mtpIssue = nil
				m.message("", false)
			}
		}
		return nil
	}
	p := &m.panes[m.focus]
	entries := p.visible()
	switch key {
	case "tab":
		m.focus = 1 - m.focus
		return nil
	case "up", "k":
		p.cursor = max(0, p.cursor-1)
		return nil
	case "down", "j":
		p.cursor = max(0, min(len(entries)-1, p.cursor+1))
		return nil
	}
	if m.busy {
		return nil
	}
	switch key {
	case "r":
		return m.refresh()
	case "backspace", "left", "h":
		parent := filepath.Dir(p.path)
		if parent != p.path {
			return m.navigate(m.focus, parent)
		}
	case "enter", "right", "l", "c":
		if len(entries) == 0 {
			return nil
		}
		entry := entries[p.cursor]
		path := filepath.Join(p.path, entry.Name)
		switch {
		case entry.Locked:
			m.message("Cannot access this item. Check its permissions.", true)
		case key == "c" && entry.Folder:
			m.message("Select a file to copy.", true)
		case key == "c":
			return m.startCopy(entry)
		case entry.Folder:
			return m.navigate(m.focus, path)
		default:
			return m.openFile(m.focus, path, entry.Name)
		}
	}
	return nil
}
