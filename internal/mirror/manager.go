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
	Prepare(context.Context, device.Device, Options) (launch, error)
}

type launch struct {
	serial, name string
	cmd          *exec.Cmd
	viewer       webViewer
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
	device device.USBIdentity
	launch
	output     *os.File
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	stopping   bool
	stopReason string
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

	backend := m.backend
	if target.Kind == "iOS/iPadOS" {
		backend = iPad{}
	}
	plan, err := backend.Prepare(m.ctx, target, m.opts)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ctx.Err(); err != nil {
		return err
	}
	key := plan.serial
	if plan.name != "" {
		key = "apple:" + key
	}
	if s := m.sessions[key]; s != nil {
		m.stop(s)
		return nil
	}

	output, err := startLogged(plan.cmd)
	if err != nil {
		return fmt.Errorf("start mirror: %w", err)
	}
	ctx, cancel := context.WithCancel(m.ctx)
	s := &session{device: target.USB, launch: plan, output: output, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	m.sessions[key] = s
	m.tasks.Go(func() { m.watch(key, s) })
	return nil
}

func startLogged(cmd *exec.Cmd) (*os.File, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	output, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer writer.Close()
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := cmd.Start(); err != nil {
		output.Close()
		return nil, err
	}
	return output, nil
}

func drainLogs(output *os.File, line func(string)) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer output.Close()
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			line(strings.TrimSpace(scanner.Text()))
		}
		// A long line must never strand the child on a full pipe.
		_, _ = io.Copy(io.Discard, output)
	}()
	return func() {
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			output.Close()
			<-done
		}
	}
}

// stop runs under the manager lock. The session stays registered until reaped.
func (m *Manager) stop(s *session) {
	if s.stopping {
		return
	}
	s.stopping = true
	s.cancel()
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
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

func (m *Manager) watch(key string, s *session) {
	var log recentLogs
	ready := make(chan struct{}, 1)
	viewerDone := make(chan error, 1)
	if s.viewer.url != "" {
		go func() {
			err := m.watchViewer(s, ready)
			m.mu.Lock()
			if s.ctx.Err() == nil && (s.viewer.app || err != nil) {
				m.stop(s)
			}
			m.mu.Unlock()
			viewerDone <- err
		}()
	} else {
		message := "Mirror started."
		if s.name != "" {
			message = "Mirroring iPad via " + s.name
		}
		m.emit(Event{USB: s.device, State: Started, Message: message})
		viewerDone <- nil
	}
	reported := make(map[string]bool)
	waitLogs := drainLogs(s.output, func(line string) {
		log.add(line)
		if s.viewer.url != "" && strings.Contains(line, "Open "+s.viewer.url) {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
		if s.name != "" {
			message := ipadDiagnostic(line)
			if message == ipadDisconnectedMessage {
				m.mu.Lock()
				if !s.stopping {
					s.stopReason = message
					m.stop(s)
				}
				m.mu.Unlock()
			} else if s.name == "devicehub_rs" && message != "" && !reported[message] && s.ctx.Err() == nil {
				reported[message] = true
				m.emit(Event{USB: s.device, Message: message, Warning: true})
			}
		}
		if s.name == "" && pushCompleted(line) {
			const pushed = "File pushed to Android."
			m.emit(Event{USB: s.device, Message: "✓ " + pushed})
			m.tasks.Go(func() { m.notify(pushed) })
		}
	})
	err := s.cmd.Wait()
	s.cancel()
	waitLogs()
	viewErr := <-viewerDone

	m.mu.Lock()
	defer m.mu.Unlock()
	if s.stopping || s.name != "" {
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	}
	delete(m.sessions, key)
	close(s.done)
	event := exitEvent(err, s.stopping, log.diagnostic())
	if s.name != "" {
		switch {
		case s.stopReason != "":
			event = Event{Message: s.stopReason, Warning: true}
		case viewErr != nil:
			message := log.ipadDiagnostic()
			if message == "" {
				message = viewErr.Error()
			}
			event = Event{Message: message, Warning: true}
		case s.stopping:
			event = Event{Message: "Mirror closed."}
		default:
			if message := log.ipadDiagnostic(); message != "" {
				event = Event{Message: message, Warning: true}
			} else if err != nil {
				event = Event{Message: "iPad mirror failed. " + log.diagnostic(), Warning: true}
			}
		}
	}
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

// Diagnostics are read after the output reader has stopped.
func (r *recentLogs) ipadDiagnostic() string {
	for i := 1; i <= len(r.lines); i++ {
		line := r.lines[(r.next+len(r.lines)-i)%len(r.lines)]
		if message := ipadDiagnostic(line); message != "" {
			return message
		}
	}
	return ""
}

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
