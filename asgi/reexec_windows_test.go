package asgi_test

import (
	"errors"
	"os"
	"os/exec"
)

// reexec runs exe with args and env as a child, which is as close as Windows comes to
// exec, and exits with its status.
func reexec(exe string, args, env []string) error {
	cmd := exec.Command(exe, args[1:]...)
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = env, os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	if err == nil {
		os.Exit(0)
	}
	return err
}
