package runtimejobs

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func configureRuntimeCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func terminateRuntimeCommand(cmd *exec.Cmd, done <-chan struct{}, grace time.Duration) error {
	if err := signalProcessGroup(cmd, syscall.SIGTERM); err != nil {
		return err
	}
	select {
	case <-done:
		return nil
	case <-time.After(grace):
	}
	return signalProcessGroup(cmd, syscall.SIGKILL)
}

func terminateStartedRuntimeCommand(cmd *exec.Cmd, grace time.Duration) error {
	if err := signalProcessGroup(cmd, syscall.SIGTERM); err != nil {
		return err
	}
	timer := time.AfterFunc(grace, func() {
		_ = signalProcessGroup(cmd, syscall.SIGKILL)
	})
	err := cmd.Wait()
	timer.Stop()
	return err
}

func signalProcessGroup(cmd *exec.Cmd, signal syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	if err := syscall.Kill(-pgid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
