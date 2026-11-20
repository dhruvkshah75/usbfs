package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"usbfs/internal/device"
	"usbfs/internal/fsops"
	"usbfs/internal/mirror"
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
		if m.details != nil {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				m.detailsScroll(-3)
			case tea.MouseButtonWheelDown:
				m.detailsScroll(3)
			}
			return m, nil
		}
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
		for i := range m.panes {
			m.panes[i].receiveSearchMatches()
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case mirror.Event:
		switch msg.State {
		case mirror.Started:
			m.mirroring[msg.USB] = true
		case mirror.Exited:
			delete(m.mirroring, msg.USB)
		}
		m.message(msg.Message, msg.Warning)
		return m, m.watchMirror()
	case mirrorToggleResult:
		m.mirrorPending = false
		if msg.err != nil {
			m.message(msg.err.Error(), true)
		}
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
			} else if msg.device.Owner == device.MountOwnerExternalGVfs && len(msg.external) == 0 {
				m.message("Phone is mounted but storage is empty — unlock the phone, switch USB mode to File Transfer (MTP), and press r to retry.", false)
			}
		default:
			m.message("", false)
		}
	case listResult:
		m.busy = false
		m.panes[msg.index].loadingPath = ""
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
		var searches []tea.Cmd
		for i, ok := range []bool{external, host} {
			if ok && m.panes[i].filtering {
				searches = append(searches, m.searchQuery(i, m.panes[i].query))
			}
		}
		return m, tea.Batch(searches...)
	case transferTick:
		if msg.copy == m.transfer {
			return m, watch(msg.copy)
		}
	case detailsResult:
		m.busy = false
		if msg.err != nil {
			m.message("Cannot read item details: "+msg.err.Error(), true)
		} else if !m.confirm {
			m.details, m.detailsOffset, m.help = &msg, 0, false
		}
	case searchResult:
		m.searchFinished(msg)
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
	case clipboardResult:
		m.busy = false
		if msg.err != nil {
			m.message(msg.err.Error(), true)
		} else if !m.confirm && m.ready {
			input := newInput(m.nameWidth(), "Filename")
			input.SetValue("clipboard-" + time.Now().Format("20060102-150405") + msg.ext)
			input.CursorEnd()
			m.create, m.help = &createPrompt{input: input, image: &msg}, false
			return m, m.create.input.Focus()
		}
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
			p := &m.panes[1]
			for i, entry := range p.results {
				if entry.Name == msg.name {
					p.results = append(p.results[:i], p.results[i+1:]...)
					p.replace(p.path, p.entries)
					break
				}
			}
			m.message("✓ Deleted "+msg.name+" from Linux", false)
			if p.filtering {
				return m, m.searchQuery(1, p.query)
			}
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
		for i := range m.panes {
			m.panes[i].stopSearch()
		}
		return tea.Quit
	}
	if m.details != nil {
		switch key {
		case "esc", "alt+i":
			m.details = nil
		case "up":
			m.detailsScroll(-1)
		case "down":
			m.detailsScroll(1)
		case "pgup":
			m.detailsScroll(-m.detailsPageSize())
		case "pgdown":
			m.detailsScroll(m.detailsPageSize())
		}
		return nil
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
				for i := range m.panes {
					m.panes[i].stopSearch()
				}
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
			name := prompt.input.Value()
			if prompt.image != nil {
				name = strings.TrimSpace(name)
				if name == "" || name == "." || name == ".." {
					m.message("Enter a filename to save the image.", true)
					return nil
				}
				ext := strings.ToLower(filepath.Ext(name))
				if ext != prompt.image.ext && !(prompt.image.ext == ".jpg" && ext == ".jpeg") {
					name += prompt.image.ext
				}
			}
			m.create = nil
			return m.createItem(name, prompt)
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
	if key == "alt+m" {
		return m.toggleMirror()
	}
	if key == "r" && m.ready && m.device.Owner == device.MountOwnerExternalGVfs && !m.busy && !m.panes[m.focus].filtering {
		for i := range m.panes {
			m.panes[i].stopSearch()
		}
		m.ready = false
		return m.scan()
	}
	if m.ready && m.panes[m.focus].loadingPath == "" {
		p := &m.panes[m.focus]
		switch {
		case p.filtering && (key == "ctrl+u" || key == "esc"):
			return m.searchQuery(m.focus, "")
		case p.filtering && key == "backspace":
			_, size := utf8.DecodeLastRuneInString(p.query)
			return m.searchQuery(m.focus, p.query[:len(p.query)-size])
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
				return m.searchQuery(m.focus, p.query+input)
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
	case "alt+i":
		if len(entries) > 0 {
			return m.inspectItem(m.focus, filepath.Join(p.path, entries[p.cursor].Name))
		}
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
	case "ctrl+n", "ctrl+f", "ctrl+v":
		if m.focus == 0 && p.path == "." && m.platform == device.Apple {
			m.message("Open a mounted folder on the external device first.", true)
			return nil
		}
		if key == "ctrl+v" {
			return m.pasteImage()
		}
		m.create = &createPrompt{folder: key == "ctrl+n", input: newInput(m.nameWidth(), "Name")}
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
