package mirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"usbfs/internal/device"
)

var testIPad = device.Device{Kind: "iOS/iPadOS", Name: "iPad", USB: device.USBIdentity{Serial: "00008100-1234"}}

func TestIPadBackendSelection(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	check := func(mode, want, hint string) {
		t.Helper()
		got, _, err := ipadBackend(mode)
		if got != want || (hint == "" && err != nil) || (hint != "" && (err == nil || !strings.Contains(err.Error(), hint))) {
			t.Fatalf("mode %q got %q, %v; want %q, %q", mode, got, err, want, hint)
		}
	}
	check("", "", "install devicehub_rs")
	check("invalid", "", "must be auto")
	writeScript(t, filepath.Join(dir, "pymobiledevice3"), "exit 0")
	writeScript(t, filepath.Join(dir, "devicehub_rs"), "exit 0")
	check("auto", "serve-web", "")
	check("devicehub", "", "ffmpeg")
	writeScript(t, filepath.Join(dir, "ffmpeg"), "exit 0")
	check("", "devicehub_rs", "")
	check("devicehub", "devicehub_rs", "")
	check("web", "serve-web", "")
}

func TestWebViewerSelection(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	if _, err := findWebViewer(); err == nil {
		t.Fatal("missing browser was accepted")
	}
	for _, name := range []string{"xdg-open", "brave-browser", "google-chrome", "chromium-browser", "chromium"} {
		writeScript(t, filepath.Join(dir, name), "exit 0")
		viewer, err := findWebViewer()
		if err != nil || filepath.Base(viewer.browser) != name {
			t.Fatalf("wanted %s, got %+v, %v", name, viewer, err)
		}
		viewer.url = "http://127.0.0.1:39107/"
		args := viewer.command("/tmp/profile").Args[1:]
		if name == "xdg-open" {
			if !reflect.DeepEqual(args, []string{viewer.url}) {
				t.Fatalf("fallback args = %v", args)
			}
		} else if !strings.Contains(strings.Join(args, " "), "--app="+viewer.url) {
			t.Fatalf("browser args = %v", args)
		}
	}
}

func TestIPadDiagnostics(t *testing.T) {
	for log, expected := range map[string]string{
		"Screen mirroring requires iOS 27 or later": "iPadOS 27 or later",
		"ERROR: Developer Mode is disabled":         "Enable Developer Mode",
		"InvalidServiceError":                       "Developer Disk Image",
		"Developer Disk Image is not mounted":       "Developer Disk Image",
		"PairingDialogResponsePendingError":         "Trust This Computer",
		"Device locked":                             "Trust This Computer",
		"Camera or microphone in use (9022)":        "camera or microphone",
		"Device disconnected":                       "mirror stopped",
	} {
		if got := ipadDiagnostic(log); !strings.Contains(got, expected) {
			t.Errorf("diagnostic for %q = %q, want %q", log, got, expected)
		}
	}
}

func TestIPadDDIPreflight(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("USBFS_IPAD_MIRROR_BACKEND", "devicehub")
	calls := filepath.Join(dir, "ddi")
	t.Setenv("DDI_CALLS", calls)
	writeScript(t, filepath.Join(dir, "devicehub_rs"), "exit 0")
	writeScript(t, filepath.Join(dir, "ffmpeg"), "exit 0")
	writeScript(t, filepath.Join(dir, "pymobiledevice3"), `
case "$1 $2" in
  "cryptex list") printf '%s' "$DDI_LIST";;
  "mounter auto-mount") printf '%s' "$*" > "$DDI_CALLS"; exit 1;;
  *) exit 2;;
esac`)
	t.Setenv("DDI_LIST", "com.apple.MobileAsset.DDI")
	backend := iPad{}
	plan, err := backend.Prepare(context.Background(), testIPad, Options{})
	if err != nil || plan.serial != testIPad.USB.Serial ||
		!reflect.DeepEqual(plan.cmd.Args[1:], []string{"--no-mcp", testIPad.USB.Serial}) {
		t.Fatalf("native launch = %+v, %v", plan, err)
	}
	if _, err := os.Stat(calls); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("existing DDI triggered auto-mount: %v", err)
	}
	t.Setenv("DDI_LIST", "[]")
	if _, err := backend.Prepare(context.Background(), testIPad, Options{}); err != nil {
		t.Fatalf("auto-mount failure prevented launch: %v", err)
	}
	if out, err := os.ReadFile(calls); err != nil || string(out) != "mounter auto-mount --udid "+testIPad.USB.Serial {
		t.Fatalf("unexpected auto-mount call: %q, %v", out, err)
	}
	writeScript(t, filepath.Join(dir, "ideviceinfo"), "echo 26.4")
	if _, err := backend.Prepare(context.Background(), testIPad, Options{}); err == nil || err.Error() != ipadVersionMessage {
		t.Fatalf("old iPadOS should fail preflight: %v", err)
	}
}

func TestIPadMirrorProcessLifecycle(t *testing.T) {
	for _, mode := range []string{"devicehub", "web"} {
		for _, stop := range []string{"toggle", "shutdown", "window"} {
			t.Run(mode+"/"+stop, func(t *testing.T) {
				testIPadMirrorLifecycle(t, mode, stop)
			})
		}
	}
}

func testIPadMirrorLifecycle(t *testing.T, mode, stop string) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("USBFS_IPAD_MIRROR_BACKEND", mode)
	t.Setenv("USBFS_TEST_DIR", dir)
	loop := `
trap 'exit 0' TERM
echo "$$" >> "$USBFS_TEST_DIR/$role"
while [ ! -f "$USBFS_TEST_DIR/close-$role" ]; do /bin/sleep 0.02; done
`
	writeScript(t, filepath.Join(dir, "devicehub_rs"), "role=native\n"+loop)
	writeScript(t, filepath.Join(dir, "ffmpeg"), "exit 0")
	writeScript(t, filepath.Join(dir, "pymobiledevice3"), `
if [ "$1 $2" = "cryptex list" ]; then echo com.apple.MobileAsset.DDI; exit; fi
role=server
for arg; do port=$arg; done
echo "Open http://127.0.0.1:$port/ in Safari/Chrome."
`+loop)
	writeScript(t, filepath.Join(dir, "chromium"), "role=browser\n"+loop)

	m := New(Android{}, Options{})
	t.Cleanup(m.Close)
	if err := m.Toggle(testIPad); err != nil {
		t.Fatal(err)
	}
	start := nextEvent(t, m)
	if start.State != Started || start.USB != testIPad.USB {
		t.Fatalf("start event = %+v", start)
	}
	window := "native"
	roles := []string{window}
	if mode == "web" {
		window = "browser"
		roles = []string{"server", window}
	}
	pids := make([]int, 0, len(roles))
	for _, role := range roles {
		path := filepath.Join(dir, role)
		deadline := time.Now().Add(time.Second)
		for {
			out, _ := os.ReadFile(path)
			if pid, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil {
				pids = append(pids, pid)
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s did not start exactly once: %q", role, out)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	switch stop {
	case "toggle":
		if err := m.Toggle(testIPad); err != nil {
			t.Fatal(err)
		}
	case "shutdown":
		m.Close()
	case "window":
		if err := os.WriteFile(filepath.Join(dir, "close-"+window), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if stop != "shutdown" {
		if event := nextEvent(t, m); event.State != Exited || event.Warning {
			t.Fatalf("stop event = %+v", event)
		}
	}
	if m.active(testIPad.USB) {
		t.Fatal("stopped iPad still has a registered session")
	}
	for _, pid := range pids {
		if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
			t.Errorf("PID %d was not reaped: %v", pid, err)
		}
	}
	for _, role := range roles {
		if out, _ := os.ReadFile(filepath.Join(dir, role)); len(strings.Fields(string(out))) != 1 {
			t.Errorf("unexpected duplicate %s processes: %q", role, out)
		}
	}
}
