package service

import (
	"bytes"
	"context"
	"image/jpeg"
	"os/exec"
	"strings"
	"testing"
	"time"

	"video-recorder/internal/config"
)

func TestCaptureArgsIncludeTimestampWatermark(t *testing.T) {
	startedAt := time.Date(2026, time.July, 29, 12, 34, 56, 0, time.Local)
	args, err := captureArgs(config.Default().Capture, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	filter := argumentAfter(args, "-vf")
	for _, expected := range []string{"Started", `2026-07-29 12\:34\:56`, "Current", "%{localtime}", "x=w-tw-12"} {
		if !strings.Contains(filter, expected) {
			t.Errorf("watermark filter %q does not contain %q", filter, expected)
		}
	}
}

func TestMockCaptureProducesWatermarkedJPEG(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	cfg := config.Default().Capture
	cfg.FFmpegPath = ffmpeg
	cfg.Width = 320
	cfg.Height = 240
	cfg.FPS = 5
	cfg.BufferSeconds = 2
	ring := NewRingBuffer(2 * time.Second)
	capture := NewCaptureService(ring, NewFrameHub(), discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := capture.Start(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	defer capture.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		frames := ring.Snapshot()
		if len(frames) > 0 {
			if _, err := jpeg.Decode(bytes.NewReader(frames[0].Data)); err != nil {
				t.Fatalf("decode captured JPEG: %v", err)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("capture did not produce a frame: %s", capture.Status().LastError)
}

func argumentAfter(args []string, name string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1]
		}
	}
	return ""
}
