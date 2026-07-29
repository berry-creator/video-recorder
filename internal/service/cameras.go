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
	"strconv"
	"strings"
	"time"
)

type CameraDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type CameraMode struct {
	PixelFormat string `json:"pixelFormat"`
	VideoCodec  string `json:"videoCodec"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	FPS         int    `json:"fps"`
}

type CameraCapabilities struct {
	Device            string       `json:"device"`
	PixelFormats      []string     `json:"pixelFormats"`
	VideoCodecs       []string     `json:"videoCodecs"`
	Modes             []CameraMode `json:"modes"`
	Recommended       CameraMode   `json:"recommended"`
	DrawtextAvailable bool         `json:"drawtextAvailable"`
	Warnings          []string     `json:"warnings"`
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

func (d *CameraDetector) Capabilities(ctx context.Context, ffmpegPath, device, pixelFormat, videoCodec string) (CameraCapabilities, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	capabilities := CameraCapabilities{Device: device, PixelFormats: []string{}, VideoCodecs: []string{}, Modes: []CameraMode{}, Warnings: []string{}}
	filterOutput, filterErr := d.run(ctx, ffmpegPath, "-hide_banner", "-filters")
	capabilities.DrawtextAvailable = filterErr == nil && hasFFmpegFilter(string(filterOutput), "drawtext")
	if !capabilities.DrawtextAvailable {
		capabilities.Warnings = append(capabilities.Warnings, "FFmpeg does not provide drawtext; capture will continue without video watermarks")
	}

	var probeOutput []byte
	var probeErr error
	switch d.platform {
	case "darwin":
		formatOutput, formatErr := d.run(ctx, ffmpegPath,
			"-hide_banner", "-loglevel", "verbose", "-f", "avfoundation",
			"-pixel_format", "monob", "-i", avFoundationInput(device),
			"-frames:v", "1", "-f", "null", "-",
		)
		capabilities.PixelFormats = parseAVFoundationPixelFormats(string(formatOutput))
		if formatErr == nil && len(capabilities.PixelFormats) == 0 {
			capabilities.PixelFormats = append(capabilities.PixelFormats, "monob")
		}
		selected := pixelFormat
		if selected == "" {
			selected = recommendedPixelFormat(capabilities.PixelFormats)
		}
		if selected != "" {
			probeOutput, probeErr = d.run(ctx, ffmpegPath,
				"-hide_banner", "-loglevel", "verbose", "-f", "avfoundation",
				"-pixel_format", selected, "-framerate", "123", "-video_size", "123x123",
				"-i", avFoundationInput(device),
				"-frames:v", "1", "-f", "null", "-",
			)
			capabilities.Modes = parseAVFoundationModes(string(probeOutput), selected)
		}
		if len(capabilities.Modes) == 0 {
			if mode, ok := parseAVFoundationStreamMode(string(probeOutput)); ok {
				capabilities.Modes = appendUniqueMode(capabilities.Modes, mode)
				capabilities.PixelFormats = appendUniqueString(capabilities.PixelFormats, mode.PixelFormat)
			}
		}
		if len(capabilities.Modes) == 0 {
			defaultOutput, defaultErr := d.run(ctx, ffmpegPath,
				"-hide_banner", "-loglevel", "verbose", "-f", "avfoundation",
				"-i", avFoundationInput(device),
				"-frames:v", "1", "-f", "null", "-",
			)
			if mode, ok := parseAVFoundationStreamMode(string(defaultOutput)); ok {
				capabilities.Modes = appendUniqueMode(capabilities.Modes, mode)
				capabilities.PixelFormats = appendUniqueString(capabilities.PixelFormats, mode.PixelFormat)
			}
			probeOutput = append(probeOutput, defaultOutput...)
			probeErr = defaultErr
		}
	case "windows":
		probeOutput, probeErr = d.run(ctx, ffmpegPath,
			"-hide_banner", "-list_options", "true", "-f", "dshow", "-i", "video="+device,
		)
		capabilities.PixelFormats, capabilities.VideoCodecs, capabilities.Modes = parseDirectShowCapabilities(string(probeOutput))
	case "linux":
		probeOutput, probeErr = d.run(ctx, ffmpegPath,
			"-hide_banner", "-f", "v4l2", "-list_formats", "all", "-i", device,
		)
		capabilities.PixelFormats, capabilities.Modes = parseV4L2Capabilities(string(probeOutput))
	default:
		return CameraCapabilities{}, fmt.Errorf("camera capability detection is unsupported on %s", d.platform)
	}
	if err := ctx.Err(); err != nil {
		return CameraCapabilities{}, fmt.Errorf("camera capability detection: %w", err)
	}
	if len(capabilities.PixelFormats) == 0 && len(capabilities.Modes) == 0 && probeErr != nil {
		return CameraCapabilities{}, fmt.Errorf("detect camera capabilities: %w", probeErr)
	}
	if len(capabilities.PixelFormats) == 0 {
		for _, mode := range capabilities.Modes {
			capabilities.PixelFormats = appendUniqueString(capabilities.PixelFormats, mode.PixelFormat)
		}
	}
	if len(capabilities.VideoCodecs) == 0 {
		for _, mode := range capabilities.Modes {
			capabilities.VideoCodecs = appendUniqueString(capabilities.VideoCodecs, mode.VideoCodec)
		}
	}
	capabilities.Recommended = recommendCameraMode(pixelFormat, videoCodec, capabilities.PixelFormats, capabilities.VideoCodecs, capabilities.Modes)
	return capabilities, nil
}

func avFoundationInput(device string) string {
	if strings.Contains(device, ":") {
		return device
	}
	return device + ":none"
}

func hasFFmpegFilter(output, name string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == name {
			return true
		}
	}
	return false
}

func parseAVFoundationPixelFormats(output string) []string {
	formats := []string{}
	inFormats := false
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "Supported pixel formats:") {
			inFormats = true
			continue
		}
		if !inFormats {
			continue
		}
		value := ffmpegLogValue(line)
		if !simpleFormatPattern.MatchString(value) {
			if len(formats) > 0 {
				break
			}
			continue
		}
		formats = appendUniqueString(formats, value)
	}
	return formats
}

func parseAVFoundationModes(output, pixelFormat string) []CameraMode {
	modes := []CameraMode{}
	for _, match := range avFoundationModePattern.FindAllStringSubmatch(output, -1) {
		width, _ := strconv.Atoi(match[1])
		height, _ := strconv.Atoi(match[2])
		maximum, _ := strconv.ParseFloat(match[4], 64)
		modes = appendUniqueMode(modes, CameraMode{PixelFormat: pixelFormat, Width: width, Height: height, FPS: int(maximum + 0.5)})
	}
	return modes
}

func parseAVFoundationStreamMode(output string) (CameraMode, bool) {
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "Video:") {
			continue
		}
		resolution := streamResolutionPattern.FindStringSubmatch(line)
		frameRate := streamFPSPattern.FindStringSubmatch(line)
		if len(resolution) != 3 || len(frameRate) != 2 {
			continue
		}
		width, _ := strconv.Atoi(resolution[1])
		height, _ := strconv.Atoi(resolution[2])
		fps, _ := strconv.ParseFloat(frameRate[1], 64)
		pixelFormat := ""
		parts := strings.Split(line[strings.Index(line, "Video:")+len("Video:"):], ",")
		if len(parts) > 1 {
			candidate := strings.TrimSpace(parts[1])
			if simpleFormatPattern.MatchString(candidate) {
				pixelFormat = candidate
			}
		}
		if width > 0 && height > 0 && fps > 0 {
			return CameraMode{PixelFormat: pixelFormat, Width: width, Height: height, FPS: int(fps + 0.5)}, true
		}
	}
	return CameraMode{}, false
}

func parseDirectShowCapabilities(output string) ([]string, []string, []CameraMode) {
	formats := []string{}
	codecs := []string{}
	modes := []CameraMode{}
	for _, line := range strings.Split(output, "\n") {
		match := directShowModePattern.FindStringSubmatch(line)
		if len(match) != 9 {
			continue
		}
		kind, format := match[1], match[2]
		minWidth, _ := strconv.Atoi(match[3])
		minHeight, _ := strconv.Atoi(match[4])
		minFPS, _ := strconv.ParseFloat(match[5], 64)
		maxWidth, _ := strconv.Atoi(match[6])
		maxHeight, _ := strconv.Atoi(match[7])
		maxFPS, _ := strconv.ParseFloat(match[8], 64)
		base := CameraMode{}
		if kind == "vcodec" {
			base.VideoCodec = format
			codecs = appendUniqueString(codecs, format)
		} else {
			base.PixelFormat = format
			formats = appendUniqueString(formats, format)
		}
		for _, candidate := range []CameraMode{
			{PixelFormat: base.PixelFormat, VideoCodec: base.VideoCodec, Width: minWidth, Height: minHeight, FPS: int(minFPS + 0.5)},
			{PixelFormat: base.PixelFormat, VideoCodec: base.VideoCodec, Width: minWidth, Height: minHeight, FPS: int(maxFPS + 0.5)},
			{PixelFormat: base.PixelFormat, VideoCodec: base.VideoCodec, Width: maxWidth, Height: maxHeight, FPS: int(minFPS + 0.5)},
			{PixelFormat: base.PixelFormat, VideoCodec: base.VideoCodec, Width: maxWidth, Height: maxHeight, FPS: int(maxFPS + 0.5)},
		} {
			modes = appendUniqueMode(modes, candidate)
		}
	}
	return formats, codecs, modes
}

func parseV4L2Capabilities(output string) ([]string, []CameraMode) {
	formats := []string{}
	modes := []CameraMode{}
	for _, line := range strings.Split(output, "\n") {
		match := v4l2RawFormatPattern.FindStringSubmatch(line)
		if len(match) != 3 {
			continue
		}
		format := strings.ToLower(match[1])
		formats = appendUniqueString(formats, format)
		for _, size := range resolutionPattern.FindAllStringSubmatch(match[2], -1) {
			width, _ := strconv.Atoi(size[1])
			height, _ := strconv.Atoi(size[2])
			modes = appendUniqueMode(modes, CameraMode{PixelFormat: format, Width: width, Height: height})
		}
	}
	return formats, modes
}

func recommendCameraMode(pixelFormat, videoCodec string, pixelFormats, videoCodecs []string, modes []CameraMode) CameraMode {
	best := CameraMode{PixelFormat: pixelFormat, VideoCodec: videoCodec}
	bestScore := int(^uint(0) >> 1)
	for _, mode := range modes {
		if pixelFormat != "" && mode.PixelFormat != pixelFormat {
			continue
		}
		if videoCodec != "" && mode.VideoCodec != videoCodec {
			continue
		}
		score := absInt(mode.Width-1280)*2 + absInt(mode.Height-720)*3 + absInt(mode.FPS-30)*20
		if mode.FPS == 0 {
			score += 1000
		}
		if strings.EqualFold(mode.VideoCodec, "mjpeg") {
			score--
		}
		if score < bestScore {
			best, bestScore = mode, score
		}
	}
	if bestScore == int(^uint(0)>>1) && pixelFormat == "" && videoCodec == "" {
		if len(videoCodecs) > 0 {
			best.VideoCodec = videoCodecs[0]
		} else {
			best.PixelFormat = recommendedPixelFormat(pixelFormats)
		}
	}
	return best
}

func recommendedPixelFormat(formats []string) string {
	for _, preferred := range []string{"nv12", "yuyv422", "uyvy422", "bgr0", "0rgb"} {
		if containsString(formats, preferred) {
			return preferred
		}
	}
	if len(formats) > 0 {
		return formats[0]
	}
	return ""
}

func ffmpegLogValue(line string) string {
	if index := strings.LastIndex(line, "] "); index >= 0 {
		return strings.TrimSpace(line[index+2:])
	}
	return strings.TrimSpace(line)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func appendUniqueString(values []string, value string) []string {
	if value == "" || containsString(values, value) {
		return values
	}
	return append(values, value)
}

func appendUniqueMode(modes []CameraMode, mode CameraMode) []CameraMode {
	for _, current := range modes {
		if current == mode {
			return modes
		}
	}
	return append(modes, mode)
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
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
	simpleFormatPattern       = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	avFoundationModePattern   = regexp.MustCompile(`(\d+)x(\d+)@\[([0-9.]+)\s+([0-9.]+)\]fps`)
	streamResolutionPattern   = regexp.MustCompile(`(?:^|[,\s])(\d{2,5})x(\d{2,5})(?:[,\s]|$)`)
	streamFPSPattern          = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s+fps\b`)
	directShowModePattern     = regexp.MustCompile(`(pixel_format|vcodec)=([A-Za-z0-9_]+).*min s=(\d+)x(\d+) fps=([0-9.]+) max s=(\d+)x(\d+) fps=([0-9.]+)`)
	v4l2RawFormatPattern      = regexp.MustCompile(`(?i)Raw\s*:\s*([A-Za-z0-9_]+)\s*:.*:\s*(.+)$`)
	resolutionPattern         = regexp.MustCompile(`(\d+)x(\d+)`)
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
