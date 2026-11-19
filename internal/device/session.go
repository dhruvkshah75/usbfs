package device

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type Session struct {
	path         string
	lock         *os.File
	inspect      func(string) ([]mountEntry, error)
	active       atomic.Bool
	mu           sync.Mutex
	closing      atomic.Bool
	mounted      bool
	current      Device
	drivers      map[string]*driverProcess
	mountedPaths []string
	mountDirs    []string
}

func NewSession(home string) (*Session, error) {
	cache, err := filepath.Abs(filepath.Join(home, ".cache"))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cache, 0700); err != nil {
		return nil, err
	}
	cache, err = filepath.EvalSymlinks(cache)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(cache, "usbfs-mount")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("cannot lock %s (another usbfs session may be running): %w", path, err)
	}
	s := &Session{path: path, lock: lock, inspect: readMounts}
	if err := s.refreshMounts(); err != nil {
		lock.Close()
		return nil, err
	}
	return s, nil
}

func (s *Session) Path() string { return s.path }

func (s *Session) HasMount() bool { return s.active.Load() }

// Work serializes filesystem operations with shutdown, even if the TUI stops consuming messages.
func (s *Session) Work(fn func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing.Load() {
		return false
	}
	fn()
	return true
}

// DetectAndMount runs inside Work.
func (s *Session) DetectAndMount(platform Platform) (Device, error) {
	if err := s.refreshMounts(); err != nil {
		return s.current, err
	}
	if s.current.Kind != "" && len(s.mountedPaths) != 0 {
		return s.current, nil
	}
	if err := s.cleanup(io.Discard); err != nil {
		return Device{}, fmt.Errorf("previous mount is still busy. Close files opened from the device: %w", err)
	}
	s.current = Device{}
	driver, err := mountDriver(platform)
	if err != nil {
		return Device{}, err
	}
	d, err := find(driver)
	if err != nil || d.Kind == "" || s.closing.Load() {
		return Device{}, err
	}
	if err := prepareMountDir(s.path, false); err != nil {
		return Device{}, err
	}
	if d.driver == "ifuse" {
		err = s.startApple(d)
	} else {
		err = s.start(d, s.path)
	}
	if len(s.mountedPaths) == 0 {
		return Device{}, err
	}
	s.current = d
	return d, err
}

func prepareMountDir(path string, empty bool) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0022 != 0 || int(info.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
		return fmt.Errorf("mount directory must be owned by you and protected from other writers: %s", path)
	}
	if empty {
		dir, err := os.Open(path)
		if err != nil {
			return err
		}
		defer dir.Close()
		if _, err := dir.Readdirnames(1); !errors.Is(err, io.EOF) {
			if err != nil {
				return err
			}
			return fmt.Errorf("mount directory is not empty: %s", path)
		}
	}
	return nil
}

// Close keeps the session lock until the kernel confirms that the mount is gone.
func (s *Session) Close(warnings io.Writer) bool {
	s.closing.Store(true)
	if !s.mu.TryLock() {
		fmt.Fprintln(warnings, "Waiting for the current device operation to finish before unmounting.")
		s.mu.Lock()
	}
	defer s.mu.Unlock()
	defer s.lock.Close()

	delay := 500 * time.Millisecond
	for attempt := 1; ; attempt++ {
		err := s.cleanup(warnings)
		if err == nil {
			return s.mounted
		}
		fmt.Fprintf(warnings, "[WARNING] Waiting for %s to unmount. Close files opened from the device and leave the mount directory in any other terminal. Retrying in %s. (%v)\n", s.path, delay, err)
		time.Sleep(delay)
		if attempt >= 3 {
			delay = min(5*time.Second, delay*2)
		}
	}
}

func (s *Session) cleanup(warnings io.Writer) error {
	if err := s.refreshMounts(); err != nil {
		return err
	}
	var failures []error
	for _, path := range s.mountedPaths {
		if err := s.unmount(path); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", path, err))
		}
	}
	if err := s.refreshMounts(); err != nil {
		return errors.Join(err, errors.Join(failures...))
	}
	if len(s.mountedPaths) != 0 {
		return errors.Join(errors.New("mounts remain active"), errors.Join(failures...))
	}
	for path, driver := range s.drivers {
		driver.wait()
		delete(s.drivers, path)
	}
	if err := s.refreshMounts(); err != nil {
		return err
	}
	if len(s.mountedPaths) != 0 {
		return errors.New("mounts remain active")
	}
	slices.SortFunc(s.mountDirs, func(a, b string) int { return len(b) - len(a) })
	for _, path := range s.mountDirs {
		// Only remove empty local directories after every mount has been verified gone.
		if err := syscall.Rmdir(path); err != nil && !errors.Is(err, os.ErrNotExist) &&
			!errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.ENOTDIR) {
			fmt.Fprintf(warnings, "[WARNING] Could not remove empty mount directory %s: %v\n", path, err)
		}
	}
	s.mountDirs = nil
	return nil
}

func (s *Session) refreshMounts() error {
	mounts, err := s.inspect(s.path)
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		if !isUSBMount(mount.kind) {
			return fmt.Errorf("unexpected filesystem mounted at %s", mount.path)
		}
		if !slices.Contains(paths, mount.path) {
			paths = append(paths, mount.path)
		}
		if mount.path != s.path && !slices.Contains(s.mountDirs, mount.path) {
			s.mountDirs = append(s.mountDirs, mount.path)
		}
	}
	// Descendants must be released before any parent mount.
	slices.SortFunc(paths, func(a, b string) int { return len(b) - len(a) })
	s.mountedPaths = paths
	s.mounted = s.mounted || len(paths) != 0
	s.active.Store(len(paths) != 0)
	return nil
}

func isUSBMount(kind string) bool {
	switch kind {
	case "fuse.ifuse", "fuse.simple-mtpfs", "fuse.jmtpfs", "fuse.usbfs":
		return true
	}
	return false
}

func (s *Session) unmount(path string) error {
	helpers := unmountHelpers()
	if len(helpers) == 0 {
		return errors.New("missing unmount helper. Ubuntu: sudo apt install fuse3")
	}
	var lastErr error
	for _, helper := range helpers {
		_, lastErr = command(helper, "-u", path)
		mounts, err := s.inspect(path)
		if err != nil {
			return err
		}
		if len(mounts) == 0 {
			return nil
		}
	}
	return errors.Join(errors.New("mount remains active"), lastErr)
}

type mountEntry struct {
	path, kind string
}

var mountEscapes = strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)

func readMounts(path string) ([]mountEntry, error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseMounts(f, path)
}

func parseMounts(r io.Reader, path string) ([]mountEntry, error) {
	var mounts []mountEntry
	seen := false
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 6 {
			return nil, errors.New("could not verify mount: malformed /proc/mounts")
		}
		seen = true
		if target := mountEscapes.Replace(fields[1]); target == path || strings.HasPrefix(target, path+string(filepath.Separator)) {
			mounts = append(mounts, mountEntry{target, fields[2]})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !seen {
		return nil, errors.New("could not verify mount: empty /proc/mounts")
	}
	return mounts, nil
}

// Shares returns the synthetic iOS root. A nil slice signals a single mount at the root.
func (s *Session) Shares() ([]string, error) {
	if s.current.driver != "ifuse" {
		return nil, nil
	}
	if err := s.refreshMounts(); err != nil {
		return nil, err
	}
	shares := make([]string, 0, len(s.mountedPaths))
	for _, path := range s.mountedPaths {
		if filepath.Dir(path) == s.path && s.drivers[path] != nil {
			shares = append(shares, filepath.Base(path))
		}
	}
	if len(shares) == 0 {
		return nil, ErrUnmounted
	}
	slices.Sort(shares)
	if i := slices.Index(shares, "Goodnotes"); i > 0 {
		copy(shares[1:i+1], shares[:i])
		shares[0] = "Goodnotes"
	}
	return shares, nil
}

// OpenPath confines operations to their actual mounted filesystem and rejects the synthetic root.
func (s *Session) OpenPath(relative string) (*os.Root, string, error) {
	if !filepath.IsLocal(relative) {
		return nil, "", errors.New("path is outside the external device")
	}
	target := s.path
	relative = filepath.Clean(relative)
	if s.current.driver == "ifuse" {
		share, rest, _ := strings.Cut(relative, string(filepath.Separator))
		if share == "." {
			return nil, "", errors.New("choose a mounted folder on the external device")
		}
		target = filepath.Join(target, share)
		if s.drivers[target] == nil {
			return nil, "", ErrUnmounted
		}
		relative = rest
		if relative == "" {
			relative = "."
		}
	}
	mounts, err := s.inspect(target)
	if err != nil {
		return nil, "", err
	}
	matches := func(m mountEntry) bool { return m.path == target && isUSBMount(m.kind) }
	if !slices.ContainsFunc(mounts, matches) {
		return nil, "", ErrUnmounted
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		return nil, "", err
	}
	mounts, err = s.inspect(target)
	if err == nil && !slices.ContainsFunc(mounts, matches) {
		err = ErrUnmounted
	}
	if err != nil {
		root.Close()
		return nil, "", err
	}
	return root, relative, nil
}

// FilesystemIDs also covers alternate paths to another share of the same device.
func (s *Session) FilesystemIDs() ([]uint64, error) {
	if err := s.refreshMounts(); err != nil {
		return nil, err
	}
	ids := make([]uint64, 0, len(s.mountedPaths))
	for _, path := range s.mountedPaths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		ids = append(ids, info.Sys().(*syscall.Stat_t).Dev)
	}
	return ids, nil
}

var ErrUnmounted = errors.New("external device is no longer mounted")
