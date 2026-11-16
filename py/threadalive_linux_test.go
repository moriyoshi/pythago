package py_test

import (
	"fmt"
	"os"
)

// threadAlive reports whether this process still has the OS thread tid ("yes"/"no").
func threadAlive(tid int) string {
	if _, err := os.Stat(fmt.Sprintf("/proc/self/task/%d", tid)); err == nil {
		return "yes"
	}
	return "no"
}
