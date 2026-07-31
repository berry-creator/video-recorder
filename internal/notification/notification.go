package notification

import (
	"fmt"
	"strings"
)

type backend interface {
	Enabled() bool
	Alert(title, message string) error
	Notify(title, message string, critical bool) error
}

type Manager struct {
	backend  backend
	language string
}

func New() *Manager {
	return &Manager{backend: newSystemBackend(), language: systemLanguage()}
}

func newManager(language string, target backend) *Manager {
	return &Manager{backend: target, language: normalizeLanguage(language)}
}

func (m *Manager) Enabled() bool {
	return m != nil && m.backend != nil && m.backend.Enabled()
}

func (m *Manager) AlertStartup(err error, instanceAlreadyRunning bool) error {
	if !m.Enabled() {
		return nil
	}
	text := labelsForLanguage(m.language)
	message := text.startupFailed
	if instanceAlreadyRunning {
		message = text.instanceAlreadyRunning
	} else if err != nil {
		message += "\n\n" + text.details + ": " + truncateText(err.Error(), 400)
	}
	return m.backend.Alert(text.startupTitle, message)
}

func (m *Manager) CaptureFailed(source, device string) error {
	if !m.Enabled() {
		return nil
	}
	text := labelsForLanguage(m.language)
	title, message := text.sourceFailedTitle, text.sourceFailed
	if source == "camera" {
		title = text.cameraFailedTitle
		message = text.cameraFailed
		if device = strings.TrimSpace(device); device != "" {
			message = fmt.Sprintf(text.cameraFailedNamed, truncateText(device, 80))
		}
	}
	return m.backend.Notify(title, message, true)
}

func (m *Manager) CaptureRecovered(source, device string) error {
	if !m.Enabled() {
		return nil
	}
	text := labelsForLanguage(m.language)
	title, message := text.sourceRecoveredTitle, text.sourceRecovered
	if source == "camera" {
		title = text.cameraRecoveredTitle
		message = text.cameraRecovered
		if device = strings.TrimSpace(device); device != "" {
			message = fmt.Sprintf(text.cameraRecoveredNamed, truncateText(device, 80))
		}
	}
	return m.backend.Notify(title, message, false)
}

type localizedLabels struct {
	startupTitle           string
	startupFailed          string
	instanceAlreadyRunning string
	details                string
	cameraFailedTitle      string
	cameraFailed           string
	cameraFailedNamed      string
	cameraRecoveredTitle   string
	cameraRecovered        string
	cameraRecoveredNamed   string
	sourceFailedTitle      string
	sourceFailed           string
	sourceRecoveredTitle   string
	sourceRecovered        string
}

func labelsForLanguage(language string) localizedLabels {
	if normalizeLanguage(language) == "zh" {
		return localizedLabels{
			startupTitle:           "Video Recorder 启动失败",
			startupFailed:          "应用无法启动，请检查配置、服务端口和 FFmpeg 设置。",
			instanceAlreadyRunning: "另一个 Video Recorder 实例已在运行。",
			details:                "详细信息",
			cameraFailedTitle:      "摄像头连接失败",
			cameraFailed:           "无法连接配置的摄像头，服务将继续自动重试。",
			cameraFailedNamed:      "无法连接摄像头“%s”，服务将继续自动重试。",
			cameraRecoveredTitle:   "摄像头连接已恢复",
			cameraRecovered:        "摄像头已重新连接，实时预览可以继续使用。",
			cameraRecoveredNamed:   "摄像头“%s”已重新连接，实时预览可以继续使用。",
			sourceFailedTitle:      "视频源启动失败",
			sourceFailed:           "无法启动配置的视频源，服务将继续自动重试。",
			sourceRecoveredTitle:   "视频源已恢复",
			sourceRecovered:        "视频源已恢复，实时预览可以继续使用。",
		}
	}
	return localizedLabels{
		startupTitle:           "Video Recorder could not start",
		startupFailed:          "The application could not start. Check the configuration, service port, and FFmpeg settings.",
		instanceAlreadyRunning: "Another Video Recorder instance is already running.",
		details:                "Details",
		cameraFailedTitle:      "Camera connection failed",
		cameraFailed:           "The configured camera could not be connected. The service will keep retrying automatically.",
		cameraFailedNamed:      "Camera \"%s\" could not be connected. The service will keep retrying automatically.",
		cameraRecoveredTitle:   "Camera connection restored",
		cameraRecovered:        "The camera reconnected and live preview is available again.",
		cameraRecoveredNamed:   "Camera \"%s\" reconnected and live preview is available again.",
		sourceFailedTitle:      "Video source failed to start",
		sourceFailed:           "The configured video source could not start. The service will keep retrying automatically.",
		sourceRecoveredTitle:   "Video source restored",
		sourceRecovered:        "The video source recovered and live preview is available again.",
	}
}

func normalizeLanguage(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "zh" || strings.HasPrefix(value, "zh-") || strings.HasPrefix(value, "zh_") {
		return "zh"
	}
	return "en"
}

func truncateText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	characters := []rune(strings.TrimSpace(value))
	if len(characters) <= limit {
		return string(characters)
	}
	if limit <= 3 {
		return string(characters[:limit])
	}
	return string(characters[:limit-3]) + "..."
}

func localeFromEnvironment(getenv func(string) string) string {
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANGUAGE", "LANG"} {
		if locale := firstLocale(getenv(name)); locale != "" {
			return locale
		}
	}
	return "en"
}

func firstLocale(value string) string {
	for _, line := range strings.Split(value, "\n") {
		candidate := strings.Trim(strings.TrimSpace(line), "\",() ")
		candidate = strings.TrimSuffix(candidate, ",")
		candidate = strings.TrimSpace(strings.Trim(candidate, "\""))
		if separator := strings.IndexByte(candidate, ':'); separator >= 0 {
			candidate = candidate[:separator]
		}
		if candidate != "" {
			return candidate
		}
	}
	return ""
}
