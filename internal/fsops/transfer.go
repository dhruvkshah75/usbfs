package fsops

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"
)

type StageError struct {
	Stage string
	Err   error
}

func (e *StageError) Error() string { return e.Stage + ": " + e.Err.Error() }
func (e *StageError) Unwrap() error { return e.Err }

func openNewFile(root *os.Root, path string) (*os.File, error) {
	if _, err := root.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return nil, os.ErrExist
		}
		return nil, err
	}
	return root.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
}

func Download(device *os.Root, source, hostDir string, progress func(int64, int64), otherDevices ...uint64) error {
	src, info, err := openRegular(device.OpenFile, source)
	if err != nil {
		return &StageError{"Cannot read file from external device", err}
	}
	defer src.Close()
	host, err := os.OpenRoot(hostDir)
	if err != nil {
		return err
	}
	defer host.Close()
	targetDir, err := host.Stat(".")
	if err != nil {
		return err
	}
	mount, err := device.Stat(".")
	if err != nil {
		return err
	}
	destination := targetDir.Sys().(*syscall.Stat_t).Dev
	if mount.Sys().(*syscall.Stat_t).Dev == destination || slices.Contains(otherDevices, destination) {
		return errors.New("choose a Linux destination outside the external device")
	}

	// Publish only a complete file, and use Link to atomically reject collisions.
	target := filepath.Base(source)
	if _, err := host.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return os.ErrExist
		}
		return err
	}
	temp := ".usbfs-" + rand.Text()
	dest, err := host.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer host.Remove(temp)
	if _, err := writeCopy(dest, src, info.Size(), progress); err != nil {
		return err
	}
	return host.Link(temp, target)
}

func Upload(device *os.Root, source, deviceDir string, progress func(int64, int64)) error {
	src, info, err := openRegular(os.OpenFile, source)
	if err != nil {
		return &StageError{"Cannot read file from Linux", err}
	}
	defer src.Close()
	name := filepath.Join(deviceDir, filepath.Base(source))
	dest, err := openNewFile(device, name)
	if err != nil {
		return &StageError{"Cannot create file on external device", err}
	}
	count, err := writeCopy(dest, src, info.Size(), progress)
	if err := confirmFile(device, name, count, err); err != nil {
		return fmt.Errorf("upload incomplete or unverified, a new file may remain on the device: %w", err)
	}
	return nil
}

func confirmFile(root *os.Root, name string, size int64, err error) error {
	if err != nil {
		return err
	}
	// Some MTP drivers publish on release and cannot report a release error to close(2).
	for range 10 {
		info, err := root.Lstat(name)
		if err == nil && info.Mode().IsRegular() && info.Size() == size {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("destination did not confirm the expected file size")
}

type trackedWriter struct {
	io.Writer
	count, total int64
	progress     func(int64, int64)
}

func (w *trackedWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.count += int64(n)
	w.progress(w.count, w.total)
	if err != nil {
		err = &StageError{"Cannot write destination", err}
	}
	return n, err
}

func writeCopy(dest, source *os.File, size int64, progress func(int64, int64)) (int64, error) {
	w := &trackedWriter{Writer: dest, total: size, progress: progress}
	progress(0, size)
	count, err := io.CopyBuffer(w, struct{ io.Reader }{source}, make([]byte, 256*1024))
	var staged *StageError
	if err != nil && !errors.As(err, &staged) {
		err = &StageError{"Cannot read source file", err}
	}
	if err == nil && count != size {
		err = errors.New("source file size changed during transfer")
	}
	if err == nil {
		err = dest.Sync()
		// Some FUSE drivers implement flush on close but do not support fsync.
		if errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EOPNOTSUPP) {
			err = nil
		}
		if err != nil {
			err = &StageError{"Cannot sync destination", err}
		}
	}
	return count, errors.Join(err, dest.Close())
}
