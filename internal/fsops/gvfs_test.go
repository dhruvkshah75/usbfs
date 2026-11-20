package fsops

import (
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGVFSNoOverwrite(t *testing.T) {
	if _, err := exec.LookPath("gio"); err != nil {
		t.Skip("gio is not installed")
	}
	host, phone := testRoot(t), testRoot(t)
	g := GVFS{Path: phone.Name(), URI: (&url.URL{Scheme: "file", Path: phone.Name()}).String()}
	write := func(root *os.Root, path, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root.Name(), path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	check := func(root *os.Root, path, want string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root.Name(), path))
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q, %v, want %q", path, data, err, want)
		}
	}
	progress := func(int64, int64) {}
	write(host, "existing.txt", "host")
	write(phone, "existing.txt", "phone")
	if err := g.Upload(phone, filepath.Join(host.Name(), "existing.txt"), ".", progress); !errors.Is(err, os.ErrExist) {
		t.Fatalf("upload conflict: %v", err)
	}
	if err := g.download(phone, host, "existing.txt", "existing.txt", 5, progress); !errors.Is(err, os.ErrExist) {
		t.Fatalf("download conflict: %v", err)
	}
	if err := gioCopy(filepath.Join(host.Name(), "existing.txt"), filepath.Join(phone.Name(), "existing.txt")); !errors.Is(err, os.ErrExist) {
		t.Fatalf("GIO collision after precheck: %v", err)
	}
	check(phone, "existing.txt", "phone")
	check(host, "existing.txt", "host")
	write(host, "new #.txt", "new")
	if err := g.Upload(phone, filepath.Join(host.Name(), "new #.txt"), ".", progress); err != nil {
		t.Fatal(err)
	}
	check(phone, "new #.txt", "new")
	download := testRoot(t)
	if err := g.download(phone, download, "new #.txt", "new #.txt", 3, progress); err != nil {
		t.Fatal(err)
	}
	check(download, "new #.txt", "new")

	source := testRoot(t)
	write(source, "file.txt", "source")
	plan, err := PlanFolder(source)
	if err != nil {
		t.Fatal(err)
	}
	added := false
	report := g.CopyFolder(source, phone, "tree", plan, false, func(_, _ int64) {
		if !added {
			added = true
			write(phone, "tree/file.txt", "another app")
		}
	})
	if report.Skipped != 1 || report.Copied != 0 || report.Failed != 0 {
		t.Fatalf("folder copy report = %+v", report)
	}
	check(phone, "tree/file.txt", "another app")
}

func TestGVFSListDefersAccessChecks(t *testing.T) {
	root := testRoot(t)
	if err := root.Mkdir("folder", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root.Name(), "file"), nil, 0000); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink("missing", "broken"); err != nil {
		t.Fatal(err)
	}
	entries, err := (GVFS{URI: "mtp://phone"}).List(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || !entries[0].Folder || !entries[1].Symlink ||
		!entries[1].Locked || entries[2].Name != "file" || entries[2].Locked {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	managed, err := List(root, ".")
	if err != nil || len(managed) != 3 || !managed[2].Locked {
		t.Fatalf("managed listing lost permission metadata: %+v, %v", managed, err)
	}
}
