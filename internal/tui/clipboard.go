package tui

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const maxClipboardImage = 32 << 20

type clipboardResult struct {
	index int
	data  []byte
	ext   string
	err   error
}

func (m *Model) pasteImage() tea.Cmd {
	m.busy = true
	index := m.focus
	return func() tea.Msg {
		data, ext, err := readClipboardImage()
		return clipboardResult{index: index, data: data, ext: ext, err: err}
	}
}

func readClipboardImage() ([]byte, string, error) {
	var commands [][]string
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		commands = append(commands, []string{"wl-paste", "--no-newline", "--type"})
	}
	if os.Getenv("DISPLAY") != "" {
		commands = append(commands, []string{"xclip", "-o", "-selection", "clipboard", "-t"})
	}
	if len(commands) == 0 {
		return nil, "", errors.New("Image paste requires a local Wayland or X11 clipboard.")
	}
	available := false
	for _, command := range commands {
		if _, err := exec.LookPath(command[0]); err != nil {
			continue
		}
		available = true
		for _, mime := range []string{"image/png", "image/jpeg"} {
			data, err := clipboardBytes(command, mime)
			if len(data) > maxClipboardImage {
				return nil, "", errors.New("Clipboard image exceeds 32 MiB.")
			}
			if err == nil {
				_, format, err := image.DecodeConfig(bytes.NewReader(data))
				if err == nil {
					if format == "jpeg" {
						format = "jpg"
					}
					return data, "." + format, nil
				}
			}
		}
	}
	if !available {
		return nil, "", errors.New("Install wl-clipboard (Wayland) or xclip (X11) to paste images.")
	}
	return nil, "", errors.New("Could not read a PNG or JPEG image from the clipboard.")
}

func clipboardBytes(command []string, mime string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	args := append(append([]string(nil), command[1:]...), mime)
	cmd := exec.CommandContext(ctx, command[0], args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(stdout, maxClipboardImage+1))
	if err != nil || len(data) > maxClipboardImage {
		cancel()
	}
	return data, errors.Join(err, cmd.Wait())
}
