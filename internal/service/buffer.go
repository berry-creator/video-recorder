package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type captureSegment struct {
	path       string
	frames     int
	bytes      int64
	oldest     time.Time
	newest     time.Time
	detachedAt time.Time
}

type CaptureBufferStats struct {
	Frames      int
	Bytes       int64
	MemoryBytes int64
	DiskBytes   int64
	Oldest      time.Time
	Newest      time.Time
}

// CaptureBuffer batches the active recording in memory and temporary storage.
// Detach atomically hands a completed recording to the exporter.
type CaptureBuffer struct {
	mu             sync.Mutex
	directory      string
	memoryDuration time.Duration
	file           *os.File
	path           string
	pending        []byte
	pendingAt      time.Time
	diskBytes      int64
	frames         int
	bytes          int64
	oldest         time.Time
	newest         time.Time
	closed         bool
}

func NewCaptureBuffer(memoryDuration time.Duration) (*CaptureBuffer, error) {
	if memoryDuration <= 0 {
		return nil, errors.New("memory buffer duration must be positive")
	}
	directory, err := os.MkdirTemp("", "video-recorder-capture-*")
	if err != nil {
		return nil, fmt.Errorf("create capture spool directory: %w", err)
	}
	return &CaptureBuffer{directory: directory, memoryDuration: memoryDuration}, nil
}

func (b *CaptureBuffer) Append(frame Frame) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("capture buffer is closed")
	}
	if b.frames == 0 {
		b.oldest = frame.CapturedAt
	}
	if len(b.pending) == 0 {
		b.pendingAt = frame.CapturedAt
	}
	b.pending = append(b.pending, frame.Data...)
	b.frames++
	b.bytes += int64(len(frame.Data))
	b.newest = frame.CapturedAt
	if frame.CapturedAt.Sub(b.pendingAt) >= b.memoryDuration {
		return b.flushLocked()
	}
	return nil
}

func (b *CaptureBuffer) SetMemoryDuration(duration time.Duration) error {
	if duration <= 0 {
		return errors.New("memory buffer duration must be positive")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("capture buffer is closed")
	}
	b.memoryDuration = duration
	if len(b.pending) > 0 && b.newest.Sub(b.pendingAt) >= duration {
		return b.flushLocked()
	}
	return nil
}

func (b *CaptureBuffer) Detach() (captureSegment, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return captureSegment{}, errors.New("capture buffer is closed")
	}
	if b.frames == 0 || b.file == nil {
		if b.frames == 0 {
			return captureSegment{}, ErrNoFrames
		}
	}
	if err := b.flushLocked(); err != nil {
		return captureSegment{}, err
	}
	if err := b.file.Close(); err != nil {
		return captureSegment{}, fmt.Errorf("close captured segment: %w", err)
	}
	segment := captureSegment{
		path:       b.path,
		frames:     b.frames,
		bytes:      b.bytes,
		oldest:     b.oldest,
		newest:     b.newest,
		detachedAt: time.Now(),
	}
	b.resetLocked()
	return segment, nil
}

func (b *CaptureBuffer) Clear() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("capture buffer is closed")
	}
	var closeErr error
	if b.file != nil {
		closeErr = b.file.Close()
	}
	removeErr := error(nil)
	if b.path != "" {
		removeErr = os.Remove(b.path)
	}
	b.resetLocked()
	if err := errors.Join(closeErr, removeErr); err != nil {
		return fmt.Errorf("clear captured segment: %w", err)
	}
	return nil
}

func (b *CaptureBuffer) Stats() CaptureBufferStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return CaptureBufferStats{
		Frames:      b.frames,
		Bytes:       b.bytes,
		MemoryBytes: int64(len(b.pending)),
		DiskBytes:   b.diskBytes,
		Oldest:      b.oldest,
		Newest:      b.newest,
	}
}

func (b *CaptureBuffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	var closeErr error
	if b.file != nil {
		closeErr = b.file.Close()
	}
	b.resetLocked()
	removeErr := os.RemoveAll(b.directory)
	if err := errors.Join(closeErr, removeErr); err != nil {
		return fmt.Errorf("close capture buffer: %w", err)
	}
	return nil
}

func (b *CaptureBuffer) ensureFileLocked() error {
	if b.file != nil {
		return nil
	}
	file, err := os.CreateTemp(b.directory, "segment-*.mjpeg")
	if err != nil {
		return fmt.Errorf("create captured segment: %w", err)
	}
	b.file = file
	b.path = filepath.Clean(file.Name())
	return nil
}

func (b *CaptureBuffer) resetLocked() {
	b.file = nil
	b.path = ""
	b.pending = nil
	b.pendingAt = time.Time{}
	b.diskBytes = 0
	b.frames = 0
	b.bytes = 0
	b.oldest = time.Time{}
	b.newest = time.Time{}
}

func (b *CaptureBuffer) flushLocked() error {
	if len(b.pending) == 0 {
		return nil
	}
	if err := b.ensureFileLocked(); err != nil {
		return err
	}
	written, err := b.file.Write(b.pending)
	if err != nil || written != len(b.pending) {
		_ = b.file.Truncate(b.diskBytes)
		_, _ = b.file.Seek(0, io.SeekEnd)
		if err != nil {
			return fmt.Errorf("write captured frames: %w", err)
		}
		return fmt.Errorf("write captured frames: %w", io.ErrShortWrite)
	}
	b.diskBytes += int64(written)
	b.pending = nil
	b.pendingAt = time.Time{}
	return nil
}
