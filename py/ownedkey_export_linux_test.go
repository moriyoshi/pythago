package py

import (
	"github.com/ebitengine/purego"

	"github.com/moriyoshi/pythago/internal/fusion"
)

// ThreadKeyProbe reads, on the calling OS thread, the thread state Do cached there
// (PyGILState_GetThisThreadState, 0 if none) and the value of pthread key 0, which
// belongs to the native image's TLS (bao creates it first, at package initialization).
func ThreadKeyProbe() (ts, key0 uintptr) {
	getspecific, err := purego.Dlsym(purego.RTLD_DEFAULT, "pthread_getspecific")
	if err != nil {
		panic(err)
	}
	return call0(cGILStateGetThisThreadState), call1(getspecific, 0)
}

// FusionSelfCheckRan reports whether Init ran the fusion's startup check, which enters
// Python on a goroutine's OS thread before initialization is complete.
func FusionSelfCheckRan() bool { return fusion.On() && freeThread }
