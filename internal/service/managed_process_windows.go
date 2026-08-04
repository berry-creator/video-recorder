//go:build windows

package service

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var managedProcessJob struct {
	once   sync.Once
	handle windows.Handle
	err    error
}

func newPlatformManagedCommand(ctx context.Context, name string, args ...string) (*exec.Cmd, func(*exec.Cmd) error) {
	cmd := exec.CommandContext(ctx, name, args...)
	configureCommand(cmd)
	return cmd, assignManagedProcessToJob
}

func assignManagedProcessToJob(cmd *exec.Cmd) error {
	job, err := childProcessJob()
	if err != nil {
		return err
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil
		}
		return err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		const stillActive = 259
		var exitCode uint32
		if exitErr := windows.GetExitCodeProcess(process, &exitCode); exitErr == nil && exitCode != stillActive {
			return nil
		}
		return err
	}
	return nil
}

func childProcessJob() (windows.Handle, error) {
	managedProcessJob.once.Do(func() {
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			managedProcessJob.err = err
			return
		}
		var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		_, err = windows.SetInformationJobObject(
			job,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&limits)),
			uint32(unsafe.Sizeof(limits)),
		)
		if err != nil {
			windows.CloseHandle(job)
			managedProcessJob.err = err
			return
		}
		managedProcessJob.handle = job
	})
	return managedProcessJob.handle, managedProcessJob.err
}
