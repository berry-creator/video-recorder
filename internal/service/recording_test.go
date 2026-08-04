package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"video-recorder/internal/config"
)

func TestRecordingSessionIgnoresFramesUntilStarted(t *testing.T) {
	buffer := newTestCaptureBuffer(t)
	session := newTestRecordingSession(t, buffer, time.Minute)
	if err := session.Record(Frame{CapturedAt: time.Now(), Data: []byte("preview")}); err != nil {
		t.Fatal(err)
	}
	if stats := buffer.Stats(); stats.Frames != 0 || stats.Bytes != 0 {
		t.Fatalf("preview frame was stored: %#v", stats)
	}
}

func TestRecordingSessionStartAndSaveReturnsToPreview(t *testing.T) {
	buffer := newTestCaptureBuffer(t)
	session := newTestRecordingSession(t, buffer, time.Minute)
	if err := session.Start(""); err != nil {
		t.Fatal(err)
	}
	if err := session.Record(Frame{CapturedAt: time.Now(), Data: []byte("frame")}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Capture.FFmpegPath = filepath.Join(t.TempDir(), "missing-ffmpeg")
	cfg.Storage.Directory = t.TempDir()
	cfg.Storage.Organization = config.StorageOrganizationNone
	exporter := NewExporter(buffer, func() config.Config { return cfg }, discardLogger())
	defer exporter.Close()
	status, err := session.Save(exporter, "recording")
	if err != nil {
		t.Fatal(err)
	}
	if status.FrameCount != 1 || session.Status().State != RecordingPreviewing {
		t.Fatalf("save status = %#v, recording = %#v", status, session.Status())
	}
	if stats := buffer.Stats(); stats.Frames != 0 || stats.Bytes != 0 {
		t.Fatalf("saved recording remains buffered: %#v", stats)
	}
	if _, err := session.Save(exporter, "again"); !errors.Is(err, ErrRecordingNotActive) {
		t.Fatalf("second Save() error = %v, want ErrRecordingNotActive", err)
	}
}

func TestRecordingSessionSaveFailureKeepsRecordingActive(t *testing.T) {
	buffer := newTestCaptureBuffer(t)
	session := newTestRecordingSession(t, buffer, time.Minute)
	if err := session.Start("keep-on-failure"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Storage.Directory = t.TempDir()
	exporter := NewExporter(buffer, func() config.Config { return cfg }, discardLogger())
	defer exporter.Close()
	if _, err := session.Save(exporter, ""); err == nil {
		t.Fatal("Save() accepted an invalid file name")
	}
	if _, err := session.Save(exporter, "empty-recording"); !errors.Is(err, ErrNoFrames) {
		t.Fatalf("Save() error = %v, want ErrNoFrames", err)
	}
	nextMetadata := "must-not-apply"
	if _, err := session.SaveAndContinue(exporter, "empty-continuation", &nextMetadata); !errors.Is(err, ErrNoFrames) {
		t.Fatalf("SaveAndContinue() error = %v, want ErrNoFrames", err)
	}
	if status := session.Status(); status.State != RecordingActive || status.Metadata != "keep-on-failure" {
		t.Fatalf("recording status after failed saves = %#v", status)
	}
}

func TestRecordingSessionSaveAndContinueStartsANewSegment(t *testing.T) {
	buffer := newTestCaptureBuffer(t)
	session := newTestRecordingSession(t, buffer, time.Minute)
	if err := session.Start(`{"caseId":"42"}`); err != nil {
		t.Fatal(err)
	}
	firstStartedAt := session.Status().StartedAt
	if err := session.Record(Frame{CapturedAt: time.Now(), Data: []byte("first")}); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Capture.FFmpegPath = filepath.Join(t.TempDir(), "missing-ffmpeg")
	cfg.Storage.Directory = t.TempDir()
	cfg.Storage.Organization = config.StorageOrganizationNone
	exporter := NewExporter(buffer, func() config.Config { return cfg }, discardLogger())
	defer exporter.Close()
	time.Sleep(time.Millisecond)
	nextMetadata := `{"caseId":"43"}`
	first, err := session.SaveAndContinue(exporter, "first", &nextMetadata)
	if err != nil {
		t.Fatal(err)
	}
	status := session.Status()
	if first.FrameCount != 1 || status.State != RecordingActive || status.Metadata != nextMetadata {
		t.Fatalf("continued recording = status %#v, export %#v", status, first)
	}
	if !status.StartedAt.After(firstStartedAt) {
		t.Fatalf("continued segment started at %v, want after %v", status.StartedAt, firstStartedAt)
	}
	if stats := buffer.Stats(); stats.Frames != 0 || stats.Bytes != 0 {
		t.Fatalf("saved segment remains buffered: %#v", stats)
	}

	if err := session.Record(Frame{CapturedAt: time.Now(), Data: []byte("second")}); err != nil {
		t.Fatal(err)
	}
	second, err := session.SaveAndContinue(exporter, "second", nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.FrameCount != 1 || session.Status().State != RecordingActive || session.Status().Metadata != nextMetadata {
		t.Fatalf("continued save without metadata = status %#v, export %#v", session.Status(), second)
	}

	if err := session.Record(Frame{CapturedAt: time.Now(), Data: []byte("third")}); err != nil {
		t.Fatal(err)
	}
	emptyMetadata := ""
	third, err := session.SaveAndContinue(exporter, "third", &emptyMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if third.FrameCount != 1 || session.Status().State != RecordingActive || session.Status().Metadata != "" {
		t.Fatalf("continued save with empty metadata = status %#v, export %#v", session.Status(), third)
	}

	if err := session.Record(Frame{CapturedAt: time.Now(), Data: []byte("fourth")}); err != nil {
		t.Fatal(err)
	}
	fourth, err := session.Save(exporter, "fourth")
	if err != nil {
		t.Fatal(err)
	}
	if fourth.FrameCount != 1 || session.Status().State != RecordingPreviewing || session.Status().Metadata != "" {
		t.Fatalf("final save = status %#v, export %#v", session.Status(), fourth)
	}
}

func TestRecordingSessionTimeoutClearsMemoryAndTemporaryFile(t *testing.T) {
	buffer := newTestCaptureBufferWithDuration(t, time.Nanosecond)
	session := newTestRecordingSession(t, buffer, 40*time.Millisecond)
	if err := session.Start(""); err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	if err := session.Record(Frame{CapturedAt: base, Data: []byte("disk")}); err != nil {
		t.Fatal(err)
	}
	if err := session.Record(Frame{CapturedAt: base.Add(time.Millisecond), Data: []byte("memory")}); err != nil {
		t.Fatal(err)
	}
	if err := session.Record(Frame{CapturedAt: base.Add(time.Millisecond), Data: []byte("pending")}); err != nil {
		t.Fatal(err)
	}
	path := buffer.path
	if path == "" {
		t.Fatal("recording did not create a temporary file")
	}
	if stats := buffer.Stats(); stats.DiskBytes == 0 || stats.MemoryBytes == 0 {
		t.Fatalf("test recording does not cover disk and memory cleanup: %#v", stats)
	}
	waitForRecordingState(t, session, RecordingTimedOut)
	if stats := buffer.Stats(); stats.Frames != 0 || stats.Bytes != 0 || stats.MemoryBytes != 0 || stats.DiskBytes != 0 {
		t.Fatalf("timed-out recording was not cleared: %#v", stats)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("timed-out temporary file still exists: %v", err)
	}
	if err := session.Start(""); err != nil {
		t.Fatal(err)
	}
	if session.Status().State != RecordingActive {
		t.Fatalf("state after restarting = %q, want recording", session.Status().State)
	}
}

func TestRecordingSessionSetMaxDurationReschedulesActiveTimeout(t *testing.T) {
	buffer := newTestCaptureBuffer(t)
	session := newTestRecordingSession(t, buffer, time.Minute)
	if err := session.Start(""); err != nil {
		t.Fatal(err)
	}
	if err := session.SetMaxDuration(30 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	waitForRecordingState(t, session, RecordingTimedOut)
}

func newTestRecordingSession(t *testing.T, buffer *CaptureBuffer, maximum time.Duration) *RecordingSession {
	t.Helper()
	session, err := NewRecordingSession(buffer, maximum)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	return session
}

func waitForRecordingState(t *testing.T, session *RecordingSession, state RecordingState) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if session.Status().State == state {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("recording state = %q, want %q", session.Status().State, state)
}
