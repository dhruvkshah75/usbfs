package device

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUSBIdentity(t *testing.T) {
	root := t.TempDir()
	for name, values := range map[string][3]string{
		"3-1": {"3", "7", "first"},
		"3-2": {"3", "19", "second"},
		"4-1": {"4", "19", "same-address-different-bus"},
	} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for i, field := range []string{"busnum", "devnum", "serial"} {
			if err := os.WriteFile(filepath.Join(dir, field), []byte(values[i]+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := usbIdentity(root, "003", "019"); got != (USBIdentity{Path: "3-2", Serial: "second", deviceNumber: 19}) {
		t.Fatalf("selected USB identity = %+v", got)
	}
	if got := usbIdentity(root, "003", "020"); got != (USBIdentity{}) {
		t.Fatalf("unknown USB address matched %+v", got)
	}
}
