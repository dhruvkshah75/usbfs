package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"usbfs/internal/device"
	"usbfs/internal/mirror"
)

func TestMirrorControls(t *testing.T) {
	m := New(&device.Session{}, t.TempDir())
	t.Cleanup(m.CloseMirrors)
	m.ready, m.platform = true, device.Android
	m.device = device.Device{
		Kind: "Android MTP", Name: "Pixel", USB: device.USBIdentity{Path: "1-2", Serial: "pixel"},
	}
	rows := strings.Split(ansi.Strip(m.View()), "\n")
	if strings.Contains(rows[0], "[Alt+m]") || !strings.HasPrefix(rows[len(rows)-1], " [Alt+m] Mirror Screen") {
		t.Fatal("Mirror Screen should appear in the bottom bar only")
	}
	shortcut := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true}
	if cmd := m.key(shortcut); cmd == nil || !m.mirrorPending {
		t.Fatal("Alt+m did not queue mirror preflight")
	}
	m.Update(mirrorToggleResult{})
	m.Update(mirror.Event{USB: m.device.USB, State: mirror.Started, Message: "Mirror started."})
	rows = strings.Split(ansi.Strip(m.View()), "\n")
	if !strings.Contains(rows[0], "● MIRRORING") || !strings.HasPrefix(rows[len(rows)-1], " [Alt+m] Stop Mirror") {
		t.Fatal("started event did not update the header badge and bottom-bar action")
	}
	msg := tea.MouseMsg{X: 1, Y: 0, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
	if cmd := m.mouse(msg); cmd != nil || m.mirrorPending {
		t.Fatal("header click still triggers mirroring")
	}
	msg.Y = m.height - 1
	if cmd := m.mouse(msg); cmd == nil || m.status != "Closing mirror…" {
		t.Fatal("clicking Stop Mirror in the bottom bar did not queue a stop")
	}
	m.Update(mirrorToggleResult{})
	m.Update(mirror.Event{USB: m.device.USB, State: mirror.Exited, Message: "Mirror closed."})
	if view := m.View(); strings.Contains(view, "● MIRRORING") || !strings.Contains(view, "[Alt+m] Mirror Screen") {
		t.Fatal("closed event did not clear the device indicator")
	}

	for i := range m.panes {
		m.focus = i
		for _, query := range []string{"m", "mm"} {
			if cmd := m.key(runes("m")); cmd == nil || m.mirrorPending || m.panes[i].query != query {
				t.Fatalf("m did not enter search query %q in pane %d", query, i)
			}
		}
	}
	if cmd := m.key(shortcut); cmd == nil || !m.mirrorPending || m.panes[m.focus].query != "mm" {
		t.Fatal("Alt+m did not queue mirroring and preserve the active search")
	}
}

func TestMirrorOnAppleQueuesPreflight(t *testing.T) {
	m := New(&device.Session{}, t.TempDir())
	t.Cleanup(m.CloseMirrors)
	m.ready, m.platform = true, device.Apple
	m.device = device.Device{Kind: "iOS/iPadOS", Name: "iPad"}
	if cmd := m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true}); cmd == nil || !m.mirrorPending || m.busy || m.status != "Preparing iPad mirror…" {
		t.Fatalf("unexpected Apple mirror response: %q, warning = %v", m.status, m.warning)
	}
}
