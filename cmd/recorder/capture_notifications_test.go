package main

import (
	"testing"
	"time"
)

func TestCaptureNotificationStateDelaysAndLimitsFailureNotifications(t *testing.T) {
	started := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	state := captureNotificationState{}

	if event := state.observe(started, true, false); event != captureNotificationNone {
		t.Fatalf("initial failure event = %v, want none", event)
	}
	if event := state.observe(started.Add(captureFailureDelay-time.Millisecond), true, false); event != captureNotificationNone {
		t.Fatalf("event before delay = %v, want none", event)
	}
	if event := state.observe(started.Add(captureFailureDelay), true, false); event != captureNotificationFailed {
		t.Fatalf("event at delay = %v, want failed", event)
	}
	if event := state.observe(started.Add(captureFailureDelay+captureFailureCooldown-time.Millisecond), true, false); event != captureNotificationNone {
		t.Fatalf("event before cooldown = %v, want none", event)
	}
	if event := state.observe(started.Add(captureFailureDelay+captureFailureCooldown), true, false); event != captureNotificationFailed {
		t.Fatalf("event at cooldown = %v, want failed", event)
	}
}

func TestCaptureNotificationStateReportsRecoveryOnlyAfterReportedFailure(t *testing.T) {
	started := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	state := captureNotificationState{}

	state.observe(started, true, false)
	if event := state.observe(started.Add(time.Second), false, true); event != captureNotificationNone {
		t.Fatalf("transient failure recovery event = %v, want none", event)
	}

	state.observe(started.Add(2*time.Second), true, false)
	if event := state.observe(started.Add(2*time.Second+captureFailureDelay), true, false); event != captureNotificationFailed {
		t.Fatalf("reported failure event = %v, want failed", event)
	}
	if event := state.observe(started.Add(8*time.Second), false, false); event != captureNotificationNone {
		t.Fatalf("connecting event = %v, want none", event)
	}
	if event := state.observe(started.Add(9*time.Second), false, true); event != captureNotificationRecovered {
		t.Fatalf("recovery event = %v, want recovered", event)
	}
	if event := state.observe(started.Add(10*time.Second), false, true); event != captureNotificationNone {
		t.Fatalf("second healthy event = %v, want none", event)
	}
}
