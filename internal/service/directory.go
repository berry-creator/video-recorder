package service

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var (
	ErrDirectorySelectionCanceled = errors.New("directory selection canceled")
	ErrDirectorySelectionBusy     = errors.New("directory selection is already open")
)

type directoryCommand func(context.Context, string, ...string) ([]byte, error)

type DirectoryPicker struct {
	mu       sync.Mutex
	platform string
	run      directoryCommand
	lookPath func(string) (string, error)
}

func NewDirectoryPicker() *DirectoryPicker {
	return &DirectoryPicker{
		platform: runtime.GOOS,
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			configureCommand(cmd)
			return cmd.CombinedOutput()
		},
		lookPath: exec.LookPath,
	}
}

func (p *DirectoryPicker) Select(ctx context.Context, current string) (string, error) {
	if !p.mu.TryLock() {
		return "", ErrDirectorySelectionBusy
	}
	defer p.mu.Unlock()

	var output []byte
	var err error
	switch p.platform {
	case "windows":
		output, err = p.selectWindows(ctx)
	case "darwin":
		output, err = p.run(ctx, "osascript", "-e", `POSIX path of (choose folder with prompt "Video Recorder")`)
	case "linux":
		output, err = p.selectLinux(ctx, current)
	default:
		return "", fmt.Errorf("system directory selection is unsupported on %s", p.platform)
	}
	return selectedDirectory(ctx, output, err)
}

func (p *DirectoryPicker) selectWindows(ctx context.Context) ([]byte, error) {
	const script = `[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
Add-Type -AssemblyName System.Windows.Forms
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = 'Video Recorder'
$dialog.ShowNewFolderButton = $true
if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
  [Console]::Write($dialog.SelectedPath)
}
$dialog.Dispose()`
	return p.run(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-STA", "-WindowStyle", "Hidden", "-Command", script)
}

func (p *DirectoryPicker) selectLinux(ctx context.Context, current string) ([]byte, error) {
	if executable, err := p.lookPath("zenity"); err == nil {
		args := []string{"--file-selection", "--directory", "--title=Video Recorder"}
		if current != "" {
			args = append(args, "--filename="+filepath.Clean(current)+string(filepath.Separator))
		}
		return p.run(ctx, executable, args...)
	}
	if executable, err := p.lookPath("kdialog"); err == nil {
		if current == "" {
			current = "."
		}
		return p.run(ctx, executable, "--getexistingdirectory", current, "--title", "Video Recorder")
	}
	return nil, errors.New("no system directory picker found; install zenity or kdialog")
}

func selectedDirectory(ctx context.Context, output []byte, commandErr error) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value := strings.TrimSpace(strings.TrimPrefix(string(output), "\ufeff"))
	if commandErr != nil {
		var startErr *exec.Error
		if errors.As(commandErr, &startErr) {
			return "", fmt.Errorf("start system directory picker: %w", commandErr)
		}
		if strings.Contains(value, "(-128)") {
			return "", ErrDirectorySelectionCanceled
		}
		var exitErr *exec.ExitError
		if errors.As(commandErr, &exitErr) && value == "" {
			return "", ErrDirectorySelectionCanceled
		}
		if value != "" {
			return "", fmt.Errorf("system directory picker failed: %s", value)
		}
		return "", fmt.Errorf("system directory picker failed: %w", commandErr)
	}
	if value == "" {
		return "", ErrDirectorySelectionCanceled
	}
	value = filepath.Clean(value)
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("system directory picker returned a non-absolute path: %q", value)
	}
	return value, nil
}
