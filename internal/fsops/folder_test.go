package fsops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFolderCopyBothDirections(t *testing.T) {
	for _, tc := range []struct {
		name     string
		download bool
	}{{"upload", false}, {"download", true}} {
		t.Run(tc.name, func(t *testing.T) {
			source, target := testRoot(t), testRoot(t)
			for _, path := range []string{"nested", "nested/empty"} {
				if err := source.Mkdir(path, 0755); err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range []string{"top.txt", "nested/file.txt"} {
				if err := source.WriteFile(path, []byte(path), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(filepath.Join(source.Name(), "nested"), filepath.Join(source.Name(), "link")); err != nil {
				t.Fatal(err)
			}
			plan, err := PlanFolder(source)
			if err != nil || plan.Files != 2 || plan.Skipped != 1 {
				t.Fatalf("plan = %+v, %v", plan, err)
			}
			var last int64
			progress := func(count, total int64) {
				if count < last || total != 2 {
					t.Fatalf("progress = %d/%d after %d", count, total, last)
				}
				last = count
			}
			report := CopyFolder(source, target, "tree", plan, tc.download, progress)
			if report.Copied != 2 || report.Skipped != 1 || report.Failed != 0 || last != 2 {
				t.Fatalf("report = %+v, last progress = %d", report, last)
			}
			if info, err := target.Stat("tree/nested/empty"); err != nil || !info.IsDir() {
				t.Fatalf("empty subtree missing: %v", err)
			}
			if _, err := target.Lstat("tree/link"); !os.IsNotExist(err) {
				t.Fatalf("symlink was copied: %v", err)
			}
			if err := target.WriteFile("tree/top.txt", []byte("keep"), 0644); err != nil {
				t.Fatal(err)
			}
			report = CopyFolder(source, target, "tree", plan, tc.download, progress)
			if report.Copied != 0 || report.Skipped == 0 || report.Failed != 0 {
				t.Fatalf("collision report = %+v", report)
			}
			for path, want := range map[string]string{"tree/top.txt": "keep", "tree/nested/file.txt": "nested/file.txt"} {
				if got, err := target.ReadFile(path); err != nil || string(got) != want {
					t.Fatalf("%s = %q, %v", path, got, err)
				}
			}
		})
	}
}

func testRoot(t *testing.T) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}
