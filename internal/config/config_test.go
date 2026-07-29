package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCreatesDefaultAndPersistsUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if store.Get().Capture.Source != "mock" {
		t.Fatalf("default source = %q, want mock", store.Get().Capture.Source)
	}
	if store.Get().Capture.FPS != 26 {
		t.Fatalf("default FPS = %d, want 26", store.Get().Capture.FPS)
	}
	if store.Get().Storage.Organization != StorageOrganizationDay {
		t.Fatalf("default storage organization = %q, want day", store.Get().Storage.Organization)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("default config was not persisted: %v", err)
	}

	next := store.Get()
	next.Capture.FPS = 24
	next.Storage.Directory = filepath.Join(t.TempDir(), "recordings")
	if err := store.Update(next); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if got := reloaded.Get().Capture.FPS; got != 24 {
		t.Fatalf("persisted FPS = %d, want 24", got)
	}
}

func TestInvalidStorageOrganizationIsRejected(t *testing.T) {
	for _, organization := range []string{"", "week"} {
		cfg := Default()
		cfg.Storage.Organization = organization
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate() accepted storage organization %q", organization)
		}
	}
}

func TestServerAddressRequiresValidPort(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "127.0.0.1:0", "127.0.0.1:65536"} {
		cfg := Default()
		cfg.Server.Address = address
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate() accepted server address %q", address)
		}
	}
}

func TestInvalidUpdateDoesNotChangeStore(t *testing.T) {
	store, err := Load(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	invalid := store.Get()
	invalid.Capture.FPS = 0
	if err := store.Update(invalid); err == nil {
		t.Fatal("Update() accepted invalid FPS")
	}
	if got := store.Get().Capture.FPS; got != Default().Capture.FPS {
		t.Fatalf("store changed after rejected update: FPS = %d", got)
	}
}

func TestCameraSourceRequiresSpecificDevice(t *testing.T) {
	cfg := Default()
	cfg.Capture.Source = "camera"
	cfg.Capture.Device = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted a camera source without a device ID")
	}

	cfg.Capture.Device = "0"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() rejected a camera source with a device ID: %v", err)
	}
}
