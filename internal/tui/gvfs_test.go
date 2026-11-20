package tui

import (
	"strings"
	"testing"

	"usbfs/internal/device"
)

func TestEmptyGVFSStorage(t *testing.T) {
	m := New(&device.Session{}, t.TempDir())
	t.Cleanup(m.CloseMirrors)
	m.platform = device.Android
	d := device.Device{Kind: "Android MTP", Name: "OnePlus", Owner: device.MountOwnerExternalGVfs}
	m.Update(scanResult{device: d})
	if !m.ready || m.warning || !strings.Contains(m.status, "switch USB mode to File Transfer (MTP), and press r") {
		t.Fatalf("empty storage: ready=%v warning=%v status=%q", m.ready, m.warning, m.status)
	}
	if cmd := m.key(runes("r")); cmd == nil || !m.busy {
		t.Fatal("r did not queue GVfs detection")
	}
}
