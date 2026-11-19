package tui

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"usbfs/internal/device"
)

func TestClipboardPasteKeySavesNamedImageWithoutOverwriting(t *testing.T) {
	host := t.TempDir()
	m := New(&device.Session{}, host)
	m.ready, m.focus = true, 1
	m.panes[0].path = "Brave"
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "screenshot.png")
	if err := os.WriteFile(fixture, pngBytes.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "wl-paste"), []byte("#!/bin/sh\n[ \"$3\" = image/png ] || exit 1\nexec /bin/cat \"$USBFS_TEST_CLIPBOARD\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("WAYLAND_DISPLAY", "test-wayland")
	t.Setenv("DISPLAY", "")
	t.Setenv("USBFS_TEST_CLIPBOARD", fixture)

	for _, name := range []string{"capture", "capture", "../outside", "cancelled"} {
		m.focus = 1
		read := m.key(tea.KeyMsg{Type: tea.KeyCtrlV})
		if read == nil {
			t.Fatal("Ctrl+V did not start clipboard read")
		}
		m.key(tea.KeyMsg{Type: tea.KeyTab})
		m.Update(read())
		if m.create == nil || !strings.Contains(ansi.Strip(m.View()), "Save clipboard image · Linux") {
			t.Fatalf("clipboard prompt lost the original pane: %s", m.status)
		}
		if !strings.HasSuffix(m.create.input.Value(), ".png") {
			t.Fatal("default filename did not preserve the image type")
		}
		m.create.input.SetValue(name)
		if name == "cancelled" {
			m.key(tea.KeyMsg{Type: tea.KeyEsc})
			break
		}
		m.Update(m.key(tea.KeyMsg{Type: tea.KeyEnter})())
	}
	imagePath := filepath.Join(host, "capture.png")
	if got, err := os.ReadFile(imagePath); err != nil || !bytes.Equal(got, pngBytes.Bytes()) {
		t.Fatalf("saved image differs: %v", err)
	}
	items, err := os.ReadDir(host)
	if err != nil || len(items) != 1 {
		t.Fatalf("clipboard save created unexpected files: %v, %v", items, err)
	}
}

func TestClipboardPasteRequiresMountedAppleFolder(t *testing.T) {
	m := New(&device.Session{}, t.TempDir())
	m.ready, m.platform = true, device.Apple
	if cmd := m.key(tea.KeyMsg{Type: tea.KeyCtrlV}); cmd != nil || !strings.Contains(m.status, "mounted folder") {
		t.Fatal("clipboard paste accepted the Apple share picker as a destination")
	}
}
