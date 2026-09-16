//go:build windows

package stack

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func readStateFile(path string) ([]byte, error) {
	name, err := windowsFilePath(path)
	if err != nil {
		return nil, err
	}
	// Let publication replace the name while readers finish with the old file.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(handle), path)
	data, err := io.ReadAll(f)
	return data, errors.Join(err, f.Close())
}

func publishFile(temp, path string, replace bool) error {
	from, err := windowsFilePath(temp)
	if err != nil {
		return err
	}
	to, err := windowsFilePath(path)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if replace {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	// Never remove the destination first, or allow a cross-volume copy/delete.
	if err := windows.MoveFileEx(from, to, flags); err != nil {
		return &os.LinkError{Op: "publish", Old: temp, New: path, Err: err}
	}
	return nil
}

func windowsFilePath(path string) (*uint16, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	switch {
	case strings.HasPrefix(path, `\\?\`), strings.HasPrefix(path, `\\.\`):
	case strings.HasPrefix(path, `\\`):
		path = `\\?\UNC\` + path[2:]
	default:
		path = `\\?\` + path
	}
	return windows.UTF16PtrFromString(path)
}

func syncDirectory(string) error {
	// Windows does not expose directory fsync; publication uses WRITE_THROUGH.
	return nil
}
