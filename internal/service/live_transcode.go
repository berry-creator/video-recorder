package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"video-recorder/internal/config"
)

type TranscodeState string

const (
	TranscodeDisabled   TranscodeState = "disabled"
	TranscodeStarting   TranscodeState = "starting"
	TranscodeRunning    TranscodeState = "running"
	TranscodeFinalizing TranscodeState = "finalizing"
	TranscodeCompleted  TranscodeState = "completed"
	TranscodeFallback   TranscodeState = "fallback"
)

type TranscodeStatus struct {
	State     TranscodeState `json:"state"`
	Encoder   string         `json:"encoder,omitempty"`
	Speed     float64        `json:"speed,omitempty"`
	LastError string         `json:"lastError,omitempty"`
}

type liveEncodingResult struct {
	path    string
	encoder string
	err     error
}

type liveEncoding struct {
	path    string
	encoder string
	done    <-chan liveEncodingResult
	cancel  context.CancelFunc
}

type recordingTranscoder interface {
	Start()
	Write(Frame)
	Detach() *liveEncoding
	Discard()
	Status() TranscodeStatus
	Close()
}

type encoderSpec struct {
	name string
	args []string
}

type liveSession struct {
	owner   *LiveTranscoder
	path    string
	encoder encoderSpec
	frames  chan []byte
	done    chan liveEncodingResult
	cancel  context.CancelFunc

	mu            sync.Mutex
	accepting     bool
	aborted       bool
	abortOnce     sync.Once
	closeOnce     sync.Once
	lowSpeedSince time.Time
}

type LiveTranscoder struct {
	config func() config.Config
	log    *slog.Logger

	mu         sync.RWMutex
	active     *liveSession
	finalizing *liveSession
	status     TranscodeStatus
	probes     map[string]encoderSpec
	closing    bool
}

func NewLiveTranscoder(configProvider func() config.Config, logger *slog.Logger) *LiveTranscoder {
	return &LiveTranscoder{
		config: configProvider,
		log:    logger,
		status: TranscodeStatus{State: TranscodeDisabled},
		probes: make(map[string]encoderSpec),
	}
}

func (t *LiveTranscoder) Start() {
	t.Discard()
	t.mu.Lock()
	t.finalizing = nil
	t.mu.Unlock()
	cfg := t.config()
	if !cfg.Export.TranscodeDuringRecording {
		t.setStatus(TranscodeStatus{State: TranscodeDisabled})
		return
	}
	t.setStatus(TranscodeStatus{State: TranscodeStarting})

	encoder, err := t.selectEncoder(cfg)
	if err != nil {
		t.fallback("encoder probe failed: " + err.Error())
		return
	}
	workDir := filepath.Join(cfg.Storage.Directory, ".video-recorder-work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.fallback("create live transcode directory: " + err.Error())
		return
	}
	file, err := os.CreateTemp(workDir, "live-*.part.mp4")
	if err != nil {
		t.fallback("create live transcode file: " + err.Error())
		return
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		t.fallback("close live transcode file: " + err.Error())
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	queueSize := max(cfg.Capture.FPS*2, 1)
	session := &liveSession{
		owner:     t,
		path:      path,
		encoder:   encoder,
		frames:    make(chan []byte, queueSize),
		done:      make(chan liveEncodingResult, 1),
		cancel:    cancel,
		accepting: true,
	}
	if err := session.run(ctx, cfg.Capture.FFmpegPath, cfg.Capture.FPS); err != nil {
		cancel()
		_ = os.Remove(path)
		t.fallback("start live transcode: " + err.Error())
		return
	}

	t.mu.Lock()
	if t.closing {
		t.mu.Unlock()
		session.abort("application is shutting down")
		return
	}
	t.active = session
	t.finalizing = nil
	t.status = TranscodeStatus{State: TranscodeRunning, Encoder: encoder.name}
	t.mu.Unlock()
	t.log.Info("live transcode started", "encoder", encoder.name, "queueFrames", queueSize)
}

func (t *LiveTranscoder) Write(frame Frame) {
	t.mu.RLock()
	session := t.active
	t.mu.RUnlock()
	if session == nil {
		return
	}
	session.mu.Lock()
	if !session.accepting {
		session.mu.Unlock()
		return
	}
	data := append([]byte(nil), frame.Data...)
	failed := false
	select {
	case session.frames <- data:
	default:
		session.accepting = false
		session.aborted = true
		session.abortOnce.Do(session.cancel)
		session.closeOnce.Do(func() { close(session.frames) })
		failed = true
	}
	session.mu.Unlock()
	if failed {
		t.fallbackFor(session, "live encoder could not keep up with capture")
	}
}

func (t *LiveTranscoder) Detach() *liveEncoding {
	t.mu.Lock()
	session := t.active
	if session == nil {
		t.mu.Unlock()
		return nil
	}
	t.active = nil
	t.finalizing = session
	t.status.State = TranscodeFinalizing
	t.mu.Unlock()

	session.mu.Lock()
	if !session.accepting || session.aborted {
		session.mu.Unlock()
		return nil
	}
	session.accepting = false
	session.closeOnce.Do(func() { close(session.frames) })
	session.mu.Unlock()
	return &liveEncoding{path: session.path, encoder: session.encoder.name, done: session.done, cancel: session.cancel}
}

func (t *LiveTranscoder) Discard() {
	t.mu.Lock()
	session := t.active
	t.active = nil
	t.mu.Unlock()
	if session != nil {
		session.abort("recording discarded")
		select {
		case <-session.done:
		case <-time.After(3 * time.Second):
		}
	}
}

func (t *LiveTranscoder) Status() TranscodeStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.status
}

func (t *LiveTranscoder) Close() {
	t.mu.Lock()
	t.closing = true
	t.mu.Unlock()
	t.Discard()
}

func (t *LiveTranscoder) setStatus(status TranscodeStatus) {
	t.mu.Lock()
	t.status = status
	t.mu.Unlock()
}

func (t *LiveTranscoder) fallback(message string) {
	t.setStatus(TranscodeStatus{State: TranscodeFallback, LastError: message})
	t.log.Warn("live transcode unavailable; save-time encoding will be used", "error", message)
}

func (t *LiveTranscoder) fallbackFor(session *liveSession, message string) {
	t.mu.Lock()
	for key, encoder := range t.probes {
		if encoder.name == session.encoder.name {
			delete(t.probes, key)
		}
	}
	if t.active == session {
		t.active = nil
		t.status = TranscodeStatus{State: TranscodeFallback, Encoder: session.encoder.name, LastError: message}
	} else if t.finalizing == session {
		t.status = TranscodeStatus{State: TranscodeFallback, Encoder: session.encoder.name, LastError: message}
		t.finalizing = nil
	}
	t.mu.Unlock()
	t.log.Warn("live transcode stopped; save-time encoding will be used", "encoder", session.encoder.name, "error", message)
}

func (t *LiveTranscoder) completeFor(session *liveSession) {
	t.mu.Lock()
	if t.finalizing == session {
		t.status = TranscodeStatus{State: TranscodeCompleted, Encoder: session.encoder.name, Speed: t.status.Speed}
		t.finalizing = nil
	}
	t.mu.Unlock()
}

func (t *LiveTranscoder) updateSpeed(session *liveSession, speed float64) {
	t.mu.Lock()
	if t.active == session && t.status.State == TranscodeRunning {
		t.status.Speed = speed
	}
	t.mu.Unlock()
	if session.observeSpeed(speed) {
		t.fallbackFor(session, fmt.Sprintf("live encoder speed remained below real time (%.2fx)", speed))
	}
}

func (s *liveSession) observeSpeed(speed float64) bool {
	if speed <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.accepting {
		return false
	}
	if speed >= 0.90 {
		s.lowSpeedSince = time.Time{}
		return false
	}
	if s.lowSpeedSince.IsZero() {
		s.lowSpeedSince = time.Now()
		return false
	}
	if time.Since(s.lowSpeedSince) < 10*time.Second {
		return false
	}
	s.accepting = false
	s.aborted = true
	s.abortOnce.Do(s.cancel)
	s.closeOnce.Do(func() { close(s.frames) })
	return true
}

func (t *LiveTranscoder) selectEncoder(cfg config.Config) (encoderSpec, error) {
	key := strings.Join([]string{cfg.Capture.FFmpegPath, cfg.Export.Encoder, strconv.Itoa(cfg.Export.SoftwareThreads), strconv.Itoa(cfg.Capture.Width), strconv.Itoa(cfg.Capture.Height), strconv.Itoa(cfg.Capture.FPS), runtime.GOOS}, "\x00")
	t.mu.RLock()
	if cached, ok := t.probes[key]; ok {
		t.mu.RUnlock()
		return cached, nil
	}
	t.mu.RUnlock()

	candidates := encoderCandidates(runtime.GOOS, cfg.Export.Encoder, cfg.Export.SoftwareThreads, cfg.Capture.Width, cfg.Capture.Height, cfg.Capture.FPS)
	var probeErrors []error
	for _, candidate := range candidates {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := probeEncoder(ctx, cfg.Capture.FFmpegPath, candidate)
		cancel()
		if err != nil {
			probeErrors = append(probeErrors, fmt.Errorf("%s: %w", candidate.name, err))
			continue
		}
		t.mu.Lock()
		t.probes[key] = candidate
		t.mu.Unlock()
		return candidate, nil
	}
	return encoderSpec{}, errors.Join(probeErrors...)
}

func encoderCandidates(platform, preference string, softwareThreads, width, height, fps int) []encoderSpec {
	software := encoderSpec{name: "libx264", args: []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-threads", strconv.Itoa(softwareThreads), "-pix_fmt", "yuv420p"}}
	if preference == config.ExportEncoderSoftware {
		return []encoderSpec{software}
	}
	var names []string
	switch platform {
	case "darwin":
		names = []string{"h264_videotoolbox"}
	case "windows":
		names = []string{"h264_qsv", "h264_nvenc", "h264_amf", "h264_mf"}
	default:
		names = []string{"h264_vaapi", "h264_qsv", "h264_nvenc"}
	}
	candidates := make([]encoderSpec, 0, len(names)+1)
	bitrate := min(max(width*height*fps/7, 1_000_000), 40_000_000)
	bitrateText := strconv.Itoa(bitrate)
	for _, name := range names {
		args := []string{"-c:v", name, "-b:v", bitrateText, "-pix_fmt", "yuv420p"}
		if name == "h264_vaapi" {
			args = []string{"-vf", "format=nv12,hwupload", "-c:v", name, "-b:v", bitrateText}
		}
		candidates = append(candidates, encoderSpec{name: name, args: args})
	}
	return append(candidates, software)
}

func probeEncoder(ctx context.Context, ffmpegPath string, encoder encoderSpec) error {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-f", "lavfi", "-i", "color=size=64x64:rate=1", "-frames:v", "1"}
	args = append(args, encoder.args...)
	args = append(args, "-f", "null", "-")
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	configureCommand(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return errors.New(detail)
		}
		return err
	}
	return nil
}

func (s *liveSession) run(ctx context.Context, ffmpegPath string, fps int) error {
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "image2pipe", "-framerate", strconv.Itoa(fps), "-vcodec", "mjpeg", "-i", "pipe:0",
		"-an",
	}
	args = append(args, s.encoder.args...)
	args = append(args, "-progress", "pipe:2", "-nostats", s.path)
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	configureCommand(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return err
	}
	go s.consumeProgress(stderr)
	go s.writeFrames(cmd, stdin)
	return nil
}

func (s *liveSession) consumeProgress(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "speed=") {
			continue
		}
		value := strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(line, "speed=")), "x")
		if speed, err := strconv.ParseFloat(value, 64); err == nil {
			s.owner.updateSpeed(s, speed)
		}
	}
}

func (s *liveSession) writeFrames(cmd *exec.Cmd, stdin io.WriteCloser) {
	var writeErr error
	for frame := range s.frames {
		if _, err := stdin.Write(frame); err != nil {
			writeErr = err
			break
		}
	}
	_ = stdin.Close()
	waitErr := cmd.Wait()
	s.mu.Lock()
	aborted := s.aborted
	s.mu.Unlock()
	resultErr := errors.Join(writeErr, waitErr)
	if aborted && resultErr == nil {
		resultErr = errors.New("live transcode aborted")
	}
	if resultErr != nil {
		s.owner.fallbackFor(s, resultErr.Error())
	} else {
		s.owner.completeFor(s)
	}
	s.done <- liveEncodingResult{path: s.path, encoder: s.encoder.name, err: resultErr}
	close(s.done)
	if aborted {
		_ = os.Remove(s.path)
	}
}

func (s *liveSession) abort(reason string) {
	s.mu.Lock()
	if !s.accepting && s.aborted {
		s.mu.Unlock()
		return
	}
	s.accepting = false
	s.aborted = true
	s.abortOnce.Do(s.cancel)
	s.closeOnce.Do(func() { close(s.frames) })
	s.mu.Unlock()
	s.owner.fallbackFor(s, reason)
}
