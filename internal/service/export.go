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
	ErrNoFrames  = errors.New("the current recording is empty")
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
	ID             string      `json:"id"`
	FileName       string      `json:"fileName"`
	State          ExportState `json:"state"`
	FrameCount     int         `json:"frameCount"`
	CreatedAt      time.Time   `json:"createdAt"`
	StartedAt      time.Time   `json:"startedAt,omitempty"`
	FinishedAt     time.Time   `json:"finishedAt,omitempty"`
	Error          string      `json:"error,omitempty"`
	Encoder        string      `json:"encoder,omitempty"`
	FallbackReason string      `json:"fallbackReason,omitempty"`
}

type exportJob struct {
	status           ExportStatus
	segment          captureSegment
	targetPath       string
	tempPath         string
	ffmpegPath       string
	fps              int
	softwareThreads  int
	videoBitrateKbps int
	live             *liveEncoding
}

type Exporter struct {
	buffer    *CaptureBuffer
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

func NewExporter(buffer *CaptureBuffer, configProvider func() config.Config, logger *slog.Logger) *Exporter {
	e := &Exporter{
		buffer:      buffer,
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
	return e.enqueue(name, nil)
}

func (e *Exporter) enqueue(name string, detachLive func() *liveEncoding) (ExportStatus, error) {
	base, err := normalizeFileName(name)
	if err != nil {
		return ExportStatus{}, err
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
	segment, err := e.buffer.Detach()
	if err != nil {
		e.mu.Unlock()
		return ExportStatus{}, err
	}
	var live *liveEncoding
	if detachLive != nil {
		live = detachLive()
	}
	nameKey := strings.ToLower(target)
	e.sequence++
	id := fmt.Sprintf("%d-%06d", time.Now().UnixMilli(), e.sequence)
	status := ExportStatus{
		ID:         id,
		FileName:   fileName,
		State:      ExportQueued,
		FrameCount: segment.frames,
		CreatedAt:  segment.detachedAt,
	}
	job := &exportJob{
		status:           status,
		segment:          segment,
		targetPath:       target,
		tempPath:         filepath.Join(directory, "."+selectedBase+"-"+id+".part.mp4"),
		ffmpegPath:       cfg.Capture.FFmpegPath,
		fps:              cfg.Capture.FPS,
		softwareThreads:  cfg.Export.SoftwareThreads,
		videoBitrateKbps: cfg.Export.VideoBitrateKbps,
		live:             live,
	}
	if live != nil {
		job.status.Encoder = live.encoder
		status.Encoder = live.encoder
	} else {
		job.status.Encoder = "libx264"
		status.Encoder = "libx264"
	}
	e.jobs[id] = status
	e.activeNames[nameKey] = struct{}{}
	e.pending++
	e.pruneJobsLocked()
	e.queue <- job
	e.mu.Unlock()
	return status, nil
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
		e.log.Info("video export completed", "file", job.status.FileName, "frames", job.segment.frames)
		e.finish(job, ExportCompleted, nil)
	}
}

func (e *Exporter) export(job *exportJob) error {
	if job.live != nil {
		if err := e.publishLive(job); err == nil {
			_ = os.Remove(job.segment.path)
			return nil
		} else {
			job.status.FallbackReason = err.Error()
			job.status.Encoder = "libx264"
			e.updateRunningStatus(job)
			e.log.Warn("live transcode unavailable during export; using save-time encoding", "file", job.status.FileName, "error", err)
		}
	}
	timeout := time.Duration(job.segment.frames/max(job.fps, 1))*time.Second + 30*time.Second
	if timeout < 45*time.Second {
		timeout = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	defer func() {
		_ = os.Remove(job.tempPath)
		_ = os.Remove(job.segment.path)
	}()

	reader, err := os.Open(job.segment.path)
	if err != nil {
		return fmt.Errorf("open captured segment: %w", err)
	}
	defer reader.Close()
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "image2pipe", "-framerate", strconv.Itoa(job.fps), "-vcodec", "mjpeg", "-i", "pipe:0",
		"-an", "-c:v", "libx264", "-preset", "veryfast",
	}
	args = append(args, videoBitrateArgs(job.videoBitrateKbps)...)
	args = append(args, "-threads", strconv.Itoa(job.softwareThreads), "-pix_fmt", "yuv420p", "-movflags", "+faststart", job.tempPath)
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

func (e *Exporter) publishLive(job *exportJob) error {
	defer func() { _ = os.Remove(job.live.path) }()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	var result liveEncodingResult
	select {
	case current, ok := <-job.live.done:
		if !ok {
			return errors.New("live transcode ended without a result")
		}
		result = current
	case <-timer.C:
		job.live.cancel()
		return errors.New("live transcode finalization timed out")
	}
	if result.err != nil {
		return fmt.Errorf("live transcode failed: %w", result.err)
	}
	info, err := os.Stat(result.path)
	if err != nil {
		return fmt.Errorf("inspect live transcode output: %w", err)
	}
	if info.Size() == 0 {
		return errors.New("live transcode output is empty")
	}
	if err := os.Link(result.path, job.targetPath); err == nil {
		return nil
	}
	if err := copyFileExclusive(result.path, job.tempPath, job.targetPath); err != nil {
		return fmt.Errorf("publish live transcode output: %w", err)
	}
	return nil
}

func copyFileExclusive(sourcePath, tempPath, targetPath string) error {
	defer func() { _ = os.Remove(tempPath) }()
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	temp, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = temp.Close()
		if !ok {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := io.Copy(temp, source); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Link(tempPath, targetPath); err != nil {
		return err
	}
	ok = true
	return nil
}

func (e *Exporter) updateRunningStatus(job *exportJob) {
	e.mu.Lock()
	e.jobs[job.status.ID] = job.status
	e.mu.Unlock()
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
