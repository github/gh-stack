//go:build !windows

package stack

import (
	"errors"
	"os"
)

func readStateFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func publishFile(temp, path string, replace bool) error {
	if replace {
		return os.Rename(temp, path)
	}
	// Linking publishes a complete file without replacing an existing backup.
	// The temporary name is removed by writeFileAtomic.
	return os.Link(temp, path)
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
