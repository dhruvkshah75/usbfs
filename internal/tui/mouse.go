package tui

import (
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"usbfs/internal/device"
	"usbfs/internal/fsops"
)

type paneBox struct{ x, y, width, height int }

// The view and hit testing share the same pane geometry and visible row window.
func (m *Model) paneBoxes(height int) [2]paneBox {
	if m.width >= 66 {
		left := (m.width - 2) / 2
		return [2]paneBox{{0, 1, left, height}, {left + 2, 1, m.width - left - 2, height}}
	}
	if height >= 13 {
		top := (height - 1) / 2
		return [2]paneBox{{0, 1, m.width, top}, {0, top + 2, m.width, height - top - 1}}
	}
	var boxes [2]paneBox
	boxes[m.focus] = paneBox{0, 1, m.width, height}
	return boxes
}

func (p *pane) entryWindow(height int) (row, start, end int) {
	count := len(p.visible())
	if height < 5 {
		if height > 1 && count > 0 {
			return 1, p.cursor, p.cursor + 1
		}
		return 1, 0, 0
	}
	row = 3
	if p.filtering && height >= 6 {
		row++
	}
	capacity := height - row - 1
	start = max(0, p.cursor-capacity+1)
	return row, start, min(count, start+capacity)
}

type fileDrag struct {
	from, x, y int
	entry      fsops.Entry
	moved      bool
}

func (m *Model) mouseHit(x, y int) (index, row int) {
	if m.width < 16 || m.height < 6 {
		return -1, -1
	}
	for index, box := range m.paneBoxes(m.paneHeight()) {
		if x < box.x || x >= box.x+box.width || y < box.y || y >= box.y+box.height {
			continue
		}
		first, start, end := m.panes[index].entryWindow(box.height)
		row := start + y - box.y - first
		if row >= start && row < end && (box.height < 5 || (x >= box.x+2 && x < box.x+box.width-2)) {
			return index, row
		}
		return index, -1
	}
	return -1, -1
}

func (m *Model) dropTarget(index, row int) (string, bool) {
	p := &m.panes[index]
	if row >= 0 {
		entry := p.visible()[row]
		if entry.Locked {
			return "", false
		}
		if entry.Folder {
			return filepath.Join(p.path, entry.Name), true
		}
	}
	return p.path, !(index == 0 && p.path == "." && m.platform == device.Apple)
}

func destinationLabel(index int, path string) string {
	if index == 1 {
		return "Linux · " + path
	}
	if path == "." {
		return "External device"
	}
	return "External · " + path
}

func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	if m.width >= 16 && m.height >= 6 && !m.confirm && m.folder == nil && m.create == nil && m.deleteName == "" &&
		msg.Y == m.height-1 && msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
		if msg.X >= 1 && msg.X <= len(m.mirrorAction(m.mirroring[m.device.USB])) {
			m.drag = nil
			return m.toggleMirror()
		}
	}
	if !m.ready || m.busy || m.confirm || m.folder != nil || m.create != nil || m.deleteName != "" {
		m.drag = nil
		return nil
	}
	index, row := m.mouseHit(msg.X, msg.Y)
	switch {
	case msg.Action == tea.MouseActionRelease && (msg.Button == tea.MouseButtonLeft || msg.Button == tea.MouseButtonNone):
		drag := m.drag
		m.drag = nil
		if drag == nil {
			return nil
		}
		if index == 0 && drag.from == 0 && row >= 0 && drag.entry.Folder &&
			!drag.moved && drag.x == msg.X && drag.y == msg.Y {
			return m.navigate(index, filepath.Join(m.panes[index].path, drag.entry.Name))
		}
		if index < 0 || index == drag.from {
			if drag.moved || drag.x != msg.X || drag.y != msg.Y {
				m.message("Copy cancelled. Drop the item in the opposite pane.", true)
			}
			return nil
		}
		target, ok := m.dropTarget(index, row)
		if !ok {
			pane := "external device"
			if index == 1 {
				pane = "Linux"
			}
			m.message("Drop onto an accessible folder in the "+pane+" pane.", true)
			return nil
		}
		return m.startCopy(drag.from, drag.entry, target)
	case msg.Action == tea.MouseActionMotion:
		if m.drag != nil {
			m.drag.moved = m.drag.moved || m.drag.x != msg.X || m.drag.y != msg.Y
			m.drag.x, m.drag.y = msg.X, msg.Y
		}
	case msg.Action == tea.MouseActionPress && index >= 0:
		p := &m.panes[index]
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			p.cursor = max(0, p.cursor-3)
		case tea.MouseButtonWheelDown:
			p.cursor = max(0, min(len(p.visible())-1, p.cursor+3))
		case tea.MouseButtonLeft:
			m.drag = nil
			m.focus = index
			if row >= 0 {
				p.cursor = row
				entry := p.visible()[row]
				if !entry.Locked && (!entry.Folder || !entry.Symlink) {
					m.drag = &fileDrag{from: index, x: msg.X, y: msg.Y, entry: entry}
				}
			}
		}
	}
	return nil
}
