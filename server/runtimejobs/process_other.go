//go:build !linux

package runtimejobs

import (
	"errors"
	"os"
	"os/exec"
	"time"
)

func configureRuntimeCommand(cmd *exec.Cmd) {}

func terminateRuntimeCommand(cmd *exec.Cmd, done <-chan struct{}, grace time.Duration) error {
	if err := signalProcess(cmd, os.Interrupt); err != nil {
		return err
	}
	select {
	case <-done:
		return nil
	case <-time.After(grace):
	}
	return signalProcess(cmd, os.Kill)
}

func terminateStartedRuntimeCommand(cmd *exec.Cmd, grace time.Duration) error {
	if err := signalProcess(cmd, os.Interrupt); err != nil {
		return err
	}
	timer := time.AfterFunc(grace, func() {
		_ = signalProcess(cmd, os.Kill)
	})
	err := cmd.Wait()
	timer.Stop()
	return err
}

func signalProcess(cmd *exec.Cmd, signal os.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Signal(signal); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
