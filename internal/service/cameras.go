package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

type CameraDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cameraCommand func(context.Context, string, ...string) ([]byte, error)

type CameraDetector struct {
	platform string
	run      cameraCommand
	glob     func(string) ([]string, error)
	readFile func(string) ([]byte, error)
}

func NewCameraDetector() *CameraDetector {
	return &CameraDetector{
		platform: runtime.GOOS,
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			configureCommand(cmd)
			return cmd.CombinedOutput()
		},
		glob:     filepath.Glob,
		readFile: os.ReadFile,
	}
}

func (d *CameraDetector) List(ctx context.Context, ffmpegPath string) ([]CameraDevice, error) {
	switch d.platform {
	case "linux":
		return d.listLinux()
	case "darwin", "windows":
		return d.listFFmpeg(ctx, ffmpegPath)
	default:
		return nil, fmt.Errorf("camera detection is unsupported on %s", d.platform)
	}
}

func (d *CameraDetector) listLinux() ([]CameraDevice, error) {
	paths, err := d.glob("/dev/video*")
	if err != nil {
		return nil, fmt.Errorf("find camera devices: %w", err)
	}
	sort.Strings(paths)
	devices := make([]CameraDevice, 0, len(paths))
	for _, path := range paths {
		name := filepath.Base(path)
		sysfsName := filepath.Join("/sys/class/video4linux", name, "name")
		if data, err := d.readFile(sysfsName); err == nil && strings.TrimSpace(string(data)) != "" {
			name = strings.TrimSpace(string(data))
		}
		devices = append(devices, CameraDevice{ID: path, Name: name})
	}
	return devices, nil
}

func (d *CameraDetector) listFFmpeg(ctx context.Context, ffmpegPath string) ([]CameraDevice, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	var args []string
	if d.platform == "darwin" {
		args = []string{"-hide_banner", "-f", "avfoundation", "-list_devices", "true", "-i", ""}
	} else {
		args = []string{"-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy"}
	}
	output, commandErr := d.run(ctx, ffmpegPath, args...)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, errors.New("camera detection timed out")
	}

	var devices []CameraDevice
	if d.platform == "darwin" {
		devices = parseAVFoundationDevices(string(output))
	} else {
		devices = parseDirectShowDevices(string(output))
	}
	if len(devices) > 0 || commandErr == nil || recognizedCameraList(d.platform, string(output)) {
		return devices, nil
	}
	return nil, fmt.Errorf("ffmpeg camera detection failed: %w", commandErr)
}

func recognizedCameraList(platform, output string) bool {
	output = strings.ToLower(output)
	if platform == "darwin" {
		return strings.Contains(output, "avfoundation video devices:")
	}
	return strings.Contains(output, "(video)") || strings.Contains(output, "video devices")
}

var (
	avFoundationDevicePattern = regexp.MustCompile(`\]\s+\[(\d+)\]\s+(.+?)\s*$`)
	directShowVideoPattern    = regexp.MustCompile(`^\[[^]]+\]\s+"(.*)"\s+\(video\)\s*$`)
	directShowAltPattern      = regexp.MustCompile(`^\[[^]]+\]\s+Alternative name "(.*)"\s*$`)
)

func parseAVFoundationDevices(output string) []CameraDevice {
	devices := make([]CameraDevice, 0)
	inVideoSection := false
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.Contains(line, "AVFoundation video devices:"):
			inVideoSection = true
			continue
		case strings.Contains(line, "AVFoundation audio devices:"):
			inVideoSection = false
			continue
		}
		if !inVideoSection {
			continue
		}
		match := avFoundationDevicePattern.FindStringSubmatch(line)
		if len(match) != 3 || strings.HasPrefix(strings.ToLower(match[2]), "capture screen") {
			continue
		}
		devices = append(devices, CameraDevice{ID: match[1], Name: match[2]})
	}
	return uniqueCameraDevices(devices)
}

func parseDirectShowDevices(output string) []CameraDevice {
	devices := make([]CameraDevice, 0)
	var pending *CameraDevice
	flush := func() {
		if pending != nil {
			devices = append(devices, *pending)
			pending = nil
		}
	}

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if match := directShowVideoPattern.FindStringSubmatch(line); len(match) == 2 {
			flush()
			pending = &CameraDevice{ID: match[1], Name: match[1]}
			continue
		}
		if pending != nil {
			if match := directShowAltPattern.FindStringSubmatch(line); len(match) == 2 {
				pending.ID = match[1]
				flush()
			}
		}
	}
	flush()
	return uniqueCameraDevices(devices)
}

func uniqueCameraDevices(devices []CameraDevice) []CameraDevice {
	seen := make(map[string]struct{}, len(devices))
	result := make([]CameraDevice, 0, len(devices))
	for _, device := range devices {
		if device.ID == "" {
			continue
		}
		if _, exists := seen[device.ID]; exists {
			continue
		}
		seen[device.ID] = struct{}{}
		result = append(result, device)
	}
	return result
}
