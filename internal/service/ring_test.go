package service

import (
	"bytes"
	"testing"
	"time"
)

func TestRingBufferPrunesByCaptureTime(t *testing.T) {
	ring := NewRingBuffer(2 * time.Second)
	base := time.Now()
	ring.Append(Frame{CapturedAt: base, Data: []byte("old")})
	ring.Append(Frame{CapturedAt: base.Add(time.Second), Data: []byte("middle")})
	ring.Append(Frame{CapturedAt: base.Add(3 * time.Second), Data: []byte("new")})

	frames := ring.Snapshot()
	if len(frames) != 2 {
		t.Fatalf("Snapshot() has %d frames, want 2", len(frames))
	}
	if string(frames[0].Data) != "middle" || string(frames[1].Data) != "new" {
		t.Fatalf("unexpected frames after pruning: %q, %q", frames[0].Data, frames[1].Data)
	}
	count, size, _, _ := ring.Stats()
	if count != 2 || size != int64(len("middle")+len("new")) {
		t.Fatalf("Stats() = (%d, %d), want (2, 9)", count, size)
	}
}

func TestRingBufferClearDiscardsCapturedFrames(t *testing.T) {
	ring := NewRingBuffer(time.Second)
	ring.Append(Frame{CapturedAt: time.Now(), Data: []byte("frame")})
	ring.Clear()
	frames, size, oldest, newest := ring.Stats()
	if frames != 0 || size != 0 || !oldest.IsZero() || !newest.IsZero() || len(ring.Snapshot()) != 0 {
		t.Fatalf("ring was not empty after Clear(): frames=%d size=%d oldest=%v newest=%v", frames, size, oldest, newest)
	}
}

func TestFrameHubSlowSubscriberReceivesNewestFrame(t *testing.T) {
	hub := NewFrameHub()
	frames, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	hub.Publish(Frame{Data: []byte("first")})
	hub.Publish(Frame{Data: []byte("latest")})

	select {
	case frame := <-frames:
		if !bytes.Equal(frame.Data, []byte("latest")) {
			t.Fatalf("subscriber received %q, want latest", frame.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive a frame")
	}
}
