//go:build !windows

package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

const parentProcessSupervisorScript = `parent_pid=$1
shift
target_pid=$$

(
  while kill -0 "$parent_pid" 2>/dev/null && kill -0 "$target_pid" 2>/dev/null; do
    sleep 0.25
  done
  if ! kill -0 "$target_pid" 2>/dev/null; then exit 0; fi
  kill -TERM "$target_pid" 2>/dev/null || exit 0
  attempts=0
  while kill -0 "$target_pid" 2>/dev/null && [ "$attempts" -lt 20 ]; do
    sleep 0.1
    attempts=$((attempts + 1))
  done
  kill -KILL "$target_pid" 2>/dev/null || true
) >/dev/null 2>&1 &

exec "$@"`

func newPlatformManagedCommand(ctx context.Context, name string, args ...string) (*exec.Cmd, func(*exec.Cmd) error) {
	wrapperArgs := []string{"-c", parentProcessSupervisorScript, "video-recorder-process-supervisor", strconv.Itoa(os.Getpid()), name}
	wrapperArgs = append(wrapperArgs, args...)
	cmd := exec.CommandContext(ctx, "/bin/sh", wrapperArgs...)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := cmd.Process.Signal(syscall.SIGTERM)
		if errors.Is(err, os.ErrProcessDone) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 4 * time.Second
	return cmd, nil
}
