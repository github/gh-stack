package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestOptions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		opts options
	}{
		{"no workers", options{shards: 1, timeout: time.Second}},
		{"no shards", options{workers: 1, timeout: time.Second}},
		{"negative shard", options{workers: 1, shards: 1, shard: -1, timeout: time.Second}},
		{"missing shard", options{workers: 1, shards: 1, shard: 1, timeout: time.Second}},
		{"too many partitions", options{workers: 256, shards: 2, timeout: time.Second}},
		{"no timeout", options{workers: 1, shards: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, tc.opts.validate())
		})
	}
	require.NoError(t, (options{workers: 4, shards: 4, shard: 3, timeout: time.Minute}).validate())
}

func TestPackageMetadata(t *testing.T) {
	t.Parallel()
	var pkg testPackage
	require.NoError(t, json.Unmarshal([]byte(`{"ImportPath":"example/pkg","Dir":"/example/pkg","TestGoFiles":["pkg_test.go"]}`), &pkg))
	assert.Equal(t, "/example/pkg", pkg.Dir)
	assert.Equal(t, "example/pkg", pkg.ImportPath)
	assert.Equal(t, []string{"pkg_test.go"}, pkg.TestGoFiles)
}

func TestSchedule(t *testing.T) {
	t.Parallel()
	modify := "github.com/github/gh-stack/internal/modify"
	packages := []testPackage{
		{ImportPath: modify, binary: "modify.test", tests: []string{"TestDistributedModify_FoldThenRebaseRecovery", "TestDistributedModify_RebaseRecovery", "TestNewRegression"}},
		{ImportPath: "new/package", binary: "new.test", tests: []string{"ExampleThing", "FuzzThing", "TestThing"}},
		{ImportPath: "helper/package", binary: "helper.test"},
		{ImportPath: "no/tests"},
	}
	var inventory []testID
	for _, pkg := range packages {
		for _, name := range pkg.tests {
			inventory = append(inventory, testID{pkg.ImportPath, name})
		}
	}
	for _, count := range []int{1, 2, 4, 8} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			assignments := schedule(packages, count)
			require.NoError(t, validateAssignments(inventory, assignments))
			shuffled := slices.Clone(packages)
			slices.Reverse(shuffled)
			assert.Equal(t, assignments, schedule(shuffled, count))
			helpers := 0
			for _, groups := range assignments {
				for _, group := range groups {
					if group.Package == "helper/package" {
						helpers++
					}
					assert.NotEqual(t, "no/tests", group.Package)
				}
			}
			assert.Equal(t, 1, helpers, "a helper-only package still executes its TestMain")
		})
	}
}

func TestSelectors(t *testing.T) {
	t.Parallel()
	names := []string{"TestOne", "TestOneMore", "Test_λ", "ExampleThing", "FuzzInput"}
	rx := regexp.MustCompile(selector([]string{"TestOne", "Test_λ"}))
	for _, name := range names {
		assert.Equal(t, name == "TestOne" || name == "Test_λ", rx.MatchString(name))
	}
	assert.False(t, rx.MatchString("prefixTestOne"))
	assert.False(t, regexp.MustCompile(selector(nil)).MatchString("TestAnything"))
	assert.Equal(t, `^(Test\+special)$`, selector([]string{"Test+special"}))
}

func TestScheduleBoundsArguments(t *testing.T) {
	t.Parallel()
	pkg := testPackage{ImportPath: "large/package", binary: "large.test"}
	var inventory []testID
	for i := range 500 {
		name := fmt.Sprintf("Test_%s_%d", strings.Repeat("λ", 40), i)
		pkg.tests = append(pkg.tests, name)
		inventory = append(inventory, testID{pkg.ImportPath, name})
	}
	assignments := schedule([]testPackage{pkg}, 1)
	require.Greater(t, len(assignments[0]), 1)
	require.NoError(t, validateAssignments(inventory, assignments))
	for _, group := range assignments[0] {
		assert.LessOrEqual(t, len(selector(group.Tests)), 8000)
	}
}

func TestAssignmentValidation(t *testing.T) {
	t.Parallel()
	inventory := []testID{{"pkg", "TestOne"}, {"pkg", "TestTwo"}}
	for _, tc := range []struct {
		name   string
		groups [][]batch
	}{
		{"missing", [][]batch{{{Package: "pkg", Tests: []string{"TestOne"}}}}},
		{"duplicate", [][]batch{{{Package: "pkg", Tests: []string{"TestOne", "TestTwo", "TestTwo"}}}}},
		{"unknown", [][]batch{{{Package: "pkg", Tests: []string{"TestOne", "TestTwo", "TestThree"}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, validateAssignments(inventory, tc.groups))
		})
	}
	require.Error(t, validateAssignments(append(inventory, inventory[0]), nil))
}

func TestParallelBound(t *testing.T) {
	t.Parallel()
	var active, maximum atomic.Int32
	err := parallel(30, 3, func(i int) error {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, n) {
				break
			}
		}
		if i == 4 {
			return fmt.Errorf("task failure")
		}
		return nil
	})
	require.ErrorContains(t, err, "task failure")
	assert.LessOrEqual(t, maximum.Load(), int32(3))
	assert.Zero(t, active.Load())
}

func sampleReport() report {
	return report{
		Revision: "revision", OS: "linux", Arch: "amd64", Go: "go-test",
		Shards: 2, Workers: 1, Packages: []string{"pkg"},
		Inventory: []testID{{"pkg", "TestOne"}, {"pkg", "TestTwo"}},
		Assignments: [][]batch{
			{{Package: "pkg", Tests: []string{"TestOne"}}},
			{{Package: "pkg", Tests: []string{"TestTwo"}}},
		},
		Results:  []testResult{{Package: "pkg", Test: "TestOne", Action: "pass"}},
		Complete: true, Success: true,
	}
}

func TestResultValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*report)
	}{
		{"missing", func(r *report) { r.Results = nil }},
		{"duplicate", func(r *report) { r.Results = append(r.Results, r.Results[0]) }},
		{"unassigned", func(r *report) { r.Results[0].Test = "TestTwo" }},
		{"failed", func(r *report) { r.Results[0].Action = "fail" }},
		{"missing parent", func(r *report) { r.Results[0].Test += "/child" }},
		{"invalid partition count", func(r *report) { r.Assignments = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep := sampleReport()
			tc.change(&rep)
			require.Error(t, validateResults(rep))
		})
	}
	rep := sampleReport()
	rep.Results = append(rep.Results, testResult{Package: "pkg", Test: "TestOne/child", Action: "skip"})
	require.NoError(t, validateResults(rep))
}

func TestVerifyReports(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func([]report) []report
	}{
		{"complete", func(reps []report) []report { return reps }},
		{"missing shard", func(reps []report) []report { return reps[:1] }},
		{"duplicate shard", func(reps []report) []report { return append(reps, reps[0]) }},
		{"failed shard", func(reps []report) []report { reps[1].Success = false; return reps }},
		{"cancelled shard", func(reps []report) []report { reps[1].Complete = false; return reps }},
		{"missing test", func(reps []report) []report { reps[1].Results = nil; return reps }},
		{"different revision", func(reps []report) []report { reps[1].Revision = "other"; return reps }},
		{"different inventory", func(reps []report) []report {
			reps[1].Inventory = append(reps[1].Inventory, testID{"pkg", "TestNew"})
			return reps
		}},
		{"different packages", func(reps []report) []report {
			reps[1].Packages = append(reps[1].Packages, "new/package")
			return reps
		}},
		{"wrong OS", func(reps []report) []report { reps[1].OS = "windows"; return reps }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reps := []report{sampleReport(), sampleReport()}
			reps[1].Shard = 1
			reps[1].Results[0].Test = "TestTwo"
			reps = tc.change(reps)
			dir := t.TempDir()
			for i, rep := range reps {
				target := filepath.Join(dir, fmt.Sprint(i))
				require.NoError(t, os.Mkdir(target, 0755))
				data, err := json.Marshal(rep)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(target, "report.json"), data, 0644))
			}
			var out bytes.Buffer
			err := verifyReports(dir, []string{"linux"}, &out)
			if tc.name == "complete" {
				require.NoError(t, err)
				assert.Contains(t, out.String(), "2 top-level tests")
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestWorkflowMatrix(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "test.yml"))
	require.NoError(t, err)
	var workflow struct {
		On map[string]struct {
			Branches       []string `yaml:"branches"`
			BranchesIgnore []string `yaml:"branches-ignore"`
		} `yaml:"on"`
		Jobs map[string]struct {
			Strategy struct {
				Matrix struct {
					Include []struct {
						OS      string `yaml:"os"`
						Shard   int    `yaml:"shard"`
						Shards  int    `yaml:"shards"`
						Workers int    `yaml:"workers"`
					} `yaml:"include"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	pullRequest, exists := workflow.On["pull_request"]
	require.True(t, exists)
	assert.Empty(t, pullRequest.Branches, "stacked PRs target another layer, not necessarily main")
	assert.Empty(t, pullRequest.BranchesIgnore)
	seen := make(map[string]map[int]bool)
	counts := make(map[string]int)
	for _, entry := range workflow.Jobs["test"].Strategy.Matrix.Include {
		require.NoError(t, validatePartitions(entry.Workers, entry.Shards, entry.Shard))
		if seen[entry.OS] == nil {
			seen[entry.OS] = make(map[int]bool)
			counts[entry.OS] = entry.Shards
		}
		require.Equal(t, counts[entry.OS], entry.Shards)
		require.False(t, seen[entry.OS][entry.Shard], "duplicate %s shard %d", entry.OS, entry.Shard)
		seen[entry.OS][entry.Shard] = true
	}
	require.Len(t, seen, 3)
	for _, platform := range []string{"ubuntu-latest", "windows-latest", "macos-latest"} {
		require.NotEmpty(t, seen[platform], platform)
		require.Len(t, seen[platform], counts[platform], platform)
		for i := range counts[platform] {
			require.True(t, seen[platform][i], "missing %s shard %d", platform, i)
		}
	}
	_, gate := workflow.Jobs["test-complete"]
	require.True(t, gate, "the completion gate must not be removed")
}

func TestRunnerEndToEnd(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	t.Chdir(repo)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	require.NoError(t, os.WriteFile("go.mod", []byte("module fixture\n\ngo 1.26.0\n"), 0644))
	require.NoError(t, os.WriteFile("fixture.go", []byte("package fixture\n"), 0644))
	require.NoError(t, os.WriteFile("race_test.go", []byte("//go:build race\n\npackage fixture\nimport \"testing\"\nfunc TestRaceOnly(t *testing.T) {}\n"), 0644))
	_, err := output(ctx, repo, "git", "init", "-q", "-b", "main")
	require.NoError(t, err)
	_, err = output(ctx, repo, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com",
		"-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "fixture")
	require.NoError(t, err)
	_, err = output(ctx, repo, "go", "vet", "-race", "./...")
	require.NoError(t, err)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	for shard := range 2 {
		err := run(ctx, options{workers: 1, shards: 2, shard: shard, timeout: time.Minute, output: dir},
			[]string{"./..."}, &stdout, &stderr)
		require.NoError(t, err, stderr.String())
	}
	require.NoError(t, verifyReports(dir, []string{runtime.GOOS}, &stdout))
	assert.Contains(t, stdout.String(), "1 top-level tests")
	assert.Empty(t, stderr.String())
	reports, err := filepath.Glob(filepath.Join(dir, "gh-stack-tests-*", "report.json"))
	require.NoError(t, err)
	require.Len(t, reports, 2)
	executed := 0
	for _, path := range reports {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		var rep report
		require.NoError(t, json.Unmarshal(data, &rep))
		require.Equal(t, []testID{{"fixture", "TestRaceOnly"}}, rep.Inventory)
		for _, result := range rep.Results {
			require.Equal(t, "TestRaceOnly", result.Test)
			require.Equal(t, "pass", result.Action)
			executed++
		}
	}
	assert.Equal(t, 1, executed)
}

func TestRunBatch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture\n\ngo 1.26.0\n"), 0644))
	source := `package fixture
import ("fmt"; "os"; "testing"; "time")
func block(phase string) {
	if os.Getenv("GH_STACK_TESTSUITE_BLOCK_PHASE") == phase {
		fmt.Println("blocking", phase)
		for { time.Sleep(time.Hour) }
	}
}
func init() { block("init") }
func TestMain(m *testing.M) {
	if os.Getenv("GH_STACK_TESTSUITE_EARLY_EXIT") == "1" { os.Exit(0) }
	block("before")
	code := m.Run()
	block("after")
	os.Exit(code)
}
func TestPass(t *testing.T) { t.Run("child", func(t *testing.T) {}) }
func TestFailure(t *testing.T) { t.Fatal("intentional child failure") }
func TestRace(t *testing.T) {
	var value int
	done := make(chan struct{})
	go func() { value++; close(done) }()
	value++
	<-done
	t.Log(value)
}
func TestSlow(t *testing.T) { select {} }
func Thing() { fmt.Println("example") }
func ExampleThing() {
	Thing()
	// Output: example
}
func FuzzSeed(f *testing.F) {
	f.Add("seed")
	f.Fuzz(func(t *testing.T, input string) {
		if input == "" { t.Fatal("empty seed") }
	})
}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fixture_test.go"), []byte(source), 0644))
	binary := filepath.Join(dir, "fixture.test")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx := context.Background()
	_, err := output(ctx, dir, "go", "vet", "./...")
	require.NoError(t, err)
	_, err = output(ctx, dir, "go", "test", "-race", "-c", "-o", binary, ".")
	require.NoError(t, err)
	pkg := testPackage{ImportPath: "fixture", Dir: dir, binary: binary}
	for _, tc := range []struct {
		name, test string
		timeout    time.Duration
		wantError  string
		results    []string
	}{
		{"success", "TestPass", time.Minute, "", []string{"TestPass/child", "TestPass"}},
		{"example", "ExampleThing", time.Minute, "", []string{"ExampleThing"}},
		{"fuzz seed", "FuzzSeed", time.Minute, "", []string{"FuzzSeed/seed#0", "FuzzSeed"}},
		{"failure", "TestFailure", time.Minute, "intentional child failure", nil},
		{"race", "TestRace", time.Minute, "DATA RACE", nil},
		{"timeout", "TestSlow", 100 * time.Millisecond, "test timed out", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			results, err := runBatch(ctx, pkg, batch{pkg.ImportPath, []string{tc.test}}, tc.timeout, path)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
			} else {
				require.NoError(t, err)
				names := make([]string, len(results))
				for i, result := range results {
					names[i] = result.Test
				}
				assert.Equal(t, tc.results, names)
			}
			data, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			assert.Contains(t, string(data), `"Action"`)
		})
	}
	t.Run("early successful exit is not a completed suite", func(t *testing.T) {
		t.Setenv("GH_STACK_TESTSUITE_EARLY_EXIT", "1")
		results, err := runBatch(ctx, pkg, batch{pkg.ImportPath, []string{"TestPass"}}, time.Minute, filepath.Join(t.TempDir(), "events.jsonl"))
		require.NoError(t, err)
		rep := sampleReport()
		rep.Results = results
		require.ErrorContains(t, validateResults(rep), "missing result")
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		_, err := runBatch(ctx, pkg, batch{pkg.ImportPath, []string{"TestSlow"}}, time.Minute, filepath.Join(t.TempDir(), "events.jsonl"))
		require.Error(t, err)
		require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	})
	for _, phase := range []string{"init", "before", "after"} {
		t.Run("external deadline/"+phase, func(t *testing.T) {
			t.Setenv("GH_STACK_TESTSUITE_BLOCK_PHASE", phase)
			path := filepath.Join(t.TempDir(), "events.jsonl")
			_, err := runBatch(ctx, pkg, batch{pkg.ImportPath, []string{"TestPass"}}, 100*time.Millisecond, path)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			assert.Contains(t, err.Error(), "blocking "+phase, "the fixture must reach the blocked lifecycle phase")
		})
	}
	t.Run("discovery deadline", func(t *testing.T) {
		t.Setenv("GH_STACK_TESTSUITE_BLOCK_PHASE", "after")
		names, err := discoverTests(ctx, pkg, 100*time.Millisecond)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Nil(t, names)
		assert.Contains(t, err.Error(), "blocking after", "list mode must not bypass the external deadline")
	})
}
