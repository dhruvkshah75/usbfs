package fsops

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

type FolderPlan struct {
	Files, Skipped, Failed int
	items                  []folderItem
	firstError             error
}

type folderItem struct {
	path string
	dir  bool
}

type FolderReport struct {
	Copied, Skipped, Failed int
	FirstError              error
}

// PlanFolder enumerates a single filesystem without following links.
func PlanFolder(source *os.Root) (FolderPlan, error) {
	var plan FolderPlan
	top, err := source.Stat(".")
	if err != nil {
		return plan, err
	}
	dev := top.Sys().(*syscall.Stat_t).Dev
	err = fs.WalkDir(source.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if path == "." {
			return err
		}
		if err != nil {
			plan.Failed++
			if plan.firstError == nil {
				plan.firstError = fmt.Errorf("%s: %w", path, err)
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			plan.Failed++
			if plan.firstError == nil {
				plan.firstError = fmt.Errorf("%s: %w", path, err)
			}
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if info.Sys().(*syscall.Stat_t).Dev != dev || (!info.IsDir() && !info.Mode().IsRegular()) {
			plan.Skipped++
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		plan.items = append(plan.items, folderItem{path, info.IsDir()})
		if !info.IsDir() {
			plan.Files++
		}
		return nil
	})
	return plan, err
}

// CopyFolder creates a new tree. A conflict skips the item and any descendants.
func CopyFolder(source, target *os.Root, name string, plan FolderPlan, download bool, progress func(int64, int64)) FolderReport {
	return (GVFS{}).CopyFolder(source, target, name, plan, download, progress)
}

func (g GVFS) CopyFolder(source, target *os.Root, name string, plan FolderPlan, download bool, progress func(int64, int64)) FolderReport {
	report := FolderReport{Skipped: plan.Skipped, Failed: plan.Failed, FirstError: plan.firstError}
	fail := func(path string, err error) {
		if errors.Is(err, os.ErrExist) {
			report.Skipped++
		} else {
			report.Failed++
			if report.FirstError == nil {
				report.FirstError = fmt.Errorf("%s: %w", path, err)
			}
		}
	}
	writer := g
	if download {
		writer = GVFS{}
	}
	if err := writer.Create(target, ".", name, true, nil); err != nil {
		fail(name, err)
		report.Skipped += len(plan.items)
		return report
	}
	tree, err := target.OpenRoot(name)
	if err != nil {
		fail(name, err)
		report.Skipped += len(plan.items)
		return report
	}
	defer tree.Close()
	blocked := make(map[string]bool)
	attempted := int64(0)
	progress(0, int64(plan.Files))
	for _, item := range plan.items {
		dst := filepath.Join(name, item.path)
		if blocked[filepath.Dir(item.path)] {
			blocked[item.path] = item.dir
			report.Skipped++
		} else if item.dir {
			if err := writer.mkdir(tree, item.path); err != nil {
				fail(dst, err)
				blocked[item.path] = true
			}
		} else if err := g.copyFolderFile(source, tree, item.path, download); err != nil {
			fail(dst, err)
		} else {
			report.Copied++
		}
		if !item.dir {
			attempted++
			progress(attempted, int64(plan.Files))
		}
	}
	return report
}

func (g GVFS) copyFolderFile(source, target *os.Root, path string, download bool) error {
	progress := func(int64, int64) {}
	if g.URI != "" && download {
		info, err := source.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("only regular files can be copied")
		}
		return g.download(source, target, path, path, info.Size(), progress)
	}
	src, info, err := openRegular(func(name string, flags int, mode os.FileMode) (*os.File, error) {
		return source.OpenFile(name, flags|syscall.O_NOFOLLOW, mode)
	}, path)
	if err != nil {
		return err
	}
	defer src.Close()
	if g.URI != "" {
		return g.upload(target, path, src, info.Size(), progress)
	}
	if download {
		return downloadFile(target, path, src, info, progress)
	}
	return uploadFile(target, path, src, info, progress)
}
