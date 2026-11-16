//go:build !windows

package asgi_test

import "syscall"

// reexec replaces this process with exe run with args and env.
func reexec(exe string, args, env []string) error { return syscall.Exec(exe, args, env) }
