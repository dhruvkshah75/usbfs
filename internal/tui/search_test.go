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
		tree[fmt.Sprintf("nested/match%04d", i)] = &fstest.MapFile{}
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
		run := m.key(runes("usbfs"))
		if run == nil || m.panes[index].search == nil || m.busy {
			t.Fatal("search did not start")
		}
		var result searchResult
		if index == 0 {
			// Simulate entries returned by a mounted share.
			result = searchResult{index: index, job: m.panes[index].search}
			tree := fstest.MapFS{"docs/USB FS": {Mode: os.ModeDir}}
			if err := result.searchTree(context.Background(), tree, "Brave", "", "usbfs"); err != nil {
				t.Fatal(err)
			}
		} else {
			result = run().(searchResult)
		}
		m.Update(result)
		p := &m.panes[index]
		if p.query != "usbfs" || len(p.visible()) != 1 || !strings.Contains(ansi.Strip(m.View()), "docs/USB FS") {
			t.Fatalf("pane %d did not show search results: %+v", index, p)
		}
		if index == 0 {
			m.key(tea.KeyMsg{Type: tea.KeyEsc})
			if p.query != "" || m.confirm || p.visible()[0].Name != "Brave" {
				t.Fatal("closing search did not restore the external pane")
			}
		} else {
			m.Update(m.key(tea.KeyMsg{Type: tea.KeyRight})())
			if p.query != "" || p.path != filepath.Join(host, "docs", "USB FS") {
				t.Fatalf("opening a recursive result navigated to %s", p.path)
			}
		}
	}
}

func TestLiveSearchDiscardsSupersededAndClearedResults(t *testing.T) {
	host := t.TempDir()
	for _, name := range []string{"notes/old.txt", "notes/new.txt"} {
		path := filepath.Join(host, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	m := New(&device.Session{}, host)
	m.ready, m.focus = true, 1
	p := &m.panes[1]
	old := m.key(runes("old"))()
	m.key(tea.KeyMsg{Type: tea.KeyCtrlU})
	run := m.key(runes("new"))
	m.Update(old)
	if p.query != "new" || len(p.visible()) != 0 {
		t.Fatal("an older search replaced the current query")
	}
	current := run()
	m.Update(current)
	if len(p.visible()) != 1 || p.visible()[0].Name != "notes/new.txt" {
		t.Fatalf("current results = %v", p.visible())
	}
	stillPending := m.key(tea.KeyMsg{Type: tea.KeyBackspace})
	m.key(tea.KeyMsg{Type: tea.KeyEsc})
	if stillPending() != nil {
		t.Fatal("clearing the filter did not cancel the pending search")
	}
	m.Update(current)
	if p.search != nil || p.filtering || len(p.visible()) != 0 || m.confirm {
		t.Fatal("cleared results resurfaced or Esc opened the exit confirmation")
	}
}
