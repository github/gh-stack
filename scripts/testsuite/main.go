package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/token"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Allow native timeout diagnostics and race-runtime shutdown bounded grace.
const testExitGrace = 5 * time.Second

type options struct {
	workers, shard, shards int
	output, verify         string
	timeout                time.Duration
}

type testPackage struct {
	ImportPath   string
	Dir          string
	TestGoFiles  []string
	XTestGoFiles []string
	binary       string
	tests        []string
}

type event struct {
	Time    time.Time
	Action  string
	Package string
	Test    string
	Elapsed float64
	Output  string
}

type testResult struct {
	Package string
	Test    string
	Action  string
	Elapsed float64
}

type report struct {
	Revision    string
	OS          string
	Arch        string
	Go          string
	Git         string
	Image       string
	CPUs        int
	Shard       int
	Shards      int
	Workers     int
	Packages    []string
	Inventory   []testID
	Assignments [][]batch
	Results     []testResult
	BuildTime   float64
	TestTime    float64
	Complete    bool
	Success     bool
	Error       string
}

func main() {
	opts := options{}
	flag.IntVar(&opts.workers, "workers", min(4, runtime.NumCPU()), "maximum concurrent test processes")
	flag.IntVar(&opts.shard, "shard", 0, "zero-based CI shard index")
	flag.IntVar(&opts.shards, "shards", 1, "number of CI shards")
	flag.StringVar(&opts.output, "output", "", "parent directory for result artifacts (default: system temporary directory)")
	flag.StringVar(&opts.verify, "verify", "", "verify downloaded reports for all three CI platforms instead of running tests")
	flag.DurationVar(&opts.timeout, "timeout", 30*time.Minute, "timeout for each test process")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	if opts.verify != "" {
		err = verifyReports(opts.verify, []string{"linux", "darwin", "windows"}, os.Stdout)
	} else {
		err = run(ctx, opts, flag.Args(), os.Stdout, os.Stderr)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func (o options) validate() error {
	if err := validatePartitions(o.workers, o.shards, o.shard); err != nil {
		return err
	}
	if o.timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	return nil
}

func command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	configureProcess(cmd)
	return cmd
}

func output(ctx context.Context, dir string, args ...string) ([]byte, error) {
	data, err := command(ctx, dir, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s: %w\n%s", strings.Join(args, " "), err, data)
	}
	return data, nil
}

func run(ctx context.Context, opts options, patterns []string, stdout, stderr io.Writer) (err error) {
	if err := opts.validate(); err != nil {
		return err
	}
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	if opts.output != "" {
		if err := os.MkdirAll(opts.output, 0755); err != nil {
			return err
		}
	}
	dir, err := os.MkdirTemp(opts.output, "gh-stack-tests-")
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Test results: %s\n", dir)
	rep := report{
		OS: runtime.GOOS, Arch: runtime.GOARCH, Go: runtime.Version(), CPUs: runtime.NumCPU(),
		Image: os.Getenv("ImageOS") + " " + os.Getenv("ImageVersion"),
		Shard: opts.shard, Shards: opts.shards, Workers: opts.workers,
	}
	defer func() {
		if err != nil {
			rep.Error = err.Error()
		}
		rep.Success = err == nil && rep.Complete
		data, marshalErr := json.MarshalIndent(rep, "", "  ")
		if marshalErr != nil {
			err = errors.Join(err, marshalErr)
			return
		}
		err = errors.Join(err, os.WriteFile(filepath.Join(dir, "report.json"), append(data, '\n'), 0644))
	}()
	sha, err := output(ctx, "", "git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	rep.Revision = strings.TrimSpace(string(sha))
	version, err := output(ctx, "", "git", "--version")
	if err != nil {
		return err
	}
	rep.Git = strings.TrimSpace(string(version))
	data, err := output(ctx, "", append([]string{"go", "list", "-race", "-json"}, patterns...)...)
	if err != nil {
		return err
	}
	var packages []testPackage
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	for {
		var pkg testPackage
		if err := decoder.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
		if pkg.Dir == "" || pkg.ImportPath == "" {
			return errors.New("go list returned a package without its directory or import path")
		}
		packages = append(packages, pkg)
	}
	if len(packages) == 0 {
		return errors.New("no packages selected")
	}
	slices.SortFunc(packages, func(a, b testPackage) int { return strings.Compare(a.ImportPath, b.ImportPath) })
	buildDir, err := os.MkdirTemp("", "gh-stack-test-binaries-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(buildDir)) }()
	start := time.Now()
	err = parallel(len(packages), opts.workers, func(i int) error {
		pkg := &packages[i]
		name := fmt.Sprintf("%d.test", i)
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		pkg.binary = filepath.Join(buildDir, name)
		if _, err := output(ctx, pkg.Dir, "go", "test", "-race", "-p=1", "-c", "-o", pkg.binary, "."); err != nil {
			return err
		}
		if len(pkg.TestGoFiles)+len(pkg.XTestGoFiles) == 0 {
			pkg.binary = ""
			return nil
		}
		var err error
		pkg.tests, err = discoverTests(ctx, *pkg, opts.timeout)
		return err
	})
	rep.BuildTime = time.Since(start).Seconds()
	if err != nil {
		return err
	}
	byPackage := make(map[string]testPackage, len(packages))
	for _, pkg := range packages {
		rep.Packages = append(rep.Packages, pkg.ImportPath)
		byPackage[pkg.ImportPath] = pkg
		for _, name := range pkg.tests {
			rep.Inventory = append(rep.Inventory, testID{pkg.ImportPath, name})
		}
	}
	rep.Assignments = schedule(packages, opts.shards*opts.workers)
	if err := validateAssignments(rep.Inventory, rep.Assignments); err != nil {
		return err
	}
	// Publish an incomplete manifest before execution; cancellation cannot look successful.
	initial, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), initial, 0644); err != nil {
		return err
	}
	var mu sync.Mutex
	start = time.Now()
	err = parallel(opts.workers, opts.workers, func(worker int) error {
		partition := opts.shard*opts.workers + worker
		var failures []error
		for index, group := range rep.Assignments[partition] {
			path := filepath.Join(dir, fmt.Sprintf("worker-%d-package-%d.jsonl", worker, index))
			results, runErr := runBatch(ctx, byPackage[group.Package], group, opts.timeout, path)
			mu.Lock()
			rep.Results = append(rep.Results, results...)
			if runErr != nil {
				fmt.Fprintf(stderr, "FAIL %s: %v\n", group.Package, runErr)
			} else {
				fmt.Fprintf(stdout, "PASS %s (%d top-level tests)\n", group.Package, len(group.Tests))
			}
			mu.Unlock()
			failures = append(failures, runErr)
			if ctx.Err() != nil {
				break
			}
		}
		return errors.Join(failures...)
	})
	rep.TestTime = time.Since(start).Seconds()
	rep.Complete = true
	if parityErr := validateResults(rep); parityErr != nil {
		err = errors.Join(err, parityErr)
	}
	printSlowTests(stdout, rep.Results)
	fmt.Fprintf(stdout, "Build/discovery: %.2fs; tests: %.2fs; shard %d/%d\n", rep.BuildTime, rep.TestTime, opts.shard+1, opts.shards)
	return err
}

func parallel(count, workers int, fn func(int) error) error {
	errs := make([]error, count)
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(count, workers) {
		wg.Go(func() {
			for i := range jobs {
				errs[i] = fn(i)
			}
		})
	}
	for i := range count {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return errors.Join(errs...)
}

func discoverTests(ctx context.Context, pkg testPackage, timeout time.Duration) ([]string, error) {
	ctx, cancel := context.WithDeadline(ctx, time.Now().Add(timeout).Add(testExitGrace))
	defer cancel()
	data, err := output(ctx, pkg.Dir, pkg.binary, "-test.list=.")
	if err != nil {
		return nil, errors.Join(err, ctx.Err())
	}
	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		name := strings.TrimSpace(line)
		if token.IsIdentifier(name) && (strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Example") || strings.HasPrefix(name, "Fuzz")) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names, nil
}

func runBatch(ctx context.Context, pkg testPackage, group batch, timeout time.Duration, path string) ([]testResult, error) {
	ctx, cancel := context.WithDeadline(ctx, time.Now().Add(timeout).Add(testExitGrace))
	defer cancel()
	log, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	cmd := command(ctx, pkg.Dir, "go", "tool", "test2json", "-t", "-p", pkg.ImportPath, pkg.binary,
		"-test.v=test2json", "-test.count=1", "-test.parallel=1", "-test.timeout="+timeout.String(), "-test.run="+selector(group.Tests))
	stream, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var diagnostics strings.Builder
	cmd.Stderr = &diagnostics
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(io.TeeReader(stream, log))
	var results []testResult
	var messages strings.Builder
	var decodeErr error
	packagePassed := false
	for {
		var e event
		if err := decoder.Decode(&e); err != nil {
			if !errors.Is(err, io.EOF) {
				decodeErr = err
				_, drainErr := io.Copy(log, stream)
				decodeErr = errors.Join(decodeErr, drainErr)
			}
			break
		}
		messages.WriteString(e.Output)
		if e.Test == "" && e.Action == "pass" {
			packagePassed = true
		}
		if e.Test != "" && (e.Action == "pass" || e.Action == "fail" || e.Action == "skip") {
			results = append(results, testResult{e.Package, e.Test, e.Action, e.Elapsed})
		}
	}
	err = errors.Join(cmd.Wait(), ctx.Err(), decodeErr, log.Close())
	if !packagePassed {
		err = errors.Join(err, errors.New("test process did not report package success"))
	}
	if err != nil {
		return results, fmt.Errorf("%w\n%s%s\nJSON log: %s", err, messages.String(), diagnostics.String(), path)
	}
	return results, nil
}

func printSlowTests(w io.Writer, results []testResult) {
	top := make([]testResult, 0)
	for _, result := range results {
		if !strings.Contains(result.Test, "/") {
			top = append(top, result)
		}
	}
	slices.SortFunc(top, func(a, b testResult) int {
		if a.Elapsed > b.Elapsed {
			return -1
		}
		if a.Elapsed < b.Elapsed {
			return 1
		}
		return strings.Compare(a.Package+"/"+a.Test, b.Package+"/"+b.Test)
	})
	fmt.Fprintln(w, "Slowest top-level tests (including their subtests):")
	for _, result := range top[:min(10, len(top))] {
		fmt.Fprintf(w, "  %.2fs %s/%s\n", result.Elapsed, result.Package, result.Test)
	}
}
