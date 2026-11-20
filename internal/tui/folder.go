package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"usbfs/internal/device"
	"usbfs/internal/fsops"
)

type folderCopy struct {
	from                 int
	source, target, view string
	plan                 fsops.FolderPlan
}

type folderPrepared struct {
	copy folderCopy
	err  error
}

func openFolder(session *device.Session, index int, path string) (*os.Root, error) {
	if index == 1 {
		return session.OpenHostDirectory(path)
	}
	root, relative, err := session.OpenPath(path)
	if err != nil || relative == "." {
		return root, err
	}
	defer root.Close()
	info, err := root.Lstat(relative)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("choose a real folder")
	}
	return root.OpenRoot(relative)
}

func (m *Model) prepareFolder(from int, entry fsops.Entry, target string) tea.Cmd {
	if entry.Symlink {
		m.message("Choose a real folder to copy.", true)
		return nil
	}
	m.busy = true
	copy := folderCopy{from: from, source: filepath.Join(m.panes[from].path, entry.Name),
		target: target, view: m.panes[1-from].path}
	session := m.session
	m.message("Scanning folder…", false)
	return m.work(func() tea.Msg {
		result := folderPrepared{copy: copy}
		source, err := openFolder(session, from, copy.source)
		if err != nil {
			result.err = err
			return result
		}
		defer source.Close()
		result.copy.plan, result.err = fsops.PlanFolder(source)
		return result
	})
}

func (m *Model) copyFolder(job *folderCopy) tea.Cmd {
	name := filepath.Base(job.source)
	copy := &transfer{name: name, destination: destinationLabel(1-job.from, job.target), folder: true}
	copy.total.Store(int64(job.plan.Files))
	m.busy, m.transfer = true, copy
	session := m.session
	run := m.work(func() tea.Msg {
		result := copyResult{download: job.from == 0, name: name, destination: copy.destination}
		source, err := openFolder(session, job.from, job.source)
		if err != nil {
			result.err = err
			return result
		}
		defer source.Close()
		target, err := openFolder(session, 1-job.from, job.target)
		if err != nil {
			result.err = err
			return result
		}
		defer target.Close()
		path, uri := session.GVFS()
		gvfs := fsops.GVFS{Path: path, URI: uri}
		report := gvfs.CopyFolder(source, target, name, job.plan, result.download, func(count, _ int64) {
			copy.count.Store(count)
		})
		result.folder = &report
		if result.download {
			result.target = list(nil, 1, job.view)
		} else {
			result.target = listExternal(session, job.view)
		}
		return result
	})
	return tea.Batch(run, watch(copy))
}

func (m *Model) folderFinished(name string, report fsops.FolderReport, refreshErr error) {
	warning := report.Skipped > 0 || report.Failed > 0 || refreshErr != nil
	m.message(fmt.Sprintf("%s · %d files copied · %d skipped · %d failed", name, report.Copied, report.Skipped, report.Failed), warning)
	switch {
	case report.FirstError != nil:
		m.statusDetail = report.FirstError.Error()
	case refreshErr != nil:
		m.statusDetail = "Refresh failed: " + refreshErr.Error()
	}
}
