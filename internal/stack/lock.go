package stack

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	lockFileName          = "gh-stack.lock"
	operationLockFileName = "gh-stack-operation.lock"
)

// LockError is returned when the catalog or operation lock times out.
// Callers can check for this with errors.As to distinguish lock failures
// from other errors.
type LockError struct {
	Err error
}

func (e *LockError) Error() string { return e.Err.Error() }
func (e *LockError) Unwrap() error { return e.Err }

// StaleError is returned when the stack file was modified on disk since it
// was loaded.  This indicates another process wrote to the file concurrently.
// Callers can check for this with errors.As.
type StaleError struct {
	Err error
}

func (e *StaleError) Error() string { return e.Err.Error() }
func (e *StaleError) Unwrap() error { return e.Err }

// LockTimeout is how long Lock and LockOperation wait for an exclusive lock.
var LockTimeout = 5 * time.Second

// lockRetryInterval is the sleep between non-blocking lock attempts.
const lockRetryInterval = 100 * time.Millisecond

// FileLock provides an exclusive advisory catalog or operation lock.
type FileLock struct {
	f *os.File
}

// Lock acquires an exclusive lock on the stack file in the given git directory.
// It retries with a non-blocking attempt every 100ms for up to LockTimeout.
//
// Most callers should not use Lock directly — stack.Save() acquires the lock
// automatically.  Use Lock only when you need to hold the lock across multiple
// operations (e.g. Load-Modify-SaveWithLock as an atomic unit).
func Lock(gitDir string) (*FileLock, error) {
	lock, _, err := acquireLock(filepath.Join(gitDir, lockFileName), "stack", true)
	return lock, err
}

// LockOperation serializes gh-stack mutations across the clone. Acquire it
// before loading mutation snapshots and before taking the catalog lock.
// Unlike the catalog lock, it may be held across Git operations.
func LockOperation(commonDir string) (*FileLock, error) {
	lock, _, err := acquireLock(filepath.Join(commonDir, operationLockFileName), "stack operation", true)
	return lock, err
}

// TryLockOperation attempts to acquire the operation lock without waiting.
// A false result with no error means contention; other failures are returned.
func TryLockOperation(commonDir string) (*FileLock, bool, error) {
	return acquireLock(filepath.Join(commonDir, operationLockFileName), "stack operation", false)
}

func acquireLock(path, name string, wait bool) (*FileLock, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, false, fmt.Errorf("opening lock file: %w", err)
	}

	deadline := time.Now().Add(LockTimeout)
	for {
		err := tryLockFile(f)
		if err == nil {
			return &FileLock{f: f}, true, nil
		}
		if !isLockBusy(err) {
			return nil, false, fmt.Errorf("locking %s file: %w", name, errors.Join(err, f.Close()))
		}
		if !wait {
			if err := f.Close(); err != nil {
				return nil, false, fmt.Errorf("closing lock file: %w", err)
			}
			return nil, false, nil
		}
		if time.Now().After(deadline) {
			return nil, false, &LockError{Err: errors.Join(fmt.Errorf(
				"timed out waiting for %s lock after %s — another gh-stack process may be running", name, LockTimeout), f.Close())}
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
	l.f = nil
}
