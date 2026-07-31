package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var ErrInstanceAlreadyRunning = errors.New("another video-recorder instance is already running")

func acquireSingleInstance(allowMultiple bool) (*instanceGuard, error) {
	directory, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("resolve single-instance directory: %w", err)
	}
	directory = filepath.Join(directory, "video-recorder")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create single-instance directory: %w", err)
	}
	return acquireFileLock(filepath.Join(directory, "instance.lock"), allowMultiple)
}

type instanceGuard struct {
	once    sync.Once
	release func() error
	err     error
}

func newInstanceGuard(release func() error) *instanceGuard {
	return &instanceGuard{release: release}
}

func (g *instanceGuard) Close() error {
	if g == nil {
		return nil
	}
	g.once.Do(func() {
		if g.release != nil {
			g.err = g.release()
		}
	})
	return g.err
}
