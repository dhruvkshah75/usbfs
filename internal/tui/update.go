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
	"github.com/charmbracelet/bubbles/textinput"
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
			root, path, err = session.OpenPath(path)
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
	m.status, m.statusDetail, m.warning = message, "", warning
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
	if result.folder != nil {
		m.folderFinished(result.name, *result.folder, result.target.err)
		return
	}
	if result.err == nil {
		m.message("✓ Copied "+result.name, false)
		m.statusDetail = "→ " + result.destination
		if result.target.err != nil {
			m.message("✓ Copied "+result.name+". Refresh failed: "+result.target.err.Error(), true)
			m.statusDetail = "→ " + result.destination
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
		m.drag = nil
		if m.create != nil {
			m.create.input.Width = m.nameWidth()
		}
	case tea.MouseMsg:
		if m.help {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				m.helpScroll(-3)
			case tea.MouseButtonWheelDown:
				m.helpScroll(3)
			}
			return m, nil
		}
		return m, m.mouse(msg)
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
			if msg.warning != nil {
				m.message(msg.warning.Error(), true)
			}
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
	case folderPrepared:
		m.busy = false
		if msg.err != nil {
			m.message("Cannot scan folder: "+msg.err.Error(), true)
		} else if !m.confirm {
			m.folder, m.help = &msg.copy, false
			m.message("", false)
		}
	case copyResult:
		m.copyFinished(msg)
	case createResult:
		m.busy = false
		switch {
		case errors.Is(msg.err, os.ErrExist):
			m.message("Create blocked: an item with that name already exists.", true)
		case errors.Is(msg.err, os.ErrPermission), errors.Is(msg.err, syscall.EROFS):
			m.message("Create blocked: this folder does not allow writes.", true)
		case msg.err != nil:
			m.message("Cannot create item: "+msg.err.Error(), true)
		case m.applyList(msg.target):
			p := &m.panes[msg.target.index]
			for i, entry := range p.visible() {
				if entry.Name == msg.name {
					p.cursor = i
					break
				}
			}
			m.message("Created "+msg.name, false)
		}
	case deleteResult:
		m.busy = false
		switch {
		case msg.err != nil:
			m.message("Cannot delete on Linux: "+msg.err.Error(), true)
		case m.applyList(msg.target):
			m.message("✓ Deleted "+msg.name+" from Linux", false)
		default:
			m.message("Deleted "+msg.name+" from Linux. Refresh failed: "+msg.target.err.Error(), true)
		}
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
	default:
		if m.create != nil {
			var cmd tea.Cmd
			m.create.input, cmd = m.create.input.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m *Model) key(msg tea.KeyMsg) tea.Cmd {
	m.drag = nil
	key := msg.String()
	if key == "ctrl+c" {
		return tea.Quit
	}
	if m.folder != nil {
		switch key {
		case "y":
			job := m.folder
			m.folder = nil
			return m.copyFolder(job)
		case "n", "esc":
			m.folder = nil
			m.message("Folder copy cancelled.", false)
		}
		return nil
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
	if m.deleteName != "" {
		switch key {
		case "y":
			name := m.deleteName
			m.deleteName = ""
			return m.deleteItem(name)
		case "n", "esc":
			m.deleteName = ""
		}
		return nil
	}
	if m.create != nil {
		prompt := m.create
		switch key {
		case "esc":
			m.create = nil
		case "enter":
			m.create = nil
			return m.createItem(prompt.input.Value(), prompt.folder)
		default:
			var cmd tea.Cmd
			prompt.input, cmd = prompt.input.Update(msg)
			return cmd
		}
		return nil
	}
	if m.help {
		switch key {
		case "f1", "esc":
			m.help = false
		case "up":
			m.helpScroll(-1)
		case "down":
			m.helpScroll(1)
		case "pgup":
			m.helpScroll(-m.helpPageSize())
		case "pgdown":
			m.helpScroll(m.helpPageSize())
		case "home":
			m.helpOffset = 0
		case "end":
			m.helpOffset = len(m.helpRows())
			m.helpScroll(0)
		}
		return nil
	}
	if key == "f1" {
		m.help, m.helpOffset = true, 0
		return nil
	}
	if m.ready {
		p := &m.panes[m.focus]
		switch {
		case p.filtering && key == "ctrl+u":
			p.setFilter(false, "")
			return nil
		case p.filtering && key == "backspace":
			_, size := utf8.DecodeLastRuneInString(p.query)
			p.setFilter(len(p.query) > size, p.query[:len(p.query)-size])
			return nil
		case !msg.Alt && (msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace):
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
	if key == "esc" || key == "alt+q" || (!m.ready && key == "q") {
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
	case "up":
		p.cursor = max(0, p.cursor-1)
		return nil
	case "down":
		p.cursor = max(0, min(len(entries)-1, p.cursor+1))
		return nil
	}
	if m.busy {
		return nil
	}
	switch key {
	case "alt+r":
		return m.refresh()
	case "delete":
		switch {
		case m.focus == 0:
			m.message("Delete is available in the Linux host pane only.", true)
		case len(entries) == 0:
			return nil
		case entries[p.cursor].Folder || entries[p.cursor].Locked || entries[p.cursor].Symlink:
			m.message("Delete is available for regular files only.", true)
		default:
			m.deleteName = entries[p.cursor].Name
		}
	case "ctrl+n", "ctrl+f":
		if m.focus == 0 && p.path == "." && m.platform == device.Apple {
			m.message("Open a mounted folder on the external device first.", true)
			return nil
		}
		input := textinput.New()
		input.Placeholder = "Name"
		input.Prompt = "> "
		input.Width = m.nameWidth()
		input.TextStyle = foreground(coral, surfaceElevated)
		input.PromptStyle = input.TextStyle
		input.PlaceholderStyle = foreground(muted, surfaceElevated)
		// Bubbles reverses the cursor style when drawing the caret.
		input.Cursor.Style = foreground(surfaceElevated, coral)
		input.Cursor.TextStyle = input.TextStyle
		m.create = &createPrompt{folder: key == "ctrl+n", input: input}
		return m.create.input.Focus()
	case "left":
		parent := filepath.Dir(p.path)
		if parent != p.path {
			return m.navigate(m.focus, parent)
		}
	case "enter", "right", "alt+c":
		if len(entries) == 0 {
			return nil
		}
		entry := entries[p.cursor]
		path := filepath.Join(p.path, entry.Name)
		switch {
		case entry.Locked:
			m.message("Cannot access this item. Check its permissions.", true)
		case key == "alt+c":
			return m.startCopy(m.focus, entry, m.panes[1-m.focus].path)
		case entry.Folder:
			return m.navigate(m.focus, path)
		default:
			return m.openFile(m.focus, path, entry.Name)
		}
	}
	return nil
}
