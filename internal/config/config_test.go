package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadBackfillsLiveTranscodeDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	export := document["export"].(map[string]any)
	delete(export, "transcodeDuringRecording")
	delete(export, "encoder")
	delete(export, "softwareThreads")
	delete(export, "videoBitrateKbps")
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Get().Export; !got.TranscodeDuringRecording || got.Encoder != ExportEncoderAuto || got.SoftwareThreads != 2 || got.VideoBitrateKbps != 1000 {
		t.Fatalf("backfilled export settings = %#v", got)
	}
}

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
	wantPixelFormat, wantVideoCodec := "nv12", ""
	if runtime.GOOS == "windows" {
		wantPixelFormat, wantVideoCodec = "", "mjpeg"
	}
	if loaded.PixelFormat != wantPixelFormat || loaded.VideoCodec != wantVideoCodec {
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
	if persisted.Capture.PixelFormat != wantPixelFormat || persisted.Capture.VideoCodec != wantVideoCodec {
		t.Fatalf("normalized camera input was not persisted: %#v", persisted.Capture)
	}
}

func TestNormalizeLoadedCameraInputByPlatform(t *testing.T) {
	tests := []struct {
		platform        string
		pixelFormat     string
		videoCodec      string
		wantPixelFormat string
		wantVideoCodec  string
	}{
		{platform: "windows", pixelFormat: "yuyv422", videoCodec: "mjpeg", wantVideoCodec: "mjpeg"},
		{platform: "darwin", pixelFormat: "nv12", videoCodec: "mjpeg", wantPixelFormat: "nv12"},
		{platform: "darwin", videoCodec: "mjpeg"},
		{platform: "linux", videoCodec: "mjpeg"},
	}
	for _, test := range tests {
		t.Run(test.platform+"/"+test.pixelFormat+"/"+test.videoCodec, func(t *testing.T) {
			cfg := Default()
			cfg.Capture.Source = "camera"
			cfg.Capture.Device = "camera"
			cfg.Capture.PixelFormat = test.pixelFormat
			cfg.Capture.VideoCodec = test.videoCodec
			if !normalizeLoadedConfigForPlatform(&cfg, test.platform) {
				t.Fatal("camera input was not normalized")
			}
			if cfg.Capture.PixelFormat != test.wantPixelFormat || cfg.Capture.VideoCodec != test.wantVideoCodec {
				t.Fatalf("normalized input = (%q, %q), want (%q, %q)", cfg.Capture.PixelFormat, cfg.Capture.VideoCodec, test.wantPixelFormat, test.wantVideoCodec)
			}
		})
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
	if store.Get().Recording.MaxDurationMinutes != 180 {
		t.Fatalf("default maximum recording duration = %d, want 180", store.Get().Recording.MaxDurationMinutes)
	}
	if store.Get().Storage.Organization != StorageOrganizationDay {
		t.Fatalf("default storage organization = %q, want day", store.Get().Storage.Organization)
	}
	if store.Get().Server.AllowMultipleInstances {
		t.Fatal("multiple instances are enabled by default")
	}
	if export := store.Get().Export; !export.TranscodeDuringRecording || export.Encoder != ExportEncoderAuto || export.SoftwareThreads != 2 || export.VideoBitrateKbps != 1000 {
		t.Fatalf("default export settings = %#v", export)
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

func TestReadAllowMultipleInstances(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	if allowed, err := ReadAllowMultipleInstances(missing); err != nil || allowed {
		t.Fatalf("missing config policy = %v, %v; want false, nil", allowed, err)
	}

	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.Server.AllowMultipleInstances = true
	writeTestConfig(t, path, cfg)
	if allowed, err := ReadAllowMultipleInstances(path); err != nil || !allowed {
		t.Fatalf("configured policy = %v, %v; want true, nil", allowed, err)
	}

	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAllowMultipleInstances(path); err == nil {
		t.Fatal("malformed instance policy config was accepted")
	}
}

func TestValidateExportTranscodeSettings(t *testing.T) {
	for _, update := range []func(*Config){
		func(cfg *Config) { cfg.Export.Encoder = "unknown" },
		func(cfg *Config) { cfg.Export.SoftwareThreads = 0 },
		func(cfg *Config) { cfg.Export.SoftwareThreads = 17 },
		func(cfg *Config) { cfg.Export.VideoBitrateKbps = 99 },
		func(cfg *Config) { cfg.Export.VideoBitrateKbps = 100001 },
	} {
		cfg := Default()
		update(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("Validate() accepted export settings %#v", cfg.Export)
		}
	}
}

func TestDefaultFFmpegPathByPlatform(t *testing.T) {
	if got := defaultFFmpegPath("darwin"); got != "/opt/homebrew/bin/ffmpeg" {
		t.Fatalf("macOS default FFmpeg path = %q", got)
	}
	for _, platform := range []string{"linux", "windows"} {
		if got := defaultFFmpegPath(platform); got != "ffmpeg" {
			t.Fatalf("%s default FFmpeg path = %q", platform, got)
		}
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
	if err := cfg.Validate(); runtime.GOOS == "windows" && err != nil {
		t.Fatalf("Validate() rejected a Windows video codec camera input: %v", err)
	} else if runtime.GOOS != "windows" && err == nil {
		t.Fatal("Validate() accepted a video codec camera input outside Windows")
	}
}
