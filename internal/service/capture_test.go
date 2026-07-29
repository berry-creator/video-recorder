package service

import (
	"bytes"
	"context"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"video-recorder/internal/config"
)

func TestCaptureArgsIncludeCurrentTimeWatermark(t *testing.T) {
	args, err := captureArgs(config.Default().Capture, true)
	if err != nil {
		t.Fatal(err)
	}
	filter := argumentAfter(args, "-vf")
	for _, expected := range []string{"%{localtime}", "x=w-tw-12", "y=12"} {
		if !strings.Contains(filter, expected) {
			t.Errorf("watermark filter %q does not contain %q", filter, expected)
		}
	}
	if strings.Contains(filter, "Current") {
		t.Fatalf("watermark filter still contains the Current prefix: %q", filter)
	}
	if strings.Contains(filter, "Started") {
		t.Fatalf("watermark filter still contains capture start time: %q", filter)
	}
}

func TestCaptureArgsOmitWatermarkWhenDrawtextIsUnavailable(t *testing.T) {
	args, err := captureArgs(config.Default().Capture, false)
	if err != nil {
		t.Fatal(err)
	}
	if filter := argumentAfter(args, "-vf"); filter != "" {
		t.Fatalf("capture filter = %q, want no watermark filter", filter)
	}
}

func TestCameraInputArgsUseSelectedPixelFormat(t *testing.T) {
	cfg := config.Default().Capture
	cfg.Source = "camera"
	cfg.Device = "0"
	cfg.PixelFormat = "nv12"
	args, err := cameraInputArgs("darwin", cfg, "1280x720", "30")
	if err != nil {
		t.Fatal(err)
	}
	if argumentAfter(args, "-pixel_format") != "nv12" || argumentAfter(args, "-framerate") != "30" || argumentAfter(args, "-i") != "0:none" {
		t.Fatalf("macOS camera args = %#v", args)
	}
}

func TestWindowsCameraInputArgsUseSelectedVideoCodec(t *testing.T) {
	cfg := config.Default().Capture
	cfg.Source = "camera"
	cfg.Device = `@device_pnp_\\?\usb#vid_046d&pid_0825`
	cfg.VideoCodec = "mjpeg"
	args, err := cameraInputArgs("windows", cfg, "1280x720", "30")
	if err != nil {
		t.Fatal(err)
	}
	if argumentAfter(args, "-vcodec") != "mjpeg" || argumentAfter(args, "-pixel_format") != "" || argumentAfter(args, "-i") != "video="+cfg.Device {
		t.Fatalf("Windows camera args = %#v", args)
	}
}

func TestCameraInputArgsAllowDeviceDefaultFormat(t *testing.T) {
	cfg := config.Default().Capture
	cfg.Source = "camera"
	cfg.Device = "0"
	args, err := cameraInputArgs("darwin", cfg, "1280x720", "30")
	if err != nil {
		t.Fatal(err)
	}
	if argumentAfter(args, "-pixel_format") != "" || argumentAfter(args, "-framerate") != "" || argumentAfter(args, "-video_size") != "" || argumentAfter(args, "-i") != "0:none" {
		t.Fatalf("macOS default-format camera args = %#v", args)
	}
}

func TestMacOSCameraInputArgsIgnoreUnsupportedVideoCodec(t *testing.T) {
	cfg := config.Default().Capture
	cfg.Source = "camera"
	cfg.Device = "0"
	cfg.VideoCodec = "mjpeg"
	args, err := cameraInputArgs("darwin", cfg, "1280x720", "30")
	if err != nil {
		t.Fatal(err)
	}
	if argumentAfter(args, "-vcodec") != "" || argumentAfter(args, "-pixel_format") != "" || argumentAfter(args, "-i") != "0:none" {
		t.Fatalf("macOS fallback camera args = %#v", args)
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
	buffer := newTestCaptureBuffer(t)
	recording, err := NewRecordingSession(buffer, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer recording.Close()
	if err := recording.Start(); err != nil {
		t.Fatal(err)
	}
	capture := NewCaptureService(recording, NewFrameHub(), discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := capture.Start(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	defer capture.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stats := buffer.Stats()
		if stats.Frames > 0 {
			segment, err := buffer.Detach()
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(segment.path)
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(segment.path)
			if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil {
				t.Fatalf("decode captured JPEG: %v", err)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("capture did not produce a frame: %s", capture.Status().LastError)
}

func TestSegmentBoundariesDoNotRestartCaptureProcess(t *testing.T) {
	buffer := newTestCaptureBuffer(t)
	recording, err := NewRecordingSession(buffer, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer recording.Close()
	capture := NewCaptureService(recording, NewFrameHub(), discardLogger())
	cfg := config.Default().Capture
	cfg.FFmpegPath = filepath.Join(t.TempDir(), "missing-ffmpeg")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := capture.Start(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	defer capture.Stop()

	capture.mu.RLock()
	processDone := capture.done
	capture.mu.RUnlock()
	if err := recording.Start(); err != nil {
		t.Fatal(err)
	}
	if err := recording.Record(Frame{CapturedAt: time.Now(), Data: []byte("discarded")}); err != nil {
		t.Fatal(err)
	}
	if err := recording.Start(); err != nil {
		t.Fatal(err)
	}

	capture.mu.RLock()
	currentDone := capture.done
	capture.mu.RUnlock()
	if currentDone != processDone {
		t.Fatal("logical segment boundary replaced the capture process")
	}
}

func argumentAfter(args []string, name string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1]
		}
	}
	return ""
}
