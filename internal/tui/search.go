package tui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"usbfs/internal/device"
	"usbfs/internal/fsops"
)

const searchLimit = 1000

type searchResult struct {
	index      int
	query      string
	entries    []fsops.Entry
	unreadable int
	limited    bool
	err        error
}

func (r *searchResult) searchTree(ctx context.Context, tree fs.FS, prefix, excluded, needle string) error {
	return fs.WalkDir(tree, ".", func(path string, item fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if path == excluded {
			return fs.SkipDir
		}
		if err != nil {
			if path == "." {
				return err
			}
			r.unreadable++
			return nil
		}
		if path == "." || (!item.IsDir() && !item.Type().IsRegular()) ||
			!strings.Contains(filterText(item.Name()), needle) {
			return nil
		}
		if len(r.entries) == searchLimit {
			r.limited = true
			return fs.SkipAll
		}
		r.entries = append(r.entries, fsops.Entry{Name: filepath.Join(prefix, path), Folder: item.IsDir()})
		return nil
	})
}

func searchPane(ctx context.Context, session *device.Session, index int, path, query string) searchResult {
	result := searchResult{index: index, query: query}
	needle := filterText(query)
	if index == 0 && path == "." {
		shares, err := session.Shares()
		if err != nil {
			result.err = err
			return result
		}
		if shares != nil {
			for _, share := range shares {
				if ctx.Err() != nil || result.limited {
					break
				}
				root, err := openFolder(session, 0, share)
				if err == nil {
					if strings.Contains(filterText(share), needle) {
						if len(result.entries) == searchLimit {
							result.limited = true
						} else {
							result.entries = append(result.entries, fsops.Entry{Name: share, Folder: true})
						}
					}
					if !result.limited {
						err = result.searchTree(ctx, root.FS(), share, "", needle)
					}
					root.Close()
				}
				if err != nil {
					result.unreadable++
				}
			}
			result.err = ctx.Err()
			return result
		}
	}

	root, err := openFolder(session, index, path)
	if err != nil {
		result.err = err
		return result
	}
	defer root.Close()
	excluded := ""
	if index == 1 && session.Path() != "" {
		if real, err := filepath.EvalSymlinks(path); err == nil {
			if relative, err := filepath.Rel(real, session.Path()); err == nil && filepath.IsLocal(relative) {
				excluded = relative
			}
		}
	}
	result.err = result.searchTree(ctx, root.FS(), "", excluded, needle)
	return result
}

func (m *Model) startSearch(index int, query string) tea.Cmd {
	query = strings.TrimSpace(query)
	if filterText(query) == "" {
		m.message("Enter a filename to search subfolders.", true)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.busy, m.cancelSearch = true, cancel
	m.message("Searching subfolders… Press Esc to cancel.", false)
	session, path := m.session, m.panes[index].path
	return m.work(func() tea.Msg { return searchPane(ctx, session, index, path, query) })
}

func (m *Model) searchFinished(result searchResult) {
	m.busy = false
	cancelled := m.cancelSearch == nil || errors.Is(result.err, context.Canceled)
	if m.cancelSearch != nil {
		m.cancelSearch()
		m.cancelSearch = nil
	}
	if cancelled {
		return
	}
	if result.err != nil {
		m.message("Cannot search folder: "+result.err.Error(), true)
		return
	}
	p := &m.panes[result.index]
	p.search, p.results, p.cursor = result.query, result.entries, 0
	count := fmt.Sprintf("%d matches", len(result.entries))
	if result.limited {
		count = fmt.Sprintf("First %d matches. Refine the query for more specific results.", searchLimit)
	}
	m.message(count, result.unreadable > 0 || result.limited)
	if result.unreadable > 0 {
		m.statusDetail = fmt.Sprintf("%d locations could not be read.", result.unreadable)
	}
}

func newInput(width int, placeholder string) textinput.Model {
	input := textinput.New()
	input.Placeholder = placeholder
	input.Prompt = "> "
	input.Width = width
	input.TextStyle = foreground(coral, surfaceElevated)
	input.PromptStyle = input.TextStyle
	input.PlaceholderStyle = foreground(muted, surfaceElevated)
	// Bubbles reverses the cursor style when drawing the caret.
	input.Cursor.Style = foreground(surfaceElevated, coral)
	input.Cursor.TextStyle = input.TextStyle
	return input
}
