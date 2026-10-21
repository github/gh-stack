package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		// Killing only test2json would orphan the test binary and its Git children.
		out, err := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").CombinedOutput()
		if err != nil {
			return fmt.Errorf("stopping test process tree: %w: %s", err, out)
		}
		return nil
	}
	cmd.WaitDelay = 5 * time.Second
}
