//go:build devcontainer || headless || (linux && !desktop)

package notification

import "testing"

func TestHeadlessBackendIsDisabled(t *testing.T) {
	manager := New()
	if manager.Enabled() {
		t.Fatal("headless notification backend must be disabled")
	}
	if err := manager.AlertStartup(nil, false); err != nil {
		t.Fatalf("headless startup alert: %v", err)
	}
}
