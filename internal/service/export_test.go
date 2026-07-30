package service

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"video-recorder/internal/config"
)

func TestNormalizeFileName(t *testing.T) {
	for _, invalid := range []string{"", "../escape", `folder\\file`, "bad:name", ".mp4"} {
		if _, err := normalizeFileName(invalid); err == nil {
			t.Errorf("normalizeFileName(%q) unexpectedly succeeded", invalid)
		}
	}
	if got, err := normalizeFileName("任务_001.mp4"); err != nil || got != "任务_001" {
		t.Fatalf("normalizeFileName() = %q, %v", got, err)
	}
}

func TestExporterCreatesPlayableMP4(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	directory := t.TempDir()
	cfg := config.Default()
	cfg.Capture.FFmpegPath = ffmpeg
	cfg.Capture.FPS = 10
	cfg.Storage.Directory = directory
	cfg.Storage.Organization = config.StorageOrganizationNone
	buffer := newTestCaptureBuffer(t)
	base := time.Now()
	for i := 0; i < 12; i++ {
		if err := buffer.Append(Frame{CapturedAt: base.Add(time.Duration(i) * 100 * time.Millisecond), Data: testJPEG(t, uint8(i*15))}); err != nil {
			t.Fatal(err)
		}
	}
	exporter := NewExporter(buffer, func() config.Config { return cfg }, discardLogger())
	defer exporter.Close()
	status, err := exporter.Enqueue("integration")
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		current, ok := exporter.Status(status.ID)
		if !ok {
			t.Fatal("export status disappeared")
		}
		if current.State == ExportFailed {
			t.Fatalf("export failed: %s", current.Error)
		}
		if current.State == ExportCompleted {
			data, err := os.ReadFile(filepath.Join(directory, "integration.mp4"))
			if err != nil {
				t.Fatalf("read MP4: %v", err)
			}
			if len(data) < 12 || !bytes.Contains(data[:min(len(data), 64)], []byte("ftyp")) {
				t.Fatalf("output does not look like MP4: %d bytes", len(data))
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("export did not finish before deadline")
}

func TestLiveTranscoderPublishesH264WithoutSaveTimeEncoding(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	directory := t.TempDir()
	cfg := config.Default()
	cfg.Capture.FFmpegPath = ffmpeg
	cfg.Capture.FPS = 10
	cfg.Storage.Directory = directory
	cfg.Storage.Organization = config.StorageOrganizationNone
	cfg.Export.Encoder = config.ExportEncoderSoftware
	cfg.Export.SoftwareThreads = 1

	buffer := newTestCaptureBuffer(t)
	recording, err := NewRecordingSession(buffer, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	transcoder := NewLiveTranscoder(func() config.Config { return cfg }, discardLogger())
	recording.SetTranscoder(transcoder)
	exporter := NewExporter(buffer, func() config.Config { return cfg }, discardLogger())
	defer func() {
		recording.Close()
		exporter.Close()
	}()

	if err := recording.Start(); err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	for i := 0; i < 12; i++ {
		if err := recording.Record(Frame{CapturedAt: base.Add(time.Duration(i) * 100 * time.Millisecond), Data: testJPEG(t, uint8(i*15))}); err != nil {
			t.Fatal(err)
		}
	}
	if status := recording.Status().Transcode; status.State != TranscodeRunning || status.Decoder != "mjpeg" || status.Encoder != "libx264" {
		t.Fatalf("live transcode status = %#v", status)
	}
	job, err := recording.Save(exporter, "live-integration")
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := exporter.Status(job.ID)
		if current.State == ExportFailed {
			t.Fatalf("live export failed: %s", current.Error)
		}
		if current.State != ExportCompleted {
			time.Sleep(25 * time.Millisecond)
			continue
		}
		if current.Encoder != "libx264" || current.FallbackReason != "" {
			t.Fatalf("live export status = %#v", current)
		}
		output, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name", "-of", "default=noprint_wrappers=1:nokey=1", filepath.Join(directory, "live-integration.mp4")).Output()
		if err != nil {
			t.Fatalf("probe live MP4: %v", err)
		}
		if strings.TrimSpace(string(output)) != "h264" {
			t.Fatalf("live output codec = %q, want h264", output)
		}
		return
	}
	t.Fatal("live export did not finish before deadline")
}

func TestExporterFallsBackWhenLiveTranscodeFails(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	directory := t.TempDir()
	cfg := config.Default()
	cfg.Capture.FFmpegPath = ffmpeg
	cfg.Capture.FPS = 10
	cfg.Storage.Directory = directory
	cfg.Storage.Organization = config.StorageOrganizationNone
	cfg.Export.SoftwareThreads = 1
	buffer := newTestCaptureBuffer(t)
	base := time.Now()
	for i := 0; i < 12; i++ {
		if err := buffer.Append(Frame{CapturedAt: base.Add(time.Duration(i) * 100 * time.Millisecond), Data: testJPEG(t, uint8(i*15))}); err != nil {
			t.Fatal(err)
		}
	}
	livePath := filepath.Join(directory, "failed-live.part.mp4")
	if err := os.WriteFile(livePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan liveEncodingResult, 1)
	done <- liveEncodingResult{path: livePath, encoder: "h264_hardware", err: errors.New("hardware encoder stopped")}
	close(done)
	exporter := NewExporter(buffer, func() config.Config { return cfg }, discardLogger())
	defer exporter.Close()
	status, err := exporter.enqueue("fallback-integration", func() *liveEncoding {
		return &liveEncoding{path: livePath, encoder: "h264_hardware", done: done, cancel: func() {}}
	})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := exporter.Status(status.ID)
		if current.State == ExportFailed {
			t.Fatalf("fallback export failed: %s", current.Error)
		}
		if current.State != ExportCompleted {
			time.Sleep(25 * time.Millisecond)
			continue
		}
		if current.Encoder != "libx264" || !strings.Contains(current.FallbackReason, "hardware encoder stopped") {
			t.Fatalf("fallback export status = %#v", current)
		}
		return
	}
	t.Fatal("fallback export did not finish before deadline")
}

func testJPEG(t *testing.T, value uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			img.SetRGBA(x, y, color.RGBA{R: value, G: uint8(x), B: uint8(y), A: 255})
		}
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestExporterRejectsEmptyBuffer(t *testing.T) {
	cfg := config.Default()
	cfg.Storage.Directory = t.TempDir()
	exporter := NewExporter(newTestCaptureBuffer(t), func() config.Config { return cfg }, discardLogger())
	defer exporter.Close()
	if _, err := exporter.Enqueue("empty"); err != ErrNoFrames {
		t.Fatalf("Enqueue() error = %v, want ErrNoFrames", err)
	}
}

func TestExporterAppendsSequenceToDuplicateFileName(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"recording.mp4", "recording_01.mp4"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("existing"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.Capture.FFmpegPath = filepath.Join(directory, "missing-ffmpeg")
	cfg.Storage.Directory = directory
	cfg.Storage.Organization = config.StorageOrganizationNone
	buffer := newTestCaptureBuffer(t)
	if err := buffer.Append(Frame{CapturedAt: time.Now(), Data: testJPEG(t, 1)}); err != nil {
		t.Fatal(err)
	}
	exporter := NewExporter(buffer, func() config.Config { return cfg }, discardLogger())
	defer exporter.Close()
	status, err := exporter.Enqueue("recording.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if status.FileName != "recording_02.mp4" {
		t.Fatalf("Enqueue() file name = %q, want recording_02.mp4", status.FileName)
	}
}

func TestOrganizedStorageDirectory(t *testing.T) {
	base := t.TempDir()
	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.Local)
	tests := []struct {
		organization string
		suffix       string
	}{
		{organization: config.StorageOrganizationDay, suffix: "20260729"},
		{organization: config.StorageOrganizationMonth, suffix: "202607"},
		{organization: config.StorageOrganizationNone, suffix: ""},
	}
	for _, test := range tests {
		t.Run(test.organization, func(t *testing.T) {
			directory, err := organizedStorageDirectory(config.StorageConfig{Directory: base, Organization: test.organization}, now)
			if err != nil {
				t.Fatal(err)
			}
			want := base
			if test.suffix != "" {
				want = filepath.Join(base, test.suffix)
			}
			if directory != want {
				t.Fatalf("organizedStorageDirectory() = %q, want %q", directory, want)
			}
		})
	}
}

func TestLimitedStringWriterReportsConsumedInput(t *testing.T) {
	var output strings.Builder
	writer := &limitedStringWriter{builder: &output, limit: 3}
	n, err := writer.Write([]byte("abcdef"))
	if err != nil || n != 6 || output.String() != "abc" {
		t.Fatalf("Write() = (%d, %v), output %q", n, err, output.String())
	}
}
