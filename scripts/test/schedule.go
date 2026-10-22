package main

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const maxSelectorBytes = 8000

type testID struct {
	Package string
	Test    string
}

type batch struct {
	Package string
	Tests   []string
}

func validatePartitions(workers, shards, shard int) error {
	if workers < 1 || shards < 1 || shards > 256 || workers > 256/shards {
		return errors.New("workers and shards must be positive, with at most 256 total worker partitions")
	}
	if shard < 0 || shard >= shards {
		return errors.New("shard must be between zero and shards-1")
	}
	return nil
}

func selector(names []string) string {
	if len(names) == 0 {
		return "^$"
	}
	escaped := make([]string, len(names))
	for i, name := range names {
		escaped[i] = regexp.QuoteMeta(name)
	}
	return "^(" + strings.Join(escaped, "|") + ")$"
}

func weight(id testID) float64 {
	name := strings.TrimPrefix(id.Package, "github.com/github/gh-stack/") + "/" + id.Test
	if seconds := timingHints[name]; seconds > 0 {
		return seconds
	}
	return 0.1
}

func schedule(packages []testPackage, partitions int) [][]batch {
	type unit struct {
		batch
		cost float64
	}
	var units []unit
	for _, pkg := range packages {
		if pkg.binary == "" {
			continue
		}
		// Only scheduling granularity is hinted. Every discovered package/test is assigned.
		switch strings.TrimPrefix(pkg.ImportPath, "github.com/github/gh-stack/") {
		case "cmd", "internal/git", "internal/modify":
			for _, name := range pkg.tests {
				units = append(units, unit{batch{pkg.ImportPath, []string{name}}, weight(testID{pkg.ImportPath, name})})
			}
			if len(pkg.tests) == 0 {
				units = append(units, unit{batch{Package: pkg.ImportPath}, 0.1})
			}
		default:
			cost := 0.1
			for _, name := range pkg.tests {
				cost += weight(testID{pkg.ImportPath, name})
			}
			units = append(units, unit{batch{pkg.ImportPath, slices.Clone(pkg.tests)}, cost})
		}
	}
	slices.SortFunc(units, func(a, b unit) int {
		if a.cost > b.cost {
			return -1
		}
		if a.cost < b.cost {
			return 1
		}
		return strings.Compare(a.Package+"/"+strings.Join(a.Tests, ","), b.Package+"/"+strings.Join(b.Tests, ","))
	})
	result := make([][]batch, partitions)
	load := make([]float64, partitions)
	for _, item := range units {
		target := 0
		for i := range partitions {
			if load[i] < load[target] {
				target = i
			}
		}
		load[target] += item.cost
		merged := false
		for i := range result[target] {
			group := &result[target][i]
			if group.Package == item.Package {
				group.Tests = append(group.Tests, item.Tests...)
				merged = true
				break
			}
		}
		if !merged {
			result[target] = append(result[target], item.batch)
		}
	}
	for i, groups := range result {
		var bounded []batch
		for _, group := range groups {
			slices.Sort(group.Tests)
			current := batch{Package: group.Package}
			for _, name := range group.Tests {
				// Leave ample space below Windows' command-line limit, including UTF-16 paths.
				if len(current.Tests) > 0 && len(selector(current.Tests))+len(regexp.QuoteMeta(name))+1 > maxSelectorBytes {
					bounded = append(bounded, current)
					current = batch{Package: group.Package}
				}
				current.Tests = append(current.Tests, name)
			}
			bounded = append(bounded, current)
		}
		result[i] = bounded
	}
	return result
}

func validateAssignments(inventory []testID, assignments [][]batch) error {
	expected := make(map[testID]bool, len(inventory))
	for _, id := range inventory {
		if expected[id] {
			return fmt.Errorf("duplicate discovery: %s/%s", id.Package, id.Test)
		}
		expected[id] = true
	}
	seen := make(map[testID]bool, len(inventory))
	for _, groups := range assignments {
		for _, group := range groups {
			if len(selector(group.Tests)) > maxSelectorBytes {
				return fmt.Errorf("test selector exceeds the argument limit for %s", group.Package)
			}
			for _, name := range group.Tests {
				id := testID{group.Package, name}
				if !expected[id] || seen[id] {
					return fmt.Errorf("unexpected or duplicate assignment: %s/%s", id.Package, id.Test)
				}
				seen[id] = true
			}
		}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("incomplete assignments: assigned %d of %d tests", len(seen), len(expected))
	}
	return nil
}
