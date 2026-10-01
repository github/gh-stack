//go:build !windows

package cmd

import (
	"os"
	"syscall"
)

var upgradeExec = syscall.Exec

func replaceWithUpgrade(path string, args []string) error {
	argv := append([]string{path}, args...)
	return upgradeExec(path, argv, os.Environ())
}
