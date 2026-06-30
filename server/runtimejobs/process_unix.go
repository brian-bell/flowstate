//go:build unix

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
	pgid, err := runtimeProcessGroupID(cmd)
	if err != nil {
		return err
	}
	if err := signalProcessGroupID(pgid, syscall.SIGTERM); err != nil {
		return err
	}
	if waitForRuntimeProcessGroupExit(pgid, grace) {
		return nil
	}
	if err := signalProcessGroupID(pgid, syscall.SIGKILL); err != nil {
		return err
	}
	if waitForRuntimeProcessGroupExit(pgid, grace) {
		return nil
	}
	return errors.New("runtime command did not exit after forced kill")
}

func terminateStartedRuntimeCommand(cmd *exec.Cmd, grace time.Duration) error {
	pgid, err := runtimeProcessGroupID(cmd)
	if err != nil {
		return err
	}
	if err := signalProcessGroupID(pgid, syscall.SIGTERM); err != nil {
		return err
	}
	timer := time.AfterFunc(grace, func() {
		_ = signalProcessGroupID(pgid, syscall.SIGKILL)
	})
	err = cmd.Wait()
	timer.Stop()
	return err
}

func runtimeProcessGroupID(cmd *exec.Cmd) (int, error) {
	if cmd == nil || cmd.Process == nil {
		return 0, nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return 0, nil
		}
		return 0, err
	}
	return pgid, nil
}

func signalProcessGroupID(pgid int, signal syscall.Signal) error {
	if pgid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pgid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func waitForRuntimeProcessGroupExit(pgid int, timeout time.Duration) bool {
	if pgid <= 0 || runtimeProcessGroupExited(pgid) {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-timer.C:
			return runtimeProcessGroupExited(pgid)
		case <-ticker.C:
			if runtimeProcessGroupExited(pgid) {
				return true
			}
		}
	}
}

func runtimeProcessGroupExited(pgid int) bool {
	if pgid <= 0 {
		return true
	}
	err := syscall.Kill(-pgid, 0)
	return errors.Is(err, syscall.ESRCH)
}
