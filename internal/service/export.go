package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"video-recorder/internal/config"
)

var (
	ErrNoFrames  = errors.New("the video buffer is empty")
	ErrQueueFull = errors.New("the export queue is full")
)

type ExportState string

const (
	ExportQueued    ExportState = "queued"
	ExportRunning   ExportState = "running"
	ExportCompleted ExportState = "completed"
	ExportFailed    ExportState = "failed"
)

type ExportStatus struct {
	ID         string      `json:"id"`
	FileName   string      `json:"fileName"`
	State      ExportState `json:"state"`
	FrameCount int         `json:"frameCount"`
	CreatedAt  time.Time   `json:"createdAt"`
	StartedAt  time.Time   `json:"startedAt,omitempty"`
	FinishedAt time.Time   `json:"finishedAt,omitempty"`
	Error      string      `json:"error,omitempty"`
}

type exportJob struct {
	status     ExportStatus
	frames     []Frame
	targetPath string
	tempPath   string
	ffmpegPath string
	fps        int
}

type Exporter struct {
	ring      *RingBuffer
	config    func() config.Config
	log       *slog.Logger
	queue     chan *exportJob
	closeOnce sync.Once
	wg        sync.WaitGroup

	mu          sync.RWMutex
	jobs        map[string]ExportStatus
	activeNames map[string]struct{}
	pending     int
	sequence    uint64
	closed      bool
}

func NewExporter(ring *RingBuffer, configProvider func() config.Config, logger *slog.Logger) *Exporter {
	e := &Exporter{
		ring:        ring,
		config:      configProvider,
		log:         logger,
		queue:       make(chan *exportJob, 100),
		jobs:        make(map[string]ExportStatus),
		activeNames: make(map[string]struct{}),
	}
	e.wg.Add(1)
	go e.run()
	return e
}

func (e *Exporter) Enqueue(name string) (ExportStatus, error) {
	base, err := normalizeFileName(name)
	if err != nil {
		return ExportStatus{}, err
	}
	frames := e.ring.Snapshot()
	if len(frames) == 0 {
		return ExportStatus{}, ErrNoFrames
	}
	cfg := e.config()
	directory, err := organizedStorageDirectory(cfg.Storage, time.Now())
	if err != nil {
		return ExportStatus{}, fmt.Errorf("resolve storage directory: %w", err)
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return ExportStatus{}, fmt.Errorf("create storage directory: %w", err)
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return ExportStatus{}, errors.New("exporter is closed")
	}
	if e.pending >= cfg.Export.QueueSize {
		e.mu.Unlock()
		return ExportStatus{}, ErrQueueFull
	}
	fileName, selectedBase, target, err := e.nextAvailableTargetLocked(directory, base)
	if err != nil {
		e.mu.Unlock()
		return ExportStatus{}, err
	}
	nameKey := strings.ToLower(target)
	e.sequence++
	id := fmt.Sprintf("%d-%06d", time.Now().UnixMilli(), e.sequence)
	status := ExportStatus{
		ID:         id,
		FileName:   fileName,
		State:      ExportQueued,
		FrameCount: len(frames),
		CreatedAt:  time.Now(),
	}
	job := &exportJob{
		status:     status,
		frames:     frames,
		targetPath: target,
		tempPath:   filepath.Join(directory, "."+selectedBase+"-"+id+".part.mp4"),
		ffmpegPath: cfg.Capture.FFmpegPath,
		fps:        cfg.Capture.FPS,
	}
	e.jobs[id] = status
	e.activeNames[nameKey] = struct{}{}
	e.pending++
	e.pruneJobsLocked()
	select {
	case e.queue <- job:
		e.mu.Unlock()
		return status, nil
	default:
		delete(e.jobs, id)
		delete(e.activeNames, nameKey)
		e.pending--
		e.mu.Unlock()
		return ExportStatus{}, ErrQueueFull
	}
}

func organizedStorageDirectory(storage config.StorageConfig, now time.Time) (string, error) {
	directory, err := filepath.Abs(storage.Directory)
	if err != nil {
		return "", err
	}
	switch storage.Organization {
	case config.StorageOrganizationDay:
		return filepath.Join(directory, now.Format("20060102")), nil
	case config.StorageOrganizationMonth:
		return filepath.Join(directory, now.Format("200601")), nil
	case config.StorageOrganizationNone:
		return directory, nil
	default:
		return "", fmt.Errorf("unsupported storage organization: %s", storage.Organization)
	}
}

func (e *Exporter) nextAvailableTargetLocked(directory, base string) (fileName, selectedBase, target string, err error) {
	for sequence := 0; ; sequence++ {
		selectedBase = base
		if sequence > 0 {
			selectedBase = fmt.Sprintf("%s_%02d", base, sequence)
		}
		fileName = selectedBase + ".mp4"
		target = filepath.Join(directory, fileName)
		if _, active := e.activeNames[strings.ToLower(target)]; active {
			continue
		}
		if _, statErr := os.Stat(target); statErr == nil {
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", "", "", fmt.Errorf("inspect target file: %w", statErr)
		}
		return fileName, selectedBase, target, nil
	}
}

func (e *Exporter) Status(id string) (ExportStatus, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	status, ok := e.jobs[id]
	return status, ok
}

func (e *Exporter) Close() {
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		close(e.queue)
		e.mu.Unlock()
	})
	e.wg.Wait()
}

func (e *Exporter) run() {
	defer e.wg.Done()
	for job := range e.queue {
		e.setRunning(job)
		err := e.export(job)
		if err != nil {
			e.log.Error("video export failed", "file", job.status.FileName, "error", err)
			e.finish(job, ExportFailed, err)
			continue
		}
		e.log.Info("video export completed", "file", job.status.FileName, "frames", len(job.frames))
		e.finish(job, ExportCompleted, nil)
	}
}

func (e *Exporter) export(job *exportJob) error {
	timeout := time.Duration(len(job.frames)/max(job.fps, 1))*time.Second + 30*time.Second
	if timeout < 45*time.Second {
		timeout = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	defer func() { _ = os.Remove(job.tempPath) }()

	reader := &frameReader{frames: job.frames}
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "image2pipe", "-framerate", strconv.Itoa(job.fps), "-vcodec", "mjpeg", "-i", "pipe:0",
		"-an", "-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p",
		"-movflags", "+faststart", job.tempPath,
	}
	cmd := exec.CommandContext(ctx, job.ffmpegPath, args...)
	configureCommand(cmd)
	cmd.Stdin = reader
	var stderr strings.Builder
	cmd.Stderr = &limitedStringWriter{builder: &stderr, limit: 8192}
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if ctx.Err() != nil {
			return fmt.Errorf("ffmpeg export timed out: %w", ctx.Err())
		}
		if detail != "" {
			return fmt.Errorf("ffmpeg export failed: %s", detail)
		}
		return fmt.Errorf("ffmpeg export failed: %w", err)
	}
	// Hard-linking publishes the completed file atomically and never overwrites
	// a file created by another process after this job was queued.
	if err := os.Link(job.tempPath, job.targetPath); err != nil {
		return fmt.Errorf("publish exported video: %w", err)
	}
	return nil
}

func (e *Exporter) setRunning(job *exportJob) {
	e.mu.Lock()
	defer e.mu.Unlock()
	job.status.State = ExportRunning
	job.status.StartedAt = time.Now()
	e.jobs[job.status.ID] = job.status
}

func (e *Exporter) finish(job *exportJob, state ExportState, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	job.status.State = state
	job.status.FinishedAt = time.Now()
	if err != nil {
		job.status.Error = err.Error()
	}
	e.jobs[job.status.ID] = job.status
	delete(e.activeNames, strings.ToLower(job.targetPath))
	if e.pending > 0 {
		e.pending--
	}
}

func (e *Exporter) pruneJobsLocked() {
	if len(e.jobs) <= 200 {
		return
	}
	var oldestID string
	var oldest time.Time
	for id, status := range e.jobs {
		if status.State == ExportQueued || status.State == ExportRunning {
			continue
		}
		if oldestID == "" || status.CreatedAt.Before(oldest) {
			oldestID, oldest = id, status.CreatedAt
		}
	}
	if oldestID != "" {
		delete(e.jobs, oldestID)
	}
}

func normalizeFileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if strings.HasSuffix(strings.ToLower(name), ".mp4") {
		name = name[:len(name)-4]
	}
	if name == "" || name == "." || name == ".." {
		return "", errors.New("fileName is required")
	}
	if utf8.RuneCountInString(name) > 128 {
		return "", errors.New("fileName must not exceed 128 characters")
	}
	if strings.ContainsAny(name, "<>:\"/\\|?*") {
		return "", errors.New("fileName contains invalid characters")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("fileName contains control characters")
		}
	}
	return name, nil
}

type frameReader struct {
	frames []Frame
	index  int
	offset int
}

func (r *frameReader) Read(p []byte) (int, error) {
	for r.index < len(r.frames) {
		data := r.frames[r.index].Data
		if r.offset >= len(data) {
			r.index++
			r.offset = 0
			continue
		}
		n := copy(p, data[r.offset:])
		r.offset += n
		return n, nil
	}
	return 0, io.EOF
}

type limitedStringWriter struct {
	builder *strings.Builder
	limit   int
}

func (w *limitedStringWriter) Write(p []byte) (int, error) {
	original := len(p)
	remaining := w.limit - w.builder.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = w.builder.Write(p)
	}
	return original, nil
}
