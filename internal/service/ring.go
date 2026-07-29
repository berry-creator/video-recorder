package service

import (
	"sync"
	"time"
)

type RingBuffer struct {
	mu       sync.RWMutex
	duration time.Duration
	frames   []Frame
	bytes    int64
}

func NewRingBuffer(duration time.Duration) *RingBuffer {
	return &RingBuffer{duration: duration}
}

func (r *RingBuffer) SetDuration(duration time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.duration = duration
	r.pruneLocked(time.Now())
}

func (r *RingBuffer) Append(frame Frame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, frame)
	r.bytes += int64(len(frame.Data))
	r.pruneLocked(frame.CapturedAt)
}

func (r *RingBuffer) Snapshot() []Frame {
	r.mu.RLock()
	defer r.mu.RUnlock()
	frames := make([]Frame, len(r.frames))
	copy(frames, r.frames)
	return frames
}

func (r *RingBuffer) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = nil
	r.bytes = 0
}

func (r *RingBuffer) Stats() (frames int, bytes int64, oldest time.Time, newest time.Time) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.frames) == 0 {
		return 0, 0, time.Time{}, time.Time{}
	}
	return len(r.frames), r.bytes, r.frames[0].CapturedAt, r.frames[len(r.frames)-1].CapturedAt
}

func (r *RingBuffer) pruneLocked(now time.Time) {
	cutoff := now.Add(-r.duration)
	remove := 0
	for remove < len(r.frames) && r.frames[remove].CapturedAt.Before(cutoff) {
		r.bytes -= int64(len(r.frames[remove].Data))
		remove++
	}
	if remove == 0 {
		return
	}
	copy(r.frames, r.frames[remove:])
	r.frames = r.frames[:len(r.frames)-remove]
	if len(r.frames) == 0 {
		r.frames = nil
	}
}
