package manifest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/befabri/goqual/internal/mutations"
)

func TestStripEmbedded(t *testing.T) {
	content := `package sample

func ok() {}

// mutate4go-manifest-begin
// {"version":1}
// mutate4go-manifest-end
`
	got := StripEmbedded(content)
	want := "package sample\n\nfunc ok() {}\n"
	if got != want {
		t.Fatalf("StripEmbedded() = %q, want %q", got, want)
	}
}

func TestStoreSaveLoad(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	source := "internal/foo/foo.go"
	manifest := Build([]mutations.Function{{
		ID:        "func/Example",
		Name:      "Example",
		StartLine: 3,
		EndLine:   5,
		Text:      "func Example() { return }",
	}}, source, "package foo\n", time.Unix(123, 0).UTC())

	if err := store.Save(source, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Path(source)); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := store.Load(source)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Load() did not find saved manifest")
	}
	if loaded.SourcePath != filepath.ToSlash(source) {
		t.Fatalf("SourcePath = %q, want %q", loaded.SourcePath, filepath.ToSlash(source))
	}
	if len(loaded.Functions) != 1 || loaded.Functions[0].ID != "func/Example" {
		t.Fatalf("unexpected functions: %#v", loaded.Functions)
	}
}
