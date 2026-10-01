//go:build windows

package cmd

import (
	"errors"
	"os"
)

var upgradeExec = func(_ string, _ []string, _ []string) error {
	return errors.New("process replacement is not supported on Windows")
}

func replaceWithUpgrade(path string, args []string) error {
	return upgradeExec(path, append([]string{path}, args...), os.Environ())
}
