package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

type Config struct {
	Server    ServerConfig    `json:"server"`
	Capture   CaptureConfig   `json:"capture"`
	Recording RecordingConfig `json:"recording"`
	Storage   StorageConfig   `json:"storage"`
	Export    ExportConfig    `json:"export"`
}

type ServerConfig struct {
	Address        string   `json:"address"`
	AllowedOrigins []string `json:"allowedOrigins"`
}

type CaptureConfig struct {
	Source        string `json:"source"`
	Device        string `json:"device"`
	PixelFormat   string `json:"pixelFormat"`
	VideoCodec    string `json:"videoCodec"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	FPS           int    `json:"fps"`
	JPEGQuality   int    `json:"jpegQuality"`
	BufferSeconds int    `json:"bufferSeconds"`
	FFmpegPath    string `json:"ffmpegPath"`
}

type StorageConfig struct {
	Directory    string `json:"directory"`
	Organization string `json:"organization"`
}

type RecordingConfig struct {
	MaxDurationMinutes int `json:"maxDurationMinutes"`
}

const (
	StorageOrganizationDay   = "day"
	StorageOrganizationMonth = "month"
	StorageOrganizationNone  = "none"
)

type ExportConfig struct {
	QueueSize int `json:"queueSize"`
}

func Default() Config {
	return Config{
		Server: ServerConfig{Address: "127.0.0.1:9000", AllowedOrigins: []string{}},
		Capture: CaptureConfig{
			Source:        "mock",
			Device:        "",
			PixelFormat:   "",
			VideoCodec:    "",
			Width:         1280,
			Height:        720,
			FPS:           30,
			JPEGQuality:   5,
			BufferSeconds: 30,
			FFmpegPath:    "ffmpeg",
		},
		Recording: RecordingConfig{MaxDurationMinutes: 60},
		Storage:   StorageConfig{Directory: defaultStorageDirectory(), Organization: StorageOrganizationDay},
		Export:    ExportConfig{QueueSize: 8},
	}
}

func defaultStorageDirectory() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "recordings"
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Movies", "Video Recorder")
	case "windows":
		return filepath.Join(home, "Videos", "Video Recorder")
	default:
		return filepath.Join(home, "Videos", "Video Recorder")
	}
}

func (c Config) Validate() error {
	return c.validate(true)
}

func (c Config) validate(requireCameraInput bool) error {
	if strings.TrimSpace(c.Server.Address) == "" {
		return errors.New("server address is required")
	}
	_, portText, err := net.SplitHostPort(c.Server.Address)
	if err != nil {
		return errors.New("server address must contain a valid host and port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("server port must be between 1 and 65535")
	}
	for _, origin := range c.Server.AllowedOrigins {
		origin = strings.TrimSpace(origin)
		if origin == "*" {
			continue
		}
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
			(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
			return fmt.Errorf("invalid allowed origin: %q", origin)
		}
	}
	if c.Capture.Source != "mock" && c.Capture.Source != "camera" {
		return errors.New("capture source must be mock or camera")
	}
	if c.Capture.Source == "camera" && strings.TrimSpace(c.Capture.Device) == "" {
		return errors.New("camera device is required")
	}
	if c.Capture.Source == "camera" {
		hasPixelFormat := strings.TrimSpace(c.Capture.PixelFormat) != ""
		hasVideoCodec := strings.TrimSpace(c.Capture.VideoCodec) != ""
		if hasVideoCodec && runtime.GOOS != "windows" {
			return errors.New("camera video codec inputs are supported only on Windows")
		}
		if hasPixelFormat && hasVideoCodec {
			return errors.New("camera requires exactly one pixel format or video codec")
		}
		if requireCameraInput && !hasPixelFormat && !hasVideoCodec {
			return errors.New("camera pixel format or video codec is required")
		}
	}
	if c.Capture.Width < 160 || c.Capture.Width > 7680 || c.Capture.Height < 120 || c.Capture.Height > 4320 {
		return errors.New("capture resolution is outside the supported range")
	}
	if c.Capture.FPS < 1 || c.Capture.FPS > 120 {
		return errors.New("capture fps must be between 1 and 120")
	}
	if c.Capture.JPEGQuality < 2 || c.Capture.JPEGQuality > 31 {
		return errors.New("JPEG quality must be between 2 and 31")
	}
	if c.Capture.BufferSeconds < 1 || c.Capture.BufferSeconds > 3600 {
		return errors.New("memory buffer duration must be between 1 and 3600 seconds")
	}
	if strings.TrimSpace(c.Capture.FFmpegPath) == "" {
		return errors.New("ffmpeg path is required")
	}
	if c.Recording.MaxDurationMinutes < 1 || c.Recording.MaxDurationMinutes > 10080 {
		return errors.New("maximum recording duration must be between 1 and 10080 minutes")
	}
	if strings.TrimSpace(c.Storage.Directory) == "" {
		return errors.New("storage directory is required")
	}
	if c.Storage.Organization != StorageOrganizationDay && c.Storage.Organization != StorageOrganizationMonth && c.Storage.Organization != StorageOrganizationNone {
		return errors.New("storage organization must be day, month, or none")
	}
	if c.Export.QueueSize < 1 || c.Export.QueueSize > 100 {
		return errors.New("export queue size must be between 1 and 100")
	}
	return nil
}

type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

func Load(path string) (*Store, error) {
	store := &Store{path: path, cfg: Default()}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := store.save(store.cfg); err != nil {
			return nil, err
		}
		return store, nil
	}
	if err := json.Unmarshal(data, &store.cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	normalized := normalizeLoadedConfig(&store.cfg)
	if err := store.cfg.validate(false); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	if normalized {
		if err := store.save(store.cfg); err != nil {
			return nil, fmt.Errorf("save normalized config: %w", err)
		}
	}
	return store, nil
}

func normalizeLoadedConfig(cfg *Config) bool {
	return normalizeLoadedConfigForPlatform(cfg, runtime.GOOS)
}

func normalizeLoadedConfigForPlatform(cfg *Config, platform string) bool {
	if cfg.Capture.Source != "camera" || strings.TrimSpace(cfg.Capture.VideoCodec) == "" {
		return false
	}
	if platform == "windows" {
		if strings.TrimSpace(cfg.Capture.PixelFormat) == "" {
			return false
		}
		cfg.Capture.PixelFormat = ""
		return true
	}
	cfg.Capture.VideoCodec = ""
	return true
}

func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Store) Update(next Config) error {
	if err := next.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.save(next); err != nil {
		return err
	}
	s.cfg = next
	return nil
}

func (s *Store) save(cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("set config permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close config: %w", err)
	}
	if err := replaceFile(tmpName, s.path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	ok = true
	return nil
}
