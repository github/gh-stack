package stack

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLock_Basic(t *testing.T) {
	dir := t.TempDir()

	lock, err := Lock(dir)
	require.NoError(t, err)
	require.NotNil(t, lock)

	lock.Unlock()
}

func TestLock_NilUnlockSafe(t *testing.T) {
	// Unlock on nil should not panic.
	var lock *FileLock
	lock.Unlock()
}

func TestLock_BlocksUntilReleased(t *testing.T) {
	dir := t.TempDir()

	lock1, err := Lock(dir)
	require.NoError(t, err)

	acquired := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		lock2, err := Lock(dir)
		if err != nil {
			errCh <- err
			return
		}
		close(acquired)
		lock2.Unlock()
	}()

	// lock2 should be blocked while lock1 is held.
	select {
	case <-acquired:
		t.Fatal("lock2 acquired while lock1 was still held")
	case err := <-errCh:
		t.Fatalf("lock2 failed: %v", err)
	case <-time.After(300 * time.Millisecond):
		// expected — lock2 is waiting
	}

	lock1.Unlock()

	// After releasing lock1, lock2 should acquire promptly.
	select {
	case <-acquired:
		// success
	case err := <-errCh:
		t.Fatalf("lock2 failed after lock1 released: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("lock2 did not acquire after lock1 was released")
	}
}

func TestLock_SerializesConcurrentAccess(t *testing.T) {
	dir := t.TempDir()

	// Write an initial stack file with 0 stacks.
	sf := &StackFile{SchemaVersion: 1, Stacks: []Stack{}}
	require.NoError(t, Save(dir, sf))

	// Run 10 concurrent goroutines, each adding a stack under lock.
	// Uses Lock + Load + writeStackFile for atomic read-modify-write.
	errCh := make(chan error, 10)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			lock, err := Lock(dir)
			if err != nil {
				errCh <- fmt.Errorf("goroutine %d Lock: %w", idx, err)
				return
			}
			defer lock.Unlock()

			loaded, err := Load(dir)
			if err != nil {
				errCh <- fmt.Errorf("goroutine %d Load: %w", idx, err)
				return
			}

			loaded.AddStack(makeStack("main", "branch"))
			if err := writeStackFile(dir, loaded); err != nil {
				errCh <- fmt.Errorf("goroutine %d writeStackFile: %w", idx, err)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}

	// All 10 stacks should be present — no lost updates.
	final, err := Load(dir)
	require.NoError(t, err)
	assert.Len(t, final.Stacks, 10, "all concurrent writes should be preserved")
}

func TestLock_FileLeftOnDisk(t *testing.T) {
	dir := t.TempDir()

	lock, err := Lock(dir)
	require.NoError(t, err)
	lock.Unlock()

	// Lock file should still exist after unlock (no os.Remove race).
	_, err = os.Stat(filepath.Join(dir, lockFileName))
	require.NoError(t, err, "lock file should remain on disk after unlock")

	lock2, err := Lock(dir)
	require.NoError(t, err, "should be able to re-lock after unlock")
	lock2.Unlock()
}

func TestLock_TimesOut(t *testing.T) {
	dir := t.TempDir()

	// Hold the lock so the second attempt can never acquire it.
	lock1, err := Lock(dir)
	require.NoError(t, err)
	defer lock1.Unlock()

	// Save original timeout and set a short one for the test.
	origTimeout := LockTimeout
	LockTimeout = 200 * time.Millisecond
	defer func() { LockTimeout = origTimeout }()

	start := time.Now()
	lock2, err := Lock(dir)
	elapsed := time.Since(start)

	assert.Nil(t, lock2, "should not acquire lock")
	require.Error(t, err)

	var lockErr *LockError
	require.True(t, errors.As(err, &lockErr), "error should be *LockError, got %T", err)
	assert.Contains(t, lockErr.Error(), "timed out")

	// Should have waited roughly LockTimeout before giving up.
	assert.GreaterOrEqual(t, elapsed, 150*time.Millisecond, "should wait near the timeout")
}

func TestSave_DetectsStaleFile(t *testing.T) {
	dir := t.TempDir()

	// Write an initial stack file.
	sf := &StackFile{SchemaVersion: 1, Stacks: []Stack{}}
	require.NoError(t, Save(dir, sf))

	// Load — captures the on-disk checksum.
	loaded, err := Load(dir)
	require.NoError(t, err)

	// Simulate another process: load, modify, save.
	other, err := Load(dir)
	require.NoError(t, err)
	other.AddStack(makeStack("main", "sneaky"))
	require.NoError(t, Save(dir, other))

	// Our loaded copy tries to save — should detect staleness.
	loaded.AddStack(makeStack("main", "my-branch"))
	err = Save(dir, loaded)
	require.Error(t, err)

	var staleErr *StaleError
	require.True(t, errors.As(err, &staleErr), "error should be *StaleError, got %T", err)
	assert.Contains(t, staleErr.Error(), "modified by another process")
}

func TestSave_AllowsWriteWhenFileUnchanged(t *testing.T) {
	dir := t.TempDir()

	// Write, load, modify, save — no concurrent changes.
	sf := &StackFile{SchemaVersion: 1, Stacks: []Stack{}}
	require.NoError(t, Save(dir, sf))

	loaded, err := Load(dir)
	require.NoError(t, err)

	loaded.AddStack(makeStack("main", "feature"))
	require.NoError(t, Save(dir, loaded))

	// Verify the write actually persisted.
	final, err := Load(dir)
	require.NoError(t, err)
	assert.Len(t, final.Stacks, 1)
}

func TestSave_AllowsFirstWrite(t *testing.T) {
	dir := t.TempDir()

	// File doesn't exist — Load returns nil checksum, Save should succeed.
	sf, err := Load(dir)
	require.NoError(t, err)
	assert.Empty(t, sf.Stacks)

	sf.AddStack(makeStack("main", "first"))
	require.NoError(t, Save(dir, sf), "first save to a new file should succeed")

	final, err := Load(dir)
	require.NoError(t, err)
	assert.Len(t, final.Stacks, 1)
}

func TestSave_DoubleSaveSucceeds(t *testing.T) {
	dir := t.TempDir()

	sf, err := Load(dir)
	require.NoError(t, err)

	sf.AddStack(makeStack("main", "first"))
	require.NoError(t, Save(dir, sf), "first save should succeed")

	// A second Save on the same instance must not spuriously fail —
	// writeStackFile refreshes loadChecksum after writing.
	sf.AddStack(makeStack("main", "second"))
	require.NoError(t, Save(dir, sf), "second save on same instance should succeed")

	final, err := Load(dir)
	require.NoError(t, err)
	assert.Len(t, final.Stacks, 2)
}
