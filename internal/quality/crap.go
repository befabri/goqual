package quality

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/befabri/goqual/internal/complexity"
	"github.com/befabri/goqual/internal/coverage"
	"github.com/befabri/goqual/internal/crap"
)

const defaultCoverageProfile = "target/coverage/coverage.out"
const defaultTestCommand = "go test ./..."

const crapHelp = `Usage: goqual crap [module-filter ...] [options]

Runs Go coverage, computes CRAP scores, and prints a report sorted worst first.

Options:
  -h, --help                 Print this help message and exit
  --coverage-profile PATH    Coverage profile path (default target/coverage/coverage.out)
  --reuse-coverage           Reuse the existing coverage profile
  --max-workers N            Analyze files in parallel (default half logical CPUs)
  --test-command COMMAND     Test command for coverage (default "go test ./...")
                             If COMMAND contains {coverprofile}, it is replaced.

Arguments:
  module-filter              Optional source path fragment. When present, only
                             matching .go source files are analyzed.`

type crapOptions struct {
	Filters         []string
	CoverageProfile string
	ReuseCoverage   bool
	MaxWorkers      int
	TestCommand     string
}

func runCRAP(args []string) (int, error) {
	options, ok, err := parseCRAPArgs(args)
	if err != nil {
		return 1, err
	}
	if !ok {
		fmt.Println(crapHelp)
		return 0, nil
	}
	if err := runCRAPReport(options); err != nil {
		return 1, err
	}
	return 0, nil
}

func parseCRAPArgs(args []string) (crapOptions, bool, error) {
	options := crapOptions{
		CoverageProfile: defaultCoverageProfile,
		MaxWorkers:      defaultMaxWorkers(),
		TestCommand:     defaultTestCommand,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-h", "--help":
			return options, false, nil
		case "--reuse-coverage":
			options.ReuseCoverage = true
		case "--coverage-profile", "--max-workers", "--test-command":
			if i+1 >= len(args) {
				return options, true, fmt.Errorf("%s requires a value", arg)
			}
			i++
			value := args[i]
			switch arg {
			case "--coverage-profile":
				if strings.TrimSpace(value) == "" {
					return options, true, fmt.Errorf("--coverage-profile requires a path")
				}
				options.CoverageProfile = value
			case "--max-workers":
				workers, err := strconv.Atoi(value)
				if err != nil || workers < 1 {
					return options, true, fmt.Errorf("--max-workers requires a positive integer")
				}
				options.MaxWorkers = workers
			case "--test-command":
				if strings.TrimSpace(value) == "" {
					return options, true, fmt.Errorf("--test-command requires a command")
				}
				options.TestCommand = value
			}
		default:
			if strings.HasPrefix(arg, "--") {
				return options, true, fmt.Errorf("unknown option: %s", arg)
			}
			options.Filters = append(options.Filters, arg)
		}
	}
	return options, true, nil
}

func runCRAPReport(options crapOptions) error {
	if !options.ReuseCoverage {
		if err := prepareCoverageDir(options.CoverageProfile); err != nil {
			return err
		}
		if err := runCoverage(options.TestCommand, options.CoverageProfile); err != nil {
			return err
		}
	}
	profile, err := coverage.LoadProfile(options.CoverageProfile)
	if err != nil {
		return err
	}
	if profile == nil {
		return fmt.Errorf("coverage profile not found: %s", options.CoverageProfile)
	}
	entries, err := sortedCRAPEntries(options, profile)
	if err != nil {
		return err
	}
	fmt.Print(crap.FormatReport(entries))
	return nil
}

func prepareCoverageDir(profile string) error {
	dir := filepath.Dir(profile)
	if dir == "." || dir == "" {
		err := os.Remove(profile)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o755)
}

func runCoverage(testCommand, coverageProfile string) error {
	cmd := exec.Command("sh", "-c", coverage.CoverageCommand(testCommand, coverageProfile))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func sortedCRAPEntries(options crapOptions, profile map[string][]coverage.Segment) ([]crap.Entry, error) {
	files, err := findSourceFiles(".")
	if err != nil {
		return nil, err
	}
	files = filterSourceFiles(files, options.Filters)
	if options.MaxWorkers <= 1 || len(files) <= 1 {
		return sortedCRAPEntriesSerial(files, profile)
	}
	return sortedCRAPEntriesParallel(files, profile, options.MaxWorkers)
}

func sortedCRAPEntriesSerial(files []string, profile map[string][]coverage.Segment) ([]crap.Entry, error) {
	var entries []crap.Entry
	for _, file := range files {
		fileEntries, err := entriesForFile(file, profile)
		if err != nil {
			return nil, err
		}
		entries = append(entries, fileEntries...)
	}
	return crap.SortByCRAP(entries), nil
}

func sortedCRAPEntriesParallel(files []string, profile map[string][]coverage.Segment, maxWorkers int) ([]crap.Entry, error) {
	if maxWorkers > len(files) {
		maxWorkers = len(files)
	}
	type job struct {
		index int
		file  string
	}
	type result struct {
		index   int
		entries []crap.Entry
		err     error
	}

	jobs := make(chan job, len(files))
	results := make(chan result, len(files))
	var wg sync.WaitGroup
	for i := 0; i < maxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				entries, err := entriesForFile(job.file, profile)
				results <- result{index: job.index, entries: entries, err: err}
			}
		}()
	}
	for i, file := range files {
		jobs <- job{index: i, file: file}
	}
	close(jobs)
	wg.Wait()
	close(results)

	byFile := make([][]crap.Entry, len(files))
	for result := range results {
		if result.err != nil {
			return nil, result.err
		}
		byFile[result.index] = result.entries
	}
	var entries []crap.Entry
	for _, fileEntries := range byFile {
		entries = append(entries, fileEntries...)
	}
	return crap.SortByCRAP(entries), nil
}

func entriesForFile(file string, profile map[string][]coverage.Segment) ([]crap.Entry, error) {
	functions, err := complexity.ExtractFunctions(file)
	if err != nil {
		return nil, err
	}
	var entries []crap.Entry
	for _, fn := range functions {
		cov := coverage.CoverageForRange(profile, file, fn.StartLine, fn.EndLine)
		entries = append(entries, crap.Entry{
			Name:       fn.Name,
			Package:    fn.Package,
			Complexity: fn.Complexity,
			Coverage:   cov,
			CRAP:       crap.Score(fn.Complexity, cov),
		})
	}
	return entries, nil
}

func findSourceFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".reference", "reference", "target", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".go") && !strings.HasSuffix(entry.Name(), "_test.go") {
			files = append(files, filepath.ToSlash(path))
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func filterSourceFiles(files []string, filters []string) []string {
	if len(filters) == 0 {
		return files
	}
	var out []string
	for _, file := range files {
		for _, filter := range filters {
			if strings.Contains(file, filter) {
				out = append(out, file)
				break
			}
		}
	}
	return out
}

func defaultMaxWorkers() int {
	workers := runtime.NumCPU() / 2
	if workers < 1 {
		return 1
	}
	return workers
}
