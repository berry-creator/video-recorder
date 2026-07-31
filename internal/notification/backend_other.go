//go:build !windows && !darwin && !linux && !devcontainer && !headless

package notification

import "os"

type unsupportedBackend struct{}

func newSystemBackend() backend {
	return unsupportedBackend{}
}

func (unsupportedBackend) Enabled() bool {
	return false
}

func (unsupportedBackend) Alert(string, string) error {
	return nil
}

func (unsupportedBackend) Notify(string, string, bool) error {
	return nil
}

func systemLanguage() string {
	return normalizeLanguage(localeFromEnvironment(os.Getenv))
}
