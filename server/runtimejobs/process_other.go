//go:build !unix

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
	if waitForRuntimeCommand(done, grace) {
		return nil
	}
	if err := signalProcess(cmd, os.Kill); err != nil {
		return err
	}
	if waitForRuntimeCommand(done, grace) {
		return nil
	}
	return errors.New("runtime command did not exit after forced kill")
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
