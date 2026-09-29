package stack

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ReadStateFile reads a complete state file, allowing WriteAtomic to replace it
// while the read is in progress. On Windows, its handle permits delete sharing.
func ReadStateFile(path string) ([]byte, error) {
	return readStateFile(path)
}

// WriteAtomic publishes data at path using a fully written temporary file in
// the same directory. It preserves an existing regular file's permissions and
// uses 0644 for a new file. The parent directory must already exist.
// It does not acquire locks; callers must serialize mutations.
func WriteAtomic(path string, data []byte) error {
	return writeFileAtomic(path, data, 0644, true)
}

// writeFileAtomic publishes a fully written sibling of path. When replace is
// false, an existing destination is never overwritten, even on a racing create.
func writeFileAtomic(path string, data []byte, mode os.FileMode, replace bool) (err error) {
	if replace {
		info, statErr := os.Lstat(path)
		switch {
		case statErr == nil:
			if !info.Mode().IsRegular() {
				return fmt.Errorf("cannot replace non-regular file %q", path)
			}
			mode = info.Mode().Perm()
		case !errors.Is(statErr, os.ErrNotExist):
			return statErr
		}
	}

	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, f.Close())
		}
		if removeErr := os.Remove(temp); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("removing temporary file: %w", removeErr))
		}
	}()

	if err := f.Chmod(mode); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	closed = true
	if err := f.Close(); err != nil {
		return err
	}
	if err := publishFile(temp, path, replace); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
