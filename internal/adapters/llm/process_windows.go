//go:build windows

package llm

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"time"
)

func configureCommandTermination(command *exec.Cmd) {
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		if err := exec.Command(
			"taskkill", "/T", "/F", "/PID", strconv.Itoa(command.Process.Pid),
		).Run(); err == nil {
			return nil
		}
		err := command.Process.Kill()
		if errors.Is(err, os.ErrProcessDone) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = 2 * time.Second
}
