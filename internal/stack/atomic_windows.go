//go:build windows

package stack

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func readStateFile(path string) ([]byte, error) {
	name, err := windowsFilePath(path)
	if err != nil {
		return nil, err
	}
	// Let publication replace the name while readers finish with the old file.
	handle, err := windows.CreateFile(&name[0], windows.GENERIC_READ,
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
	// Never remove the destination first, or allow a cross-volume copy/delete.
	if replace {
		err = replaceFileWindows(from, to)
	} else {
		err = windows.MoveFileEx(&from[0], &to[0], windows.MOVEFILE_WRITE_THROUGH)
	}
	if err != nil {
		return &os.LinkError{Op: "publish", Old: temp, New: path, Err: err}
	}
	return nil
}

func replaceFileWindows(from, to []uint16) error {
	handle, err := windows.CreateFile(&from[0], windows.DELETE|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_WRITE_THROUGH, 0)
	if err != nil {
		return err
	}

	// FILE_RENAME_INFO has pointer-sized padding and a trailing UTF-16 name.
	type renameInfo struct {
		Flags          uint32
		RootDirectory  windows.Handle
		FileNameLength uint32
		FileName       [1]uint16
	}
	buffer := make([]byte, int(unsafe.Sizeof(renameInfo{}))+len(to)*2)
	info := (*renameInfo)(unsafe.Pointer(&buffer[0]))
	info.Flags = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	info.FileNameLength = uint32(len(to)-1) * 2
	copy(unsafe.Slice(&info.FileName[0], len(to)), to)

	err = windows.SetFileInformationByHandle(handle, windows.FileRenameInfoEx, &buffer[0], uint32(len(buffer)))
	if err == nil {
		return errors.Join(windows.FlushFileBuffers(handle), windows.CloseHandle(handle))
	}
	if closeErr := windows.CloseHandle(handle); closeErr != nil {
		return errors.Join(err, closeErr)
	}

	// Older Windows versions/filesystems may lack POSIX rename. Only fall back
	// for unsupported operations; legacy rename still reports open-reader errors.
	if errors.Is(err, windows.ERROR_NOT_SUPPORTED) || errors.Is(err, windows.ERROR_INVALID_PARAMETER) ||
		errors.Is(err, windows.ERROR_INVALID_FUNCTION) {
		return windows.MoveFileEx(&from[0], &to[0], windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
	}
	return err
}

func windowsFilePath(path string) ([]uint16, error) {
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
	return windows.UTF16FromString(path)
}

func syncDirectory(string) error {
	// Windows has no directory fsync; publication flushes the file or uses WRITE_THROUGH.
	return nil
}
