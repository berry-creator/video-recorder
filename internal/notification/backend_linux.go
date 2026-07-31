//go:build linux && desktop && !devcontainer && !headless

package notification

import (
	"errors"
	"os"
	"os/exec"
)

type linuxBackend struct {
	zenity     string
	kdialog    string
	notifySend string
}

func newSystemBackend() backend {
	return linuxBackend{
		zenity:     findCommand("zenity"),
		kdialog:    findCommand("kdialog"),
		notifySend: findCommand("notify-send"),
	}
}

func (b linuxBackend) Enabled() bool {
	return (os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "") &&
		(b.zenity != "" || b.kdialog != "" || b.notifySend != "")
}

func (b linuxBackend) Alert(title, message string) error {
	if b.zenity != "" {
		return exec.Command(b.zenity, "--error", "--title", title, "--text", message).Run()
	}
	if b.kdialog != "" {
		return exec.Command(b.kdialog, "--error", message, "--title", title).Run()
	}
	return errors.New("no supported desktop alert command is available")
}

func (b linuxBackend) Notify(title, message string, critical bool) error {
	if b.notifySend != "" {
		urgency := "normal"
		if critical {
			urgency = "critical"
		}
		return exec.Command(b.notifySend, "--app-name", "Video Recorder", "--urgency", urgency, title, message).Run()
	}
	if b.kdialog != "" {
		return exec.Command(b.kdialog, "--passivepopup", message, "10", "--title", title).Run()
	}
	return errors.New("no supported desktop notification command is available")
}

func systemLanguage() string {
	return normalizeLanguage(localeFromEnvironment(os.Getenv))
}

func findCommand(name string) string {
	path, _ := exec.LookPath(name)
	return path
}
