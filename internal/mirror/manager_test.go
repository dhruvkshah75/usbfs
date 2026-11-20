package mirror

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"usbfs/internal/device"
)

type testBackend func(context.Context, device.Device, Options) (string, *exec.Cmd, error)

func (f testBackend) Prepare(ctx context.Context, d device.Device, opts Options) (string, *exec.Cmd, error) {
	return f(ctx, d, opts)
}

var testPhone = device.Device{Name: "Phone", USB: device.USBIdentity{Serial: "serial"}}

func (m *Manager) active(usb device.USBIdentity) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.find(usb) != nil
}

func nextEvent(t *testing.T, m *Manager) Event {
	t.Helper()
	select {
	case e := <-m.events:
		return e
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for mirror event")
		return Event{}
	}
}

func TestMirrorToggle(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	notice := filepath.Join(dir, "notice")
	t.Setenv("USBFS_TEST_NOTICE", notice)
	writeScript(t, filepath.Join(dir, "notify-send"), `printf '%s\n' "$@" > "$USBFS_TEST_NOTICE"`)
	var prepares atomic.Int32
	m := New(testBackend(func(_ context.Context, d device.Device, _ Options) (string, *exec.Cmd, error) {
		prepares.Add(1)
		return d.USB.Serial, exec.Command("/bin/sh", "-c", `
trap 'exit 0' TERM
printf 'INFO: /tmp/note.txt successfully pushed to /sdcard/Download/\n'
while :; do /bin/sleep 0.05; done`), nil
	}), DefaultOptions())
	t.Cleanup(m.Close)

	if err := m.Toggle(testPhone); err != nil {
		t.Fatal(err)
	}
	if !m.active(testPhone.USB) || nextEvent(t, m).Message != "Mirror started." {
		t.Fatal("mirror did not start")
	}
	if got := nextEvent(t, m); got.Message != "✓ File pushed to Android." {
		t.Fatalf("push event = %+v", got)
	}
	deadline := time.Now().Add(time.Second)
	for {
		body, _ := os.ReadFile(notice)
		if strings.Contains(string(body), "File pushed to Android.") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("desktop notification was not sent, output = %q", body)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := m.Toggle(testPhone); err != nil {
		t.Fatal(err)
	}
	if got := nextEvent(t, m); got.Message != "Mirror closed." || got.Warning {
		t.Fatalf("stop event = %+v", got)
	}
	if m.active(testPhone.USB) || prepares.Load() != 1 {
		t.Fatal("toggle must stop and reap the existing mirror without preparing another")
	}
}

func TestMirrorExit(t *testing.T) {
	for _, tt := range []struct {
		code    int
		message string
	}{
		{0, "Mirror closed."},
		{1, "Mirror failed. ERROR: Could not connect"},
		{2, "Device disconnected."},
	} {
		t.Run(strconv.Itoa(tt.code), func(t *testing.T) {
			m := New(testBackend(func(_ context.Context, d device.Device, _ Options) (string, *exec.Cmd, error) {
				script := fmt.Sprintf("echo 'ERROR: Could not connect' >&2; echo 'Info: Shutting down'; exit %d", tt.code)
				return d.USB.Serial, exec.Command("/bin/sh", "-c", script), nil
			}), DefaultOptions())
			t.Cleanup(m.Close)
			if err := m.Toggle(testPhone); err != nil {
				t.Fatal(err)
			}
			nextEvent(t, m)
			if got := nextEvent(t, m); got.Message != tt.message || got.Warning != (tt.code != 0) {
				t.Fatalf("exit event = %+v", got)
			}
			if m.active(testPhone.USB) {
				t.Fatal("exited mirror is still active")
			}
		})
	}
}

func TestRecentLogs(t *testing.T) {
	var logs recentLogs
	logs.add("ERROR: old error")
	for i := 1; i <= len(logs.lines); i++ {
		logs.add(strconv.Itoa(i))
	}
	if got := logs.diagnostic(); got != strconv.Itoa(len(logs.lines)) {
		t.Fatalf("diagnostic = %q after wraparound", got)
	}
	logs.add("adb: error: no device")
	logs.add("INFO: shutting down")
	if got := logs.diagnostic(); got != "adb: error: no device" {
		t.Fatalf("diagnostic = %q, want specific error", got)
	}
}

func TestCloseKillsUnresponsiveMirror(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var cmd *exec.Cmd
	m := New(testBackend(func(_ context.Context, d device.Device, _ Options) (string, *exec.Cmd, error) {
		cmd = exec.Command("/bin/sh", "-c", `trap '' TERM
printf 'INFO: /tmp/test successfully pushed to /sdcard/Download/\n'
exec /bin/sleep 30`)
		return d.USB.Serial, cmd, nil
	}), DefaultOptions())
	t.Cleanup(m.Close)
	if err := m.Toggle(testPhone); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, m)
	nextEvent(t, m) // The child has installed its signal handler.
	m.Close()
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() >= 0 || m.active(testPhone.USB) {
		t.Fatal("unresponsive mirror was not killed and reaped")
	}
}

func TestConcurrentMirrorToggleStartsOnce(t *testing.T) {
	commands := make(chan *exec.Cmd, 2)
	ready := make(chan struct{})
	m := New(testBackend(func(_ context.Context, d device.Device, _ Options) (string, *exec.Cmd, error) {
		cmd := exec.Command("/bin/sleep", "30")
		commands <- cmd
		<-ready
		return d.USB.Serial, cmd, nil
	}), DefaultOptions())
	t.Cleanup(m.Close)
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- m.Toggle(testPhone) }()
	}
	first, second := <-commands, <-commands
	close(ready)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if (first.Process != nil) == (second.Process != nil) {
		t.Fatal("concurrent toggles must start exactly one process")
	}
}
