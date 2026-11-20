package mirror

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"usbfs/internal/device"
)

type Options struct {
	TurnScreenOff, StayAwake, PowerOffOnClose bool
	PushTarget                                string
	MaxSize, MaxFPS                           int
}

func DefaultOptions() Options {
	return Options{
		TurnScreenOff: true, StayAwake: true, PowerOffOnClose: true,
		PushTarget: "/sdcard/Download/", MaxSize: 1280, MaxFPS: 60,
	}
}

func (o Options) args(serial, model string) []string {
	args := []string{"-s", serial, "--window-title", "USBFS - " + model}
	if o.TurnScreenOff {
		args = append(args, "--turn-screen-off")
	}
	if o.StayAwake {
		args = append(args, "--stay-awake")
	}
	if o.PowerOffOnClose {
		args = append(args, "--power-off-on-close")
	}
	if o.PushTarget != "" {
		args = append(args, "--push-target="+o.PushTarget)
	}
	if o.MaxSize > 0 {
		args = append(args, "--max-size="+strconv.Itoa(o.MaxSize))
	}
	if o.MaxFPS > 0 {
		args = append(args, "--max-fps="+strconv.Itoa(o.MaxFPS))
	}
	return args
}

type Android struct{}

func (Android) Prepare(ctx context.Context, target device.Device, opts Options) (launch, error) {
	scrcpy, err := exec.LookPath("scrcpy")
	if err != nil {
		return launch{}, errors.New("scrcpy is missing. Install the official Genymobile scrcpy release and add it to PATH")
	}
	adb, err := exec.LookPath("adb")
	if err != nil {
		return launch{}, errors.New("adb is missing. On Ubuntu, run sudo apt install adb")
	}
	if target.USB.Serial == "" && !target.USB.Present() {
		return launch{}, errors.New("could not identify the mounted USB device. Reconnect it and reopen usbfs")
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	list := exec.CommandContext(ctx, adb, "devices", "-l")
	list.WaitDelay = time.Second
	output, err := list.CombinedOutput()
	if ctx.Err() != nil {
		return launch{}, fmt.Errorf("ADB device check: %w", ctx.Err())
	}
	if err != nil {
		return launch{}, fmt.Errorf("adb devices: %w %s", err, strings.Join(strings.Fields(string(output)), " "))
	}
	phone, err := selectADB(parseADB(string(output)), target.USB)
	if err != nil {
		return launch{}, err
	}
	model := strings.ReplaceAll(phone.model, "_", " ")
	if model == "" {
		model = target.Name
	}
	cmd := exec.Command(scrcpy, opts.args(phone.serial, model)...)
	// Use the same ADB client for preflight and scrcpy to avoid a daemon restart.
	cmd.Env = append(cmd.Environ(), "ADB="+adb)
	return launch{serial: phone.serial, cmd: cmd}, nil
}

type adbDevice struct {
	serial, state, model, usb string
}

func parseADB(output string) []adbDevice {
	var devices []adbDevice
	_, body, _ := strings.Cut(output, "List of devices attached")
	for _, line := range strings.Split(body, "\n") {
		serial, details, tab := strings.Cut(strings.TrimSpace(line), "\t")
		if !tab {
			serial, details, _ = strings.Cut(serial, " ")
		}
		fields := strings.Fields(details)
		if serial == "" || len(fields) == 0 || strings.HasPrefix(serial, "*") {
			continue
		}
		d := adbDevice{serial: strings.TrimSpace(serial), state: fields[0]}
		if len(fields) > 1 && fields[0] == "no" && fields[1] == "permissions" {
			d.state = "no permissions"
		}
		for _, field := range fields[1:] {
			key, value, _ := strings.Cut(field, ":")
			switch key {
			case "model":
				d.model = value
			case "usb":
				d.usb = value
			}
		}
		devices = append(devices, d)
	}
	return devices
}

func selectADB(devices []adbDevice, usb device.USBIdentity) (adbDevice, error) {
	var match adbDevice
	for _, d := range devices {
		sameSerial := usb.Serial != "" && d.serial == usb.Serial
		samePort := usb.Path != "" && d.usb == usb.Path
		if (samePort && (usb.Serial == "" || sameSerial || d.state == "no permissions")) || (d.usb == "" && sameSerial) {
			if match.serial != "" {
				return adbDevice{}, errors.New("multiple ADB transports match the mounted device. Disconnect the extra ADB connection")
			}
			match = d
		}
	}
	switch match.state {
	case "device":
		for _, d := range devices {
			if d.serial == match.serial && d != match {
				return adbDevice{}, errors.New("the ADB serial is shared by multiple devices. Disconnect the extra ADB connection")
			}
		}
		return match, nil
	case "unauthorized", "authorizing":
		return adbDevice{}, errors.New("unlock the phone and accept the Allow USB debugging dialog, then press m")
	case "offline":
		return adbDevice{}, errors.New("ADB device is offline. Reconnect the USB cable and try again")
	case "no permissions":
		return adbDevice{}, errors.New("ADB cannot access this phone. Check the USB udev rules and your user permissions")
	case "":
		return adbDevice{}, errors.New("mounted phone is missing from ADB. Enable USB debugging and reconnect the USB cable")
	default:
		return adbDevice{}, fmt.Errorf("ADB device is %s. Boot Android normally and try again", match.state)
	}
}
