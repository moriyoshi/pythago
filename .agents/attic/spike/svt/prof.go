package main

import (
	"debug/elf"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/moriyoshi/pythago/internal/spike/pyapi"
)

// Sampling profiler for C code on carriers: Go sends profSignal to each carrier thread
// and the glue handler records the interrupted pc plus frame-pointer chain.
const profSignal = syscall.Signal(42)

var (
	profInstall func(sig int32) int32
	profBuf     func(n *uintptr) uintptr
	carrierMu   sync.Mutex
	carrierTIDs []int
)

func profStart(stop chan struct{}) {
	pyapi.Register(&profInstall, "pythago_prof_install")
	pyapi.Register(&profBuf, "pythago_prof_buf")
	if profInstall(int32(profSignal)) != 0 {
		panic("sigaction")
	}
	go func() {
		pid := syscall.Getpid()
		for {
			select {
			case <-stop:
				return
			case <-time.After(500 * time.Microsecond):
			}
			carrierMu.Lock()
			for _, tid := range carrierTIDs {
				syscall.Tgkill(pid, tid, profSignal)
			}
			carrierMu.Unlock()
		}
	}()
}

func profReport() {
	var n uintptr
	base := profBuf(&n)
	buf := unsafe.Slice((*uintptr)(unsafe.Pointer(base)), n)
	f, err := elf.Open("/proc/self/exe")
	if err != nil {
		panic(err)
	}
	syms, _ := f.Symbols()
	sort.Slice(syms, func(i, j int) bool { return syms[i].Value < syms[j].Value })
	name := func(pc uintptr) string {
		i := sort.Search(len(syms), func(i int) bool { return syms[i].Value > uint64(pc) }) - 1
		if i < 0 {
			return fmt.Sprintf("%#x", pc)
		}
		return syms[i].Name
	}
	leaf, incl := map[string]int{}, map[string]int{}
	samples := 0
	for i := 0; i < len(buf); {
		d := int(buf[i])
		st := buf[i+1 : i+1+d]
		i += 1 + d
		samples++
		leaf[name(st[0])]++
		seen := map[string]bool{}
		for _, pc := range st {
			if nm := name(pc); !seen[nm] {
				seen[nm] = true
				incl[nm]++
			}
		}
	}
	top := func(m map[string]int, k int) {
		type kv struct {
			k string
			v int
		}
		var s []kv
		for a, b := range m {
			s = append(s, kv{a, b})
		}
		sort.Slice(s, func(i, j int) bool { return s[i].v > s[j].v })
		for _, e := range s[:min(k, len(s))] {
			fmt.Fprintf(os.Stderr, "  %5.1f%%  %s\n", 100*float64(e.v)/float64(samples), e.k)
		}
	}
	fmt.Fprintf(os.Stderr, "%d samples\nleaf:\n", samples)
	top(leaf, 15)
	fmt.Fprintln(os.Stderr, "inclusive:")
	top(incl, 25)
}

// profDumpStacks prints the k most common full stacks sampled so far.
func profDumpStacks(k int) {
	var n uintptr
	base := profBuf(&n)
	buf := unsafe.Slice((*uintptr)(unsafe.Pointer(base)), n)
	f, err := elf.Open("/proc/self/exe")
	if err != nil {
		panic(err)
	}
	syms, _ := f.Symbols()
	sort.Slice(syms, func(i, j int) bool { return syms[i].Value < syms[j].Value })
	name := func(pc uintptr) string {
		i := sort.Search(len(syms), func(i int) bool { return syms[i].Value > uint64(pc) }) - 1
		if i < 0 {
			return fmt.Sprintf("%#x", pc)
		}
		return syms[i].Name
	}
	counts := map[string]int{}
	for i := 0; i < len(buf); {
		d := int(buf[i])
		var parts []string
		for _, pc := range buf[i+1 : i+1+d] {
			parts = append(parts, name(pc))
		}
		i += 1 + d
		counts[strings.Join(parts, "\n      ")]++
	}
	type kv struct {
		k string
		v int
	}
	var s []kv
	for a, b := range counts {
		s = append(s, kv{a, b})
	}
	sort.Slice(s, func(i, j int) bool { return s[i].v > s[j].v })
	for _, e := range s[:min(k, len(s))] {
		fmt.Printf("  [%d samples]\n      %s\n", e.v, e.k)
	}
}

// symbolizer returns a function name for a pc in this binary.
func symbolizer() func(uintptr) string {
	f, err := elf.Open("/proc/self/exe")
	if err != nil {
		panic(err)
	}
	syms, _ := f.Symbols()
	sort.Slice(syms, func(i, j int) bool { return syms[i].Value < syms[j].Value })
	return func(pc uintptr) string {
		i := sort.Search(len(syms), func(i int) bool { return syms[i].Value > uint64(pc) }) - 1
		if i < 0 {
			return fmt.Sprintf("%#x", pc)
		}
		n := syms[i].Name
		if j := strings.LastIndex(n, ".text."); j >= 0 {
			n = strings.TrimSuffix(n[j+6:], ")")
		}
		return n
	}
}
