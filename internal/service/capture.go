package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"video-recorder/internal/config"
)

type CaptureStatus struct {
	Running     bool      `json:"running"`
	Connecting  bool      `json:"connecting"`
	Source      string    `json:"source"`
	StartedAt   time.Time `json:"startedAt,omitempty"`
	Frames      uint64    `json:"frames"`
	LastFrameAt time.Time `json:"lastFrameAt,omitempty"`
	LastError   string    `json:"lastError,omitempty"`
	Warning     string    `json:"warning,omitempty"`
	Restarts    uint64    `json:"restarts"`
}

const CaptureWarningWatermarkUnavailable = "watermarkUnavailable"

type CaptureService struct {
	recording *RecordingSession
	hub       *FrameHub
	log       *slog.Logger

	restartMu sync.Mutex
	mu        sync.RWMutex
	root      context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	status    CaptureStatus

	featureMu       sync.Mutex
	drawtextSupport map[string]bool
}

func NewCaptureService(recording *RecordingSession, hub *FrameHub, logger *slog.Logger) *CaptureService {
	return &CaptureService{recording: recording, hub: hub, log: logger, drawtextSupport: make(map[string]bool)}
}

func (s *CaptureService) Start(ctx context.Context, cfg config.CaptureConfig) error {
	s.mu.Lock()
	if s.root != nil {
		s.mu.Unlock()
		return errors.New("capture service is already started")
	}
	s.root = ctx
	s.status.StartedAt = time.Now()
	s.mu.Unlock()
	return s.Reconfigure(cfg)
}

func (s *CaptureService) Reconfigure(cfg config.CaptureConfig) error {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()

	s.stopCurrent()
	return s.startCurrent(cfg)
}

func (s *CaptureService) startCurrent(cfg config.CaptureConfig) error {
	s.mu.Lock()
	if s.root == nil || s.root.Err() != nil {
		s.mu.Unlock()
		return errors.New("capture service is stopped")
	}
	runCtx, cancel := context.WithCancel(s.root)
	done := make(chan struct{})
	s.cancel = cancel
	s.done = done
	s.status.Source = cfg.Source
	s.status.LastError = ""
	if s.status.StartedAt.IsZero() {
		s.status.StartedAt = time.Now()
	}
	s.mu.Unlock()

	go func() {
		defer close(done)
		s.run(runCtx, cfg)
	}()
	return nil
}

func (s *CaptureService) Stop() {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	s.stopCurrent()
	s.mu.Lock()
	s.root = nil
	s.status.Running = false
	s.status.Connecting = false
	s.mu.Unlock()
}

func (s *CaptureService) stopCurrent() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (s *CaptureService) Status() CaptureStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *CaptureService) run(ctx context.Context, cfg config.CaptureConfig) {
	for {
		s.setConnecting(true)
		err := s.captureOnce(ctx, cfg)
		s.setConnecting(false)
		if ctx.Err() != nil {
			s.setRunning(false)
			return
		}
		s.mu.Lock()
		s.status.Running = false
		s.status.LastError = err.Error()
		s.status.Restarts++
		s.mu.Unlock()
		s.log.Error("capture process stopped; retrying", "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *CaptureService) captureOnce(ctx context.Context, cfg config.CaptureConfig) error {
	drawtextAvailable := s.supportsDrawtext(ctx, cfg.FFmpegPath)
	s.mu.Lock()
	if drawtextAvailable {
		s.status.Warning = ""
	} else {
		s.status.Warning = CaptureWarningWatermarkUnavailable
	}
	s.mu.Unlock()
	args, err := captureArgs(cfg, drawtextAvailable)
	if err != nil {
		return err
	}
	cmd := newManagedCommand(ctx, cfg.FFmpegPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open ffmpeg output: %w", err)
	}
	tail := &tailWriter{limit: 4096}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	s.log.Info("capture process started", "source", cfg.Source, "device", cfg.Device)
	s.mu.Lock()
	s.status.Running = true
	s.status.Connecting = false
	s.status.LastError = ""
	s.mu.Unlock()

	reader := NewMJPEGReader(stdout)
	var readErr error
	for ctx.Err() == nil {
		data, err := reader.ReadFrame()
		if err != nil {
			readErr = err
			break
		}
		now := time.Now()
		frame := Frame{CapturedAt: now, Data: data}
		s.hub.Publish(frame)
		if err := s.recording.Record(frame); err != nil {
			s.log.Error("recording stopped; live preview continues", "error", err)
		}
		s.mu.Lock()
		s.status.Frames++
		s.status.LastFrameAt = now
		s.status.LastError = ""
		s.mu.Unlock()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	detail := strings.TrimSpace(tail.String())
	if detail != "" {
		return fmt.Errorf("ffmpeg capture failed: %s", detail)
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return fmt.Errorf("read ffmpeg stream: %w", readErr)
	}
	if waitErr != nil {
		return fmt.Errorf("ffmpeg capture failed: %w", waitErr)
	}
	return errors.New("ffmpeg capture ended unexpectedly")
}

func (s *CaptureService) setRunning(running bool) {
	s.mu.Lock()
	s.status.Running = running
	s.mu.Unlock()
}

func (s *CaptureService) setConnecting(connecting bool) {
	s.mu.Lock()
	s.status.Connecting = connecting
	s.mu.Unlock()
}

func (s *CaptureService) supportsDrawtext(ctx context.Context, ffmpegPath string) bool {
	s.featureMu.Lock()
	if available, ok := s.drawtextSupport[ffmpegPath]; ok {
		s.featureMu.Unlock()
		return available
	}
	s.featureMu.Unlock()

	cmd := newManagedCommand(ctx, ffmpegPath, "-hide_banner", "-filters")
	output, err := cmd.CombinedOutput()
	available := err == nil && hasFFmpegFilter(string(output), "drawtext")
	s.featureMu.Lock()
	s.drawtextSupport[ffmpegPath] = available
	s.featureMu.Unlock()
	if !available {
		s.log.Warn("FFmpeg drawtext filter is unavailable; capture will continue without watermarks")
	}
	return available
}

func captureArgs(cfg config.CaptureConfig, includeWatermark bool) ([]string, error) {
	resolution := strconv.Itoa(cfg.Width) + "x" + strconv.Itoa(cfg.Height)
	fps := strconv.Itoa(cfg.FPS)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if cfg.Source == "mock" {
		args = append(args, "-re", "-f", "lavfi", "-i", "testsrc=size="+resolution+":rate="+fps)
	} else {
		cameraArgs, err := cameraInputArgs(runtime.GOOS, cfg, resolution, fps)
		if err != nil {
			return nil, err
		}
		args = append(args, cameraArgs...)
	}
	if includeWatermark {
		args = append(args, "-vf", watermarkFilter())
	}
	args = append(args,
		"-an", "-c:v", "mjpeg", "-q:v", strconv.Itoa(cfg.JPEGQuality), "-pix_fmt", "yuvj420p",
		"-f", "image2pipe", "pipe:1",
	)
	return args, nil
}

func cameraInputArgs(platform string, cfg config.CaptureConfig, resolution, fps string) ([]string, error) {
	switch platform {
	case "linux":
		args := []string{"-f", "v4l2"}
		if cfg.PixelFormat != "" {
			args = append(args, "-input_format", cfg.PixelFormat)
		}
		return append(args, "-framerate", fps, "-video_size", resolution, "-i", cfg.Device), nil
	case "darwin":
		args := []string{"-f", "avfoundation"}
		if cfg.PixelFormat != "" {
			args = append(args, "-pixel_format", cfg.PixelFormat, "-framerate", fps, "-video_size", resolution)
		}
		return append(args, "-i", avFoundationInput(cfg.Device)), nil
	case "windows":
		args := []string{"-f", "dshow"}
		if cfg.VideoCodec != "" {
			args = append(args, "-vcodec", cfg.VideoCodec)
		} else if cfg.PixelFormat != "" {
			args = append(args, "-pixel_format", cfg.PixelFormat)
		}
		return append(args, "-framerate", fps, "-video_size", resolution, "-i", "video="+cfg.Device), nil
	default:
		return nil, fmt.Errorf("camera capture is unsupported on %s", platform)
	}
}

func watermarkFilter() string {
	common := "fontcolor=white:fontsize=max(14\\,h/32):box=1:boxcolor=black@0.58:boxborderw=7:x=w-tw-12"
	return "drawtext=text='%{localtime}':" + common + ":y=12"
}

type tailWriter struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.data = append(w.data, p...)
	if len(w.data) > w.limit {
		w.data = append([]byte(nil), w.data[len(w.data)-w.limit:]...)
	}
	return len(p), nil
}

func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(bytes.TrimSpace(w.data))
}
