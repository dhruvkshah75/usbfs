package device

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPlatformDriverAndDetection(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	add := func(name, script string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	add("fusermount3", "exit 0")
	add("ifuse", "exit 0")
	add("idevice_id", `if [ "$1" = "-l" ]; then printf 'ipad-123\n'; else printf 'My iPad\n'; fi`)
	if _, err := mountDriver(Android); err == nil || !strings.Contains(err.Error(), "sudo apt install jmtpfs") {
		t.Fatalf("missing Android dependency returned %v", err)
	}
	add("jmtpfs", `printf '%s\n' 'Available devices (busLocation, devNum, productId, vendorId, product, vendor):' '2, 19, 0x6860, 0x04e8, Galaxy, Samsung'`)
	check := func(platform Platform, wantDriver, wantName string, wantArgs []string) {
		t.Helper()
		driver, err := mountDriver(platform)
		if err != nil || driver != wantDriver {
			t.Fatalf("%s driver = %q, %v", platform, driver, err)
		}
		d, err := find(driver)
		if err != nil || d.Name != wantName || !reflect.DeepEqual(d.args, wantArgs) {
			t.Fatalf("%s device = %+v, %v", platform, d, err)
		}
	}
	check(Android, "jmtpfs", "Galaxy", []string{"-device=2,19"})
	check(Apple, "ifuse", "My iPad", []string{"--udid", "ipad-123"})
	add("simple-mtpfs", `printf '1: Pixel\n'`)
	check(Android, "simple-mtpfs", "Pixel", []string{"--device", "1"})
}

func TestNoJMTPDevices(t *testing.T) {
	if _, _, ok := parseJMTP("Available devices (busLocation, devNum, productId, vendorId, product, vendor):"); ok {
		t.Fatal("jmtpfs header was mistaken for a device")
	}
}

func TestAppleShareCSV(t *testing.T) {
	listing := `"CFBundleIdentifier","CFBundleVersion","CFBundleDisplayName"
com.goodnotesapp.x, "1", "Goodnotes 6"
com.goodnotesapp.x, "2", "Duplicate"
com.example.brave, "1", "Brave, Browser"
"com.example.chrome","2","../Chrome"
"com.example.media","1","Media"
"com.example.second","1","media"
--documents, "2", "Invalid"
`
	want := []appleShare{
		{"Goodnotes", goodnotesID}, {"Media", ""},
		{"Brave, Browser", "com.example.brave"}, {"_Chrome", "com.example.chrome"},
		{"Media (2)", "com.example.media"}, {"media (3)", "com.example.second"},
	}
	if got := parseAppleShares(listing); !reflect.DeepEqual(got, want) {
		t.Fatalf("shares = %+v, want %+v", got, want)
	}
}
