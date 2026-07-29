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
	State              RecordingState `json:"state"`
	StartedAt          time.Time      `json:"startedAt,omitempty"`
	Deadline           time.Time      `json:"deadline,omitempty"`
	TimedOutAt         time.Time      `json:"timedOutAt,omitempty"`
	MaxDurationMinutes int64          `json:"maxDurationMinutes"`
	LastError          string         `json:"lastError,omitempty"`
}

type RecordingSession struct {
	mu          sync.Mutex
	buffer      *CaptureBuffer
	state       RecordingState
	startedAt   time.Time
	deadline    time.Time
	timedOutAt  time.Time
	maxDuration time.Duration
	lastError   string
	timer       *time.Timer
	generation  uint64
	closed      bool
}

func NewRecordingSession(buffer *CaptureBuffer, maxDuration time.Duration) (*RecordingSession, error) {
	if maxDuration <= 0 {
		return nil, errors.New("maximum recording duration must be positive")
	}
	return &RecordingSession{buffer: buffer, state: RecordingPreviewing, maxDuration: maxDuration}, nil
}

func (s *RecordingSession) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("recording session is closed")
	}
	if err := s.buffer.Clear(); err != nil {
		return err
	}
	s.stopTimerLocked()
	now := time.Now()
	s.state = RecordingActive
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
		s.state = RecordingPreviewing
		s.startedAt = time.Time{}
		s.deadline = time.Time{}
		s.lastError = err.Error()
		clearErr := s.buffer.Clear()
		return errors.Join(fmt.Errorf("record captured frame: %w", err), clearErr)
	}
	return nil
}

func (s *RecordingSession) Save(exporter *Exporter, name string) (ExportStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ExportStatus{}, errors.New("recording session is closed")
	}
	if s.state != RecordingActive {
		return ExportStatus{}, ErrRecordingNotActive
	}
	status, err := exporter.Enqueue(name)
	if err != nil {
		return ExportStatus{}, err
	}
	s.stopTimerLocked()
	s.state = RecordingPreviewing
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
	return RecordingStatus{
		State:              s.state,
		StartedAt:          s.startedAt,
		Deadline:           s.deadline,
		TimedOutAt:         s.timedOutAt,
		MaxDurationMinutes: int64(s.maxDuration / time.Minute),
		LastError:          s.lastError,
	}
}

func (s *RecordingSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.stopTimerLocked()
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
