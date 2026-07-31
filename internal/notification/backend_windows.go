//go:build windows && !devcontainer && !headless

package notification

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	messageBoxW              = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")
	getUserDefaultUILanguage = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage")
)

type windowsBackend struct {
	powershell string
}

func newSystemBackend() backend {
	powershell, _ := exec.LookPath("powershell.exe")
	return windowsBackend{powershell: powershell}
}

func (windowsBackend) Enabled() bool {
	return true
}

func (windowsBackend) Alert(title, message string) error {
	titlePointer, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return err
	}
	messagePointer, err := windows.UTF16PtrFromString(message)
	if err != nil {
		return err
	}
	const flags = 0x00000000 | 0x00000010 | 0x00010000 // MB_OK | MB_ICONERROR | MB_SETFOREGROUND
	result, _, callErr := messageBoxW.Call(0, uintptr(unsafe.Pointer(messagePointer)), uintptr(unsafe.Pointer(titlePointer)), flags)
	if result == 0 {
		return fmt.Errorf("show Windows alert: %w", callErr)
	}
	return nil
}

func (b windowsBackend) Notify(title, message string, critical bool) error {
	if b.powershell == "" {
		return fmt.Errorf("powershell.exe is unavailable")
	}
	icon := "Info"
	if critical {
		icon = "Error"
	}
	const script = `Add-Type -AssemblyName System.Windows.Forms
$notification = New-Object System.Windows.Forms.NotifyIcon
$notification.Icon = [System.Drawing.SystemIcons]::Application
$notification.BalloonTipIcon = [System.Windows.Forms.ToolTipIcon]$args[2]
$notification.BalloonTipTitle = $args[0]
$notification.BalloonTipText = $args[1]
$notification.Visible = $true
$notification.ShowBalloonTip(5000)
Start-Sleep -Seconds 6
$notification.Dispose()`
	cmd := exec.Command(b.powershell, "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script, title, message, icon)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func systemLanguage() string {
	languageID, _, _ := getUserDefaultUILanguage.Call()
	const (
		primaryLanguageMask = 0x3ff
		primaryChinese      = 0x04
	)
	if languageID != 0 {
		if languageID&primaryLanguageMask == primaryChinese {
			return "zh"
		}
		return "en"
	}
	return normalizeLanguage(localeFromEnvironment(os.Getenv))
}
