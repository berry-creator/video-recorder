package main

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestExclusiveFileLockRejectsEverySecondInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	first := acquireTestLock(t, path, false)
	assertLockRejected(t, path, false)
	assertLockRejected(t, path, true)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reacquired := acquireTestLock(t, path, false)
	if err := reacquired.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSharedFileLocksAllowMultipleInstancesButRejectExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	first := acquireTestLock(t, path, true)
	second := acquireTestLock(t, path, true)
	assertLockRejected(t, path, false)
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
}

func acquireTestLock(t *testing.T, path string, shared bool) *instanceGuard {
	t.Helper()
	guard, err := acquireFileLock(path, shared)
	if err != nil {
		t.Fatal(err)
	}
	return guard
}

func assertLockRejected(t *testing.T, path string, shared bool) {
	t.Helper()
	guard, err := acquireFileLock(path, shared)
	if guard != nil {
		_ = guard.Close()
	}
	if !errors.Is(err, ErrInstanceAlreadyRunning) {
		t.Fatalf("second lock error = %v, want %v", err, ErrInstanceAlreadyRunning)
	}
}
