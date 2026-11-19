package device

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAndroidDriverReportsClaimConflict(t *testing.T) {
	dir := t.TempDir()
	driver := filepath.Join(dir, "jmtpfs")
	script := "#!/bin/sh\nprintf '%s\\n' 'libusb_claim_interface() reports device is busy, likely in use by GVFS or KDE MTP device handling already' >&2\nexit 1\n"
	if err := os.WriteFile(driver, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s := &Session{path: dir, inspect: func(string) ([]mountEntry, error) { return nil, nil }}
	err := s.start(Device{Kind: "Android MTP", driver: driver}, dir)
	if !errors.Is(err, ErrMTPBusy) || len(s.drivers) != 0 || s.HasMount() {
		t.Fatalf("claim conflict = %v, driver = %v, mounted = %v", err, s.drivers, s.HasMount())
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
	root := `/home/user/one two\three`
	mounts, err := parseMounts(strings.NewReader(listing), root)
	want := []mountEntry{{root, "fuse.usbfs"}, {root + "/child", "fuse.usbfs"}, {root, "fuse.ifuse"}}
	if err != nil || !reflect.DeepEqual(mounts, want) {
		t.Fatalf("mounts = %v, %v", mounts, err)
	}
	for _, invalid := range []string{"", "garbled"} {
		if _, err := parseMounts(strings.NewReader(invalid), "/mount"); err == nil {
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
		inspect: func(string) ([]mountEntry, error) {
			_, err := os.Stat(filepath.Join(dir, "detached"))
			switch {
			case err == nil:
				return nil, nil
			case os.IsNotExist(err):
				return []mountEntry{{filepath.Join(dir, "mount"), "fuse.usbfs"}}, nil
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

func TestAppleMountsAndCleansEveryShare(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "mount")
	stale := filepath.Join(root, "Stale")
	state := filepath.Join(dir, "state")
	for _, path := range []string{stale, state} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, contents string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(contents), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(state, "Stale"), "", 0600)
	t.Setenv("USBFS_TEST_DIR", dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	write(filepath.Join(dir, "idevice_id"), "#!/bin/sh\nprintf 'ipad-test\\n'\n", 0700)
	write(filepath.Join(dir, "ifuse"), `#!/bin/sh
if [ "$1" = "--list-apps" ]; then
  printf '%s\n' 'com.goodnotesapp.x, "1", "Goodnotes 6"' 'com.example.browser, "1", "Brave"' 'com.example.fail, "1", "Broken"'
  exit 0
fi
bundle=Media
while [ "$#" -gt 1 ]; do
  if [ "$1" = "--documents" ]; then
    shift
    bundle="$1"
  fi
  shift
done
name="${1##*/}"
printf 'mount %s %s\n' "$name" "$bundle" >> "$USBFS_TEST_DIR/calls"
if [ "$bundle" = "com.example.fail" ]; then
  exit 1
fi
marker="$USBFS_TEST_DIR/state/$name"
touch "$marker"
while [ -f "$marker" ]; do sleep .02; done
`, 0700)
	write(filepath.Join(dir, "fusermount3"), `#!/bin/sh
name="${2##*/}"
printf 'unmount %s\n' "$name" >> "$USBFS_TEST_DIR/calls"
if [ "$name" = "Goodnotes" ] && [ ! -f "$USBFS_TEST_DIR/release" ]; then
  exit 1
fi
rm -f "$USBFS_TEST_DIR/state/$name"
`, 0700)
	write(filepath.Join(dir, "fusermount"), "#!/bin/sh\nexit 1\n", 0700)
	lock, err := os.Create(filepath.Join(dir, "lock"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{
		path: root, lock: lock,
		inspect: func(prefix string) ([]mountEntry, error) {
			markers, err := os.ReadDir(state)
			if err != nil {
				return nil, err
			}
			var mounted []mountEntry
			for _, marker := range markers {
				path := filepath.Join(root, marker.Name())
				if path == prefix || strings.HasPrefix(path, prefix+"/") {
					mounted = append(mounted, mountEntry{path, "fuse.usbfs"})
				}
			}
			return mounted, nil
		},
	}
	t.Cleanup(func() {
		markers, _ := os.ReadDir(state)
		for _, marker := range markers {
			_ = os.Remove(filepath.Join(state, marker.Name()))
		}
		for _, driver := range s.drivers {
			driver.wait()
		}
		lock.Close()
	})

	d, err := s.DetectAndMount(Apple)
	if d.Kind == "" || err == nil || !strings.Contains(err.Error(), "skipped Broken") {
		t.Fatalf("partial mount = %+v, %v", d, err)
	}
	shares, err := s.Shares()
	if err != nil || !reflect.DeepEqual(shares, []string{"Goodnotes", "Brave", "Media"}) {
		t.Fatalf("shares = %v, %v", shares, err)
	}
	if _, _, err := s.OpenPath("."); err == nil {
		t.Fatal("synthetic root allowed filesystem writes or reads")
	}
	if _, _, err := s.OpenPath("../escape"); err == nil {
		t.Fatal("accepted a path outside the iPad shares")
	}
	share, relative, err := s.OpenPath("Goodnotes/subfolder")
	if err != nil {
		t.Fatal(err)
	}
	if share.Name() != filepath.Join(root, "Goodnotes") || relative != "subfolder" {
		t.Errorf("unexpected scoped path %q / %q", share.Name(), relative)
	}
	share.Close()

	log := &warningLog{waiting: make(chan struct{}, 1)}
	finished := make(chan bool, 1)
	go func() { finished <- s.Close(log) }()
	select {
	case <-log.waiting:
	case <-time.After(5 * time.Second):
		t.Error("shutdown did not report the busy Goodnotes mount")
	}
	select {
	case <-finished:
		t.Fatal("shutdown returned while Goodnotes was still mounted")
	default:
	}
	write(filepath.Join(dir, "release"), "", 0600)
	if !<-finished || s.HasMount() {
		t.Fatal("shutdown did not verify all shares were unmounted")
	}
	contents, err := os.ReadDir(root)
	if err != nil || len(contents) != 0 {
		t.Fatalf("left local mount directories: %v, %v", contents, err)
	}
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	want := "unmount Stale\nmount Goodnotes com.goodnotesapp.x\nmount Media Media\nmount Brave com.example.browser\nmount Broken com.example.fail\n"
	if err != nil || !strings.HasPrefix(string(calls), want) {
		t.Fatalf("mount sequence = %q, %v", calls, err)
	}
}

func TestRemoveHostFileProtectsMountAndDirectories(t *testing.T) {
	s, err := NewSession(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(os.Stderr)
	if err := os.Mkdir(s.Path(), 0700); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(s.Path())
	for _, name := range []string{filepath.Base(s.Path()), filepath.Base(s.Path()) + ".lock"} {
		if err := s.RemoveHostFile(parent, name); err == nil {
			t.Fatalf("removed protected item %s", name)
		}
	}
	host := t.TempDir()
	folder := filepath.Join(host, "notes")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveHostFile(host, "notes"); err == nil {
		t.Fatal("removed an empty folder")
	}
	note := filepath.Join(folder, "note.txt")
	if err := os.WriteFile(note, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveHostFile(host, "notes"); err == nil {
		t.Fatal("removed a nonempty folder")
	}
	if err := os.Symlink(folder, filepath.Join(host, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveHostFile(host, "alias"); err == nil {
		t.Fatal("removed a symbolic link")
	}
}
