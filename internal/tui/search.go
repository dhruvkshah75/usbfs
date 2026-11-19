package tui

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"usbfs/internal/device"
	"usbfs/internal/fsops"
)

const searchLimit = 1000

type searchResult struct {
	index      int
	job        *searchJob
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
		if path == "." || (prefix == "" && filepath.Dir(path) == ".") || (!item.IsDir() && !item.Type().IsRegular()) ||
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
	result := searchResult{index: index}
	if ctx.Err() != nil {
		result.err = ctx.Err()
		return result
	}
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
					err = result.searchTree(ctx, root.FS(), share, "", needle)
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

type searchJob struct {
	cancel context.CancelFunc
}

func (p *pane) stopSearch() {
	if p.search != nil {
		p.search.cancel()
		p.search = nil
	}
}

func (m *Model) searchQuery(index int, query string) tea.Cmd {
	p := &m.panes[index]
	p.setFilter(query != "", query)
	p.stopSearch()
	if filterText(query) == "" {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	job := &searchJob{cancel: cancel}
	p.search = job
	session, path := m.session, p.path
	run := m.work(func() tea.Msg {
		result := searchPane(ctx, session, index, path, query)
		result.job = job
		return result
	})
	return func() tea.Msg {
		delay := time.NewTimer(180 * time.Millisecond)
		defer delay.Stop()
		select {
		case <-ctx.Done():
			return nil
		case <-delay.C:
			return run()
		}
	}
}

func (m *Model) searchFinished(result searchResult) {
	p := &m.panes[result.index]
	if result.job == nil || result.job != p.search {
		return
	}
	p.stopSearch()
	p.searchStatus = ""
	if result.err != nil {
		p.searchStatus = " · Subfolders unavailable"
		return
	}
	p.results = result.entries
	p.replace(p.path, p.entries)
	if result.limited {
		p.searchStatus = fmt.Sprintf(" · First %d nested matches", searchLimit)
	}
	if result.unreadable > 0 {
		p.searchStatus += fmt.Sprintf(" · %d unreadable", result.unreadable)
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
