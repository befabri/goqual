package coverage

import (
	"strings"
	"testing"
)

func TestParseProfileAndCoverageForRange(t *testing.T) {
	profile, err := ParseProfile(strings.NewReader(`mode: set
github.com/example/project/internal/foo/foo.go:3.1,5.2 2 1
github.com/example/project/internal/foo/foo.go:6.1,8.2 3 0
`))
	if err != nil {
		t.Fatal(err)
	}
	coverage := CoverageForRange(profile, "internal/foo/foo.go", 3, 8)
	if coverage == nil {
		t.Fatal("CoverageForRange() returned nil")
	}
	if *coverage != 40 {
		t.Fatalf("coverage = %.1f, want 40.0", *coverage)
	}
	if !Covered(profile, "internal/foo/foo.go", 4) {
		t.Fatal("line 4 should be covered")
	}
	if Covered(profile, "internal/foo/foo.go", 7) {
		t.Fatal("line 7 should not be covered")
	}
}

func TestCoverageCommand(t *testing.T) {
	got := CoverageCommand("go test ./internal/app {coverprofile} -run TestApp", "target/coverage/coverage.out")
	want := "go test ./internal/app -coverprofile=target/coverage/coverage.out -run TestApp"
	if got != want {
		t.Fatalf("CoverageCommand() = %q, want %q", got, want)
	}
}
