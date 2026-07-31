package notification

import (
	"errors"
	"strings"
	"testing"
)

type recordedNotification struct {
	title    string
	message  string
	critical bool
}

type recordingBackend struct {
	alert        recordedNotification
	notification recordedNotification
}

func (*recordingBackend) Enabled() bool {
	return true
}

func (b *recordingBackend) Alert(title, message string) error {
	b.alert = recordedNotification{title: title, message: message}
	return nil
}

func (b *recordingBackend) Notify(title, message string, critical bool) error {
	b.notification = recordedNotification{title: title, message: message, critical: critical}
	return nil
}

func TestLocalizedNotificationsUseChineseOnlyForChineseLocales(t *testing.T) {
	tests := []struct {
		locale      string
		wantChinese bool
	}{
		{locale: "zh-CN", wantChinese: true},
		{locale: "zh_Hant_TW", wantChinese: true},
		{locale: "en-US", wantChinese: false},
		{locale: "ja-JP", wantChinese: false},
	}
	for _, test := range tests {
		t.Run(test.locale, func(t *testing.T) {
			labels := labelsForLanguage(test.locale)
			isChinese := strings.Contains(labels.cameraFailedTitle, "摄像头")
			if isChinese != test.wantChinese {
				t.Fatalf("labelsForLanguage(%q) Chinese = %v, want %v", test.locale, isChinese, test.wantChinese)
			}
		})
	}
}

func TestStartupAlertDistinguishesDuplicateInstance(t *testing.T) {
	backend := &recordingBackend{}
	manager := newManager("zh-CN", backend)
	if err := manager.AlertStartup(errors.New("lock failed"), true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(backend.alert.message, "另一个") || strings.Contains(backend.alert.message, "lock failed") {
		t.Fatalf("duplicate-instance alert = %q", backend.alert.message)
	}

	manager = newManager("en-US", backend)
	if err := manager.AlertStartup(errors.New("bad config"), false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(backend.alert.message, "bad config") || !strings.Contains(backend.alert.title, "could not start") {
		t.Fatalf("startup alert = (%q, %q)", backend.alert.title, backend.alert.message)
	}
}

func TestCaptureNotificationsIncludeCameraName(t *testing.T) {
	backend := &recordingBackend{}
	manager := newManager("en", backend)
	if err := manager.CaptureFailed("camera", "USB Camera"); err != nil {
		t.Fatal(err)
	}
	if !backend.notification.critical || !strings.Contains(backend.notification.message, "USB Camera") {
		t.Fatalf("failure notification = %#v", backend.notification)
	}
	if err := manager.CaptureRecovered("camera", "USB Camera"); err != nil {
		t.Fatal(err)
	}
	if backend.notification.critical || !strings.Contains(backend.notification.message, "USB Camera") {
		t.Fatalf("recovery notification = %#v", backend.notification)
	}
}

func TestLocaleFromEnvironmentPriority(t *testing.T) {
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
		t.Fatalf("localeFromEnvironment() = %q, want en_GB.UTF-8", got)
	}
}
