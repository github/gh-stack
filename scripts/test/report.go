package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

func validateResults(rep report) error {
	if err := validatePartitions(rep.Workers, rep.Shards, rep.Shard); err != nil {
		return err
	}
	if len(rep.Assignments) != rep.Shards*rep.Workers {
		return errors.New("worker partition count does not match the manifest")
	}
	if err := validateAssignments(rep.Inventory, rep.Assignments); err != nil {
		return err
	}
	expected := make(map[testID]bool)
	for _, groups := range rep.Assignments[rep.Shard*rep.Workers : (rep.Shard+1)*rep.Workers] {
		for _, group := range groups {
			for _, name := range group.Tests {
				expected[testID{group.Package, name}] = true
			}
		}
	}
	seen := make(map[testID]bool)
	for _, result := range rep.Results {
		if result.Action != "pass" && result.Action != "skip" {
			return fmt.Errorf("unsuccessful test: %s/%s (%s)", result.Package, result.Test, result.Action)
		}
		root, _, _ := strings.Cut(result.Test, "/")
		if !expected[testID{result.Package, root}] {
			return fmt.Errorf("unassigned test executed: %s/%s", result.Package, result.Test)
		}
		id := testID{result.Package, result.Test}
		if seen[id] {
			return fmt.Errorf("duplicate result: %s/%s", result.Package, result.Test)
		}
		seen[id] = true
	}
	for id := range expected {
		if !seen[id] {
			return fmt.Errorf("missing result: %s/%s", id.Package, id.Test)
		}
	}
	return nil
}

func verifyReports(dir string, platforms []string, stdout io.Writer) error {
	byOS := make(map[string]map[int]report)
	revision := ""
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "report.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var rep report
		if err := json.Unmarshal(data, &rep); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if !rep.Complete || !rep.Success || rep.Error != "" {
			return fmt.Errorf("%s: incomplete or failed shard: %s", path, rep.Error)
		}
		if len(rep.Packages) == 0 || len(rep.Inventory) == 0 {
			return fmt.Errorf("%s: empty CI suite inventory", path)
		}
		if rep.Revision == "" || (revision != "" && revision != rep.Revision) {
			return errors.New("reports do not identify the same source revision")
		}
		revision = rep.Revision
		if err := validateResults(rep); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if byOS[rep.OS] == nil {
			byOS[rep.OS] = make(map[int]report)
		}
		if _, exists := byOS[rep.OS][rep.Shard]; exists {
			return fmt.Errorf("duplicate %s shard %d", rep.OS, rep.Shard)
		}
		byOS[rep.OS][rep.Shard] = rep
		return nil
	})
	if err != nil {
		return err
	}
	if len(byOS) != len(platforms) {
		return fmt.Errorf("expected %d platforms, received %d", len(platforms), len(byOS))
	}
	for _, platform := range platforms {
		shards := byOS[platform]
		first, exists := shards[0]
		if !exists || len(shards) != first.Shards {
			return fmt.Errorf("missing shards for %s", platform)
		}
		count := 0
		for i := range first.Shards {
			rep, exists := shards[i]
			if !exists || rep.Shards != first.Shards || rep.Workers != first.Workers ||
				rep.Arch != first.Arch || rep.Go != first.Go ||
				!reflect.DeepEqual(rep.Packages, first.Packages) ||
				!reflect.DeepEqual(rep.Inventory, first.Inventory) ||
				!reflect.DeepEqual(rep.Assignments, first.Assignments) {
				return fmt.Errorf("inconsistent %s shard %d manifest", platform, i)
			}
			count += len(rep.Results)
		}
		fmt.Fprintf(stdout, "%s: %d shards, %d top-level tests, %d test/subtest results\n",
			platform, first.Shards, len(first.Inventory), count)
	}
	return nil
}
