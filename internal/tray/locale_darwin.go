//go:build darwin

package tray

import (
	"os"
	"os/exec"
)

func systemLocale() string {
	output, err := exec.Command("/usr/bin/defaults", "read", "-g", "AppleLanguages").Output()
	if err == nil {
		if locale := firstLocale(string(output)); locale != "" {
			return locale
		}
	}
	return localeFromEnvironment(os.Getenv)
}
