package service

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

type RecordingState string

const (
	RecordingPreviewing RecordingState = "previewing"
	RecordingActive     RecordingState = "recording"
	RecordingTimedOut   RecordingState = "timedOut"
)

var ErrRecordingNotActive = errors.New("no recording is active")

type RecordingStatus struct {
	State              RecordingState  `json:"state"`
	Metadata           string          `json:"metadata"`
	StartedAt          time.Time       `json:"startedAt,omitempty"`
	Deadline           time.Time       `json:"deadline,omitempty"`
	TimedOutAt         time.Time       `json:"timedOutAt,omitempty"`
	MaxDurationMinutes int64           `json:"maxDurationMinutes"`
	LastError          string          `json:"lastError,omitempty"`
	Transcode          TranscodeStatus `json:"transcode"`
}

type RecordingSession struct {
	mu          sync.Mutex
	buffer      *CaptureBuffer
	state       RecordingState
	metadata    string
	startedAt   time.Time
	deadline    time.Time
	timedOutAt  time.Time
	maxDuration time.Duration
	lastError   string
	timer       *time.Timer
	generation  uint64
	closed      bool
	transcoder  recordingTranscoder
}

func (s *RecordingSession) SetTranscoder(transcoder recordingTranscoder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcoder = transcoder
}

func NewRecordingSession(buffer *CaptureBuffer, maxDuration time.Duration) (*RecordingSession, error) {
	if maxDuration <= 0 {
		return nil, errors.New("maximum recording duration must be positive")
	}
	return &RecordingSession{buffer: buffer, state: RecordingPreviewing, maxDuration: maxDuration}, nil
}

func (s *RecordingSession) Start(metadata string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("recording session is closed")
	}
	if err := s.buffer.Clear(); err != nil {
		return err
	}
	if s.transcoder != nil {
		s.transcoder.Start()
	}
	s.stopTimerLocked()
	now := time.Now()
	s.state = RecordingActive
	s.metadata = metadata
	s.startedAt = now
	s.deadline = now.Add(s.maxDuration)
	s.timedOutAt = time.Time{}
	s.lastError = ""
	s.scheduleTimeoutLocked()
	return nil
}

func (s *RecordingSession) Record(frame Frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.state != RecordingActive {
		return nil
	}
	if !time.Now().Before(s.deadline) {
		return s.timeoutLocked(time.Now())
	}
	if err := s.buffer.Append(frame); err != nil {
		s.stopTimerLocked()
		if s.transcoder != nil {
			s.transcoder.Discard()
		}
		s.state = RecordingPreviewing
		s.startedAt = time.Time{}
		s.deadline = time.Time{}
		s.lastError = err.Error()
		clearErr := s.buffer.Clear()
		return errors.Join(fmt.Errorf("record captured frame: %w", err), clearErr)
	}
	if s.transcoder != nil {
		s.transcoder.Write(frame)
	}
	return nil
}

func (s *RecordingSession) Save(exporter *Exporter, name string) (ExportStatus, error) {
	return s.save(exporter, name, false, nil)
}

func (s *RecordingSession) SaveAndContinue(exporter *Exporter, name string, nextMetadata *string) (ExportStatus, error) {
	return s.save(exporter, name, true, nextMetadata)
}

func (s *RecordingSession) save(exporter *Exporter, name string, continueRecording bool, nextMetadata *string) (ExportStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ExportStatus{}, errors.New("recording session is closed")
	}
	if s.state != RecordingActive {
		return ExportStatus{}, ErrRecordingNotActive
	}
	var detachLive func() *liveEncoding
	if s.transcoder != nil {
		detachLive = s.transcoder.Detach
	}
	status, err := exporter.enqueue(name, detachLive)
	if err != nil {
		return ExportStatus{}, err
	}
	s.stopTimerLocked()
	if continueRecording {
		if s.transcoder != nil {
			s.transcoder.Start()
		}
		if nextMetadata != nil {
			s.metadata = *nextMetadata
		}
		now := time.Now()
		s.startedAt = now
		s.deadline = now.Add(s.maxDuration)
		s.timedOutAt = time.Time{}
		s.lastError = ""
		s.scheduleTimeoutLocked()
		return status, nil
	}
	s.state = RecordingPreviewing
	s.metadata = ""
	s.startedAt = time.Time{}
	s.deadline = time.Time{}
	s.timedOutAt = time.Time{}
	s.lastError = ""
	return status, nil
}

func (s *RecordingSession) SetMaxDuration(maxDuration time.Duration) error {
	if maxDuration <= 0 {
		return errors.New("maximum recording duration must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("recording session is closed")
	}
	s.maxDuration = maxDuration
	if s.state != RecordingActive {
		return nil
	}
	s.deadline = s.startedAt.Add(maxDuration)
	if !time.Now().Before(s.deadline) {
		return s.timeoutLocked(time.Now())
	}
	s.stopTimerLocked()
	s.scheduleTimeoutLocked()
	return nil
}

func (s *RecordingSession) Status() RecordingStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := RecordingStatus{
		State:              s.state,
		Metadata:           s.metadata,
		StartedAt:          s.startedAt,
		Deadline:           s.deadline,
		TimedOutAt:         s.timedOutAt,
		MaxDurationMinutes: int64(s.maxDuration / time.Minute),
		LastError:          s.lastError,
	}
	if s.transcoder != nil {
		status.Transcode = s.transcoder.Status()
	} else {
		status.Transcode = TranscodeStatus{State: TranscodeDisabled}
	}
	return status
}

func (s *RecordingSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.stopTimerLocked()
	if s.transcoder != nil {
		s.transcoder.Close()
	}
}

func (s *RecordingSession) scheduleTimeoutLocked() {
	s.generation++
	generation := s.generation
	delay := time.Until(s.deadline)
	s.timer = time.AfterFunc(delay, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed || s.state != RecordingActive || generation != s.generation {
			return
		}
		_ = s.timeoutLocked(time.Now())
	})
}

func (s *RecordingSession) stopTimerLocked() {
	s.generation++
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

func (s *RecordingSession) timeoutLocked(now time.Time) error {
	s.stopTimerLocked()
	if s.transcoder != nil {
		s.transcoder.Discard()
	}
	clearErr := s.buffer.Clear()
	s.state = RecordingTimedOut
	s.startedAt = time.Time{}
	s.deadline = time.Time{}
	s.timedOutAt = now
	s.lastError = ""
	if clearErr != nil {
		s.lastError = clearErr.Error()
		return fmt.Errorf("discard timed-out recording: %w", clearErr)
	}
	return nil
}
