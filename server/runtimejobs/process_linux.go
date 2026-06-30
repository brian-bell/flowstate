//go:build linux

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
	if waitForRuntimeCommand(done, grace) {
		return nil
	}
	if err := signalProcessGroup(cmd, syscall.SIGKILL); err != nil {
		return err
	}
	if waitForRuntimeCommand(done, grace) {
		return nil
	}
	return errors.New("runtime command did not exit after forced kill")
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

func waitForRuntimeCommand(done <-chan struct{}, timeout time.Duration) bool {
	if done == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
