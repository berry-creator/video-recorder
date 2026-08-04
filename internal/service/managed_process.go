package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
)

type managedCommand struct {
	*exec.Cmd
	afterStart func(*exec.Cmd) error
}

func newManagedCommand(ctx context.Context, name string, args ...string) *managedCommand {
	cmd, afterStart := newPlatformManagedCommand(ctx, name, args...)
	return &managedCommand{Cmd: cmd, afterStart: afterStart}
}

func (c *managedCommand) Start() error {
	if err := c.Cmd.Start(); err != nil {
		return err
	}
	if c.afterStart == nil {
		return nil
	}
	if err := c.afterStart(c.Cmd); err != nil {
		_ = c.Process.Kill()
		_ = c.Cmd.Wait()
		return fmt.Errorf("manage child process: %w", err)
	}
	return nil
}

func (c *managedCommand) Run() error {
	if err := c.Start(); err != nil {
		return err
	}
	return c.Wait()
}

func (c *managedCommand) CombinedOutput() ([]byte, error) {
	if c.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	if c.Stderr != nil {
		return nil, errors.New("exec: Stderr already set")
	}
	var output bytes.Buffer
	c.Stdout = &output
	c.Stderr = &output
	err := c.Run()
	return output.Bytes(), err
}
