package fsops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUploadCopiesNewFileAndPreservesConflicts(t *testing.T) {
	rootDir, host := t.TempDir(), t.TempDir()
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	name := "notes.txt"
	src := filepath.Join(host, name)
	data := []byte(strings.Repeat("usbfs\n", 100_000))
	if err := os.WriteFile(src, data, 0600); err != nil {
		t.Fatal(err)
	}
	var last int64
	report := func(done, total int64) {
		if done < last || total != int64(len(data)) {
			t.Fatalf("invalid progress %d/%d after %d", done, total, last)
		}
		last = done
	}
	if err := Upload(root, src, ".", report); err != nil {
		t.Fatal(err)
	}
	if last != int64(len(data)) {
		t.Fatalf("last progress = %d", last)
	}
	if err := os.WriteFile(src, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Upload(root, src, ".", report); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite returned %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(rootDir, name)); err != nil || string(got) != string(data) {
		t.Fatalf("original content was not preserved, read error: %v", err)
	}
	if err := os.Symlink(host, filepath.Join(rootDir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := Upload(root, src, "escape", report); err == nil {
		t.Fatal("upload accepted a destination outside the device root")
	}
}

func TestDownloadRejectsDeviceAlias(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("device"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	err = Download(root, "file", alias, func(int64, int64) { t.Error("attempted to copy onto the device") })
	if err == nil || !strings.Contains(err.Error(), "outside the external device") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDownloadPublishesOnlyCompleteFile(t *testing.T) {
	host := t.TempDir()
	root, err := os.OpenRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	const name = "transfer_test.go"
	destination := filepath.Join(host, name)
	original, err := root.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	err = Download(root, name, host, func(int64, int64) {
		if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("destination became visible during the copy: %v", err)
		}
	})
	if err != nil && strings.Contains(err.Error(), "outside the external device") {
		t.Skip("download source and temporary directory are on the same filesystem")
	}
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != string(original) {
		t.Fatalf("download differs from source, read error: %v", err)
	}
	if err := Download(root, name, host, func(int64, int64) {}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("download collision returned %v", err)
	}
	items, err := os.ReadDir(host)
	if err != nil || len(items) != 1 {
		t.Fatalf("download left temporary files: %v, %v", items, err)
	}
}
