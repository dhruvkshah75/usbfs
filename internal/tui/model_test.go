package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"usbfs/internal/device"
	"usbfs/internal/fsops"
)

func TestTransferDisablesExitConfirmation(t *testing.T) {
	m := New(&device.Session{}, "/home/user")
	m.ready = true
	m.transfer = &transfer{name: "movie.mp4"}
	m.key(runes("q"))
	if !m.confirm || m.key(runes("y")) != nil || !strings.Contains(ansi.Strip(m.View()), "Transfer in progress!") {
		t.Fatal("active transfer must block the unmount confirmation")
	}
	m.transfer = nil
	if cmd := m.key(runes("y")); cmd == nil {
		t.Fatal("completed transfer must allow confirmed exit")
	}
}

func runes(text string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)}
}

func TestBusyAndroidWaitsForManualRetry(t *testing.T) {
	m := New(&device.Session{}, "/home/user")
	m.platform = device.Android
	m.Update(scanResult{err: device.ErrMTPBusy})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Android USB connection is busy") || !strings.Contains(view, "Eject the phone") {
		t.Fatalf("missing recovery instructions:\n%s", view)
	}
	m.Update(pollMsg{})
	if m.busy {
		t.Fatal("polling retried a busy USB interface")
	}
	if _, retry := m.Update(runes("r")); retry == nil || !m.busy {
		t.Fatal("manual retry did not restart detection")
	}
}

func TestLinuxTypeToFilter(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"usbfs", "USB FS Backup", "records"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	m := New(&device.Session{}, dir)
	m.ready = true
	m.Update(list(nil, 1, dir))
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	for _, key := range []tea.KeyMsg{runes("U"), runes("S"), runes("B"), {Type: tea.KeySpace}, runes("F"), runes("S")} {
		m.Update(key)
	}
	pane := &m.panes[1]
	view := ansi.Strip(m.View())
	if len(pane.visible()) != 2 || !strings.Contains(view, "Filter: USB FS") || strings.Contains(view, "records") {
		t.Fatalf("unexpected filtered pane:\n%s", view)
	}
	m.Update(runes("界"))
	if len(pane.visible()) != 0 || !strings.Contains(ansi.Strip(m.View()), "No matches") {
		t.Fatal("unmatched input should show an empty result list")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if pane.query != "USB FS" || len(pane.visible()) != 2 {
		t.Fatal("backspace should remove one rune and restore the matches")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	chosen := pane.visible()[pane.cursor].Name
	m.Update(list(nil, 1, dir))
	if pane.visible()[pane.cursor].Name != chosen {
		t.Fatal("refresh lost the current match")
	}
	_, open := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(open())
	if pane.path != filepath.Join(dir, chosen) || pane.filtering {
		t.Fatalf("opening a match did not enter its folder and clear the filter: %+v", pane)
	}
}

func TestLinuxFilterShortcutAndSafeExit(t *testing.T) {
	m := New(&device.Session{}, "/home/user")
	m.ready, m.focus = true, 1
	m.panes[1].entries = []fsops.Entry{{Name: "report"}, {Name: "qrcode"}}
	m.Update(runes("/"))
	m.Update(runes("q"))
	pane := &m.panes[1]
	if m.confirm || len(pane.visible()) != 1 || pane.visible()[0].Name != "qrcode" {
		t.Fatal("explicit filter should accept shortcut letters")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !m.confirm {
		t.Fatal("escape must still open safe-exit confirmation")
	}
	m.Update(runes("n"))
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if pane.filtering || len(pane.visible()) != 2 {
		t.Fatal("clear filter should restore the entire directory")
	}
}

func TestResponsiveViewFits(t *testing.T) {
	m := New(&device.Session{}, "/home/user")
	m.ready = true
	m.device = device.Device{Kind: "Android MTP", Name: "Tablet"}
	m.panes[0].entries = []fsops.Entry{{Name: "Pictures", Folder: true}, {Name: "A\x1b[2J照片.png"}}
	for _, size := range [][2]int{{120, 28}, {80, 24}, {52, 24}, {32, 12}, {16, 6}} {
		m.width, m.height = size[0], size[1]
		for _, confirm := range []bool{false, true} {
			m.confirm = confirm
			view := m.View()
			if lipgloss.Width(view) > m.width || lipgloss.Height(view) > m.height {
				t.Errorf("view %dx%d exceeded terminal %v (confirm = %v)", lipgloss.Width(view), lipgloss.Height(view), size, confirm)
			}
			if strings.Contains(view, "\x1b[2J") {
				t.Error("filename injected a terminal escape sequence")
			}
		}
	}
}
