//go:build !windows && !darwin

package tray

import "os"

func systemLocale() string {
	return localeFromEnvironment(os.Getenv)
}
