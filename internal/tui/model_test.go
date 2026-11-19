package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"usbfs/internal/device"
	"usbfs/internal/fsops"
)

func TestTransferDisablesExitConfirmation(t *testing.T) {
	m := New(&device.Session{}, "/home/user")
	m.ready = true
	m.transfer = &transfer{name: "movie.mp4"}
	m.key(tea.KeyMsg{Type: tea.KeyEsc})
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
	_, open := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m.Update(open())
	if pane.path != filepath.Join(dir, chosen) || pane.filtering {
		t.Fatalf("opening a match did not enter its folder and clear the filter: %+v", pane)
	}
	_, parent := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m.Update(parent())
	if pane.path != dir {
		t.Fatalf("left arrow did not return to %q", dir)
	}
}

func TestTypingFiltersBothPanesAndPreservesSafeExit(t *testing.T) {
	m := New(&device.Session{}, "/home/user")
	m.ready = true
	for i, query := range []string{"ljk", "qchr"} {
		pane := &m.panes[i]
		pane.entries = []fsops.Entry{{Name: "Documents", Folder: true}, {Name: query + ".pdf"}}
		for _, r := range query {
			_, cmd := m.Update(runes(string(r)))
			if cmd == nil || m.confirm {
				t.Fatalf("typing %q in pane %d did not start a search", r, i)
			}
		}
		if pane.query != query || len(pane.visible()) != 1 || pane.visible()[0].Name != query+".pdf" {
			t.Fatalf("pane %d did not filter on %q", i, query)
		}
		m.Update(tea.KeyMsg{Type: tea.KeyTab})
	}
	if m.panes[0].query != "ljk" || m.focus != 0 {
		t.Fatal("switching panes lost the external filter")
	}
	quit := runes("q")
	quit.Alt = true
	m.Update(quit)
	if !m.confirm {
		t.Fatal("Alt+q must open safe-exit confirmation while filtering")
	}
	m.Update(runes("n"))
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if pane := &m.panes[0]; pane.filtering || len(pane.visible()) != 2 {
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

func TestCreateInFocusedPane(t *testing.T) {
	dir := t.TempDir()
	session, err := device.NewSession(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(os.Stderr)
	m := New(session, dir)
	m.ready, m.focus, m.platform = true, 1, device.Apple
	m.panes[1].setFilter(true, "some filter")
	for _, item := range []struct {
		key  tea.KeyType
		name string
		dir  bool
	}{{tea.KeyCtrlN, "Study files", true}, {tea.KeyCtrlF, "quiz.txt", false}} {
		m.Update(tea.KeyMsg{Type: item.key})
		m.Update(runes(item.name))
		_, submit := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if submit == nil {
			t.Fatalf("creation prompt did not submit %q", item.name)
		}
		m.Update(submit())
		info, err := os.Stat(filepath.Join(dir, item.name))
		if err != nil || info.IsDir() != item.dir || (!item.dir && info.Size() != 0) {
			t.Fatalf("unexpected creation result for %q: %v, %v", item.name, info, err)
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m.Update(runes("cancelled"))
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.confirm || m.create != nil {
		t.Fatal("escape should just close the creation prompt")
	}
	m.focus = 0
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if m.create != nil || !strings.Contains(m.status, "mounted folder") {
		t.Fatal("creation should require opening an iOS share")
	}
}

func TestDragBetweenPanes(t *testing.T) {
	for _, width := range []int{100, 54} {
		for from := range 2 {
			m := New(&device.Session{}, "/home/user")
			m.ready, m.platform, m.width, m.height = true, device.Apple, width, 28
			m.panes[from].entries = []fsops.Entry{{Name: "notes.txt"}}
			m.panes[1-from].entries = []fsops.Entry{{Name: "Study files", Folder: true}}
			m.panes[1].setFilter(true, "")
			var srcX, srcY, dstX, dstY int
			for y, line := range strings.Split(ansi.Strip(m.View()), "\n") {
				if pos := strings.Index(line, "notes.txt"); pos >= 0 {
					srcX, srcY = lipgloss.Width(line[:pos]), y
				}
				if pos := strings.Index(line, "Study files"); pos >= 0 {
					dstX, dstY = lipgloss.Width(line[:pos]), y
				}
			}
			m.Update(tea.MouseMsg{X: srcX, Y: srcY, Button: tea.MouseButtonLeft})
			m.Update(tea.MouseMsg{X: dstX, Y: dstY, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
			if m.drag == nil || m.drag.from != from || !strings.Contains(ansi.Strip(m.View()), "DROP TO COPY") {
				t.Fatalf("width %d, source pane %d: missing drag feedback", width, from)
			}
			if !strings.Contains(ansi.Strip(m.footer()), "Release to copy") {
				t.Fatal("drag did not show the release instructions")
			}
			index, row := m.mouseHit(dstX, dstY)
			if index != 1-from || row != 0 {
				t.Fatalf("drop missed folder row: pane %d, row %d", index, row)
			}
			target, ok := m.dropTarget(index, row)
			if !ok || target != filepath.Join(m.panes[index].path, "Study files") {
				t.Fatalf("unexpected target %q, allowed %t", target, ok)
			}
			_, cmd := m.Update(tea.MouseMsg{X: dstX, Y: dstY, Action: tea.MouseActionRelease})
			if cmd == nil || m.transfer == nil || m.transfer.name != "notes.txt" || m.drag != nil {
				t.Fatal("release did not begin the copy")
			}
			if !strings.Contains(ansi.Strip(m.footer()), "Copying notes.txt") {
				t.Fatal("copy did not show the active transfer")
			}
			m.Update(copyResult{name: "notes.txt", destination: destinationLabel(index, target)})
			if footer := ansi.Strip(m.footer()); !strings.Contains(footer, "✓ Copied notes.txt") || !strings.Contains(footer, "Study files") {
				t.Fatalf("copy result did not identify the file and destination:\n%s", footer)
			}
		}
	}
}

func TestSelectionAndCreateInputUseCoral(t *testing.T) {
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })
	m := New(&device.Session{}, "/home/user")
	m.ready, m.focus = true, 1
	m.panes[1].entries = []fsops.Entry{{Name: "selected.txt"}}
	style := foreground(coral, surfaceElevated)
	if !strings.Contains(m.View(), style.Render("›          selected.txt")) {
		for _, row := range strings.Split(m.View(), "\n") {
			if strings.Contains(row, "selected.txt") {
				t.Fatalf("selected row does not use the theme colors across the filename: %q", row)
			}
		}
		t.Fatal("selected row is missing")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m.Update(runes("Folder"))
	if !strings.Contains(m.View(), style.Render("Folder")) {
		for _, row := range strings.Split(m.View(), "\n") {
			if strings.Contains(row, "Folder") {
				t.Fatalf("typed name does not keep the theme colors: %q", row)
			}
		}
		t.Fatal("typed name is missing")
	}
}

func TestHelpPreservesPaneAndScrolls(t *testing.T) {
	m := New(&device.Session{}, "/home/user")
	m.ready, m.focus, m.width, m.height = true, 1, 116, 26
	m.panes[1].entries = []fsops.Entry{{Name: "quiz.txt"}}
	m.panes[1].setFilter(true, "quiz")
	m.Update(tea.KeyMsg{Type: tea.KeyF1})
	view := ansi.Strip(m.View())
	if !m.help || !strings.Contains(view, "/home/user · Filter: quiz") || !strings.Contains(view, "EXTERNAL DEVICE") || lipgloss.Height(view) != m.height {
		t.Fatalf("help did not appear over the panes:\n%s", view)
	}
	dialogWidth, dialogHeight := lipgloss.Size(m.helpDialog())
	border := strings.Split(view, "\n")[(m.height-dialogHeight)/2]
	if x := strings.Index(border, "╭"); x < 0 || lipgloss.Width(border[:x]) != (m.width-dialogWidth)/2 {
		t.Fatalf("help did not center horizontally: %q", border)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if !strings.Contains(ansi.Strip(m.View()), "first or last page") {
		t.Fatal("could not scroll to the last command")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.help || m.confirm || m.panes[1].query != "quiz" {
		t.Fatal("closing help changed the pane or opened unmount confirmation")
	}
}

func TestLinuxDeleteRequiresConfirmation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(path, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	session, err := device.NewSession(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(os.Stderr)
	m := New(session, dir)
	m.ready = true
	m.panes[0].entries = []fsops.Entry{{Name: "device.txt"}}
	m.Update(tea.KeyMsg{Type: tea.KeyDelete})
	if m.deleteName != "" || !strings.Contains(m.status, "Linux host pane only") {
		t.Fatal("external pane offered deletion")
	}
	m.focus = 1
	m.Update(list(nil, 1, dir))
	_, blocked := m.Update(tea.KeyMsg{Type: tea.KeyDelete})
	if blocked != nil || m.deleteName != "" || !strings.Contains(m.status, "regular files only") {
		t.Fatal("the selected folder should be blocked without a confirmation prompt")
	}
	m.panes[1].setFilter(true, "keep")
	m.Update(tea.KeyMsg{Type: tea.KeyDelete})
	if m.deleteName != "keep.txt" || !strings.Contains(ansi.Strip(m.View()), "Permanently delete") {
		t.Fatal("Delete did not show the selected file in a confirmation dialog")
	}
	m.Update(runes("n"))
	if _, err := os.Stat(path); err != nil || m.deleteName != "" {
		t.Fatalf("cancellation failed: %v", err)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDelete})
	_, confirm := m.Update(runes("y"))
	if confirm == nil {
		t.Fatal("confirm did not start deletion")
	}
	m.Update(confirm())
	if _, err := os.Stat(path); !os.IsNotExist(err) || len(m.panes[1].visible()) != 0 {
		t.Fatalf("deletion or pane refresh failed: %v", err)
	}
}

func TestViewFillsTerminal(t *testing.T) {
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })

	for _, size := range [][2]int{{12, 8}, {28, 5}, {32, 12}, {51, 19}, {117, 26}} {
		m := New(&device.Session{}, "/home/user")
		m.width, m.height = size[0], size[1]
		m.platform = device.Android
		for _, state := range []struct {
			name                 string
			ready, help, confirm bool
		}{
			{name: "waiting"},
			{name: "panes", ready: true},
			{name: "help", ready: true, help: true},
			{name: "confirm", ready: true, confirm: true},
		} {
			m.ready, m.help, m.confirm = state.ready, state.help, state.confirm
			rows := strings.Split(m.View(), "\n")
			if len(rows) != m.height {
				t.Fatalf("%s %v: got %d rows", state.name, size, len(rows))
			}
			for i, row := range rows {
				if ansi.StringWidth(row) != m.width || !strings.Contains(row, "\x1b[") {
					t.Fatalf("%s %v: row %d does not fill the screen with theme colors: %q", state.name, size, i, row)
				}
			}
		}
	}
}

func TestFolderKeyboardAndDragRequireConfirmation(t *testing.T) {
	host := t.TempDir()
	if err := os.Mkdir(filepath.Join(host, "Study"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(host, "Study", "quiz.txt"), []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	m := New(&device.Session{}, host)
	m.ready, m.focus = true, 1
	m.panes[1].entries = []fsops.Entry{{Name: "Study", Folder: true}}
	for _, drag := range []bool{false, true} {
		var prepare tea.Cmd
		if drag {
			boxes := m.paneBoxes(m.paneHeight())
			m.mouse(tea.MouseMsg{X: boxes[1].x + 3, Y: boxes[1].y + 3, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
			prepare = m.mouse(tea.MouseMsg{X: 3, Y: boxes[0].y + 6, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
		} else {
			prepare = m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}, Alt: true})
		}
		if prepare == nil {
			t.Fatalf("folder copy did not prepare (drag = %t)", drag)
		}
		m.Update(prepare())
		view := ansi.Strip(m.View())
		if m.folder == nil || m.transfer != nil || !strings.Contains(view, "1 files") || !strings.Contains(view, "External device") {
			t.Fatalf("folder copy did not show the planned destination and file count:\n%s", view)
		}
		m.key(tea.KeyMsg{Type: tea.KeyEsc})
	}
}
