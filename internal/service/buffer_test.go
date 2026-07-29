package service

import (
	"os"
	"testing"
	"time"
)

func newTestCaptureBuffer(t *testing.T) *CaptureBuffer {
	return newTestCaptureBufferWithDuration(t, 30*time.Second)
}

func newTestCaptureBufferWithDuration(t *testing.T, duration time.Duration) *CaptureBuffer {
	t.Helper()
	buffer, err := NewCaptureBuffer(duration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := buffer.Close(); err != nil {
			t.Error(err)
		}
	})
	return buffer
}

func TestCaptureBufferRetainsFramesUntilDetached(t *testing.T) {
	buffer := newTestCaptureBuffer(t)
	base := time.Now()
	for _, frame := range []Frame{
		{CapturedAt: base, Data: []byte("old")},
		{CapturedAt: base.Add(time.Minute), Data: []byte("middle")},
		{CapturedAt: base.Add(time.Hour), Data: []byte("new")},
	} {
		if err := buffer.Append(frame); err != nil {
			t.Fatal(err)
		}
	}

	segment, err := buffer.Detach()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(segment.path)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(segment.path)
	if string(data) != "oldmiddlenew" || segment.frames != 3 || segment.bytes != int64(len(data)) {
		t.Fatalf("detached segment = %#v, data = %q", segment, data)
	}
	stats := buffer.Stats()
	if stats.Frames != 0 || stats.Bytes != 0 || !stats.Oldest.IsZero() || !stats.Newest.IsZero() {
		t.Fatalf("active segment after detach = %#v", stats)
	}
}

func TestCaptureBufferClearDiscardsCurrentSegment(t *testing.T) {
	buffer := newTestCaptureBufferWithDuration(t, time.Second)
	base := time.Now()
	if err := buffer.Append(Frame{CapturedAt: base, Data: []byte("frame-1")}); err != nil {
		t.Fatal(err)
	}
	if err := buffer.Append(Frame{CapturedAt: base.Add(time.Second), Data: []byte("frame-2")}); err != nil {
		t.Fatal(err)
	}
	path := buffer.path
	if err := buffer.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cleared segment still exists: %v", err)
	}
	stats := buffer.Stats()
	if stats.Frames != 0 || stats.Bytes != 0 || !stats.Oldest.IsZero() || !stats.Newest.IsZero() {
		t.Fatalf("buffer was not empty after Clear(): %#v", stats)
	}
}

func TestCaptureBufferFlushesMemoryInConfiguredBatches(t *testing.T) {
	buffer := newTestCaptureBufferWithDuration(t, 2*time.Second)
	base := time.Now()
	for index, offset := range []time.Duration{0, time.Second} {
		if err := buffer.Append(Frame{CapturedAt: base.Add(offset), Data: []byte{byte(index + 1)}}); err != nil {
			t.Fatal(err)
		}
	}
	before := buffer.Stats()
	if buffer.path != "" || before.MemoryBytes != 2 || before.DiskBytes != 0 {
		t.Fatalf("buffer flushed before its configured duration: path=%q stats=%#v", buffer.path, before)
	}
	if err := buffer.Append(Frame{CapturedAt: base.Add(2 * time.Second), Data: []byte{3}}); err != nil {
		t.Fatal(err)
	}
	after := buffer.Stats()
	if buffer.path == "" || after.MemoryBytes != 0 || after.DiskBytes != 3 || after.Bytes != 3 {
		t.Fatalf("buffer did not flush as one batch: path=%q stats=%#v", buffer.path, after)
	}
	if err := buffer.Append(Frame{CapturedAt: base.Add(3 * time.Second), Data: []byte{4}}); err != nil {
		t.Fatal(err)
	}
	if err := buffer.SetMemoryDuration(time.Second); err != nil {
		t.Fatal(err)
	}
	if stats := buffer.Stats(); stats.MemoryBytes != 1 || stats.DiskBytes != 3 {
		t.Fatalf("duration update flushed a batch younger than the new duration: %#v", stats)
	}
}
