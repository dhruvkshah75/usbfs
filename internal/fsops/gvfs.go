package fsops

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GVFS routes mutations and transfers through the GNOME-owned MTP session.
// Its zero value retains the managed filesystem operations.
type GVFS struct{ Path, URI string }

func (g GVFS) location(root *os.Root, path string) (string, error) {
	relative, err := filepath.Rel(g.Path, filepath.Join(root.Name(), path))
	if err != nil || !filepath.IsLocal(relative) {
		return "", errors.New("path is outside the GVfs device")
	}
	segments := strings.Split(relative, string(filepath.Separator))
	for i := range segments {
		segments[i] = url.PathEscape(segments[i])
	}
	return strings.TrimRight(g.URI, "/") + "/" + strings.Join(segments, "/"), nil
}

func checkNew(root *os.Root, path string) error {
	_, err := root.Lstat(path)
	switch {
	case err == nil:
		return os.ErrExist
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return err
	}
}

func gio(args ...string) *exec.Cmd {
	cmd := exec.Command("gio", args...)
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	return cmd
}

func gioError(output []byte, err error) error {
	return fmt.Errorf("gio: %w %s", err, strings.TrimSpace(string(output)))
}

func (g GVFS) mkdir(root *os.Root, path string) error {
	if g.URI == "" {
		return root.Mkdir(path, 0755)
	}
	if err := checkNew(root, path); err != nil {
		return err
	}
	location, err := g.location(root, path)
	if err != nil {
		return err
	}
	if output, err := gio("mkdir", "--", location).CombinedOutput(); err != nil {
		if errors.Is(checkNew(root, path), os.ErrExist) {
			return os.ErrExist
		}
		return gioError(output, err)
	}
	return nil
}

func (g GVFS) upload(root *os.Root, path string, src io.Reader, size int64, progress func(int64, int64)) error {
	if err := checkNew(root, path); err != nil {
		return &StageError{"Cannot create file on external device", err}
	}
	location, err := g.location(root, path)
	if err != nil {
		return err
	}
	progress(0, size)
	// shortcut: stage the open source in a private directory, switch to a
	// descriptor-aware GIO adapter if staging large files becomes a bottleneck.
	dir, err := os.MkdirTemp("", "usbfs-gvfs-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	staged := filepath.Join(dir, filepath.Base(path))
	file, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	count, copyErr := io.Copy(file, src)
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return err
	}
	if count != size {
		return errors.New("source file size changed during transfer")
	}
	if err := gioCopy(staged, location); err != nil {
		return &StageError{"Cannot create file on external device", err}
	}
	if err := confirmFile(root, path, size, nil); err != nil {
		return err
	}
	progress(size, size)
	return nil
}

func gioCopy(source, destination string) error {
	// Interactive mode starts without the overwrite flag. EOF declines a
	// collision discovered by GIO after our initial destination check.
	output, err := gio("copy", "-i", "-T", "--", source, destination).CombinedOutput()
	if bytes.Contains(output, []byte("copy: overwrite ")) {
		return os.ErrExist
	}
	if err != nil {
		return gioError(output, err)
	}
	return nil
}

func (g GVFS) download(device, host *os.Root, source, target string, size int64, progress func(int64, int64)) error {
	location, err := g.location(device, source)
	if err != nil {
		return err
	}
	if err := checkNew(host, target); err != nil {
		return err
	}
	// MTP supports GIO pulls even on devices lacking streaming reads.
	dir, err := os.MkdirTemp("", "usbfs-gvfs-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	staged := filepath.Join(dir, filepath.Base(source))
	progress(0, size)
	if err := gioCopy(location, staged); err != nil {
		return &StageError{"Cannot read file from external device", err}
	}
	file, err := os.Open(staged)
	if err != nil {
		return err
	}
	defer file.Close()
	return publishDownload(host, target, func(dest *os.File) error {
		_, err := writeCopy(dest, file, size, progress)
		return err
	})
}
