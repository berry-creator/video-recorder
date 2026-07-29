package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadNormalizesAmbiguousCameraInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.Capture.Source = "camera"
	cfg.Capture.Device = "0"
	cfg.Capture.PixelFormat = "nv12"
	cfg.Capture.VideoCodec = "mjpeg"
	writeTestConfig(t, path, cfg)

	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded := store.Get().Capture
	if loaded.PixelFormat != "" || loaded.VideoCodec != "mjpeg" {
		t.Fatalf("normalized camera input = %#v", loaded)
	}

	var persisted Config
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Capture.PixelFormat != "" || persisted.Capture.VideoCodec != "mjpeg" {
		t.Fatalf("normalized camera input was not persisted: %#v", persisted.Capture)
	}
}

func TestLoadAllowsMissingCameraInputSoConsoleCanStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.Capture.Source = "camera"
	cfg.Capture.Device = "0"
	writeTestConfig(t, path, cfg)

	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if store.Get().Capture.Source != "camera" {
		t.Fatalf("loaded source = %q", store.Get().Capture.Source)
	}
}

func writeTestConfig(t *testing.T, path string, cfg Config) {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadCreatesDefaultAndPersistsUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if store.Get().Capture.Source != "mock" {
		t.Fatalf("default source = %q, want mock", store.Get().Capture.Source)
	}
	if store.Get().Capture.FPS != 30 {
		t.Fatalf("default FPS = %d, want 30", store.Get().Capture.FPS)
	}
	if store.Get().Capture.BufferSeconds != 30 {
		t.Fatalf("default memory buffer duration = %d, want 30", store.Get().Capture.BufferSeconds)
	}
	if store.Get().Recording.MaxDurationMinutes != 60 {
		t.Fatalf("default maximum recording duration = %d, want 60", store.Get().Recording.MaxDurationMinutes)
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

func TestInvalidRecordingDurationIsRejected(t *testing.T) {
	for _, duration := range []int{0, 10081} {
		cfg := Default()
		cfg.Recording.MaxDurationMinutes = duration
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate() accepted maximum recording duration %d", duration)
		}
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
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted a camera source without an input format")
	}
	cfg.Capture.PixelFormat = "nv12"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() rejected a camera source with a device ID: %v", err)
	}
	cfg.Capture.VideoCodec = "mjpeg"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted both a pixel format and video codec")
	}
	cfg.Capture.PixelFormat = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() rejected a video codec camera input: %v", err)
	}
}
