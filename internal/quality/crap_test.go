package quality

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareCoverageDirWithFileInWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("keep.txt", []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("coverage.out", []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := prepareCoverageDir("coverage.out"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.txt")); err != nil {
		t.Fatal("prepareCoverageDir removed unrelated working-directory file")
	}
	if _, err := os.Stat(filepath.Join(dir, "coverage.out")); !os.IsNotExist(err) {
		t.Fatalf("coverage.out still exists or unexpected stat error: %v", err)
	}
}
