package tui

import tea "github.com/charmbracelet/bubbletea"

type mirrorToggleResult struct{ err error }

func (m *Model) CloseMirrors() { m.mirrors.Close() }

func (m *Model) watchMirror() tea.Cmd {
	return func() tea.Msg {
		if event, ok := m.mirrors.NextEvent(); ok {
			return event
		}
		return nil
	}
}

func (m *Model) toggleMirror() tea.Cmd {
	target := m.device
	if target.Kind == "" || m.mirrorPending {
		return nil
	}
	m.mirrorPending = true
	if m.mirroring[target.USB] {
		m.message("Closing mirror…", false)
	} else if target.Kind == "iOS/iPadOS" {
		m.message("Preparing iPad mirror…", false)
	} else {
		m.message("Checking Android USB debugging…", false)
	}
	return func() tea.Msg {
		return mirrorToggleResult{m.mirrors.Toggle(target)}
	}
}

func (m *Model) mirrorAction(active bool) string {
	if m.device.Kind == "" {
		return ""
	}
	if active {
		return "[Alt+m] Stop Mirror"
	}
	if m.width < 22 {
		return "[Alt+m] Mirror"
	}
	return "[Alt+m] Mirror Screen"
}

func (m *Model) deviceHeaderPrefix() string {
	if m.width < 29 {
		return " "
	}
	return " usbfs  "
}
