package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/befabri/goqual/internal/mutations"
)

const beginMarker = "// mutate4go-manifest-begin"
const endMarker = "// mutate4go-manifest-end"

type Manifest struct {
	Version    int        `json:"version"`
	TestedAt   string     `json:"tested_at"`
	SourcePath string     `json:"source_path,omitempty"`
	ModuleHash string     `json:"module_hash"`
	Functions  []Function `json:"functions"`
}

type Function struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line"`
	Hash    string `json:"hash"`
}

type Store struct {
	Dir string
}

func StripEmbedded(content string) string {
	start := strings.Index(content, beginMarker)
	if start < 0 {
		return content
	}
	return strings.TrimRight(content[:start], "\n") + "\n"
}

func ExtractEmbedded(content string) (*Manifest, bool) {
	start := strings.Index(content, beginMarker)
	end := strings.Index(content, endMarker)
	if start < 0 || end < 0 || end <= start {
		return nil, false
	}
	block := content[start+len(beginMarker) : end]
	var lines []string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "//")
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	var manifest Manifest
	if err := json.Unmarshal([]byte(strings.Join(lines, "\n")), &manifest); err != nil {
		return nil, false
	}
	return &manifest, true
}

func Build(functions []mutations.Function, sourcePath, content string, now time.Time) Manifest {
	out := Manifest{
		Version:    1,
		TestedAt:   now.Format(time.RFC3339),
		SourcePath: filepath.ToSlash(sourcePath),
		ModuleHash: hash(content),
	}
	for _, fn := range functions {
		out.Functions = append(out.Functions, Function{
			ID:      fn.ID,
			Name:    fn.Name,
			Line:    fn.StartLine,
			EndLine: fn.EndLine,
			Hash:    hash(normalize(fn.Text)),
		})
	}
	return out
}

func ChangedFunctionIDs(previous *Manifest, current Manifest) map[string]bool {
	changed := map[string]bool{}
	if previous == nil {
		for _, fn := range current.Functions {
			changed[fn.ID] = true
		}
		return changed
	}
	prior := map[string]string{}
	for _, fn := range previous.Functions {
		prior[fn.ID] = fn.Hash
	}
	for _, fn := range current.Functions {
		if prior[fn.ID] != fn.Hash {
			changed[fn.ID] = true
		}
	}
	return changed
}

func (s Store) Load(sourcePath string) (*Manifest, bool, error) {
	data, err := os.ReadFile(s.Path(sourcePath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, false, err
	}
	return &manifest, true, nil
}

func (s Store) Save(sourcePath string, manifest Manifest) error {
	path := s.Path(sourcePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func (s Store) Path(sourcePath string) string {
	name := filepath.ToSlash(sourcePath)
	sum := sha256.Sum256([]byte(name))
	shortHash := hex.EncodeToString(sum[:8])
	safe := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_").Replace(name)
	return filepath.Join(s.Dir, shortHash+"_"+safe+".json")
}

func (s Store) BackupPath(sourcePath string) string {
	name := filepath.ToSlash(sourcePath)
	sum := sha256.Sum256([]byte(name))
	shortHash := hex.EncodeToString(sum[:8])
	safe := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_").Replace(name)
	return filepath.Join(s.Dir, "backups", shortHash+"_"+safe+".bak")
}

func (s Store) SaveBackup(sourcePath, content string) error {
	path := s.BackupPath(sourcePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func (s Store) RestoreBackup(sourcePath string) (bool, error) {
	path := s.BackupPath(sourcePath)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if err := os.WriteFile(sourcePath, data, 0o644); err != nil {
		return false, err
	}
	return true, os.Remove(path)
}

func (s Store) CleanupBackup(sourcePath string) error {
	err := os.Remove(s.BackupPath(sourcePath))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func normalize(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
