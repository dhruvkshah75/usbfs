package mirror

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"usbfs/internal/device"
)

type iPad struct{}

type webViewer struct {
	browser string
	url     string
	app     bool
}

func ipadBackend(mode string) (name, path string, err error) {
	switch mode {
	case "", "auto", "devicehub", "web":
	default:
		return "", "", errors.New("USBFS_IPAD_MIRROR_BACKEND must be auto, devicehub, or web")
	}
	if mode != "web" {
		native, nativeErr := exec.LookPath("devicehub_rs")
		_, ffmpegErr := exec.LookPath("ffmpeg")
		if nativeErr == nil && ffmpegErr == nil {
			return "devicehub_rs", native, nil
		}
		if mode == "devicehub" {
			return "", "", errors.New("Install devicehub_rs and ffmpeg and add both to PATH")
		}
	}
	if web, err := exec.LookPath("pymobiledevice3"); err == nil {
		return "serve-web", web, nil
	}
	if mode == "web" {
		return "", "", errors.New("Install a current pymobiledevice3 release with serve-web and add it to PATH")
	}
	return "", "", errors.New("For iPad mirroring, install devicehub_rs with ffmpeg, or install pymobiledevice3 with serve-web")
}

func findWebViewer() (webViewer, error) {
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "brave-browser", "xdg-open"} {
		if path, err := exec.LookPath(name); err == nil {
			return webViewer{browser: path, app: name != "xdg-open"}, nil
		}
	}
	return webViewer{}, errors.New("Install Chromium, Chrome, or Brave to open the iPad mirror")
}

func (w webViewer) command(profile string) *exec.Cmd {
	if !w.app {
		return exec.Command(w.browser, w.url)
	}
	// A dedicated profile gives this window its own process, even if the browser is already open.
	return exec.Command(w.browser, "--user-data-dir="+profile, "--no-first-run",
		"--disable-background-mode", "--window-size=960,720", "--app="+w.url+"#usbfs-app")
}

func (iPad) Prepare(ctx context.Context, target device.Device, _ Options) (launch, error) {
	udid := target.USB.Serial
	if udid == "" || strings.HasPrefix(udid, "-") {
		return launch{}, errors.New("Could not identify the mounted iPad. Reconnect it and reopen usbfs")
	}
	name, path, err := ipadBackend(strings.ToLower(strings.TrimSpace(os.Getenv("USBFS_IPAD_MIRROR_BACKEND"))))
	if err != nil {
		return launch{}, err
	}
	plan := launch{serial: udid, name: name}
	if name == "devicehub_rs" {
		// Upstream's CLI takes a positional UDID. Disable its optional agent listener.
		plan.cmd = exec.Command(path, "--no-mcp", udid)
		plan.cmd.Env = append(plan.cmd.Environ(), "RUST_LOG=devicehub_rs=info")
	} else {
		viewer, err := findWebViewer()
		if err != nil {
			return launch{}, err
		}
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return launch{}, fmt.Errorf("choose iPad viewer port: %w", err)
		}
		port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
		listener.Close()
		viewer.url = "http://127.0.0.1:" + port + "/"
		plan.viewer = viewer
		plan.cmd = exec.Command(path, "developer", "core-device", "display", "serve-web",
			"--udid", udid, "--userspace", "--bind", "127.0.0.1", "--http-port", port)
	}
	if info, err := exec.LookPath("ideviceinfo"); err == nil {
		out, err := ipadProbe(ctx, 3*time.Second, info, "-u", udid, "-k", "ProductVersion")
		major, _, _ := strings.Cut(strings.TrimSpace(string(out)), ".")
		if version, parseErr := strconv.Atoi(major); err == nil && parseErr == nil && version < 27 {
			return launch{}, errors.New(ipadVersionMessage)
		}
	}
	if pmd, err := exec.LookPath("pymobiledevice3"); err == nil {
		// iPadOS 27 exposes the mounted DDI in the cryptex list.
		out, err := ipadProbe(ctx, 4*time.Second, pmd, "cryptex", "list", "--udid", udid, "--userspace")
		if err == nil && !strings.Contains(string(out), "com.apple.MobileAsset.DDI") {
			if _, err := ipadProbe(ctx, 12*time.Second, pmd, "mounter", "auto-mount", "--udid", udid); err != nil {
				return launch{}, ipadMountError(err)
			}
		}
	}
	return plan, ctx.Err()
}

func ipadProbe(ctx context.Context, timeout time.Duration, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil && ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, err
}

func ipadMountError(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		var log recentLogs
		for line := range strings.SplitSeq(string(exit.Stderr), "\n") {
			log.add(strings.TrimSpace(line))
		}
		if message := log.ipadDiagnostic(); message != "" {
			return errors.New(message)
		}
		if message := log.diagnostic(); message != "" {
			return fmt.Errorf("Could not mount the Developer Disk Image. %s", message)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("Developer Disk Image mount timed out. Run pymobiledevice3 mounter auto-mount, then retry")
	}
	return fmt.Errorf("Could not mount the Developer Disk Image: %w", err)
}

const (
	ipadVersionMessage      = "iPadOS 27 or later is required for USB screen mirroring."
	ipadDisconnectedMessage = "iPad disconnected — mirror stopped."
)

func ipadDiagnostic(log string) string {
	log = strings.ToLower(log)
	switch {
	case strings.Contains(log, "requires ios 27"), strings.Contains(log, "requires ipados 27"),
		strings.Contains(log, "ios 27 or later"), strings.Contains(log, "ipados 27 or later"),
		strings.Contains(log, "ios 27+"):
		return ipadVersionMessage
	case strings.Contains(log, "camera or microphone") && (strings.Contains(log, "using") || strings.Contains(log, "in use")),
		strings.Contains(log, "camera in use"), strings.Contains(log, "microphone in use"),
		strings.Contains(log, "9022"):
		return "iPadOS blocks screen mirroring while an app is using the camera or microphone."
	case strings.Contains(log, "developermodedisabled"), strings.Contains(log, "developer mode") &&
		(strings.Contains(log, "disabled") || strings.Contains(log, "not enabled") || strings.Contains(log, "enable developer mode")):
		return "Enable Developer Mode on iPad (Settings → Privacy & Security → Developer Mode)."
	case strings.Contains(log, "ddi not mounted"), strings.Contains(log, "developerdiskimagenotmounted"),
		strings.Contains(log, "developerdiskimage") && strings.Contains(log, "none is mounted"),
		strings.Contains(log, "developer disk image") && strings.Contains(log, "not mounted"),
		strings.Contains(log, "invalidservice"), strings.Contains(log, "service not found"), strings.Contains(log, "invalid service"):
		return "Developer Disk Image not mounted or missing the mirror service. Run pymobiledevice3 mounter auto-mount or reconnect iPad."
	case strings.Contains(log, "not trusted"), strings.Contains(log, "untrusted"),
		strings.Contains(log, "device locked"), strings.Contains(log, "device is locked"),
		strings.Contains(log, "notpaired"), strings.Contains(log, "passwordprotected"),
		strings.Contains(log, "pairingdialogresponsepending"), strings.Contains(log, "userdeniedpairing"):
		return "Unlock your iPad and tap ‘Trust This Computer’."
	case strings.Contains(log, "disconnected"), strings.Contains(log, "connection reset by peer"),
		strings.Contains(log, "device not found"), strings.Contains(log, "no device connected"),
		strings.Contains(log, "nodeviceconnected"):
		return ipadDisconnectedMessage
	default:
		return ""
	}
}

func (m *Manager) watchViewer(s *session, ready <-chan struct{}) error {
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return nil
	case <-timer.C:
		return errors.New("iPad web mirror did not become ready within 10 seconds")
	case <-ready:
	}
	if s.ctx.Err() != nil {
		return nil
	}

	var profile string
	if s.viewer.app {
		var err error
		profile, err = os.MkdirTemp("", "usbfs-mirror-*")
		if err != nil {
			return fmt.Errorf("create iPad viewer profile: %w", err)
		}
		defer os.RemoveAll(profile)
	}
	viewer := s.viewer
	screenURL, closeViewer, err := startScreenOnlyViewer(viewer.url)
	if err != nil {
		return fmt.Errorf("start screen-only iPad viewer: %w", err)
	}
	defer closeViewer()
	viewer.url = screenURL
	cmd := viewer.command(profile)
	output, err := startLogged(cmd)
	if err != nil {
		return fmt.Errorf("open iPad viewer: %w", err)
	}
	var logs recentLogs
	waitLogs := drainLogs(output, logs.add)
	defer waitLogs()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	message := "Mirroring iPad via serve-web · F2 toggles controls"
	if !s.viewer.app {
		message += " · Use Alt+m to stop the server"
	}
	m.emit(Event{USB: s.device, State: Started, Message: message})

	pid := cmd.Process.Pid
	select {
	case err := <-done:
		if s.viewer.app {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		if err != nil && s.ctx.Err() == nil {
			waitLogs()
			return fmt.Errorf("iPad viewer closed: %w %s", err, logs.diagnostic())
		}
		if !viewer.app {
			// xdg-open can hand off to an existing browser and exit immediately.
			<-s.ctx.Done()
		}
	case <-s.ctx.Done():
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		case <-timer.C:
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			<-done
		}
	}
	return nil
}
