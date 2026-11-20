package device

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// USBIdentity links the mount driver's device to ADB's USB path or serial.
type USBIdentity struct {
	Path, Serial string
	deviceNumber int
}

const usbSysfs = "/sys/bus/usb/devices"

// Present checks that the USB path still belongs to the original device address.
func (u USBIdentity) Present() bool {
	number, err := strconv.Atoi(readUSBAttribute(filepath.Join(usbSysfs, u.Path), "devnum"))
	return err == nil && number == u.deviceNumber
}

func readUSBAttribute(dir, name string) string {
	value, _ := os.ReadFile(filepath.Join(dir, name))
	return strings.TrimSpace(string(value))
}

func mountedUSB(pid int) USBIdentity {
	fds, _ := filepath.Glob(filepath.Join("/proc", strconv.Itoa(pid), "fd", "*"))
	var node string
	for _, fd := range fds {
		target, err := os.Readlink(fd)
		if err != nil || !strings.HasPrefix(target, "/dev/bus/usb/") {
			continue
		}
		if node != "" && node != target {
			return USBIdentity{}
		}
		node = target
	}
	bus, dev := filepath.Base(filepath.Dir(node)), filepath.Base(node)
	return usbIdentity(usbSysfs, bus, dev)
}

func usbIdentity(sysfs, bus, dev string) USBIdentity {
	busNum, busErr := strconv.Atoi(bus)
	devNum, devErr := strconv.Atoi(dev)
	if busErr != nil || devErr != nil {
		return USBIdentity{}
	}
	entries, _ := os.ReadDir(sysfs)
	for _, entry := range entries {
		dir := filepath.Join(sysfs, entry.Name())
		b, _ := strconv.Atoi(readUSBAttribute(dir, "busnum"))
		d, _ := strconv.Atoi(readUSBAttribute(dir, "devnum"))
		if b == busNum && d == devNum {
			return USBIdentity{Path: entry.Name(), Serial: readUSBAttribute(dir, "serial"), deviceNumber: d}
		}
	}
	return USBIdentity{}
}
