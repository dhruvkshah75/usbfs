package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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
	if !strings.Contains(m.View(), "[Alt+m] Mirror Screen") {
		t.Fatal("device header does not offer mirroring")
	}
	shortcut := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true}
	if cmd := m.key(shortcut); cmd == nil || !m.mirrorPending {
		t.Fatal("Alt+m did not queue mirror preflight")
	}
	m.Update(mirrorToggleResult{})
	m.Update(mirror.Event{USB: m.device.USB, State: mirror.Started, Message: "Mirror started."})
	if view := m.View(); !strings.Contains(view, "● Mirroring") || !strings.Contains(view, "[Alt+m] Stop Mirror") {
		t.Fatal("started event did not update the device header")
	}
	msg := tea.MouseMsg{X: len(m.deviceHeaderPrefix()), Y: 0, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
	if cmd := m.mouse(msg); cmd == nil || m.status != "Closing mirror…" {
		t.Fatal("clicking Stop Mirror did not queue a stop")
	}
	m.Update(mirrorToggleResult{})
	m.Update(mirror.Event{USB: m.device.USB, State: mirror.Exited, Message: "Mirror closed."})
	if view := m.View(); strings.Contains(view, "● Mirroring") || !strings.Contains(view, "[Alt+m] Mirror Screen") {
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

func TestMirrorOnAppleShowsAvailability(t *testing.T) {
	m := New(&device.Session{}, t.TempDir())
	t.Cleanup(m.CloseMirrors)
	m.ready, m.platform = true, device.Apple
	m.device = device.Device{Kind: "iOS/iPadOS", Name: "iPad"}
	if cmd := m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true}); cmd != nil || m.warning || !strings.Contains(m.status, "available for Android") {
		t.Fatalf("unexpected Apple mirror response: %q, warning = %v", m.status, m.warning)
	}
}
