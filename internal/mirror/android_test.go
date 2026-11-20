package mirror

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"usbfs/internal/device"
)

const adbListing = `* daemon started successfully
List of devices attached
emulator-5554          device product:sdk model:Emulator transport_id:9
PIXEL                  device usb:2-1 product:husky model:Pixel_8_Pro device:husky transport_id:1
GALAXY                 unauthorized usb:2-2 transport_id:2
TABLET                 offline usb:2-3 transport_id:3
LIBUSB                 device product:tablet model:Tab transport_id:4
????????????           no permissions usb:2-4 transport_id:5
`

func TestADBDiscovery(t *testing.T) {
	devices := parseADB(adbListing)
	if len(devices) != 6 {
		t.Fatalf("parsed devices = %+v", devices)
	}
	for _, tt := range []struct {
		path, serial string
		want, hint   string
	}{
		{path: "2-1", serial: "PIXEL", want: "Pixel_8_Pro"},
		{path: "2-2", serial: "GALAXY", hint: "Allow USB debugging"},
		{path: "2-3", serial: "TABLET", hint: "Reconnect the USB cable"},
		{path: "2-4", serial: "BLOCKED", hint: "udev"},
		{path: "2-5", serial: "LIBUSB", want: "Tab"},
		{path: "2-6", serial: "MISSING", hint: "Enable USB debugging"},
		{path: "2-6", serial: "PIXEL", hint: "Enable USB debugging"},
	} {
		t.Run(tt.serial+"/"+tt.path, func(t *testing.T) {
			got, err := selectADB(devices, device.USBIdentity{Path: tt.path, Serial: tt.serial})
			if tt.hint != "" {
				if err == nil || !strings.Contains(err.Error(), tt.hint) {
					t.Fatalf("got %+v, %v, want %q", got, err, tt.hint)
				}
				return
			}
			if err != nil || got.serial != tt.serial || got.model != tt.want {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}

	duplicate := append(devices, adbDevice{serial: "PIXEL", state: "device", usb: "2-8"})
	if _, err := selectADB(duplicate, device.USBIdentity{Path: "2-1", Serial: "PIXEL"}); err == nil {
		t.Fatal("shared ADB serial was accepted")
	}
}

func TestAndroidPrepare(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	target := device.Device{Name: "Phone", USB: device.USBIdentity{Path: "2-1", Serial: "PIXEL"}}
	backend := Android{}
	opts := DefaultOptions()
	if _, _, err := backend.Prepare(context.Background(), target, opts); err == nil || !strings.Contains(err.Error(), "official Genymobile") {
		t.Fatalf("missing scrcpy: %v", err)
	}
	writeScript(t, filepath.Join(dir, "scrcpy"), "exit 0")
	if _, _, err := backend.Prepare(context.Background(), target, opts); err == nil || !strings.Contains(err.Error(), "sudo apt install adb") {
		t.Fatalf("missing adb: %v", err)
	}
	adb := filepath.Join(dir, "adb")
	writeScript(t, adb, `[ "$1 $2" = "devices -l" ] || exit 1
printf '%s' '`+adbListing+`'`)
	serial, cmd, err := backend.Prepare(context.Background(), target, opts)
	want := []string{"-s", "PIXEL", "--window-title", "USBFS - Pixel 8 Pro", "--turn-screen-off",
		"--stay-awake", "--power-off-on-close", "--push-target=/sdcard/Download/", "--max-size=1280", "--max-fps=60"}
	if err != nil || serial != "PIXEL" || !reflect.DeepEqual(cmd.Args[1:], want) {
		t.Fatalf("prepare got %q, %v, %v", serial, cmd, err)
	}
	if !strings.Contains(strings.Join(cmd.Environ(), "\n"), "\nADB="+adb) {
		t.Fatalf("scrcpy is not configured to reuse the preflight ADB client")
	}
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
}
