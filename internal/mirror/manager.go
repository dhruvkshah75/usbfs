package mirror

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"usbfs/internal/device"
)

// MirrorBackend resolves a selected device and builds its unstarted mirror command.
type MirrorBackend interface {
	Prepare(context.Context, device.Device, Options) (serial string, cmd *exec.Cmd, err error)
}

type State uint8

const (
	Unchanged State = iota
	Started
	Exited
)

type Event struct {
	USB     device.USBIdentity
	State   State
	Message string
	Warning bool
}

type Manager struct {
	backend MirrorBackend
	opts    Options
	ctx     context.Context
	cancel  context.CancelFunc
	events  chan Event

	mu       sync.Mutex
	sessions map[string]*session
	tasks    sync.WaitGroup
}

type session struct {
	device   device.USBIdentity
	cmd      *exec.Cmd
	pipes    [2]io.ReadCloser
	done     chan struct{}
	stopping bool
}

func New(backend MirrorBackend, opts Options) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		backend: backend, opts: opts, ctx: ctx, cancel: cancel,
		events: make(chan Event, 16), sessions: make(map[string]*session),
	}
}

func (m *Manager) NextEvent() (Event, bool) {
	select {
	case event := <-m.events:
		return event, true
	case <-m.ctx.Done():
		return Event{}, false
	}
}

func (m *Manager) emit(event Event) {
	select {
	case m.events <- event:
	case <-m.ctx.Done():
	}
}

func (m *Manager) find(usb device.USBIdentity) *session {
	for _, s := range m.sessions {
		if s.device == usb {
			return s
		}
	}
	return nil
}

// Toggle may run a device preflight. Call it off the UI loop.
func (m *Manager) Toggle(target device.Device) error {
	m.mu.Lock()
	if err := m.ctx.Err(); err != nil {
		m.mu.Unlock()
		return err
	}
	if s := m.find(target.USB); s != nil {
		m.stop(s)
		m.mu.Unlock()
		return nil
	}
	m.tasks.Add(1)
	m.mu.Unlock()
	defer m.tasks.Done()

	serial, cmd, err := m.backend.Prepare(m.ctx, target, m.opts)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ctx.Err(); err != nil {
		return err
	}
	if s := m.sessions[serial]; s != nil {
		m.stop(s)
		return nil
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("mirror stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdout.Close()
		cmd.Stdout.(*os.File).Close()
		return fmt.Errorf("mirror stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start mirror: %w", err)
	}

	s := &session{device: target.USB, cmd: cmd, pipes: [2]io.ReadCloser{stdout, stderr}, done: make(chan struct{})}
	m.sessions[serial] = s
	m.tasks.Go(func() { m.watch(serial, s) })
	return nil
}

// stop runs under the manager lock. The session stays registered until reaped.
func (m *Manager) stop(s *session) {
	if s.stopping {
		return
	}
	s.stopping = true
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	go func() {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-s.done:
		case <-timer.C:
			m.mu.Lock()
			defer m.mu.Unlock()
			select {
			case <-s.done:
				return
			default:
			}
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
			for _, pipe := range s.pipes {
				pipe.Close()
			}
		}
	}()
}

func (m *Manager) Close() {
	m.cancel()
	m.mu.Lock()
	for _, s := range m.sessions {
		m.stop(s)
	}
	m.mu.Unlock()
	m.tasks.Wait()
}

func (m *Manager) watch(serial string, s *session) {
	m.emit(Event{USB: s.device, State: Started, Message: "Mirror started."})
	var log recentLogs
	var readers sync.WaitGroup
	for _, pipe := range s.pipes {
		readers.Go(func() {
			scanner := bufio.NewScanner(pipe)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				log.add(line)
				if pushCompleted(line) {
					const pushed = "File pushed to Android."
					m.emit(Event{USB: s.device, Message: "✓ " + pushed})
					m.tasks.Go(func() { m.notify(pushed) })
				}
			}
			// A long log line must never strand the child on a full pipe.
			_, _ = io.Copy(io.Discard, pipe)
		})
	}
	// Cmd.Wait closes both pipes. Drain them first to retain startup diagnostics.
	readers.Wait()
	err := s.cmd.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, serial)
	close(s.done)
	event := exitEvent(err, s.stopping, log.diagnostic())
	event.USB, event.State = s.device, Exited
	// Publish the exit before a new session can start for this serial.
	m.emit(event)
}

func pushCompleted(line string) bool {
	return strings.HasPrefix(line, "INFO: ") && strings.Contains(line, " successfully pushed to ")
}

func (m *Manager) notify(message string) {
	path, err := exec.LookPath("notify-send")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(m.ctx, 2*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, path, "--", "USBFS", message).Run()
}

type recentLogs struct {
	mu    sync.Mutex
	lines [16]string
	next  int
}

func (r *recentLogs) add(line string) {
	if line == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines[r.next] = line
	r.next = (r.next + 1) % len(r.lines)
}

// diagnostic is read after both output readers have stopped.
func (r *recentLogs) diagnostic() string {
	latest := r.lines[(r.next+len(r.lines)-1)%len(r.lines)]
	for i := 1; i <= len(r.lines); i++ {
		line := r.lines[(r.next+len(r.lines)-i)%len(r.lines)]
		if strings.Contains(strings.ToLower(line), "error:") {
			return line
		}
	}
	return latest
}

func exitEvent(err error, stopping bool, diagnostic string) Event {
	var exit *exec.ExitError
	if err == nil || (stopping && errors.As(err, &exit) && exit.ExitCode() < 0) {
		return Event{Message: "Mirror closed."}
	}
	if errors.As(err, &exit) && exit.ExitCode() == 2 {
		return Event{Message: "Device disconnected.", Warning: true}
	}
	if diagnostic == "" {
		diagnostic = err.Error()
	}
	return Event{Message: "Mirror failed. " + diagnostic, Warning: true}
}
