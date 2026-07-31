//go:build darwin && !devcontainer && !headless

package notification

import (
	"os"
	"os/exec"
)

type darwinBackend struct{}

func newSystemBackend() backend {
	return darwinBackend{}
}

func (darwinBackend) Enabled() bool {
	return true
}

func (darwinBackend) Alert(title, message string) error {
	const script = `on run argv
display alert (item 1 of argv) message (item 2 of argv) as critical
end run`
	return exec.Command("/usr/bin/osascript", "-e", script, title, message).Run()
}

func (darwinBackend) Notify(title, message string, critical bool) error {
	script := `on run argv
display notification (item 2 of argv) with title (item 1 of argv)
end run`
	if critical {
		script = `on run argv
display notification (item 2 of argv) with title (item 1 of argv) sound name "default"
end run`
	}
	return exec.Command("/usr/bin/osascript", "-e", script, title, message).Run()
}

func systemLanguage() string {
	output, err := exec.Command("/usr/bin/defaults", "read", "-g", "AppleLanguages").Output()
	if err == nil {
		if locale := firstLocale(string(output)); locale != "" {
			return normalizeLanguage(locale)
		}
	}
	return normalizeLanguage(localeFromEnvironment(os.Getenv))
}
