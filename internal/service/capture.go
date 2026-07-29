package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"video-recorder/internal/config"
)

type CaptureStatus struct {
	Running     bool      `json:"running"`
	Source      string    `json:"source"`
	StartedAt   time.Time `json:"startedAt,omitempty"`
	Frames      uint64    `json:"frames"`
	LastFrameAt time.Time `json:"lastFrameAt,omitempty"`
	LastError   string    `json:"lastError,omitempty"`
	Restarts    uint64    `json:"restarts"`
}

type CaptureService struct {
	ring *RingBuffer
	hub  *FrameHub
	log  *slog.Logger

	restartMu sync.Mutex
	mu        sync.RWMutex
	root      context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	status    CaptureStatus
}

func NewCaptureService(ring *RingBuffer, hub *FrameHub, logger *slog.Logger) *CaptureService {
	return &CaptureService{ring: ring, hub: hub, log: logger}
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
	s.ring.SetDuration(time.Duration(cfg.BufferSeconds) * time.Second)
	return s.startCurrent(cfg)
}

func (s *CaptureService) Reset(cfg config.CaptureConfig) error {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()

	s.stopCurrent()
	s.ring.SetDuration(time.Duration(cfg.BufferSeconds) * time.Second)
	s.ring.Clear()
	s.mu.Lock()
	s.status.StartedAt = time.Now()
	s.status.Frames = 0
	s.status.LastFrameAt = time.Time{}
	s.status.LastError = ""
	s.mu.Unlock()
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
	startedAt := s.status.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
		s.status.StartedAt = startedAt
	}
	s.mu.Unlock()

	go func() {
		defer close(done)
		s.run(runCtx, cfg, startedAt)
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

func (s *CaptureService) run(ctx context.Context, cfg config.CaptureConfig, startedAt time.Time) {
	for {
		err := s.captureOnce(ctx, cfg, startedAt)
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

func (s *CaptureService) captureOnce(ctx context.Context, cfg config.CaptureConfig, startedAt time.Time) error {
	args, err := captureArgs(cfg, startedAt)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, cfg.FFmpegPath, args...)
	configureCommand(cmd)
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
	s.setRunning(true)

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
		s.ring.Append(frame)
		s.hub.Publish(frame)
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

func captureArgs(cfg config.CaptureConfig, startedAt time.Time) ([]string, error) {
	resolution := strconv.Itoa(cfg.Width) + "x" + strconv.Itoa(cfg.Height)
	fps := strconv.Itoa(cfg.FPS)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if cfg.Source == "mock" {
		args = append(args, "-re", "-f", "lavfi", "-i", "testsrc=size="+resolution+":rate="+fps)
	} else {
		switch runtime.GOOS {
		case "linux":
			args = append(args, "-f", "v4l2", "-framerate", fps, "-video_size", resolution, "-i", cfg.Device)
		case "darwin":
			args = append(args, "-f", "avfoundation", "-framerate", fps, "-video_size", resolution, "-i", cfg.Device)
		case "windows":
			args = append(args, "-f", "dshow", "-framerate", fps, "-video_size", resolution, "-i", "video="+cfg.Device)
		default:
			return nil, fmt.Errorf("camera capture is unsupported on %s", runtime.GOOS)
		}
	}
	args = append(args,
		"-vf", watermarkFilter(startedAt),
		"-an", "-c:v", "mjpeg", "-q:v", strconv.Itoa(cfg.JPEGQuality),
		"-f", "image2pipe", "pipe:1",
	)
	return args, nil
}

func watermarkFilter(startedAt time.Time) string {
	started := escapeDrawtextText(startedAt.Local().Format("2006-01-02 15:04:05"))
	common := "fontcolor=white:fontsize=max(14\\,h/32):box=1:boxcolor=black@0.58:boxborderw=7:x=w-tw-12"
	return "drawtext=text='Started\\: " + started + "':" + common + ":y=12," +
		"drawtext=text='Current\\: %{localtime}':" + common + ":y=12+lh+12"
}

func escapeDrawtextText(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "'", "\\'")
	value = strings.ReplaceAll(value, ":", "\\:")
	value = strings.ReplaceAll(value, "%", "\\%")
	return value
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
