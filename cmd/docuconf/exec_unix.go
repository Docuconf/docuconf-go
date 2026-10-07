//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// execProgram replaces the process with argv. It returns only on error.
func execProgram(argv, env []string) error {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, argv, env)
}
