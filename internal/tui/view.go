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

func styledLine(width int, text, background string) string {
	text = ansi.Truncate(text, width, "")
	return text + line(foreground(cream, background), width-lipgloss.Width(text), "")
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
		if m.help {
			return line(foreground(coral, surfaceElevated), m.width, "F1/Esc Close")
		}
		if m.confirm || m.create != nil || m.deleteName != "" {
			return m.compactModal()
		}
		return line(foreground(coral, surface), m.width, "F1 Help · Esc")
	}
	header := line(foreground(coral, surfaceElevated).Bold(true), m.width, " usbfs")
	if m.device.Kind != "" {
		header = styledLine(m.width, foreground(coral, surfaceElevated).Bold(true).Render(" usbfs   ")+
			foreground(teal, surfaceElevated).Bold(true).Render("● MOUNTED   ")+
			foreground(cream, surfaceElevated).Render(clean(m.device.Kind+" · "+m.device.Name))+
			foreground(muted, surfaceElevated).Render("   DEVICE DELETE PROTECTED"), surfaceElevated)
	}
	footer := m.footer()
	height := m.paneHeight()
	boxes := m.paneBoxes(height)
	var main string
	switch {
	case !m.ready:
		main = m.waiting(height)
	case m.width >= 66:
		gutter := strings.TrimSuffix(strings.Repeat(fill(2)+"\n", height), "\n")
		main = lipgloss.JoinHorizontal(lipgloss.Top, m.pane(0, boxes[0].width, height), gutter, m.pane(1, boxes[1].width, height))
	case boxes[0].height > 0 && boxes[1].height > 0:
		main = lipgloss.JoinVertical(lipgloss.Left, m.pane(0, m.width, boxes[0].height), fill(m.width), m.pane(1, m.width, boxes[1].height))
	default:
		main = m.pane(m.focus, m.width, height)
	}
	screen := lipgloss.JoinVertical(lipgloss.Left, header, main, footer)
	if m.help {
		screen = m.centerDialog(screen, m.helpDialog())
	} else if m.confirm || m.create != nil || m.deleteName != "" {
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
		heading = m.spinner.View() + foreground(cream, surfaceElevated).Bold(true).Render(" "+title)
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
	border, heading := outline, cream
	if m.focus == index {
		border, heading = coral, coral
	}
	dropRow := -1
	if m.drag != nil && m.drag.moved && m.drag.from != index {
		if hit, row := m.mouseHit(m.drag.x, m.drag.y); hit == index {
			if _, ok := m.dropTarget(index, row); ok {
				border, heading = coral, coral
				title = "↓ DROP TO COPY"
				if row >= 0 && entries[row].Folder {
					dropRow = row
				}
			}
		}
	}
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
			_, start, end := p.entryWindow(height)
			if start < end {
				label = " › " + clean(entries[start].Name)
			}
			rows = append(rows, line(foreground(heading, surfaceElevated), width, label))
		}
		for len(rows) < height {
			rows = append(rows, fill(width))
		}
		return strings.Join(rows, "\n")
	}
	content := width - 4
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
	if len(entries) == 0 {
		rows = append(rows, line(foreground(muted, surfaceSoft), content, empty))
	}
	_, start, end := p.entryWindow(height)
	for i := start; i < end; i++ {
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
				ink = coral
			}
		}
		if i == dropRow {
			prefix, ink, background = "↓ ", coral, surfaceElevated
		} else if m.drag != nil && m.drag.moved && m.drag.from == index && entry.Name == m.drag.entry.Name {
			prefix, ink, background = "↗ ", coral, surfaceElevated
		}
		rows = append(rows, line(foreground(ink, background), content, prefix+label+clean(entry.Name)))
	}
	for len(rows) < height-2 {
		rows = append(rows, line(foreground(muted, surfaceSoft), content, ""))
	}
	return card(border, surfaceSoft).Padding(0, 1).Render(strings.Join(rows, "\n"))
}

func (m *Model) statusHeight() int {
	if m.height < 9 {
		return 1
	}
	return 2
}

func (m *Model) paneHeight() int {
	return max(1, m.height-2-m.statusHeight())
}

func (m *Model) footer() string {
	status, detail, ink := clean(m.status), clean(m.statusDetail), muted
	switch {
	case m.transfer != nil:
		copy := m.transfer
		status, ink = ansi.Strip(m.spinner.View())+" Copying "+clean(copy.name), coral
		detail = "→ " + clean(copy.destination)
		if total := copy.total.Load(); total >= 0 {
			percent := 100
			if total > 0 {
				percent = min(100, int(float64(copy.count.Load())/float64(total)*100))
			}
			filled := percent / 10
			status = fmt.Sprintf(" %s%s %3d%%  %s", strings.Repeat("━", filled), strings.Repeat("─", 10-filled), percent, status)
		}
	case m.drag != nil && m.drag.moved:
		status, detail, ink = "↗ Dragging "+clean(m.drag.entry.Name), "Move to the opposite pane to copy", coral
		if index, row := m.mouseHit(m.drag.x, m.drag.y); index >= 0 && index != m.drag.from {
			if path, ok := m.dropTarget(index, row); ok {
				detail = "↓ Release to copy → " + clean(destinationLabel(index, path))
			} else {
				detail = "Choose an accessible destination folder"
			}
		}
	case m.warning:
		ink = amber
	case strings.HasPrefix(status, "✓"):
		ink = coral
	}
	keys := "[F1] Help   [Esc] Exit"
	switch {
	case m.ready:
		keys += "   [Tab] Switch   [Alt+c] Copy"
	case m.platform != "" && m.width >= 36:
		keys += "   [r] Retry"
	}
	if m.ready && m.width >= 66 {
		keys += "   Type to filter"
	}
	if detail == "" && m.statusHeight() > 1 {
		pieces := strings.SplitN(ansi.Wrap(status, m.width-1, ""), "\n", 2)
		status = pieces[0]
		if len(pieces) == 2 {
			detail = strings.ReplaceAll(pieces[1], "\n", " ")
		}
	}
	rows := []string{line(foreground(ink, surface), m.width, " "+status)}
	if m.statusHeight() > 1 {
		rows = append(rows, line(foreground(ink, surface), m.width, " "+detail))
	}
	return strings.Join(append(rows, line(foreground(muted, surfaceElevated), m.width, " "+keys)), "\n")
}

func (m *Model) modal(screen string) string {
	width := min(56, m.width-2)
	prompt, controls := "Unmount device and exit?", "[y] Unmount & Quit   [n] Cancel"
	if m.transfer != nil {
		prompt, controls = "Transfer in progress! Wait for the copy to finish before unmounting.", "[n] Return to transfer"
	}
	if m.deleteName != "" {
		prompt, controls = "Permanently delete this file from Linux?", "[y] Yes, delete   [n/Esc] No"
	}
	contentWidth := width - 4
	input := ""
	if m.deleteName != "" {
		input = foreground(coral, surfaceElevated).Render(ansi.Wrap(clean(m.deleteName), contentWidth, "")) + "\n"
	}
	if m.create != nil {
		destination := "external device"
		if m.focus == 1 {
			destination = "Linux host"
		}
		prompt, controls = m.create.title()+" · "+destination, "[Enter] Create   [Esc] Cancel"
		input = styledLine(contentWidth, m.create.input.View(), surfaceElevated) + "\n"
	}
	body := foreground(cream, surfaceElevated).Bold(true).Render(ansi.Wrap(prompt, contentWidth, "")) + "\n" + input +
		foreground(coral, surfaceElevated).Render(ansi.Wrap(controls, contentWidth, ""))
	dialog := card(coral, surfaceElevated).Width(width-2).Padding(0, 1).Render(body)
	if lipgloss.Height(dialog) > m.height {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.compactModal(),
			lipgloss.WithWhitespaceBackground(lipgloss.Color(surface)))
	}
	return m.centerDialog(screen, dialog)
}

func (m *Model) centerDialog(screen, dialog string) string {
	rows, overlay := strings.Split(screen, "\n"), strings.Split(dialog, "\n")
	width := lipgloss.Width(dialog)
	x, y := (m.width-width)/2, (len(rows)-len(overlay))/2
	for i, row := range overlay {
		behind := rows[y+i]
		left, right := ansi.Cut(behind, 0, x), ansi.Cut(behind, x+width, m.width)
		rows[y+i] = left + strings.Repeat(" ", max(0, x-lipgloss.Width(left))) + row + right
	}
	return strings.Join(rows, "\n")
}

func (m *Model) compactModal() string {
	if m.create != nil {
		return styledLine(m.width, m.create.input.View(), surfaceElevated)
	}
	label := "y Unmount / n"
	if m.deleteName != "" {
		label = "y Delete / n"
		if m.height >= 2 {
			return line(foreground(coral, surfaceElevated).Bold(true), m.width, label) + "\n" +
				line(foreground(coral, surfaceElevated), m.width, clean(m.deleteName))
		}
	}
	if m.transfer != nil {
		label = "n Back · Copy"
	}
	return line(foreground(coral, surfaceElevated).Bold(true), m.width, label)
}

func (m *Model) nameWidth() int {
	return max(1, min(56, m.width-2)-6)
}
