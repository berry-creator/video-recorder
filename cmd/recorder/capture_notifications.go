package main

import (
	"context"
	"log/slog"
	"time"

	"video-recorder/internal/config"
	"video-recorder/internal/notification"
	"video-recorder/internal/service"
)

const (
	captureFailureDelay    = 5 * time.Second
	captureFailureCooldown = 5 * time.Minute
)

type captureNotificationEvent int

const (
	captureNotificationNone captureNotificationEvent = iota
	captureNotificationFailed
	captureNotificationRecovered
)

type captureNotificationState struct {
	failureStartedAt time.Time
	lastFailureAt    time.Time
	failureReported  bool
}

func (s *captureNotificationState) observe(now time.Time, failed, healthy bool) captureNotificationEvent {
	if failed {
		if s.failureStartedAt.IsZero() {
			s.failureStartedAt = now
		}
		if now.Sub(s.failureStartedAt) < captureFailureDelay {
			return captureNotificationNone
		}
		if s.lastFailureAt.IsZero() || now.Sub(s.lastFailureAt) >= captureFailureCooldown {
			s.lastFailureAt = now
			s.failureReported = true
			return captureNotificationFailed
		}
		return captureNotificationNone
	}

	if !healthy {
		return captureNotificationNone
	}

	s.failureStartedAt = time.Time{}
	s.lastFailureAt = time.Time{}
	if s.failureReported {
		s.failureReported = false
		return captureNotificationRecovered
	}
	return captureNotificationNone
}

func monitorCaptureNotifications(
	ctx context.Context,
	capture *service.CaptureService,
	store *config.Store,
	notifier *notification.Manager,
	log *slog.Logger,
) {
	if notifier == nil || !notifier.Enabled() {
		return
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	state := captureNotificationState{}

	check := func(now time.Time) {
		status := capture.Status()
		event := state.observe(now, status.LastError != "", status.Running)
		if event == captureNotificationNone {
			return
		}
		captureConfig := store.Get().Capture
		var err error
		if event == captureNotificationFailed {
			err = notifier.CaptureFailed(captureConfig.Source, captureConfig.Device)
		} else {
			err = notifier.CaptureRecovered(captureConfig.Source, captureConfig.Device)
		}
		if err != nil {
			if event == captureNotificationFailed {
				state.failureReported = false
			}
			log.Warn("show capture desktop notification", "error", err)
		}
	}

	check(time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			check(now)
		}
	}
}
