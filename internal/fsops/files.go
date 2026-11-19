package fsops

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode"
)

// Create adds one new item inside dir without modifying any existing item.
func Create(root *os.Root, dir, name string, folder bool) error {
	if strings.TrimSpace(name) == "" || name == "." || name == ".." ||
		strings.ContainsRune(name, filepath.Separator) || strings.ContainsFunc(name, unicode.IsControl) {
		return errors.New("enter a single file or folder name")
	}
	path := filepath.Join(dir, name)
	if folder {
		return root.Mkdir(path, 0755)
	}
	file, err := openNewFile(root, path)
	if err != nil {
		return err
	}
	return confirmFile(root, path, 0, file.Close())
}

type Entry struct {
	Name                    string
	Folder, Locked, Symlink bool
}

// List uses absolute paths for the host and root-relative paths for the device.
func List(root *os.Root, path string) ([]Entry, error) {
	open, stat := os.Open, os.Stat
	if root != nil {
		open, stat = root.Open, root.Lstat
	}
	dir, err := open(path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	items, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(items))
	for _, item := range items {
		entry := Entry{Name: item.Name(), Folder: item.IsDir(), Symlink: item.Type()&os.ModeSymlink != 0}
		if root != nil || entry.Symlink {
			info, err := stat(filepath.Join(path, item.Name()))
			if err != nil {
				entry.Locked = true
			} else {
				entry.Folder = info.IsDir()
				mode := info.Mode()
				entry.Locked = (!mode.IsRegular() && !mode.IsDir()) ||
					(root != nil && (mode.Perm()&0444 == 0 || (entry.Folder && mode.Perm()&0111 == 0)))
			}
		} else {
			entry.Locked = !item.Type().IsRegular() && !entry.Folder
		}
		entries = append(entries, entry)
	}
	slices.SortFunc(entries, func(a, b Entry) int {
		if a.Folder != b.Folder {
			if a.Folder {
				return -1
			}
			return 1
		}
		if order := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); order != 0 {
			return order
		}
		return strings.Compare(a.Name, b.Name)
	})
	return entries, nil
}

func openRegular(open func(string, int, os.FileMode) (*os.File, error), path string) (*os.File, os.FileInfo, error) {
	f, err := open(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		if err == nil {
			err = errors.New("only regular files can be opened or copied")
		}
		return nil, nil, err
	}
	return f, info, nil
}

func Launch(root *os.Root, path string) (<-chan error, error) {
	open := os.OpenFile
	if root != nil {
		open = root.OpenFile
		resolved, err := filepath.EvalSymlinks(filepath.Join(root.Name(), path))
		if err != nil {
			return nil, err
		}
		path, err = filepath.Rel(root.Name(), resolved)
		if err != nil || !filepath.IsLocal(path) {
			return nil, errors.New("file resolves outside the external device")
		}
	}
	file, _, err := openRegular(open, path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if root != nil {
		path = filepath.Join(root.Name(), path)
	}
	cmd := exec.Command("xdg-open", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, errors.New("xdg-open is missing. Ubuntu: sudo apt install xdg-utils")
		}
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return done, nil
}
