package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"usbfs/internal/device"
	"usbfs/internal/fsops"
)

func TestDetailsShortcutReadsSelectedSearchResult(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	const name = "USB FS notes.txt"
	path := filepath.Join(dir, "docs", name)
	if err := os.WriteFile(path, []byte("abc"), 0644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2025, time.April, 2, 3, 4, 5, 0, time.Local)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	m := New(&device.Session{}, dir)
	m.ready, m.focus = true, 1
	p := &m.panes[1]
	p.setFilter(true, "usbfs")
	p.results = []fsops.Entry{{Name: filepath.Join("docs", name)}}
	p.setFilter(true, "usbfs")
	shortcut := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}, Alt: true}
	cmd := m.key(shortcut)
	if cmd == nil {
		t.Fatal("Alt+I did not read the selected item")
	}
	m.Update(cmd())
	view := ansi.Strip(m.View())
	for _, want := range []string{"Details", name, "Linux host", "docs/" + name, "Type: File", "3 bytes", "2025-04-02 03:04:05"} {
		if !strings.Contains(view, want) {
			t.Fatalf("details missing %q:\n%s", want, view)
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 8})
	m.key(tea.KeyMsg{Type: tea.KeyPgDown})
	m.key(tea.KeyMsg{Type: tea.KeyPgDown})
	if !strings.Contains(ansi.Strip(m.View()), "Modified:") {
		t.Fatalf("details did not scroll on a small terminal:\n%s", ansi.Strip(m.View()))
	}
	m.key(tea.KeyMsg{Type: tea.KeyEsc})
	if m.details != nil || p.query != "usbfs" || !strings.Contains(ansi.Strip(m.View()), "Filter: usbfs") {
		t.Fatal("closing details changed the search pane")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	m.Update(m.key(shortcut)())
	if m.details != nil || !m.warning || !strings.Contains(m.status, "Cannot read item details") {
		t.Fatal("details did not handle a vanished result")
	}
}

func TestDetailsKeyboardWorksInExternalPane(t *testing.T) {
	m := New(&device.Session{}, t.TempDir())
	m.ready, m.platform = true, device.Apple
	m.panes[0].entries = []fsops.Entry{{Name: "Media", Folder: true}}
	if cmd := m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}, Alt: true}); cmd == nil || !m.busy {
		t.Fatal("Alt+I did not request details in the external pane")
	}

	// Simulate directory metadata returned by a mounted share.
	info, err := os.Stat(m.panes[1].path)
	if err != nil {
		t.Fatal(err)
	}
	m.Update(detailsResult{path: "Media", location: "External device · Media", info: info})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "External device · Media") || !strings.Contains(view, "Type: Folder") {
		t.Fatalf("share details missing:\n%s", view)
	}
}

func TestFolderDetailsCountsNestedContentsWithoutFollowingLinks(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "StudyFiles")
	if err := os.MkdirAll(filepath.Join(path, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"one.txt": "123", "nested/two.txt": "45678"} {
		if err := os.WriteFile(filepath.Join(path, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(path, "one.txt"), filepath.Join(path, "link")); err != nil {
		t.Fatal(err)
	}
	result := readDetails(&device.Session{}, device.Android, 1, path)
	if result.err != nil || result.folderSize != "8 bytes (contents)" {
		t.Fatalf("folder size = %q, error = %v", result.folderSize, result.err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if got := folderDetailsSize(root, "StudyFiles", result.info); got != "8 bytes (contents)" {
		t.Fatalf("root-relative folder size = %q", got)
	}
	if !strings.Contains(strings.Join(result.lines(), "\n"), "Size: 8 bytes (contents)") {
		t.Fatal("folder size missing from rendered details")
	}
}
