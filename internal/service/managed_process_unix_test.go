//go:build !windows

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	managedProcessHelperMode = "VIDEO_RECORDER_MANAGED_PROCESS_HELPER"
	managedProcessPIDFile    = "VIDEO_RECORDER_MANAGED_PROCESS_PID_FILE"
)

func TestManagedCommandPreservesStandardInputAndOutput(t *testing.T) {
	cmd := newManagedCommand(context.Background(), "/bin/sh", "-c", "cat")
	cmd.Stdin = strings.NewReader("managed process input")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(output); got != "managed process input" {
		t.Fatalf("managed command output = %q", got)
	}
}

func TestManagedCommandStopsAfterParentProcessExits(t *testing.T) {
	switch os.Getenv(managedProcessHelperMode) {
	case "parent":
		runManagedProcessParentHelper()
	case "child":
		runManagedProcessChildHelper()
	}

	pidFile := t.TempDir() + "/child.pid"
	parent := exec.Command(os.Args[0], "-test.run=^TestManagedCommandStopsAfterParentProcessExits$")
	parent.Env = append(os.Environ(),
		managedProcessHelperMode+"=parent",
		managedProcessPIDFile+"="+pidFile,
	)
	if output, err := parent.CombinedOutput(); err != nil {
		t.Fatalf("parent helper failed: %v: %s", err, output)
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("managed child process %d remained after its parent exited", pid)
}

func runManagedProcessParentHelper() {
	pidFile := os.Getenv(managedProcessPIDFile)
	cmd := newManagedCommand(context.Background(), os.Args[0], "-test.run=^TestManagedCommandStopsAfterParentProcessExits$")
	cmd.Env = append(os.Environ(),
		managedProcessHelperMode+"=child",
		managedProcessPIDFile+"="+pidFile,
	)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pidFile); err == nil {
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "managed child did not start")
	os.Exit(3)
}

func runManagedProcessChildHelper() {
	if err := os.WriteFile(os.Getenv(managedProcessPIDFile), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	}
	for {
		time.Sleep(time.Second)
	}
}
