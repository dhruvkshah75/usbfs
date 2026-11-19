package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"usbfs/internal/device"
	"usbfs/internal/fsops"
)

func TestRecursiveSearchIncludesRelativePathsAndSkipsLinks(t *testing.T) {
	tree := fstest.MapFS{
		"first/USB FS.txt":   {},
		"second/usbfs.txt":   {},
		"second/USBFS docs":  {Mode: os.ModeDir},
		"second/USBFS link":  {Mode: os.ModeSymlink, Data: []byte("/outside")},
		"external/usbfs.txt": {},
		"unmatched.txt":      {},
	}
	var result searchResult
	if err := result.searchTree(context.Background(), tree, "Brave", "external", filterText(" Usb Fs ")); err != nil {
		t.Fatal(err)
	}
	want := []string{"Brave/first/USB FS.txt", "Brave/second/USBFS docs", "Brave/second/usbfs.txt"}
	if len(result.entries) != len(want) {
		t.Fatalf("matches: %+v", result.entries)
	}
	for i, entry := range result.entries {
		if entry.Name != want[i] || entry.Folder != (i == 1) {
			t.Fatalf("match %d: %+v", i, entry)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := result.searchTree(ctx, tree, "", "", "usbfs"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled walk returned %v", err)
	}
}

func TestRecursiveSearchLimit(t *testing.T) {
	tree := fstest.MapFS{}
	for i := range searchLimit + 2 {
		tree[fmt.Sprintf("match%04d", i)] = &fstest.MapFile{}
	}
	var result searchResult
	if err := result.searchTree(context.Background(), tree, "", "", "match"); err != nil {
		t.Fatal(err)
	}
	if len(result.entries) != searchLimit || !result.limited {
		t.Fatalf("limit = %d, limited = %t", len(result.entries), result.limited)
	}
}

func TestSearchControlsInBothPanes(t *testing.T) {
	host := t.TempDir()
	if err := os.MkdirAll(filepath.Join(host, "docs", "USB FS"), 0755); err != nil {
		t.Fatal(err)
	}
	m := New(&device.Session{}, host)
	m.ready = true
	m.panes[0].entries = []fsops.Entry{{Name: "Brave", Folder: true}}
	for index := range m.panes {
		m.focus = index
		m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}, Alt: true})
		m.key(runes("usbfs"))
		if !strings.Contains(ansi.Strip(m.View()), "Search subfolders") {
			t.Fatal("search input did not open")
		}
		run := m.key(tea.KeyMsg{Type: tea.KeyEnter})
		if run == nil || m.cancelSearch == nil || !m.busy {
			t.Fatal("search did not start")
		}
		var result searchResult
		if index == 0 {
			// Simulate entries returned by a mounted share.
			result = searchResult{index: index, query: "usbfs"}
			tree := fstest.MapFS{"docs/USB FS": {Mode: os.ModeDir}}
			if err := result.searchTree(context.Background(), tree, "Brave", "", "usbfs"); err != nil {
				t.Fatal(err)
			}
		} else {
			result = run().(searchResult)
		}
		m.Update(result)
		p := &m.panes[index]
		if p.search != "usbfs" || len(p.visible()) != 1 || !strings.Contains(ansi.Strip(m.View()), "docs/USB FS") {
			t.Fatalf("pane %d did not show search results: %+v", index, p)
		}
		if index == 0 {
			m.key(tea.KeyMsg{Type: tea.KeyEsc})
			if p.search != "" || m.confirm || p.visible()[0].Name != "Brave" {
				t.Fatal("closing search did not restore the external pane")
			}
		} else {
			m.Update(m.key(tea.KeyMsg{Type: tea.KeyRight})())
			if p.search != "" || p.path != filepath.Join(host, "docs", "USB FS") {
				t.Fatalf("opening a recursive result navigated to %s", p.path)
			}
		}
	}
}

func TestSearchCancelDiscardsPendingResult(t *testing.T) {
	m := New(&device.Session{}, t.TempDir())
	m.ready, m.focus = true, 1
	run := m.startSearch(1, "test")
	m.key(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(run())
	if m.busy || m.cancelSearch != nil || m.panes[1].search != "" || m.status != "Search cancelled." {
		t.Fatalf("cancelled search changed the UI: %q", m.status)
	}
}
