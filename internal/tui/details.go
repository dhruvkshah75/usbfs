package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"usbfs/internal/device"
)

type detailsResult struct {
	path, location string
	info           os.FileInfo
	err            error
}

func readDetails(session *device.Session, platform device.Platform, index int, path string) detailsResult {
	result := detailsResult{path: path, location: "Linux host"}
	if index == 1 {
		result.info, result.err = os.Lstat(path)
		return result
	}
	root, relative, err := session.OpenPath(path)
	if err != nil {
		result.err = err
		return result
	}
	defer root.Close()
	result.location = "External device"
	if platform == device.Apple {
		share, _, _ := strings.Cut(path, string(filepath.Separator))
		result.location += " · " + share
	}
	result.info, result.err = root.Lstat(relative)
	return result
}

func (m *Model) inspectItem(index int, path string) tea.Cmd {
	m.busy = true
	session, platform := m.session, m.platform
	return m.work(func() tea.Msg { return readDetails(session, platform, index, path) })
}

func (d detailsResult) lines() []string {
	name, size, kind, modified := filepath.Base(d.path), "—", "Special entry", "Unavailable"
	switch {
	case d.info.Mode().IsRegular():
		kind, size = "File", fmt.Sprintf("%d bytes", d.info.Size())
	case d.info.IsDir():
		kind = "Folder"
	case d.info.Mode()&os.ModeSymlink != 0:
		kind = "Symbolic link"
	}
	if stamp := d.info.ModTime(); !stamp.IsZero() {
		modified = stamp.Local().Format(time.DateTime + " MST")
	}
	return []string{
		"Name: " + clean(name),
		"Location: " + clean(d.location),
		"Path: " + clean(d.path),
		"Type: " + kind,
		"Size: " + size,
		"Modified: " + modified,
	}
}

func (m *Model) detailsWidth() int { return min(68, m.width-4) }

func (m *Model) detailsRows() []string {
	return strings.Split(ansi.Wrap(strings.Join(m.details.lines(), "\n"), m.detailsWidth(), ""), "\n")
}

func (m *Model) detailsPageSize() int { return max(1, m.height-4) }

func (m *Model) detailsScroll(delta int) {
	m.detailsOffset = max(0, min(m.detailsOffset+delta, len(m.detailsRows())-m.detailsPageSize()))
}

func (m *Model) detailsDialog() string {
	width := m.detailsWidth()
	rows := m.detailsRows()
	start := max(0, min(m.detailsOffset, len(rows)-m.detailsPageSize()))
	body := []string{line(foreground(coral, surfaceElevated).Bold(true), width, "Details")}
	for _, row := range rows[start:min(len(rows), start+m.detailsPageSize())] {
		body = append(body, line(foreground(cream, surfaceElevated), width, row))
	}
	keys := "[Esc] Close"
	if len(rows) > m.detailsPageSize() {
		keys += "   ↑↓ Scroll"
	}
	body = append(body, line(foreground(coral, surfaceElevated), width, keys))
	return card(coral, surfaceElevated).Padding(0, 1).Render(strings.Join(body, "\n"))
}
