package quality

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveExistingModulePathPrefersCurrentDirectoryThenModuleRoot(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "internal", "pkg")
	writeFile(t, filepath.Join(workDir, "foo.go"), "package pkg\n")
	writeFile(t, filepath.Join(root, "internal", "app", "app.go"), "package app\n")

	rel, abs, err := resolveExistingModulePath("foo.go", root, workDir)
	if err != nil {
		t.Fatal(err)
	}
	if rel != "internal/pkg/foo.go" {
		t.Fatalf("cwd-relative rel = %q", rel)
	}
	if abs != filepath.Join(workDir, "foo.go") {
		t.Fatalf("cwd-relative abs = %q", abs)
	}

	rel, abs, err = resolveExistingModulePath("internal/app/app.go", root, workDir)
	if err != nil {
		t.Fatal(err)
	}
	if rel != "internal/app/app.go" {
		t.Fatalf("module-root-relative rel = %q", rel)
	}
	if abs != filepath.Join(root, "internal", "app", "app.go") {
		t.Fatalf("module-root-relative abs = %q", abs)
	}
}

func TestMutateStrictFromSubdirectoryUsesModuleRoot(t *testing.T) {
	root, sourcePath, source := writeMutationFixture(t)
	subdir := filepath.Join(root, "internal", "pkg")
	restore := chdirForTest(t, subdir)
	defer restore()

	var code int
	var err error
	out := captureStdout(t, func() {
		code, err = Run([]string{"mutate", "foo.go", "--strict", "--mutation-warning", "99"})
	})

	assertStrictMutationRun(t, code, err, out, sourcePath, source)
}

func TestMutateAcceptsModuleRootRelativeSourceFromSubdirectory(t *testing.T) {
	root, sourcePath, source := writeMutationFixture(t)
	subdir := filepath.Join(root, "internal", "pkg")
	restore := chdirForTest(t, subdir)
	defer restore()

	var code int
	var err error
	out := captureStdout(t, func() {
		code, err = Run([]string{"mutate", "internal/pkg/foo.go", "--strict", "--mutation-warning", "99"})
	})

	assertStrictMutationRun(t, code, err, out, sourcePath, source)
}

func writeMutationFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/subdir\n\ngo 1.26\n")
	sourcePath := filepath.Join(root, "internal", "pkg", "foo.go")
	source := `package pkg

func AddZero(v int) int {
	return v + 0
}
`
	writeFile(t, sourcePath, source)
	writeFile(t, filepath.Join(root, "internal", "pkg", "foo_test.go"), `package pkg

import "testing"

func TestAddZero(t *testing.T) {
	if AddZero(7) != 7 {
		t.Fatal("bad result")
	}
}
`)
	return root, sourcePath, source
}

func assertStrictMutationRun(t *testing.T, code int, err error, out, sourcePath, source string) {
	t.Helper()
	if code == 0 {
		t.Fatalf("Run() code = 0, want non-zero; output:\n%s", out)
	}
	if err == nil || !strings.Contains(err.Error(), "strict mutation quality gate failed") {
		t.Fatalf("Run() error = %v, want strict gate error", err)
	}
	if !strings.Contains(out, "Mutation run: internal/pkg/foo.go") {
		t.Fatalf("output did not use module-relative source path:\n%s", out)
	}
	if !strings.Contains(out, "Survived: 1") {
		t.Fatalf("output did not report the surviving AddZero mutation:\n%s", out)
	}
	if got := readFile(t, sourcePath); got != source {
		t.Fatalf("source was not restored:\n%s", got)
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(sourcePath)))
	if _, err := os.Stat(filepath.Join(root, "target", "goqual", "manifests")); err != nil {
		t.Fatalf("sidecar manifest directory was not created at module root: %v", err)
	}
}

func TestCRAPFromSubdirectoryUsesModuleRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/crapsubdir\n\ngo 1.26\n")
	writeFile(t, filepath.Join(root, "internal", "pkg", "foo.go"), `package pkg

func Foo(v int) int {
	if v > 0 {
		return v
	}
	return 0
}
`)
	writeFile(t, filepath.Join(root, "target", "coverage", "coverage.out"), `mode: set
example.com/crapsubdir/internal/pkg/foo.go:3.1,8.2 3 1
`)
	restore := chdirForTest(t, filepath.Join(root, "internal", "pkg"))
	defer restore()

	var code int
	var err error
	out := captureStdout(t, func() {
		code, err = Run([]string{"crap", "--reuse-coverage", "."})
	})

	if code != 0 || err != nil {
		t.Fatalf("Run() code=%d err=%v output:\n%s", code, err, out)
	}
	if !strings.Contains(out, "Foo") {
		t.Fatalf("CRAP output did not include subdirectory function:\n%s", out)
	}
	if strings.Contains(out, "N/A") {
		t.Fatalf("CRAP output did not find module-root coverage profile:\n%s", out)
	}

	out = captureStdout(t, func() {
		code, err = Run([]string{"crap", "--reuse-coverage", "internal/pkg"})
	})
	if code != 0 || err != nil {
		t.Fatalf("root-relative filter code=%d err=%v output:\n%s", code, err, out)
	}
	if !strings.Contains(out, "Foo") {
		t.Fatalf("root-relative filter did not match module path:\n%s", out)
	}
}

func TestMutationGateOptions(t *testing.T) {
	summary := mutationSummary{Killed: 2, Survived: 1, Uncovered: 1}
	if err := enforceMutationGate(mutateOptions{}, summary); err != nil {
		t.Fatalf("report-only gate returned error: %v", err)
	}
	if err := enforceMutationGate(mutateOptions{FailOnSurvived: true}, summary); err == nil || !strings.Contains(err.Error(), "survived=1") {
		t.Fatalf("survived gate error = %v", err)
	}
	if err := enforceMutationGate(mutateOptions{FailOnUncovered: true}, summary); err == nil || !strings.Contains(err.Error(), "uncovered=1") {
		t.Fatalf("uncovered gate error = %v", err)
	}
	if err := enforceMutationGate(mutateOptions{FailOnSurvived: true, FailOnUncovered: true}, summary); err == nil || !strings.Contains(err.Error(), "strict mutation quality gate failed") {
		t.Fatalf("strict gate error = %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func chdirForTest(t *testing.T, dir string) func() {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatal(err)
		}
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = old
	}()
	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
