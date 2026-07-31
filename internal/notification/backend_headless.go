//go:build devcontainer || headless || (linux && !desktop)

package notification

import "os"

type noopBackend struct{}

func newSystemBackend() backend {
	return noopBackend{}
}

func (noopBackend) Enabled() bool {
	return false
}

func (noopBackend) Alert(string, string) error {
	return nil
}

func (noopBackend) Notify(string, string, bool) error {
	return nil
}

func systemLanguage() string {
	return normalizeLanguage(localeFromEnvironment(os.Getenv))
}
