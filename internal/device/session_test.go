package device

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAndroidDriverReportsClaimConflict(t *testing.T) {
	dir := t.TempDir()
	driver := filepath.Join(dir, "jmtpfs")
	script := "#!/bin/sh\nprintf '%s\\n' 'libusb_claim_interface() reports device is busy, likely in use by GVFS or KDE MTP device handling already' >&2\nexit 1\n"
	if err := os.WriteFile(driver, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s := &Session{path: dir, inspect: func(string) ([]string, error) { return nil, nil }}
	err := s.start(Device{Kind: "Android MTP", driver: driver})
	if !errors.Is(err, ErrMTPBusy) || s.driver != nil || s.HasMount() {
		t.Fatalf("claim conflict = %v, driver = %v, mounted = %v", err, s.driver, s.HasMount())
	}
}

func TestAndroidSessionResetIsDistinguishedFromBusy(t *testing.T) {
	log := "PTP_ERROR_IO: failed to open session, trying again after resetting"
	if got := mtpIssue(Device{Kind: "Android MTP"}, log); got != ErrMTPSession {
		t.Fatalf("session failure = %v", got)
	}
	if got := mtpIssue(Device{Kind: "iOS/iPadOS"}, log); got != nil {
		t.Fatalf("classified an Apple driver failure as MTP: %v", got)
	}
}

func TestProcMountsMatchesExactEscapedPath(t *testing.T) {
	listing := `ifuse /home/user/one\040two\134three fuse.usbfs rw 0 0
ifuse /home/user/one\040two\134three/child fuse.usbfs rw 0 0
ifuse /home/user/one\040two\134three fuse.ifuse rw 0 0
`
	types, err := readMountTypes(strings.NewReader(listing), `/home/user/one two\three`)
	if err != nil || !reflect.DeepEqual(types, []string{"fuse.usbfs", "fuse.ifuse"}) {
		t.Fatalf("mount types = %v, %v", types, err)
	}
	for _, invalid := range []string{"", "garbled"} {
		if _, err := readMountTypes(strings.NewReader(invalid), "/mount"); err == nil {
			t.Fatalf("accepted invalid mount table %q as proof of unmount", invalid)
		}
	}
}

func TestCloseWaitsAndVerifiesFallback(t *testing.T) {
	dir := t.TempDir()
	lock, err := os.Create(filepath.Join(dir, "lock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	t.Setenv("USBFS_TEST_DIR", dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	makeHelper := func(name, script string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	makeHelper("fusermount3", `echo fuse3 >> "$USBFS_TEST_DIR/calls"; exit 0`)
	makeHelper("fusermount", `
echo fuse2 >> "$USBFS_TEST_DIR/calls"
if [ ! -f "$USBFS_TEST_DIR/busy" ]; then
  touch "$USBFS_TEST_DIR/busy"
  exit 1
fi
touch "$USBFS_TEST_DIR/detached"
`)
	started, release, waiting := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	s := &Session{
		path: filepath.Join(dir, "mount"), lock: lock,
		inspect: func(string) ([]string, error) {
			_, err := os.Stat(filepath.Join(dir, "detached"))
			switch {
			case err == nil:
				return nil, nil
			case os.IsNotExist(err):
				return []string{"fuse.usbfs"}, nil
			default:
				return nil, err
			}
		},
	}
	go s.Work(func() {
		close(started)
		<-release
	})
	<-started
	log := &warningLog{waiting: waiting}
	finished := make(chan bool, 1)
	go func() { finished <- s.Close(log) }()
	<-waiting
	_, early := os.Stat(filepath.Join(dir, "calls"))
	close(release)
	if !<-finished {
		t.Fatal("shutdown did not confirm previous mount")
	}
	if !os.IsNotExist(early) {
		t.Fatalf("unmount was attempted during active work: %v", early)
	}
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil || string(calls) != "fuse3\nfuse2\nfuse3\nfuse2\n" {
		t.Fatalf("unmount calls = %q, %v", calls, err)
	}
	if !strings.Contains(log.String(), "Close files opened from the device") {
		t.Fatalf("missing busy instruction: %s", log.String())
	}
	if s.Work(func() { t.Error("queued work ran after shutdown") }) {
		t.Error("session still accepting work")
	}
}

type warningLog struct {
	bytes.Buffer
	waiting chan struct{}
}

func (w *warningLog) Write(p []byte) (int, error) {
	select {
	case w.waiting <- struct{}{}:
	default:
	}
	return w.Buffer.Write(p)
}

func TestParseMTP(t *testing.T) {
	id, name, ok := parseMTP("libmtp info\n2: Acme, Phone\n3: Tablet")
	if !ok || id != "2" || name != "Acme, Phone" {
		t.Fatalf("parseMTP = %q, %q, %v", id, name, ok)
	}
	if _, _, ok := parseMTP("No raw devices found."); ok {
		t.Fatal("reported a device from an empty listing")
	}
}
