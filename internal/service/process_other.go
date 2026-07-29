//go:build !windows

package service

import "os/exec"

func configureCommand(_ *exec.Cmd) {}
