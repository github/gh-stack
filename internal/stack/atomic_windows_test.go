//go:build windows

package stack

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestAtomicPublication_HeldWindowsReader(t *testing.T) {
	for _, tt := range []struct {
		name    string
		replace bool
	}{
		{"replace", true},
		{"exclusive", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state with spaces")
			original, replacement := []byte("original recovery state"), []byte("new state")
			require.NoError(t, WriteAtomic(path, original))
			name, err := windowsFilePath(path)
			require.NoError(t, err)
			handle, err := windows.CreateFile(&name[0], windows.GENERIC_READ,
				windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
				nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
			require.NoError(t, err)
			reader := os.NewFile(uintptr(handle), path)
			t.Cleanup(func() { assert.NoError(t, reader.Close()) })

			err = writeFileAtomic(path, replacement, 0644, tt.replace)
			if tt.replace {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, os.ErrExist)
			}
			old, err := io.ReadAll(reader)
			require.NoError(t, err)
			assert.Equal(t, original, old, "the held reader must retain the original file")
			current, err := readStateFile(path)
			require.NoError(t, err)
			if tt.replace {
				assert.Equal(t, replacement, current, "new readers must see the published file")
			} else {
				assert.Equal(t, original, current, "exclusive publication must not replace the target")
			}
			temps, err := filepath.Glob(filepath.Join(dir, ".state with spaces-*"))
			require.NoError(t, err)
			assert.Empty(t, temps)
		})
	}
}

func TestReadStateFile_WindowsSharing(t *testing.T) {
	for _, shareDelete := range []bool{false, true} {
		name := "exclusive handle"
		if shareDelete {
			name = "shared delete handle"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state")
			original, replacement := []byte("original state"), []byte("new state")
			require.NoError(t, WriteAtomic(path, original))
			name, err := windowsFilePath(path)
			require.NoError(t, err)
			var shareMode uint32
			if shareDelete {
				shareMode = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
			}
			handle, err := windows.CreateFile(&name[0], windows.GENERIC_READ|windows.DELETE,
				shareMode, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
			require.NoError(t, err)
			reader := os.NewFile(uintptr(handle), path)
			t.Cleanup(func() { assert.NoError(t, reader.Close()) })

			// An existing DELETE-access handle requires new readers to share delete.
			_, err = os.ReadFile(path)
			require.ErrorIs(t, err, windows.ERROR_SHARING_VIOLATION)
			got, err := ReadStateFile(path)
			if !shareDelete {
				require.ErrorIs(t, err, windows.ERROR_SHARING_VIOLATION)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, original, got)

			require.NoError(t, WriteAtomic(path, replacement))
			got, err = ReadStateFile(path)
			require.NoError(t, err)
			assert.Equal(t, replacement, got)
			old, err := io.ReadAll(reader)
			require.NoError(t, err)
			assert.Equal(t, original, old)
		})
	}
}
