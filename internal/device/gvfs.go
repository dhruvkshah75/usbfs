package device

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type MountOwner uint8

const (
	MountOwnerManaged MountOwner = iota
	MountOwnerExternalGVfs
)

type gvfsMount struct {
	uri, path, name string
	usb             USBIdentity
}
type mtpVolume struct{ node, uri, name string }

var gioMountURI = regexp.MustCompile(` -> (mtp://\S+)$`)

func parseGVFS(list string) ([]mtpVolume, map[string]bool) {
	var volumes []mtpVolume
	mounts := make(map[string]bool)
	var volume *mtpVolume
	var mtp bool
	flush := func() {
		if volume != nil && mtp && volume.uri != "" {
			volumes = append(volumes, *volume)
		}
	}
	for _, line := range strings.Split(list, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Volume("):
			flush()
			_, name, _ := strings.Cut(line, "): ")
			volume, mtp = &mtpVolume{name: name}, false
		case strings.HasPrefix(line, "Drive("):
			flush()
			volume, mtp = nil, false
		case strings.HasPrefix(line, "Mount("):
			flush()
			volume, mtp = nil, false
			if match := gioMountURI.FindStringSubmatch(line); match != nil {
				mounts[strings.TrimRight(match[1], "/")] = true
			}
		case volume != nil && (line == "Type: GProxyVolume (GProxyVolumeMonitorMTP)" || line == "Type: GProxyVolumeMonitorMTP"):
			mtp = true
		case volume != nil && strings.HasPrefix(line, "unix-device: "):
			volume.node = strings.Trim(strings.TrimPrefix(line, "unix-device: "), "'")
		case volume != nil && strings.HasPrefix(line, "activation_root=mtp://"):
			volume.uri = strings.TrimRight(strings.TrimPrefix(line, "activation_root="), "/")
		}
	}
	flush()
	return volumes, mounts
}

func usbNode(node string) (string, string, bool) {
	bus, dev, ok := strings.Cut(strings.TrimPrefix(node, "/dev/bus/usb/"), "/")
	b, e1 := strconv.Atoi(bus)
	d, e2 := strconv.Atoi(dev)
	return bus, dev, ok && strings.HasPrefix(node, "/dev/bus/usb/") && e1 == nil && e2 == nil && b > 0 && d > 0
}

func mtpHost(uri string) string {
	// Legacy GVfs uses a bracketed usb:BBB,DDD authority.
	return strings.TrimPrefix(strings.TrimRight(uri, "/"), "mtp://")
}

func parseGVFSPath(info string) string {
	for _, line := range strings.Split(info, "\n") {
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), "local path: "); ok && filepath.IsAbs(path) {
			return filepath.Clean(path)
		}
	}
	return ""
}

func gioQuery(ctx context.Context, args ...string) string {
	cmd := exec.CommandContext(ctx, "gio", args...)
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func matchesGVFS(ctx context.Context, target Device, volume mtpVolume) bool {
	bus, dev, hasNode := usbNode(volume.node)
	switch target.driver {
	case "":
		return hasNode && usbIdentity(usbSysfs, bus, dev).Path != ""
	case "jmtpfs":
		address := strings.TrimPrefix(target.args[0], "-device=")
		if hasNode {
			b, _ := strconv.Atoi(bus)
			d, _ := strconv.Atoi(dev)
			return address == strconv.Itoa(b)+","+strconv.Itoa(d)
		}
		b, d, _ := strings.Cut(address, ",")
		host := mtpHost(volume.uri)
		bn, _ := strconv.Atoi(b)
		dn, _ := strconv.Atoi(d)
		legacy := fmt.Sprintf("[usb:%03d,%03d]", bn, dn)
		if host == legacy {
			return true
		}
		return target.USB.Serial != "" && (host == target.USB.Serial || strings.HasSuffix(host, "_"+target.USB.Serial))
	case "simple-mtpfs":
		if hasNode {
			out, _ := exec.CommandContext(ctx, target.driver, "-l", volume.node).Output()
			id, _, ok := parseMTP(string(out))
			return ok && id == target.args[1]
		}
	}
	return false
}

func resolveGVFS(target Device) gvfsMount {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	volumes, mounted := parseGVFS(gioQuery(ctx, "mount", "-li"))
	var match gvfsMount
	for _, volume := range volumes {
		if !mounted[volume.uri] || !matchesGVFS(ctx, target, volume) {
			continue
		}
		if match.uri != "" {
			return gvfsMount{}
		}
		host := mtpHost(volume.uri)
		decoded, err := url.PathUnescape(host)
		if err != nil || !filepath.IsLocal(decoded) || decoded != filepath.Base(decoded) {
			continue
		}
		path := parseGVFSPath(gioQuery(ctx, "info", "--", volume.uri+"/"))
		runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
		if !filepath.IsAbs(runtimeDir) {
			runtimeDir = filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
		}
		base := filepath.Join(runtimeDir, "gvfs")
		if path == "" {
			for _, candidate := range []string{decoded, host, strings.ReplaceAll(url.QueryEscape(decoded), "+", "%20")} {
				path = filepath.Join(base, "mtp:host="+candidate)
				if _, err := os.Lstat(path); err == nil {
					break
				}
			}
		}
		entryHost, ok := strings.CutPrefix(filepath.Base(path), "mtp:host=")
		entryDecoded, err := url.PathUnescape(entryHost)
		if filepath.Dir(path) == base && ok && (entryHost == decoded || (err == nil && entryDecoded == decoded)) {
			bus, dev, _ := usbNode(volume.node)
			match = gvfsMount{volume.uri, path, volume.name, usbIdentity(usbSysfs, bus, dev)}
		}
	}
	return match
}

func (s *Session) openGVFS(m gvfsMount) (*os.Root, error) {
	base := filepath.Dir(m.path)
	mounts, err := s.inspect(base)
	if err != nil {
		return nil, err
	}
	for _, mount := range mounts {
		if mount.path == base && mount.kind == "fuse.gvfsd-fuse" {
			before, err := os.Lstat(m.path)
			if err != nil || !before.IsDir() {
				return nil, ErrUnmounted
			}
			root, err := os.OpenRoot(m.path)
			if errors.Is(err, os.ErrNotExist) {
				err = ErrUnmounted
			}
			if err != nil {
				return nil, err
			}
			after, err := root.Stat(".")
			if err != nil || !os.SameFile(before, after) {
				root.Close()
				return nil, ErrUnmounted
			}
			return root, nil
		}
	}
	return nil, ErrUnmounted
}

// GVFS returns the externally owned mount's transfer location. Call inside Work.
func (s *Session) GVFS() (path, uri string) { return s.gvfs.path, s.gvfs.uri }
