package py

import (
	"os"

	"github.com/moriyoshi/pythago/internal/fusion"
)

// PYTHAGO_TEST_FUSION_UNSUPPORTED=1 makes the glue's CPython look unverified for the
// fusion (TestFusedBytesUnsupported's child process).
func init() {
	if os.Getenv("PYTHAGO_TEST_FUSION_UNSUPPORTED") == "1" {
		fusionSupported = func() bool { return false }
	}
}

// FusionTestModule is the _fusiontest module of the tests' Config.Modules: what a child
// process (sys.executable) needs to check the fusion from Python.
func FusionTestModule() map[string]any {
	return map[string]any{
		"on": Func(func([]*Object, Kwargs) (any, error) { return fusion.On(), nil }),
		// hooked(): the OBJ allocator's free is the fusion's hook.
		"hooked": Func(func([]*Object, Kwargs) (any, error) { return fusionHooked(), nil }),
		// fused(n): a fused bytes object of n bytes.
		"fused": Func(func(a []*Object, _ Kwargs) (any, error) {
			n, err := a[0].Int64()
			if err != nil {
				return nil, err
			}
			b := fusion.New(int(n))
			if b == nil {
				return nil, nil
			}
			for i := range b.Buf() {
				b.Buf()[i] = 'f'
			}
			return b, nil
		}),
		// live(addr): the live count of the chunk holding addr (-1: none).
		"live": Func(func(a []*Object, _ Kwargs) (any, error) {
			p, err := a[0].Int64()
			if err != nil {
				return nil, err
			}
			id, _, ok := fusion.Locate(uintptr(p))
			if !ok {
				return -1, nil
			}
			return fusion.Chunk(id).Live, nil
		}),
		// takes_slot(n, addr): a new slot for n bytes is the one at addr (released again).
		"takes_slot": Func(func(a []*Object, _ Kwargs) (any, error) {
			n, _ := a[0].Int64()
			p, _ := a[1].Int64()
			b := fusion.New(int(n))
			defer b.Release()
			return b != nil && b.Addr() == uintptr(p), nil
		}),
	}
}

// fusionHooked reports whether the OBJ allocator's free is the fusion's hook.
func fusionHooked() bool { return call0(symbol("pythago_fusion_hooked")) != 0 }
