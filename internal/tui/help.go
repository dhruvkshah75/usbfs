package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var helpCommands = []struct{ keys, action string }{
	{"", "Browse and filter"},
	{"Tab / click", "Focus a pane or select a row"},
	{"↑ ↓ / wheel", "Change selection."},
	{"Enter / →", "Open a folder or launch a file."},
	{"←", "Go up a directory."},
	{"Type", "Search the focused folder and its subfolders."},
	{"Backspace / Ctrl+U", "Erase a character or clear the search. Esc also clears it."},
	{"Alt+i", "Show details for the selected item. Esc closes the dialog."},
	{"", "Copy and create"},
	{"Drag an item", "Drop on a folder or in the opposite pane's open directory."},
	{"Alt+c", "Copy a file or folder to the opposite pane. Confirm folder copies with y."},
	{"Ctrl+N / Ctrl+F", "Create a folder or empty file in the focused pane."},
	{"Enter / Esc", "Submit or cancel a name. Use arrows and Backspace/Delete to edit."},
	{"Delete", "Delete a regular file from Linux. Confirm with y, or cancel with n/Esc."},
	{"Alt+r", "Refresh both panes and rerun active searches."},
	{"", "Connection and exit"},
	{"a / i", "Choose Android or iPhone/iPad at startup."},
	{"r / b", "Retry detection or go back to device type while unmounted."},
	{"Esc / Alt+q", "Quit or ask to unmount. q also quits during setup."},
	{"y / n or Esc", "Confirm or cancel unmount and exit. Active copies must finish first."},
	{"Ctrl+C", "Exit through the verified unmount cleanup."},
	{"", "Help"},
	{"F1 / Esc", "Return to the previous screen."},
	{"↑ ↓ / wheel", "Scroll the help screen."},
	{"PgUp/PgDn", "Scroll one page."},
	{"Home/End", "Jump to the first or last page."},
}

func (m *Model) helpWidth() int { return max(1, min(68, m.width-8)) }

func (m *Model) helpRows() []string {
	content := m.helpWidth()
	width := max(1, content-2)
	var rows []string
	for _, cmd := range helpCommands {
		if cmd.keys == "" {
			if len(rows) > 0 {
				rows = append(rows, line(foreground(cream, surfaceElevated), content, ""))
			}
			rows = append(rows, line(foreground(coral, surfaceElevated).Bold(true), content, " "+cmd.action))
			continue
		}
		keys, indent := cmd.keys, lipgloss.Width(cmd.keys)+2
		if indent > width/2 {
			for _, row := range strings.Split(ansi.Wrap(keys, width, ""), "\n") {
				rows = append(rows, line(foreground(coral, surfaceSoft), content, " "+row))
			}
			keys, indent = "", 0
		}
		for _, row := range strings.Split(ansi.Wrap(cmd.action, width-indent, ""), "\n") {
			prefix := strings.Repeat(" ", indent+1)
			if keys != "" {
				prefix, keys = " "+keys+"  ", ""
			}
			text := foreground(coral, surfaceSoft).Render(prefix) + foreground(cream, surfaceSoft).Render(row)
			rows = append(rows, styledLine(content, text, surfaceSoft))
		}
	}
	return rows
}

func (m *Model) helpPageSize() int { return max(1, min(14, m.height-7)) }

func (m *Model) helpScroll(delta int) {
	m.helpOffset = max(0, min(m.helpOffset+delta, len(m.helpRows())-m.helpPageSize()))
}

func (m *Model) helpDialog() string {
	width := m.helpWidth()
	title := line(foreground(coral, surfaceElevated).Bold(true), width, " usbfs / Help")
	context := "Device setup"
	if m.ready {
		p := &m.panes[m.focus]
		context = destinationLabel(m.focus, p.path)
		if p.filtering {
			context += " · " + p.queryLabel()
		}
	}
	context = line(foreground(muted, surfaceSoft), width, " "+clean(context))
	rows := m.helpRows()
	pageSize := m.helpPageSize()
	start := max(0, min(m.helpOffset, len(rows)-pageSize))
	body := append([]string{title, context}, rows[start:min(len(rows), start+pageSize)]...)
	body = append(body, line(foreground(coral, surfaceElevated), width, " F1/Esc Close   ↑↓ Scroll"))
	return card(coral, surfaceElevated).Padding(0, 1).Render(strings.Join(body, "\n"))
}
