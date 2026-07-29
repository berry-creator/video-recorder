package tray

import "strings"

type trayLabels struct {
	tooltip          string
	openFolder       string
	openFolderTip    string
	configuration    string
	configurationTip string
	quit             string
	quitTip          string
}

func labelsForLocale(locale string) trayLabels {
	if isChineseLocale(locale) {
		return trayLabels{
			tooltip:          "本地音视频采集与后台录屏服务",
			openFolder:       "打开存储目录",
			openFolderTip:    "打开本地视频存储文件夹",
			configuration:    "服务配置",
			configurationTip: "打开本地服务配置页面",
			quit:             "退出程序",
			quitTip:          "退出应用",
		}
	}
	return trayLabels{
		tooltip:          "Local video capture and background recording service",
		openFolder:       "Open Storage Directory",
		openFolderTip:    "Open the local video storage directory",
		configuration:    "Service Settings",
		configurationTip: "Open the local service settings page",
		quit:             "Quit",
		quitTip:          "Quit the application",
	}
}

func isChineseLocale(locale string) bool {
	locale = strings.ToLower(strings.TrimSpace(locale))
	return locale == "zh" || strings.HasPrefix(locale, "zh-") || strings.HasPrefix(locale, "zh_")
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
