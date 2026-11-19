package device

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

type driverProcess struct {
	cmd  *exec.Cmd
	log  bytes.Buffer
	done chan struct{}
	err  error
}

var ErrMTPBusy = errors.New("the Android USB connection is in use by another program")
var ErrMTPSession = errors.New("the phone did not open an MTP session")

func (p *driverProcess) wait() { <-p.done }

func (s *Session) startApple(d Device) error {
	shares, err := appleShares(d)
	var warnings []error
	if err != nil {
		warnings = append(warnings, fmt.Errorf("could not list file-sharing apps: %w", err))
	}
	for _, share := range shares {
		if s.closing.Load() {
			break
		}
		path := filepath.Join(s.path, share.name)
		if err := prepareMountDir(path, true); err != nil {
			warnings = append(warnings, fmt.Errorf("%s: %w", share.name, err))
			continue
		}
		s.mountDirs = append(s.mountDirs, path)
		mount := d
		if share.bundle != "" {
			mount.args = append(slices.Clone(d.args), "--documents", share.bundle)
		}
		if err := s.start(mount, path); err != nil {
			warnings = append(warnings, fmt.Errorf("skipped %s: %w", share.name, err))
		}
	}
	return errors.Join(warnings...)
}

func (s *Session) start(d Device, path string) error {
	// Foreground mode gives shutdown a process to wait for, including pending FUSE releases.
	p := &driverProcess{done: make(chan struct{})}
	args := append([]string{"-f", "-s", "-o", "subtype=usbfs"}, d.args...)
	p.cmd = exec.Command(d.driver, append(args, path)...)
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	p.cmd.Stdout = &p.log
	p.cmd.Stderr = &p.log
	p.cmd.WaitDelay = time.Second
	if err := p.cmd.Start(); err != nil {
		return err
	}
	if s.drivers == nil {
		s.drivers = make(map[string]*driverProcess)
	}
	s.drivers[path] = p
	go func() {
		defer close(p.done)
		p.err = p.cmd.Wait()
	}()

	wait := 12 * time.Second
	if d.Kind == "Android MTP" {
		// libmtp may reset the USB connection and try opening its session again.
		wait = 30 * time.Second
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		mounts, err := s.inspect(path)
		if err == nil && slices.ContainsFunc(mounts, func(m mountEntry) bool { return m.path == path && isUSBMount(m.kind) }) {
			s.mountedPaths = append(s.mountedPaths, path)
			s.mounted = true
			s.active.Store(true)
			return nil
		}
		select {
		case <-p.done:
			delete(s.drivers, path)
			cause := errors.Join(p.err, err)
			if message := strings.TrimSpace(p.log.String()); message != "" {
				cause = errors.Join(cause, errors.New(message))
			}
			if cause == nil {
				cause = errors.New("driver exited before the mount appeared")
			}
			return fmt.Errorf("%s failed to mount: %w", d.driver, errors.Join(mtpIssue(d, p.log.String()), cause))
		case <-timer.C:
			// Stop the entire startup group, which may include the FUSE mount helper.
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			p.wait()
			delete(s.drivers, path)
			if message := strings.TrimSpace(p.log.String()); message != "" {
				cause := errors.Join(mtpIssue(d, message), errors.New(message))
				return fmt.Errorf("%s timed out waiting for the mount: %w", d.driver, cause)
			}
			return fmt.Errorf("%s timed out waiting for the mount. Check the USB connection and device permissions", d.driver)
		case <-tick.C:
		}
	}
}

func mtpIssue(d Device, message string) error {
	if d.Kind != "Android MTP" {
		return nil
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "libusb_claim_interface") &&
		(strings.Contains(lower, "device is busy") || strings.Contains(lower, "libusb_claim_interface() = -6")) {
		return ErrMTPBusy
	}
	if strings.Contains(lower, "failed to open session") {
		return ErrMTPSession
	}
	return nil
}
