package device

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Device struct {
	Kind, Name string
	driver     string
	args       []string
}

type Platform string

const (
	Android Platform = "Android"
	Apple   Platform = "iPhone / iPad"
)

func installed(command string) bool {
	_, err := exec.LookPath(command)
	return err == nil
}

func unmountHelpers() []string {
	var helpers []string
	for _, name := range []string{"fusermount3", "fusermount"} {
		if path, err := exec.LookPath(name); err == nil {
			helpers = append(helpers, path)
		}
	}
	return helpers
}

func mountDriver(platform Platform) (string, error) {
	var packages []string
	var driver string
	switch platform {
	case Android:
		switch {
		case installed("simple-mtpfs"):
			driver = "simple-mtpfs"
		case installed("jmtpfs"):
			driver = "jmtpfs"
		default:
			packages = append(packages, "jmtpfs")
		}
	case Apple:
		driver = "ifuse"
		if !installed("ifuse") {
			packages = append(packages, "ifuse")
		}
		if !installed("idevice_id") {
			packages = append(packages, "libimobiledevice-utils")
		}
	default:
		return "", errors.New("choose a device type first")
	}
	if len(unmountHelpers()) == 0 {
		packages = append(packages, "fuse3")
	}
	if len(packages) != 0 {
		return "", fmt.Errorf("missing %s tools. Ubuntu: sudo apt install %s. Then press r to retry", platform, strings.Join(packages, " "))
	}
	return driver, nil
}

func command(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var diagnostics strings.Builder
	cmd.Stderr = &diagnostics
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		err = fmt.Errorf("%s: %w %s", name, err, strings.TrimSpace(diagnostics.String()))
	}
	return strings.TrimSpace(string(out)), err
}

func find(driver string) (Device, error) {
	if driver == "ifuse" {
		ids, err := command("idevice_id", "-l")
		id, _, _ := strings.Cut(ids, "\n")
		id = strings.TrimSpace(id)
		if err != nil || id == "" || strings.HasPrefix(id, "-") {
			return Device{}, err
		}
		name, err := command("idevice_id", id)
		if err != nil || name == "" {
			name = "Apple device"
		}
		return Device{"iOS/iPadOS", name, driver, []string{"--udid", id}}, nil
	}
	listing, err := command(driver, "-l")
	if err != nil {
		if driver == "simple-mtpfs" && strings.Contains(err.Error(), "No raw devices found.") {
			return Device{}, nil
		}
		return Device{}, err
	}
	if driver == "jmtpfs" {
		if id, name, ok := parseJMTP(listing); ok {
			return Device{"Android MTP", name, driver, []string{"-device=" + id}}, nil
		}
	} else if id, name, ok := parseMTP(listing); ok {
		return Device{"Android MTP", name, driver, []string{"--device", id}}, nil
	}
	return Device{}, nil
}

func parseMTP(listing string) (id, name string, ok bool) {
	for _, line := range strings.Split(listing, "\n") {
		number, label, found := strings.Cut(strings.TrimSpace(line), ":")
		index, err := strconv.Atoi(number)
		if !found || err != nil || index < 1 {
			continue
		}
		label = strings.TrimSpace(label)
		if label == "" {
			label = "MTP device"
		}
		return strconv.Itoa(index), label, true
	}
	return "", "", false
}

func parseJMTP(listing string) (id, name string, ok bool) {
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.SplitN(line, ",", 6)
		if len(fields) != 6 {
			continue
		}
		bus := strings.TrimSpace(fields[0])
		dev := strings.TrimSpace(fields[1])
		_, busErr := strconv.ParseUint(bus, 10, 32)
		_, devErr := strconv.ParseUint(dev, 10, 8)
		if busErr != nil || devErr != nil {
			continue
		}
		name := strings.TrimSpace(fields[4])
		if name == "" {
			name = "MTP device"
		}
		return bus + "," + dev, name, true
	}
	return "", "", false
}
