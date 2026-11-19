package tui

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

type searchRoot struct {
	tree   fs.FS
	prefix string
}

type searchDirectory struct {
	root searchRoot
	path string
}

func (r *searchResult) searchTrees(ctx context.Context, roots []searchRoot, excluded, needle string) error {
	queue := make([]searchDirectory, 0, len(roots))
	for _, root := range roots {
		queue = append(queue, searchDirectory{root, "."})
	}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir := queue[0]
		queue[0] = searchDirectory{}
		queue = queue[1:]
		children, err := fs.ReadDir(dir.root.tree, dir.path)
		if err != nil {
			if dir.path == "." && dir.root.prefix == "" {
				return err
			}
			r.unreadable++
		}
		for _, child := range children {
			if err := ctx.Err(); err != nil {
				return err
			}
			path := filepath.Join(dir.path, child.Name())
			relative := filepath.Join(dir.root.prefix, path)
			if relative == excluded || (!child.IsDir() && !child.Type().IsRegular()) {
				continue
			}
			if child.IsDir() {
				queue = append(queue, searchDirectory{dir.root, path})
			}
			// The pane already shows matches directly under its current directory.
			if filepath.Dir(relative) == "." || !strings.Contains(filterText(child.Name()), needle) {
				continue
			}
			if len(r.entries) == searchLimit {
				r.limited = true
				return nil
			}
			entry := fsops.Entry{Name: relative, Folder: child.IsDir()}
			r.entries = append(r.entries, entry)
			if r.job != nil {
				r.job.publish(entry)
			}
		}
	}
	return nil
}

func searchPane(ctx context.Context, session *device.Session, index int, path, query string, job *searchJob) searchResult {
	result := searchResult{index: index, job: job}
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
			var roots []searchRoot
			var opened []*os.Root
			defer func() {
				for _, root := range opened {
					root.Close()
				}
			}()
			for _, share := range shares {
				if ctx.Err() != nil {
					break
				}
				root, err := openFolder(session, 0, share)
				if err != nil {
					result.unreadable++
					continue
				}
				opened = append(opened, root)
				roots = append(roots, searchRoot{root.FS(), share})
			}
			result.err = result.searchTrees(ctx, roots, "", needle)
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
	result.err = result.searchTrees(ctx, []searchRoot{{tree: root.FS()}}, excluded, needle)
	return result
}

type searchJob struct {
	cancel  context.CancelFunc
	mu      sync.Mutex
	pending []fsops.Entry
}

func (job *searchJob) publish(entry fsops.Entry) {
	job.mu.Lock()
	job.pending = append(job.pending, entry)
	job.mu.Unlock()
}

func (job *searchJob) take() []fsops.Entry {
	job.mu.Lock()
	defer job.mu.Unlock()
	entries := job.pending
	job.pending = nil
	return entries
}

func (p *pane) receiveSearchMatches() {
	if p.search != nil {
		if entries := p.search.take(); len(entries) > 0 {
			p.results = append(p.results, entries...)
			p.replace(p.path, p.entries)
		}
	}
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
		return searchPane(ctx, session, index, path, query, job)
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
