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
	path    string
	lock    *os.File
	inspect func(string) ([]string, error)
	active  atomic.Bool
	mu      sync.Mutex
	closing atomic.Bool
	mounted bool
	current Device
	driver  *driverProcess
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
	types, err := mountTypes(path)
	if err != nil || slices.ContainsFunc(types, func(t string) bool {
		return t != "fuse.ifuse" && t != "fuse.simple-mtpfs" && t != "fuse.jmtpfs" && t != "fuse.usbfs"
	}) {
		lock.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("unexpected filesystem mounted at %s", path)
	}
	s := &Session{path: path, lock: lock, inspect: mountTypes, mounted: len(types) != 0}
	s.active.Store(s.mounted)
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
	types, err := s.inspect(s.path)
	if err != nil {
		return s.current, err
	}
	s.active.Store(len(types) != 0)
	if s.current.Kind != "" && len(types) != 0 {
		return s.current, nil
	}
	if len(types) != 0 {
		s.mounted = true
		if err := s.unmount(); err != nil {
			return Device{}, fmt.Errorf("previous mount is still busy. Close files opened from the device: %w", err)
		}
	}
	if s.driver != nil {
		s.driver.wait()
		s.driver = nil
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
	if err := os.Mkdir(s.path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return Device{}, err
	}
	info, err := os.Lstat(s.path)
	if err != nil {
		return Device{}, err
	}
	if !info.IsDir() || info.Mode().Perm()&0022 != 0 || int(info.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
		return Device{}, fmt.Errorf("mount directory must be owned by you and protected from other writers: %s", s.path)
	}
	if err := s.start(d); err != nil {
		return Device{}, err
	}
	s.current = d
	s.mounted = true
	return d, nil
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
		types, err := s.inspect(s.path)
		if err == nil && len(types) != 0 {
			s.mounted = true
			err = s.unmount()
		}
		if err == nil {
			if s.driver != nil {
				s.driver.wait()
				s.driver = nil
				continue
			}
			return s.mounted
		}
		fmt.Fprintf(warnings, "[WARNING] Waiting for %s to unmount. Close files opened from the device and leave the mount directory in any other terminal. Retrying in %s. (%v)\n", s.path, delay, err)
		time.Sleep(delay)
		if attempt >= 3 {
			delay = min(5*time.Second, delay*2)
		}
	}
}

func (s *Session) unmount() error {
	helpers := unmountHelpers()
	if len(helpers) == 0 {
		return errors.New("missing unmount helper. Ubuntu: sudo apt install fuse3")
	}
	var lastErr error
	for _, helper := range helpers {
		_, lastErr = command(helper, "-u", s.path)
		types, err := s.inspect(s.path)
		if err != nil {
			return err
		}
		if len(types) == 0 {
			s.active.Store(false)
			return nil
		}
	}
	return errors.Join(errors.New("mount remains active"), lastErr)
}

var mountEscapes = strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)

func mountTypes(path string) ([]string, error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readMountTypes(f, path)
}

func readMountTypes(r io.Reader, path string) ([]string, error) {
	var types []string
	seen := false
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 6 {
			return nil, errors.New("could not verify mount: malformed /proc/mounts")
		}
		seen = true
		if mountEscapes.Replace(fields[1]) == path {
			types = append(types, fields[2])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !seen {
		return nil, errors.New("could not verify mount: empty /proc/mounts")
	}
	return types, nil
}

// OpenRoot pins the verified FUSE filesystem for one operation inside Work.
func (s *Session) OpenRoot() (*os.Root, error) {
	types, err := s.inspect(s.path)
	if err != nil {
		return nil, err
	}
	if len(types) == 0 {
		s.active.Store(false)
		return nil, ErrUnmounted
	}
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return nil, err
	}
	types, err = s.inspect(s.path)
	if err == nil && len(types) == 0 {
		s.active.Store(false)
		err = ErrUnmounted
	}
	if err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

var ErrUnmounted = errors.New("external device is no longer mounted")
