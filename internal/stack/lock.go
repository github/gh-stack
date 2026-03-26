package stack

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const lockFileName = "gh-stack.lock"

// LockError is returned when the stack file lock cannot be acquired.
// Callers can check for this with errors.As to distinguish lock failures
// from other errors.
type LockError struct {
	Err error
}

func (e *LockError) Error() string { return e.Err.Error() }
func (e *LockError) Unwrap() error { return e.Err }

// LockTimeout is how long Lock() will wait for the exclusive lock before
// giving up.  With the lock held only during file writes (milliseconds),
// this timeout primarily guards against a hung process holding the lock.
const LockTimeout = 5 * time.Second

// lockRetryInterval is the sleep between non-blocking lock attempts.
const lockRetryInterval = 100 * time.Millisecond

// FileLock provides an exclusive advisory lock on the stack file to prevent
// concurrent writes between multiple gh-stack processes.
type FileLock struct {
	f *os.File
}

// Lock acquires an exclusive lock on the stack file in the given git directory.
// It retries with a non-blocking attempt every 100ms for up to LockTimeout.
//
// Most callers should not use Lock directly — stack.Save() acquires the lock
// automatically.  Use Lock only when you need to hold the lock across multiple
// operations (e.g. Load-Modify-Save as an atomic unit).
func Lock(gitDir string) (*FileLock, error) {
	path := filepath.Join(gitDir, lockFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}

	deadline := time.Now().Add(LockTimeout)
	for {
		err := tryLockFile(f)
		if err == nil {
			return &FileLock{f: f}, nil
		}
		if !isLockBusy(err) {
			// Unexpected error (e.g. bad fd) — don't retry.
			f.Close()
			return nil, fmt.Errorf("locking stack file: %w", err)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, &LockError{Err: fmt.Errorf(
				"timed out waiting for stack lock after %s — another gh-stack process may be running", LockTimeout)}
		}
		time.Sleep(lockRetryInterval)
	}
}

// Unlock releases the lock.  The lock file is intentionally left on disk to
// avoid a race where another process opens the same path, blocks on flock,
// then wakes up holding a lock on an unlinked inode while a third process
// creates a new file and locks a different inode.
func (l *FileLock) Unlock() {
	if l == nil || l.f == nil {
		return
	}
	unlockFile(l.f)
	l.f.Close()
}
