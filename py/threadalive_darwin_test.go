package py_test

// threadAlive reports whether this process still has the OS thread tid: unknown on
// darwin, where threadID is a thread_selfid and no cheap lookup by it exists.
func threadAlive(int) string { return "unknown" }
