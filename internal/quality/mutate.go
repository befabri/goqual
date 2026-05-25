package quality

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/befabri/goqual/internal/coverage"
	"github.com/befabri/goqual/internal/manifest"
	"github.com/befabri/goqual/internal/mutations"
)

const defaultManifestDir = "target/goqual/manifests"

const mutateHelp = `Usage: goqual mutate <source-file.go> [options]

Discovers mutation sites, applies each covered mutation, runs tests, and reports
killed, survived, and uncovered mutations.

Options:
  --scan                  Report mutation counts without coverage or tests
  --update-manifest       Rewrite the sidecar manifest without mutation testing
  --reuse-coverage        Reuse an existing coverage profile
  --lines L1,L2,...        Run only mutations on these source lines
  --since-last-run         Run only mutations in functions changed since manifest
  --mutate-all             Run all covered mutations even when a manifest exists
  --strict                 Fail if any mutation survives or is uncovered
  --fail-on-survived       Fail if any selected mutation survives
  --fail-on-uncovered      Fail if any mutation is uncovered
  --mutation-warning N     Warn when more than N mutations are found (default 50)
  --timeout-factor N       Mutation timeout multiplier vs baseline (default 10)
  --test-command COMMAND   Test command (default "go test ./...")
  --coverage-profile PATH  Coverage profile path (default target/coverage/coverage.out)
  --manifest-dir DIR       Sidecar manifest directory (default target/goqual/manifests)
  --max-workers N          Run mutations in parallel with isolated workers
  --verbose                Log major actions to stderr
  -h, --help               Print this help message and exit

Manifests are sidecar JSON files by default. Source files are restored without
embedded mutation comments after every run.`

type mutateOptions struct {
	SourcePath      string
	Scan            bool
	UpdateManifest  bool
	ReuseCoverage   bool
	Lines           map[int]bool
	SinceLastRun    bool
	MutateAll       bool
	FailOnSurvived  bool
	FailOnUncovered bool
	MutationWarning int
	TimeoutFactor   int
	TestCommand     string
	CoverageProfile string
	coverageUser    bool
	ManifestDir     string
	manifestUser    bool
	MaxWorkers      int
	Verbose         bool
}

type mutationResult struct {
	Site     mutations.Site
	Status   string
	Duration time.Duration
}

func runMutate(args []string) (int, error) {
	root, workDir, err := moduleRoot()
	if err != nil {
		return 1, err
	}
	options, ok, err := parseMutateArgs(args, root, workDir)
	if err != nil {
		return 1, err
	}
	if !ok {
		fmt.Println(mutateHelp)
		return 0, nil
	}
	restore, err := chdirTemporarily(root)
	if err != nil {
		return 1, err
	}
	defer restore()
	if err := runMutationCommand(options); err != nil {
		return 1, err
	}
	return 0, nil
}

func parseMutateArgs(args []string, root, workDir string) (mutateOptions, bool, error) {
	options := mutateOptions{
		MutationWarning: 50,
		TimeoutFactor:   10,
		TestCommand:     defaultTestCommand,
		CoverageProfile: defaultCoverageProfile,
		ManifestDir:     defaultManifestDir,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-h", "--help":
			return options, false, nil
		case "--verbose":
			options.Verbose = true
		case "--scan":
			options.Scan = true
		case "--update-manifest":
			options.UpdateManifest = true
		case "--reuse-coverage":
			options.ReuseCoverage = true
		case "--since-last-run":
			options.SinceLastRun = true
		case "--mutate-all":
			options.MutateAll = true
		case "--strict":
			options.FailOnSurvived = true
			options.FailOnUncovered = true
		case "--fail-on-survived":
			options.FailOnSurvived = true
		case "--fail-on-uncovered":
			options.FailOnUncovered = true
		case "--lines", "--mutation-warning", "--timeout-factor", "--test-command", "--coverage-profile", "--manifest-dir", "--max-workers":
			if i+1 >= len(args) {
				return options, true, fmt.Errorf("%s requires a value", arg)
			}
			i++
			if err := consumeMutateValue(&options, arg, args[i]); err != nil {
				return options, true, err
			}
		default:
			if strings.HasPrefix(arg, "--") {
				return options, true, fmt.Errorf("unknown option: %s", arg)
			}
			if options.SourcePath != "" {
				return options, true, fmt.Errorf("unexpected extra argument: %s", arg)
			}
			sourcePath, _, err := resolveExistingModulePath(arg, root, workDir)
			if err != nil {
				return options, true, fmt.Errorf("source file not found: %s", arg)
			}
			options.SourcePath = sourcePath
		}
	}
	if options.SourcePath == "" {
		return options, true, fmt.Errorf("missing source file argument")
	}
	options.CoverageProfile = resolveOptionalPath(options.CoverageProfile, root, workDir, options.coverageUser)
	options.ManifestDir = resolveOptionalPath(options.ManifestDir, root, workDir, options.manifestUser)
	if options.Scan && (options.UpdateManifest || options.Lines != nil || options.SinceLastRun || options.MutateAll || options.ReuseCoverage || options.FailOnSurvived || options.FailOnUncovered) {
		return options, true, fmt.Errorf("cannot combine --scan with mutation execution options")
	}
	if options.UpdateManifest && (options.Lines != nil || options.SinceLastRun || options.MutateAll || options.ReuseCoverage || options.FailOnSurvived || options.FailOnUncovered) {
		return options, true, fmt.Errorf("cannot combine --update-manifest with mutation execution options")
	}
	if options.SinceLastRun && (options.Lines != nil || options.MutateAll) {
		return options, true, fmt.Errorf("cannot combine --since-last-run with --lines or --mutate-all")
	}
	return options, true, nil
}

func consumeMutateValue(options *mutateOptions, name, value string) error {
	switch name {
	case "--lines":
		lines, err := parseLines(value)
		if err != nil {
			return err
		}
		options.Lines = lines
	case "--mutation-warning":
		n, err := parsePositiveInt(value, name)
		if err != nil {
			return err
		}
		options.MutationWarning = n
	case "--timeout-factor":
		n, err := parsePositiveInt(value, name)
		if err != nil {
			return err
		}
		options.TimeoutFactor = n
	case "--test-command":
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("--test-command requires a command")
		}
		options.TestCommand = value
	case "--coverage-profile":
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("--coverage-profile requires a path")
		}
		options.CoverageProfile = value
		options.coverageUser = true
	case "--manifest-dir":
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("--manifest-dir requires a path")
		}
		options.ManifestDir = value
		options.manifestUser = true
	case "--max-workers":
		n, err := parsePositiveInt(value, name)
		if err != nil {
			return err
		}
		options.MaxWorkers = n
	}
	return nil
}

func parseLines(value string) (map[int]bool, error) {
	lines := map[int]bool{}
	for _, part := range strings.Split(value, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("--lines requires positive comma-separated line numbers")
		}
		lines[n] = true
	}
	return lines, nil
}

func parsePositiveInt(value, name string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s requires a positive integer", name)
	}
	return n, nil
}

func runMutationCommand(options mutateOptions) error {
	store := manifest.Store{Dir: options.ManifestDir}
	verbosef(options, "restore backup start source=%q", options.SourcePath)
	restored, err := store.RestoreBackup(options.SourcePath)
	if err != nil {
		return err
	}
	verbosef(options, "restore backup finish restored=%t result=ok", restored)
	if restored {
		fmt.Println("Restored source from backup (previous run was interrupted).")
	}
	switch {
	case options.Scan:
		return scanMutations(options, store)
	case options.UpdateManifest:
		return updateMutationManifest(options, store)
	default:
		return mutate(options, store)
	}
}

func scanMutations(options mutateOptions, store manifest.Store) error {
	original, clean, err := readCleanSource(options.SourcePath)
	if err != nil {
		return err
	}
	sites, functions, err := mutations.DiscoverContent(options.SourcePath, []byte(clean))
	if err != nil {
		return err
	}
	previous, hasManifest, current, err := currentManifest(options.SourcePath, original, clean, functions, store)
	if err != nil {
		return err
	}
	changed := manifest.ChangedFunctionIDs(previous, current)
	changedCount := countChangedSites(sites, changed)
	fmt.Printf("Mutation scan: %s\n", options.SourcePath)
	fmt.Printf("Total mutation sites: %d\n", len(sites))
	fmt.Printf("Changed mutation sites: %d\n", changedCount)
	fmt.Printf("Manifest exists: %t\n", hasManifest)
	fmt.Printf("Manifest path: %s\n", store.Path(options.SourcePath))
	if len(sites) > options.MutationWarning {
		fmt.Printf("Warning: %d mutation sites exceeds threshold %d.\n", len(sites), options.MutationWarning)
	}
	return nil
}

func updateMutationManifest(options mutateOptions, store manifest.Store) error {
	original, clean, err := readCleanSource(options.SourcePath)
	if err != nil {
		return err
	}
	_, functions, err := mutations.DiscoverContent(options.SourcePath, []byte(clean))
	if err != nil {
		return err
	}
	current := manifest.Build(functions, options.SourcePath, clean, time.Now())
	if err := store.Save(options.SourcePath, current); err != nil {
		return err
	}
	if clean != original {
		if err := os.WriteFile(options.SourcePath, []byte(clean), 0o644); err != nil {
			return err
		}
	}
	fmt.Println("Updated manifest: " + store.Path(options.SourcePath))
	return nil
}

func mutate(options mutateOptions, store manifest.Store) error {
	original, clean, err := readCleanSource(options.SourcePath)
	if err != nil {
		return err
	}
	if clean != original {
		if err := os.WriteFile(options.SourcePath, []byte(clean), 0o644); err != nil {
			return err
		}
	}

	restoreSignals := restoreSourceOnSignal(options.SourcePath, clean)
	defer restoreSignals()
	defer func() {
		_ = os.WriteFile(options.SourcePath, []byte(clean), 0o644)
	}()

	sites, functions, err := mutations.DiscoverContent(options.SourcePath, []byte(clean))
	if err != nil {
		return err
	}
	previous, hasManifest, current, err := currentManifest(options.SourcePath, original, clean, functions, store)
	if err != nil {
		return err
	}
	changed := manifest.ChangedFunctionIDs(previous, current)
	profile, err := ensureMutationCoverage(options)
	if err != nil {
		return err
	}
	covered, uncovered := partitionByCoverage(profile, options.SourcePath, sites)
	effectiveSinceLastRun := options.SinceLastRun || (hasManifest && !options.MutateAll && options.Lines == nil)
	selected := selectMutationSites(covered, options.Lines, effectiveSinceLastRun, changed)
	printMutationHeader(options, sites, covered, uncovered, selected, hasManifest, changed, store)
	if len(uncovered) > 0 && options.Lines == nil && !effectiveSinceLastRun {
		printUncovered(uncovered)
	}

	baselineDuration, err := baseline(options.TestCommand, options.Verbose)
	if err != nil {
		return fmt.Errorf("baseline failed: %w", err)
	}
	timeout := time.Duration(options.TimeoutFactor) * baselineDuration
	if timeout < time.Second {
		timeout = time.Second
	}

	if err := store.SaveBackup(options.SourcePath, clean); err != nil {
		return err
	}
	defer func() {
		_ = store.CleanupBackup(options.SourcePath)
	}()

	results, err := runMutations(options.SourcePath, clean, selected, timeout, options.TestCommand, options.MaxWorkers, options.Verbose)
	if err != nil {
		return err
	}
	if err := os.WriteFile(options.SourcePath, []byte(clean), 0o644); err != nil {
		return err
	}
	summary := summarizeMutations(results, uncovered)
	if err := store.Save(options.SourcePath, current); err != nil {
		return err
	}
	fmt.Println("Updated manifest: " + store.Path(options.SourcePath))
	return enforceMutationGate(options, summary)
}

func readCleanSource(sourcePath string) (string, string, error) {
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", "", err
	}
	original := string(content)
	clean := manifest.StripEmbedded(original)
	return original, clean, nil
}

func currentManifest(sourcePath, original, clean string, functions []mutations.Function, store manifest.Store) (*manifest.Manifest, bool, manifest.Manifest, error) {
	if previous, ok, err := store.Load(sourcePath); err != nil {
		return nil, false, manifest.Manifest{}, err
	} else if ok {
		return previous, true, manifest.Build(functions, sourcePath, clean, time.Now()), nil
	}
	previous, ok := manifest.ExtractEmbedded(original)
	return previous, ok, manifest.Build(functions, sourcePath, clean, time.Now()), nil
}

func ensureMutationCoverage(options mutateOptions) (map[string][]coverage.Segment, error) {
	if options.ReuseCoverage {
		profile, err := coverage.LoadProfile(options.CoverageProfile)
		if err != nil {
			return nil, err
		}
		if profile == nil {
			return nil, fmt.Errorf("--reuse-coverage requested, but %s does not exist", options.CoverageProfile)
		}
		fmt.Println("Reusing existing coverage; covered/uncovered classification may be stale.")
		return profile, nil
	}
	if err := prepareCoverageDir(options.CoverageProfile); err != nil {
		return nil, err
	}
	if err := runCoverage(options.TestCommand, options.CoverageProfile); err != nil {
		return nil, fmt.Errorf("coverage failed: %w", err)
	}
	return coverage.LoadProfile(options.CoverageProfile)
}

func baseline(command string, verbose bool) (time.Duration, error) {
	command = strings.TrimSpace(strings.ReplaceAll(command, "{coverprofile}", ""))
	if command == "" {
		command = defaultTestCommand
	}
	verbosef(mutateOptions{Verbose: verbose}, "baseline start command=%q", command)
	start := time.Now()
	cmd := exec.Command("sh", "-c", command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	duration := time.Since(start)
	verbosef(mutateOptions{Verbose: verbose}, "baseline finish duration=%s result=%s", duration, resultString(err))
	return duration, err
}

func runMutations(sourcePath, original string, sites []mutations.Site, timeout time.Duration, testCommand string, maxWorkers int, verbose bool) ([]mutationResult, error) {
	if maxWorkers <= 1 || len(sites) <= 1 {
		return runMutationsSerial(sourcePath, original, sites, timeout, testCommand, verbose)
	}
	return runMutationsParallel(sourcePath, original, sites, timeout, testCommand, maxWorkers, verbose)
}

func runMutationsSerial(sourcePath, original string, sites []mutations.Site, timeout time.Duration, testCommand string, verbose bool) ([]mutationResult, error) {
	var results []mutationResult
	total := len(sites)
	for i, site := range sites {
		verbosef(mutateOptions{Verbose: verbose}, "mutation start mode=serial index=%d total=%d line=%d description=%q function=%q", i+1, total, site.Line, site.Description, site.FunctionID)
		mutated := mutations.Apply(original, site)
		if err := os.WriteFile(sourcePath, []byte(mutated), 0o644); err != nil {
			return nil, err
		}
		start := time.Now()
		status := runMutant(testCommand, timeout, "", verbose)
		result := mutationResult{Site: site, Status: status, Duration: time.Since(start)}
		results = append(results, result)
		if err := os.WriteFile(sourcePath, []byte(original), 0o644); err != nil {
			return nil, err
		}
		verbosef(mutateOptions{Verbose: verbose}, "mutation finish mode=serial index=%d total=%d status=%q duration=%s", i+1, total, status, result.Duration)
		fmt.Printf("[%d/%d] %s line %d %s: %s\n", i+1, total, status, site.Line, site.Description, site.FunctionID)
	}
	return results, nil
}

func runMutationsParallel(sourcePath, original string, sites []mutations.Site, timeout time.Duration, testCommand string, maxWorkers int, verbose bool) ([]mutationResult, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	absSource, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil, err
	}
	relSource, err := filepath.Rel(root, absSource)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(relSource, ".."+string(os.PathSeparator)) || relSource == ".." || filepath.IsAbs(relSource) {
		return nil, fmt.Errorf("source file must be inside working directory for parallel mutation: %s", sourcePath)
	}
	if maxWorkers > len(sites) {
		maxWorkers = len(sites)
	}

	runRoot := filepath.Join(root, "target", "goqual", "mutation-workers", fmt.Sprintf("run-%d-%d", os.Getpid(), time.Now().UnixNano()))
	defer os.RemoveAll(runRoot)
	type worker struct {
		Root       string
		SourcePath string
	}
	workers := make([]worker, maxWorkers)
	for i := range workers {
		workerRoot := filepath.Join(runRoot, fmt.Sprintf("worker-%d", i+1))
		if err := copyProject(root, workerRoot); err != nil {
			return nil, err
		}
		workers[i] = worker{
			Root:       workerRoot,
			SourcePath: filepath.Join(workerRoot, relSource),
		}
	}

	type job struct {
		Number int
		Site   mutations.Site
	}
	jobs := make(chan job, len(sites))
	for i, site := range sites {
		jobs <- job{Number: i + 1, Site: site}
	}
	close(jobs)

	results := make(chan mutationResult, len(sites))
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	for i, w := range workers {
		wg.Add(1)
		go func(workerNumber int, w worker) {
			defer wg.Done()
			for job := range jobs {
				mutated := mutations.Apply(original, job.Site)
				if err := os.WriteFile(w.SourcePath, []byte(mutated), 0o644); err != nil {
					sendFirstError(errs, err)
					return
				}
				start := time.Now()
				status := runMutant(testCommand, timeout, w.Root, verbose)
				if err := os.WriteFile(w.SourcePath, []byte(original), 0o644); err != nil {
					sendFirstError(errs, err)
					return
				}
				result := mutationResult{Site: job.Site, Status: status, Duration: time.Since(start)}
				results <- result
				fmt.Printf("[%d/%d] worker-%d %s line %d %s: %s\n", job.Number, len(sites), workerNumber, status, job.Site.Line, job.Site.Description, job.Site.FunctionID)
			}
		}(i+1, w)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	collected := make([]mutationResult, 0, len(sites))
	for result := range results {
		collected = append(collected, result)
	}
	select {
	case err := <-errs:
		return nil, err
	default:
	}
	if len(collected) != len(sites) {
		return nil, fmt.Errorf("mutation workers stopped after %d/%d results", len(collected), len(sites))
	}
	sort.SliceStable(collected, func(i, j int) bool {
		return collected[i].Site.Index < collected[j].Site.Index
	})
	return collected, nil
}

func runMutant(command string, timeout time.Duration, dir string, verbose bool) string {
	command = strings.TrimSpace(strings.ReplaceAll(command, "{coverprofile}", ""))
	if command == "" {
		command = defaultTestCommand
	}
	verbosef(mutateOptions{Verbose: verbose}, "test command start command=%q timeout=%s dir=%q", command, timeout, dir)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "timeout"
	}
	if err != nil {
		return "killed"
	}
	return "survived"
}

func copyProject(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if shouldSkipCopy(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		default:
			return nil
		}
	})
}

func shouldSkipCopy(rel string) bool {
	for _, dir := range []string{".git", ".gocache", ".gomodcache", ".reference", ".tools", "target"} {
		if rel == dir || strings.HasPrefix(rel, dir+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func partitionByCoverage(profile map[string][]coverage.Segment, sourcePath string, sites []mutations.Site) ([]mutations.Site, []mutations.Site) {
	var covered []mutations.Site
	var uncovered []mutations.Site
	for _, site := range sites {
		if coverage.Covered(profile, sourcePath, site.Line) {
			covered = append(covered, site)
		} else {
			uncovered = append(uncovered, site)
		}
	}
	return covered, uncovered
}

func selectMutationSites(sites []mutations.Site, lines map[int]bool, sinceLastRun bool, changed map[string]bool) []mutations.Site {
	var selected []mutations.Site
	for _, site := range sites {
		if lines != nil && !lines[site.Line] {
			continue
		}
		if sinceLastRun && !changed[site.FunctionID] {
			continue
		}
		selected = append(selected, site)
	}
	return selected
}

func countChangedSites(sites []mutations.Site, changed map[string]bool) int {
	n := 0
	for _, site := range sites {
		if changed[site.FunctionID] {
			n++
		}
	}
	return n
}

func printMutationHeader(options mutateOptions, all, covered, uncovered, selected []mutations.Site, hasManifest bool, changed map[string]bool, store manifest.Store) {
	fmt.Printf("Mutation run: %s\n", options.SourcePath)
	fmt.Printf("Total mutation sites: %d\n", len(all))
	fmt.Printf("Covered mutation sites: %d\n", len(covered))
	fmt.Printf("Uncovered mutation sites: %d\n", len(uncovered))
	fmt.Printf("Changed mutation sites: %d\n", countChangedSites(all, changed))
	fmt.Printf("Manifest exists: %t\n", hasManifest)
	fmt.Printf("Manifest path: %s\n", store.Path(options.SourcePath))
	fmt.Printf("Selected mutation sites: %d\n", len(selected))
	if len(all) > options.MutationWarning {
		fmt.Printf("Warning: %d mutation sites exceeds threshold %d.\n", len(all), options.MutationWarning)
	}
	if options.MaxWorkers > 0 {
		fmt.Printf("Mutation workers: %d\n", options.MaxWorkers)
	}
}

func printUncovered(sites []mutations.Site) {
	fmt.Println("Uncovered mutations:")
	for _, site := range sites {
		fmt.Printf("  line %d %s %s\n", site.Line, site.Description, site.FunctionID)
	}
}

type mutationSummary struct {
	Killed    int
	Survived  int
	Uncovered int
}

func summarizeMutations(results []mutationResult, uncovered []mutations.Site) mutationSummary {
	counts := map[string]int{}
	for _, result := range results {
		counts[result.Status]++
	}
	summary := mutationSummary{
		Killed:    counts["killed"] + counts["timeout"],
		Survived:  counts["survived"],
		Uncovered: len(uncovered),
	}
	fmt.Println()
	fmt.Println("Mutation Report")
	fmt.Println("===============")
	fmt.Printf("Killed: %d\n", summary.Killed)
	fmt.Printf("Survived: %d\n", summary.Survived)
	fmt.Printf("Uncovered: %d\n", summary.Uncovered)
	if summary.Survived > 0 {
		fmt.Println()
		fmt.Println("Survivors:")
		for _, result := range results {
			if result.Status == "survived" {
				fmt.Printf("  line %d %s %s\n", result.Site.Line, result.Site.Description, result.Site.FunctionID)
			}
		}
	}
	return summary
}

func enforceMutationGate(options mutateOptions, summary mutationSummary) error {
	failSurvived := options.FailOnSurvived && summary.Survived > 0
	failUncovered := options.FailOnUncovered && summary.Uncovered > 0
	if !failSurvived && !failUncovered {
		return nil
	}
	return mutationGateError{
		Summary:       summary,
		FailSurvived:  failSurvived,
		FailUncovered: failUncovered,
		RequireStrict: options.FailOnSurvived && options.FailOnUncovered,
	}
}

type mutationGateError struct {
	Summary       mutationSummary
	FailSurvived  bool
	FailUncovered bool
	RequireStrict bool
}

func (e mutationGateError) Error() string {
	var reasons []string
	if e.FailSurvived {
		reasons = append(reasons, fmt.Sprintf("survived=%d", e.Summary.Survived))
	}
	if e.FailUncovered {
		reasons = append(reasons, fmt.Sprintf("uncovered=%d", e.Summary.Uncovered))
	}
	mode := "mutation quality gate failed"
	if e.RequireStrict {
		mode = "strict mutation quality gate failed"
	}
	return mode + ": " + strings.Join(reasons, " ")
}

func sendFirstError(errs chan<- error, err error) {
	select {
	case errs <- err:
	default:
	}
}

func restoreSourceOnSignal(sourcePath, content string) func() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case sig := <-signals:
			_ = os.WriteFile(sourcePath, []byte(content), 0o644)
			signal.Stop(signals)
			if sig == os.Interrupt {
				os.Exit(130)
			}
			os.Exit(143)
		case <-done:
			return
		}
	}()
	return func() {
		close(done)
		signal.Stop(signals)
	}
}

func verbosef(options mutateOptions, format string, args ...any) {
	if !options.Verbose {
		return
	}
	fmt.Fprintf(os.Stderr, "verbose: "+format+"\n", args...)
}

func resultString(err error) string {
	if err == nil {
		return "ok"
	}
	return fmt.Sprintf("error=%q", err.Error())
}
