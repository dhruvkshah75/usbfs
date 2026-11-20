package device

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

const gvfsList = `Volume(0): Another phone
  Type: GProxyVolume (GProxyVolumeMonitorMTP)
  unix-device: '/dev/bus/usb/003/028'
  activation_root=mtp://Another_OTHER/
  Mount(0): Another phone -> mtp://Another_OTHER/
    Type: GProxyShadowMount (GProxyVolumeMonitorMTP)
Volume(1): OnePlus 12
  Type: GProxyVolume (GProxyVolumeMonitorMTP)
  ids:
   unix-device: '/dev/bus/usb/003/029'
  activation_root=mtp://OnePlus_OnePlus_12_SERIAL/
  Mount(0): OnePlus 12 -> mtp://OnePlus_OnePlus_12_SERIAL/
    Type: GProxyShadowMount (GProxyVolumeMonitorMTP)
Volume(2): Other USB disk
  Type: GProxyVolumeMonitorUDisks2
  unix-device: '/dev/bus/usb/003/029'
  activation_root=mtp://Wrong/
Mount(1): mtp -> mtp://OnePlus_OnePlus_12_SERIAL/
  Type: GDaemonMount
`

func TestGVFSMatchesSelectedUSB(t *testing.T) {
	volumes, mounted := parseGVFS(gvfsList)
	if len(volumes) != 2 {
		t.Fatalf("volumes: %+v", volumes)
	}
	d := Device{driver: "jmtpfs", args: []string{"-device=3,29"}, USB: USBIdentity{Serial: "SERIAL"}}
	ctx := context.Background()
	if matchesGVFS(ctx, d, volumes[0]) || !matchesGVFS(ctx, d, volumes[1]) || !mounted[volumes[1].uri] {
		t.Fatal("did not match the selected mounted phone")
	}
	fallback := volumes[1]
	fallback.node = ""
	if !matchesGVFS(ctx, d, fallback) {
		t.Fatal("serial fallback did not match")
	}
	fallback.node = "/dev/bus/usb/003/028"
	if matchesGVFS(ctx, d, fallback) {
		t.Fatal("serial overrode a conflicting USB address")
	}
	_, unmounted := parseGVFS("Volume(0): Phone\n Type: GProxyVolume (GProxyVolumeMonitorMTP)\n activation_root=mtp://Phone/\n")
	if unmounted["mtp://Phone"] {
		t.Fatal("activation root alone was treated as mounted")
	}
	if path := parseGVFSPath("uri: mtp://Phone/\nlocal path: /run/user/1000/gvfs/mtp:host=Phone\n"); path != "/run/user/1000/gvfs/mtp:host=Phone" {
		t.Fatalf("parsed path = %q", path)
	}
}

func TestGVFSMountReuseAndCleanup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("PATH", dir)
	gvfs := filepath.Join(dir, "gvfs")
	mount := filepath.Join(gvfs, "mtp:host=OnePlus_OnePlus_12_SERIAL")
	if err := os.MkdirAll(mount, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.CreateTemp(dir, "lock")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "mutation")
	t.Setenv("USBFS_TEST_MARKER", marker)
	for name, body := range map[string]string{
		"jmtpfs":      `if [ "$1" = '-l' ]; then echo '3,29,0x1234,0x4321,OnePlus 12,OnePlus'; else echo unexpected > "$USBFS_TEST_MARKER"; exit 1; fi`,
		"fusermount3": `echo unexpected > "$USBFS_TEST_MARKER"; exit 1`,
		"gio":         "case \"$1 $2\" in\n'mount -li') printf '%s' '" + gvfsList + "';;\n'info --') printf 'local path: %s\\n' '" + mount + "';;\n*) echo unexpected > \"$USBFS_TEST_MARKER\"; exit 1;;\nesac",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	s := &Session{path: filepath.Join(dir, "managed"), lock: lock, inspect: func(path string) ([]mountEntry, error) {
		if path == gvfs {
			return []mountEntry{{gvfs, "fuse.gvfsd-fuse"}}, nil
		}
		return nil, nil
	}}
	d, err := s.DetectAndMount(Android)
	if err != nil || d.Owner != MountOwnerExternalGVfs || d.MountPath != mount || !s.HasMount() {
		t.Fatalf("attach: %+v, %v", d, err)
	}
	root, _, err := s.OpenPath(".")
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	if s.Close(io.Discard) || s.HasMount() {
		t.Fatal("external mount was treated as owned during cleanup")
	}
	if _, err := os.Stat(mount); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unexpected mount management command: %v", err)
	}
}
