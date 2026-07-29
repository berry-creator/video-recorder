package tray

import "testing"

func TestLabelsForLocale(t *testing.T) {
	tests := []struct {
		locale     string
		openFolder string
		quit       string
	}{
		{locale: "zh-CN", openFolder: "打开存储目录", quit: "退出程序"},
		{locale: "zh_Hant_TW", openFolder: "打开存储目录", quit: "退出程序"},
		{locale: "en-US", openFolder: "Open Storage Directory", quit: "Quit"},
		{locale: "ja-JP", openFolder: "Open Storage Directory", quit: "Quit"},
	}
	for _, test := range tests {
		t.Run(test.locale, func(t *testing.T) {
			labels := labelsForLocale(test.locale)
			if labels.openFolder != test.openFolder || labels.quit != test.quit {
				t.Fatalf("labelsForLocale(%q) = (%q, %q), want (%q, %q)", test.locale, labels.openFolder, labels.quit, test.openFolder, test.quit)
			}
		})
	}
}

func TestLocaleFromEnvironment(t *testing.T) {
	values := map[string]string{
		"LC_ALL":      "",
		"LC_MESSAGES": "",
		"LANGUAGE":    "zh_CN:en_US",
		"LANG":        "en_US.UTF-8",
	}
	if got := localeFromEnvironment(func(name string) string { return values[name] }); got != "zh_CN" {
		t.Fatalf("localeFromEnvironment() = %q, want zh_CN", got)
	}
	values["LC_ALL"] = "en_GB.UTF-8"
	if got := localeFromEnvironment(func(name string) string { return values[name] }); got != "en_GB.UTF-8" {
		t.Fatalf("localeFromEnvironment() with LC_ALL = %q, want en_GB.UTF-8", got)
	}
}

func TestFirstLocaleReadsMacOSLanguageList(t *testing.T) {
	value := "(\n    \"zh-Hans-CN\",\n    \"en-CN\"\n)\n"
	if got := firstLocale(value); got != "zh-Hans-CN" {
		t.Fatalf("firstLocale() = %q, want zh-Hans-CN", got)
	}
}
