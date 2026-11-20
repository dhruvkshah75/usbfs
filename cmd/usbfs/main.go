package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"usbfs/internal/device"
	"usbfs/internal/tui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "usbfs:", err)
		os.Exit(1)
	}
}

func run() (err error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	var session *device.Session
	defer func() {
		if cause := recover(); cause != nil {
			err = fmt.Errorf("unexpected panic: %v", cause)
			debug.PrintStack()
		}
		if session != nil && session.Close(os.Stderr) {
			fmt.Println("[OK] External device cleanly unmounted. It is now safe to unplug the USB cable.")
		}
	}()

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	// Keep the process working directory off the mount, even when launched from inside it.
	if err := os.Chdir("/"); err != nil {
		return err
	}
	session, err = device.NewSession(home)
	if err != nil {
		return err
	}

	model := tui.New(session, cwd)
	defer model.CloseMirrors()
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithoutSignalHandler())
	go func() {
		<-ctx.Done()
		program.Quit()
	}()
	_, err = program.Run()
	return err
}
