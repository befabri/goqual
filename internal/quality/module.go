package quality

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func moduleRoot() (string, string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	cmd := exec.Command("go", "env", "GOMOD")
	cmd.Dir = wd
	out, err := cmd.Output()
	if err == nil {
		gomod := strings.TrimSpace(string(out))
		if gomod != "" && gomod != os.DevNull {
			return filepath.Dir(gomod), wd, nil
		}
	}
	root, err := findGoModUpward(wd)
	if err != nil {
		return "", "", err
	}
	return root, wd, nil
}

func findGoModUpward(start string) (string, error) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		next := filepath.Dir(dir)
		if next == dir {
			return start, nil
		}
		dir = next
	}
}

func chdirTemporarily(dir string) (func() error, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if err := os.Chdir(dir); err != nil {
		return nil, err
	}
	return func() error {
		return os.Chdir(wd)
	}, nil
}

func resolveModulePath(path, root, workDir string) (string, string, error) {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(workDir, path)
	}
	abs, err := filepath.Abs(abs)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", "", errOutsideModule(path)
	}
	return filepath.ToSlash(rel), abs, nil
}

func resolveOptionalPath(path, root, workDir string, userProvided bool) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if userProvided {
		return filepath.Clean(filepath.Join(workDir, path))
	}
	return filepath.ToSlash(filepath.Clean(filepath.Join(root, path)))
}

func normalizeFilter(filter, root, workDir string) string {
	if filepath.IsAbs(filter) || filter == "." || strings.HasPrefix(filter, "."+string(os.PathSeparator)) ||
		strings.HasPrefix(filter, ".."+string(os.PathSeparator)) {
		if rel, _, err := resolveModulePath(filter, root, workDir); err == nil {
			return rel
		}
	}
	if strings.ContainsAny(filter, `/\`) {
		rootRelative := filepath.Join(root, filter)
		if _, err := os.Stat(rootRelative); err == nil {
			if rel, _, err := resolveModulePath(rootRelative, root, workDir); err == nil {
				return rel
			}
		}
		workRelative := filepath.Join(workDir, filter)
		if _, err := os.Stat(workRelative); err == nil {
			if rel, _, err := resolveModulePath(workRelative, root, workDir); err == nil {
				return rel
			}
		}
	}
	return filepath.ToSlash(filter)
}

type outsideModuleError struct {
	path string
}

func errOutsideModule(path string) error {
	return outsideModuleError{path: path}
}

func (e outsideModuleError) Error() string {
	return "path is outside the Go module: " + e.path
}
