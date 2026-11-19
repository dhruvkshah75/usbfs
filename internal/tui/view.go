package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"usbfs/internal/device"
)

const (
	surface         = "#181715"
	surfaceSoft     = "#1f1e1b"
	surfaceElevated = "#252320"
	cream           = "#faf9f5"
	muted           = "#a09d96"
	outline         = "#6c6a64"
	coral           = "#cc785c"
	teal            = "#5db8a6"
	amber           = "#e8a55a"
)

func foreground(ink, background string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(ink)).Background(lipgloss.Color(background))
}

func clean(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return '�'
		}
		return r
	}, text)
}

func line(style lipgloss.Style, width int, text string) string {
	return style.Width(width).Render(ansi.Truncate(text, width, "…"))
}

func fill(width int) string { return line(foreground(cream, surface), width, "") }

func card(border, background string) lipgloss.Style {
	return foreground(cream, background).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(border)).
		BorderBackground(lipgloss.Color(surface))
}

func (m *Model) View() string {
	if m.width < 16 || m.height < 6 {
		if m.confirm {
			return m.compactModal()
		}
		return line(foreground(coral, surface), m.width, "usbfs · q Quit")
	}
	header := line(foreground(coral, surfaceElevated).Bold(true), m.width, " usbfs")
	if m.device.Kind != "" {
		header = line(foreground(cream, surfaceElevated), m.width, " "+
			foreground(coral, surfaceElevated).Bold(true).Render("usbfs")+"   "+
			foreground(teal, surfaceElevated).Bold(true).Render("● MOUNTED")+"   "+
			clean(m.device.Kind+" · "+m.device.Name))
	}
	footer := m.footer()
	height := max(1, m.height-lipgloss.Height(header)-lipgloss.Height(footer))
	var main string
	switch {
	case !m.ready:
		main = m.waiting(height)
	case m.width >= 66:
		left := (m.width - 2) / 2
		gutter := strings.TrimSuffix(strings.Repeat(fill(2)+"\n", height), "\n")
		main = lipgloss.JoinHorizontal(lipgloss.Top, m.pane(0, left, height), gutter, m.pane(1, m.width-left-2, height))
	case height >= 13:
		top := (height - 1) / 2
		main = lipgloss.JoinVertical(lipgloss.Left, m.pane(0, m.width, top), fill(m.width), m.pane(1, m.width, height-top-1))
	default:
		main = m.pane(m.focus, m.width, height)
	}
	screen := lipgloss.JoinVertical(lipgloss.Left, header, main, footer)
	if m.confirm {
		screen = m.modal(screen)
	}
	return lipgloss.NewStyle().MaxHeight(m.height).Render(screen)
}

func (m *Model) waiting(height int) string {
	width := min(58, m.width-4)
	contentWidth := max(1, width-6)
	title, hint := "Choose a device type", "[a] Android     [i] iPhone / iPad"
	switch {
	case m.mtpIssue == device.ErrMTPBusy:
		title, hint = "Android USB connection is busy", "Eject the phone in your Linux file manager or close the app using it. Keep the phone connected, then press r to retry."
	case m.mtpIssue == device.ErrMTPSession:
		title, hint = "Could not open Android storage", "Unlock the phone. In the USB notification, switch to Charging only, then back to File transfer / Android Auto. Press r to retry."
	case m.device.Kind != "":
		title, hint = "Waiting for device access…", "Unlock the device and check its USB access permissions."
	case m.platform == device.Android:
		title, hint = "Waiting for Android…", "Unlock your phone and open the USB notification.\nSelect File transfer / Android Auto."
	case m.platform == device.Apple:
		title, hint = "Waiting for iPhone or iPad…", "Unlock your device and tap Trust if prompted."
	}
	heading := foreground(cream, surfaceElevated).Bold(true).Render(title)
	if m.platform != "" && (m.mtpIssue == nil || m.busy) {
		heading = m.spinner.View() + " " + heading
	}
	body := heading +
		"\n\n" + foreground(muted, surfaceElevated).Render(ansi.Wrap(hint, contentWidth, ""))
	panel := card(outline, surfaceElevated).Width(width-2).Padding(1, 2).Render(ansi.Wrap(body, contentWidth, ""))
	if width < 18 || lipgloss.Height(panel) > height {
		if m.platform == "" {
			title = "a Android  i Apple"
		}
		panel = line(foreground(coral, surface), m.width, " "+title)
	}
	return lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, panel,
		lipgloss.WithWhitespaceBackground(lipgloss.Color(surface)))
}

func (m *Model) pane(index, width, height int) string {
	p := &m.panes[index]
	entries := p.visible()
	title, path := "EXTERNAL DEVICE", filepath.Join(m.session.Path(), p.path)
	if index == 1 {
		title, path = "LINUX HOST", p.path
	}
	filter := "Filter: " + clean(p.query)
	empty := "  Empty folder"
	if p.filtering {
		empty = "  No matches"
	}
	if height < 5 {
		if p.filtering {
			title = filter
		}
		rows := []string{line(foreground(coral, surfaceSoft).Bold(true), width, " "+title)}
		if height > 1 {
			label := empty
			if len(entries) != 0 {
				label = " › " + clean(entries[p.cursor].Name)
			}
			rows = append(rows, line(foreground(cream, surfaceElevated), width, label))
		}
		for len(rows) < height {
			rows = append(rows, fill(width))
		}
		return strings.Join(rows, "\n")
	}
	content := width - 4
	border, heading := outline, cream
	if m.focus == index {
		border, heading = coral, coral
	}
	pathLine := line(foreground(muted, surfaceSoft), content, clean(path))
	rows := []string{
		line(foreground(heading, surfaceSoft).Bold(true), content, title),
		pathLine,
	}
	if p.filtering {
		filterLine := line(foreground(coral, surfaceSoft), content, filter)
		if height >= 6 {
			rows = append(rows, filterLine)
		} else {
			rows[1] = filterLine
		}
	}
	capacity := height - 2 - len(rows)
	if len(entries) == 0 {
		rows = append(rows, line(foreground(muted, surfaceSoft), content, empty))
	}
	start := max(0, p.cursor-capacity+1)
	for i := start; i < min(len(entries), start+capacity); i++ {
		entry := entries[i]
		prefix, label, ink, background := "  ", "         ", cream, surfaceSoft
		switch {
		case entry.Locked:
			label, ink = "[LOCKED] ", amber
		case entry.Folder:
			label, ink = "[DIR]    ", teal
		}
		if p.cursor == i {
			prefix, background = "› ", surfaceElevated
			if m.focus == index {
				prefix = foreground(coral, background).Render(prefix)
			}
		}
		rows = append(rows, line(foreground(ink, background), content, prefix+label+clean(entry.Name)))
	}
	for len(rows) < height-2 {
		rows = append(rows, line(foreground(muted, surfaceSoft), content, ""))
	}
	return card(border, surfaceSoft).Padding(0, 1).Render(strings.Join(rows, "\n"))
}

func (m *Model) footer() string {
	status, ink := clean(m.status), muted
	if m.warning {
		ink = amber
	}
	if m.transfer != nil {
		status, ink = "Copying "+clean(m.transfer.name)+"…", cream
		if total := m.transfer.total.Load(); total >= 0 {
			count := m.transfer.count.Load()
			bar := m.progress
			bar.Width = min(24, max(4, m.width/4))
			percent := 1.0
			if total > 0 {
				percent = min(1, float64(count)/float64(total))
			}
			status = fmt.Sprintf("%s %3.0f%%  %s", bar.ViewAs(percent), percent*100, status)
		}
	}
	keys := "[r] Retry   [q] Quit"
	if m.width < 21 {
		keys = "r Retry  q Quit"
	}
	switch {
	case !m.ready && m.platform == "":
		keys = "[q] Quit"
	case !m.ready && !m.session.HasMount() && m.width >= 38:
		keys = "[r] Retry   [b] Device type   [q] Quit"
	case m.ready && m.focus == 1 && m.panes[1].filtering:
		keys = "[Ctrl+U] Clear   [↑↓] Select   [↵] Open   [Alt+c] Copy   [Alt+r] Refresh   [Tab] Switch   [Esc] Exit"
	case m.ready && m.focus == 1:
		keys = "Type or [/] to filter   [Tab] Switch   [↑↓] Select   [↵] Open   [←] Parent   [c] Copy   [r] Refresh   [q] Exit"
	case m.ready:
		keys = "[Tab] Switch   [↑↓] Select   [↵] Open   [←] Parent   [c] Copy   [r] Refresh   [q] Exit"
	}
	keyRows := []string{keys}
	if m.ready && m.height >= 12 {
		keyRows = strings.Split(ansi.Wrap(keys, m.width-1, ""), "\n")
		keyRows = keyRows[:min(len(keyRows), m.height/4)]
	}
	var controls []string
	if m.device.Kind != "" {
		controls = append(controls, line(foreground(teal, surface).Bold(true), m.width, " DEVICE DELETE PROTECTED"))
	}
	for _, row := range keyRows {
		controls = append(controls, line(foreground(muted, surfaceElevated), m.width, " "+row))
	}
	statusRows := strings.Split(ansi.Wrap(status, m.width-1, ""), "\n")
	if m.height < 12 {
		statusRows = statusRows[:1]
	}
	var rendered []string
	for _, row := range statusRows[:min(2, len(statusRows))] {
		rendered = append(rendered, line(foreground(ink, surface), m.width, " "+row))
	}
	return strings.Join(append(rendered, controls...), "\n")
}

func (m *Model) modal(screen string) string {
	width := min(56, m.width-2)
	prompt, controls := "Unmount device and exit?", "[y] Unmount & Quit   [n] Cancel"
	if m.transfer != nil {
		prompt, controls = "Transfer in progress! Wait for the copy to finish before unmounting.", "[n] Return to transfer"
	}
	contentWidth := width - 4
	body := foreground(cream, surfaceElevated).Bold(true).Render(ansi.Wrap(prompt, contentWidth, "")) + "\n" +
		foreground(coral, surfaceElevated).Render(ansi.Wrap(controls, contentWidth, ""))
	dialog := card(coral, surfaceElevated).Width(width-2).Padding(0, 1).Render(body)
	rows, overlay := strings.Split(screen, "\n"), strings.Split(dialog, "\n")
	if len(overlay) > min(len(rows), m.height) {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.compactModal(),
			lipgloss.WithWhitespaceBackground(lipgloss.Color(surface)))
	}
	x, y := (m.width-width)/2, (len(rows)-len(overlay))/2
	for i, row := range overlay {
		behind := rows[y+i]
		left, right := ansi.Cut(behind, 0, x), ansi.Cut(behind, x+width, m.width)
		rows[y+i] = left + strings.Repeat(" ", max(0, x-lipgloss.Width(left))) + row + right
	}
	return strings.Join(rows, "\n")
}

func (m *Model) compactModal() string {
	label := "y Unmount / n"
	if m.transfer != nil {
		label = "n Back · Copy"
	}
	return line(foreground(coral, surfaceElevated).Bold(true), m.width, label)
}
