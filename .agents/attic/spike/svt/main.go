// Command svt runs free-threaded Python (3.14t) as M:N virtual threads (VTs) on a few
// Go-owned carrier OS threads; see .agents/attic/spike/glue/pythago_vt.c.
//
//	PYTHONHOME=<pbs 3.14t install> svt [vts] [carriers]
//
// SVT_BASELINE=1 runs the same Python function on one threading.Thread per task instead.
package main

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ebitengine/purego"
	"github.com/moriyoshi/pythago/internal/spike/goaio"
	"github.com/moriyoshi/pythago/internal/spike/pyapi"
	"github.com/moriyoshi/pythago/internal/spike/s1/pyembed"
)

func init() { runtime.LockOSThread() }

var (
	pyInitializeEx      func(int32)
	pyFinalizeEx        func() int32
	pyRunSimpleString   func(string) int32
	pyImportAddModule   func(string) uintptr
	pyImportModule      func(string) uintptr
	pyObjectGetAttrStr  func(uintptr, string) uintptr
	pyObjectSetAttrStr  func(uintptr, string, uintptr) int32
	pyCMethodNew        func(def, self, module, cls uintptr) uintptr
	pyMemRawCalloc      func(n, size uintptr) uintptr
	pyIncRef            func(uintptr)
	pyDecRef            func(uintptr)
	pyEvalSaveThread    func() uintptr
	pyEvalRestoreThread func(uintptr)
	vtNew               func(callable uintptr, arg int64, stack uintptr, home int32) uintptr
	vtRun               func(vt uintptr) int32
	vtSleepRemaining    func(vt uintptr) int64
	vtFree              func(vt uintptr)
	vtSetGoDispatch     func(fn uintptr)
	vtStarted           func(vt uintptr) int32
	vtParkToken         func(vt uintptr) uint64
	vtWakeTok           func(tok uint64)
	vtHome              func(vt uintptr) int32
	vtTimeout           func(vt uintptr) int64
	vtIOInit            func() int32
	vtIOWait            func(n *uintptr) uintptr
	vtState             func(vt uintptr) int32
	vtSetThreads        func(carriers int32, stack uintptr)
	vtIsSpawned         func(vt uintptr) int32
	vtRelease           func(vt uintptr)
	vtPush              func(vt uintptr)
	vtKick              func(c int32)
	vtCarrierWait       func(c int32, out *uintptr, max uintptr, kicked *int32) uintptr
	vtioStats           func(out *[7]int64)
)

const (
	vtRunning = 1
	vtSleep   = 2
	vtPark    = 4
	vtHung    = 5
	vtDone    = 3
)

var goCalls atomic.Int64

func goNoop(self, args uintptr, nargs int, kwnames uintptr) uintptr {
	goCalls.Add(1)
	none := pyapi.Addr("_Py_NoneStruct")
	pyIncRef(none)
	return none
}

// sample tracks peak OS threads and RSS until stop is closed.
func sample(stop chan struct{}) (peakThreads, peakRSSKiB *atomic.Int64) {
	peakThreads, peakRSSKiB = new(atomic.Int64), new(atomic.Int64)
	go func() {
		for {
			if tasks, err := os.ReadDir("/proc/self/task"); err == nil && int64(len(tasks)) > peakThreads.Load() {
				peakThreads.Store(int64(len(tasks)))
			}
			if f, err := os.Open("/proc/self/status"); err == nil {
				sc := bufio.NewScanner(f)
				for sc.Scan() {
					if v, ok := strings.CutPrefix(sc.Text(), "VmRSS:"); ok {
						kib, _ := strconv.ParseInt(strings.Fields(v)[0], 10, 64)
						if kib > peakRSSKiB.Load() {
							peakRSSKiB.Store(kib)
						}
					}
				}
				f.Close()
			}
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	return
}

func main() {
	pyapi.Load(pyembed.SymbolAddr0())
	for _, f := range []struct {
		p    any
		name string
	}{
		{&pyInitializeEx, "Py_InitializeEx"}, {&pyFinalizeEx, "Py_FinalizeEx"},
		{&pyRunSimpleString, "PyRun_SimpleString"}, {&pyImportAddModule, "PyImport_AddModule"},
		{&pyImportModule, "PyImport_ImportModule"},
		{&pyObjectGetAttrStr, "PyObject_GetAttrString"}, {&pyObjectSetAttrStr, "PyObject_SetAttrString"},
		{&pyCMethodNew, "PyCMethod_New"}, {&pyMemRawCalloc, "PyMem_RawCalloc"},
		{&pyIncRef, "Py_IncRef"}, {&pyDecRef, "Py_DecRef"},
		{&pyEvalSaveThread, "PyEval_SaveThread"}, {&pyEvalRestoreThread, "PyEval_RestoreThread"},
		{&vtNew, "pythago_vt_new"}, {&vtRun, "pythago_vt_run"},
		{&vtSleepRemaining, "pythago_vt_sleep_remaining"}, {&vtFree, "pythago_vt_free"},
		{&vtSetGoDispatch, "pythago_vt_set_go_dispatch"}, {&vtStarted, "pythago_vt_started"},
		{&vtParkToken, "pythago_vt_park_token"}, {&vtWakeTok, "pythago_vt_wake_tok"}, {&vtHome, "pythago_vt_home"}, {&vtTimeout, "pythago_vt_timeout"},
		{&vtIOInit, "pythago_vt_io_init"}, {&vtIOWait, "pythago_vt_io_wait"},
		{&vtState, "pythago_vt_state"}, {&vtioStats, "pythago_vtio_stats"},
		{&vtSetThreads, "pythago_vt_set_threads"}, {&vtIsSpawned, "pythago_vt_is_spawned"},
		{&vtRelease, "pythago_vt_release"}, {&vtPush, "pythago_vt_push"},
		{&vtKick, "pythago_vt_kick"}, {&vtCarrierWait, "pythago_vt_carrier_wait"},
	} {
		pyapi.Register(f.p, f.name)
	}
	n, carriers := 10000, 4
	if len(os.Args) > 1 {
		n, _ = strconv.Atoi(os.Args[1])
	}
	if len(os.Args) > 2 {
		carriers, _ = strconv.Atoi(os.Args[2])
	}

	pyInitializeEx(0)
	// Go builtins share goaio's dispatcher, which on VT builds goes through the glue
	// trampoline that moves VT callbacks onto the carrier stack.
	goaio.Install()
	builtins := pyImportModule("builtins")
	fn := goaio.Builtin("go_noop", func([]uintptr) uintptr { return goNoop(0, 0, 0, 0) })
	pyObjectSetAttrStr(builtins, "go_noop", fn)
	pyDecRef(fn)
	// go_append(list, x): PyList_Append from Go, i.e. from a pinned VT. A contended list
	// makes the pinned VT wait for the list's critical section.
	var pyListAppend func(list, item uintptr) int32
	pyapi.Register(&pyListAppend, "PyList_Append")
	fn = goaio.Builtin("go_append", func(a []uintptr) uintptr {
		if pyListAppend(a[0], a[1]) != 0 {
			return 0
		}
		return goNoop(0, 0, 0, 0)
	})
	pyObjectSetAttrStr(builtins, "go_append", fn)
	pyDecRef(fn)
	// go_call(f): calls f() from Go, i.e. Python code running inside a pinned VT.
	var pyObjectCallNoArgs func(f uintptr) uintptr
	pyapi.Register(&pyObjectCallNoArgs, "PyObject_CallNoArgs")
	fn = goaio.Builtin("go_call", func(a []uintptr) uintptr { return pyObjectCallNoArgs(a[0]) })
	pyObjectSetAttrStr(builtins, "go_call", fn)
	pyDecRef(fn)
	pyDecRef(builtins)
	pyRunSimpleString(fmt.Sprintf("SVT_N = %d", n))
	threadsT := 10000
	if v := os.Getenv("SVT_T"); v != "" {
		threadsT, _ = strconv.Atoi(v)
	}
	pyRunSimpleString(fmt.Sprintf("SVT_T = %d", threadsT))
	if pyRunSimpleString(`
import sys, threading, time
shared, results, failures = [], {}, []
local = threading.local()
def body(i):
    try:
        ident = threading.get_ident()
        assert not hasattr(local, "i"), "threading.local leaked from a previous thread"
        local.i = i
        mine = [str(i) * 3 for _ in range(20)]     # owned by this carrier/thread
        for k in range(3):
            time.sleep(0.05)
            go_noop()                              # Python -> Go from the VT stack
            assert threading.get_ident() == ident, "ident changed"
            assert local.i == i, "threading.local changed"
            shared.append(mine[k])                 # objects cross VTs and carriers
            if len(shared) > 1000:
                shared.clear()
        results[i] = (ident, threading.get_native_id())
    except BaseException as e:
        failures.append(repr(e))
import os, gc
if os.environ.get("SVT_BODY") == "noop":
    def body(i):
        results[i] = (threading.get_ident(), threading.get_native_id())
if os.environ.get("SVT_BODY") == "noshare":
    def body(i):
        ident = threading.get_ident()
        local.i = i
        mine = [str(i) * 3 for _ in range(20)]
        for k in range(3):
            time.sleep(0.05)
            go_noop()
            assert threading.get_ident() == ident and local.i == i
        results[i] = (ident, threading.get_native_id())
if os.environ.get("SVT_BODY") == "sleeponly":
    def body(i):
        for k in range(3):
            time.sleep(0.05)
        results[i] = (threading.get_ident(), threading.get_native_id())
if os.environ.get("SVT_BODY") == "locks":
    import queue
    N = SVT_N
    lock, counter, q = threading.Lock(), [0], queue.Queue()
    barrier = threading.Barrier(N)
    def body(i):
        try:
            for _ in range(3):
                with lock:                  # held across a sleep: waiters must yield
                    c = counter[0]
                    time.sleep(0.0002)
                    counter[0] = c + 1
            if i % 2 == 0:
                q.put(i)                    # Condition-based handoff between VTs
            else:
                q.get(timeout=60)
            barrier.wait(timeout=60)        # every VT blocked at once
            results[i] = (threading.get_ident(), threading.get_native_id())
        except BaseException as e:
            failures.append(repr(e))
    def check():
        return f"counter {counter[0]} (want {3 * N}), queue left {q.qsize()}"
if os.environ.get("SVT_BODY") == "pinned":
    big = list(range(400000))
    def body(i):
        try:
            for k in range(10):
                if (i // 4) % 4 == 0:   # on every carrier (VTs are placed i % carriers)
                    go_append(big, k)   # from Go: a pinned VT waits for big's critical section
                else:
                    big.sort()          # holds big's critical section for a millisecond or more
                time.sleep(0.001)
            results[i] = (threading.get_ident(), threading.get_native_id())
        except BaseException as e:
            failures.append(repr(e))
    def check():
        appenders = sum(1 for i in range(SVT_N) if (i // 4) % 4 == 0)
        return f"list grew by {len(big) - 400000} of {10 * appenders}"
if os.environ.get("SVT_BODY") == "deferlock":
    L, held = threading.Lock(), [0]
    def take():
        with L:                         # a pinned VT (inside go_call) waits for L
            held[0] += 1
    def body(i):
        try:
            for k in range(10):
                if (i // 4) % 2 == 0:   # both groups on every carrier (placement is i % carriers)
                    go_call(take)
                else:
                    with L:             # holds L across a Go call: deferred if nested
                        go_noop()
                        held[0] += 1
                        time.sleep(0.0005)
                time.sleep(0.001)
            results[i] = (threading.get_ident(), threading.get_native_id())
        except BaseException as e:
            failures.append(repr(e))
    def check():
        return f"lock taken {held[0]} times of {10 * SVT_N}"
if os.environ.get("SVT_BODY") == "sockets":
    import socket
    lsock = socket.socket()
    lsock.bind(("127.0.0.1", 0))
    lsock.listen(65535)
    addr = lsock.getsockname()
    def recv_exact(s, n):
        buf = b""
        while len(buf) < n:
            chunk = s.recv(n - len(buf))
            if not chunk:
                raise EOFError
            buf += chunk
        return buf
    def body(i):
        try:
            if i % 2 == 0:                  # server: blocking accept/recv/send
                conn, _ = lsock.accept()
                with conn:
                    conn.sendall(recv_exact(conn, 65536))
            else:                           # client: blocking connect; every other one with a timeout (poll path)
                with socket.create_connection(addr) as s:
                    if i % 4 == 1:
                        s.settimeout(60)
                    payload = bytes([i % 256]) * 65536
                    s.sendall(payload)
                    assert recv_exact(s, 65536) == payload
            results[i] = (threading.get_ident(), threading.get_native_id())
        except BaseException as e:
            failures.append(repr(e))
    def check():
        return "echo ok"
if os.environ.get("SVT_BODY") == "asyncio":
    # VT pairs, each running its own asyncio loop: even = server loop, odd = its clients.
    # On one carrier this deadlocks if an idle loop pins the carrier instead of parking.
    import asyncio, goaio
    factory = goaio.GoSelectorEventLoop if os.environ.get("SVT_LOOP") == "selector" else goaio.GoEventLoop
    CONNS = 20
    ports, ready = {}, [threading.Event() for _ in range(SVT_N // 2)]
    async def server_main(pair):
        done, served = asyncio.Event(), [0]
        async def handle(r, w):
            while data := await r.read(65536):
                w.write(data)
                await w.drain()
            w.close()
            await w.wait_closed()
            served[0] += 1
            if served[0] == CONNS:
                done.set()
        srv = await asyncio.start_server(handle, "127.0.0.1", 0, backlog=4096)
        ports[pair] = srv.sockets[0].getsockname()[1]
        ready[pair].set()
        async with srv:
            await done.wait()
    async def client_main(pair):
        async def one(k):
            r, w = await asyncio.open_connection("127.0.0.1", ports[pair])
            payload = bytes([k]) * 16384
            w.write(payload)
            await w.drain()
            assert await r.readexactly(len(payload)) == payload
            await asyncio.sleep(0.01)          # timer while other loops share the carrier
            w.close()
            await w.wait_closed()
        await asyncio.gather(*(one(k) for k in range(CONNS)))
    def body(i):
        try:
            pair = i // 2
            if i % 2 == 0:
                asyncio.run(server_main(pair), loop_factory=factory)
            else:
                assert ready[pair].wait(60), "server never started"   # parks on a VT-aware lock
                asyncio.run(client_main(pair), loop_factory=factory)
            results[i] = (threading.get_ident(), threading.get_native_id())
        except BaseException as e:
            failures.append(repr(e))
    def check():
        return f"{SVT_N // 2} loop pairs x {CONNS} connections on {'GoSelector' if 'Selector' in factory.__name__ else 'GoEventLoop'}"
if os.environ.get("SVT_BODY") == "threads":
    # One VT spawns SVT_T threading.Threads (VTs themselves with SVT_THREADS=1).
    import concurrent.futures as cf, _thread
    T = SVT_T
    tlock, total, t_natives, t_idents, stats = threading.Lock(), [0], set(), set(), {}
    tl = threading.local()
    def worker(k):
        tl.k = k
        if T <= 8:
            import _thread
            print(f"  worker {k}: get_ident={threading.get_ident():#x} _thread={_thread.get_ident():#x} native={threading.get_native_id()}", flush=True)
        t_idents.add(threading.get_ident())
        for _ in range(3):
            time.sleep(0.05)
            with tlock:
                total[0] += 1
        assert tl.k == k
        t_natives.add(threading.get_native_id())
    def body(i):
        if i != 0:
            return
        try:
            t0 = time.perf_counter()
            ts = [threading.Thread(target=worker, args=(k,)) for k in range(T)]
            for t in ts:
                t.start()
            for t in ts:
                t.join()
            stats["spawn_join"] = time.perf_counter() - t0
            import signal
            try:
                signal.pthread_kill(ts[0].ident, 0)
                stats["kill_dead"] = "succeeded (BUG)"
            except ProcessLookupError:
                stats["kill_dead"] = "ESRCH"
            chain_errs, prev = [], None
            def link(p):
                try:
                    if p is not None:
                        p.join()     # joins its predecessor: needs idents that are never reused
                except BaseException as e:
                    chain_errs.append(repr(e))
            chain = []
            for _ in range(200):
                prev = threading.Thread(target=link, args=(prev,))
                prev.start()
                chain.append(prev)
            for t in chain:
                t.join()
            stats["chain"] = f"{len(chain_errs)} errors {chain_errs[:2]}"
            with cf.ThreadPoolExecutor(64) as ex:
                assert sum(ex.map(lambda x: x * 2, range(10000))) == 2 * sum(range(10000))
            done = threading.Event()
            _thread.start_new_thread(done.set, ())     # detached thread
            assert done.wait(10)
            threading.Thread(target=time.sleep, args=(3600,), daemon=True).start()  # alive at shutdown
            results[i] = (threading.get_ident(), threading.get_native_id())
        except BaseException as e:
            import traceback
            traceback.print_exception(e)
            failures.append(repr(e))
    def check():
        return (f"{T} threads x 3 x sleep(0.05) + lock: {stats.get('spawn_join', 0) * 1000:.0f} ms; "
                f"counter {total[0]} (want {3 * T}); distinct thread t_idents {len(t_idents)}; "
                f"they ran on {len(t_natives)} OS threads; pthread_kill(finished) {stats.get('kill_dead')}; "
                f"join-predecessor chain: {stats.get('chain')}")
if os.environ.get("SVT_BODY") == "chain":
    # Only the join-predecessor chain, repeated (fast reproducer).
    chain_stats = {"errs": [], "reps": 0}
    def body(i):
        if i != 0:
            return
        try:
            for rep in range(int(os.environ.get("SVT_REPS", "50"))):
                prev = None
                def link(p):
                    try:
                        if p is not None:
                            p.join()
                    except BaseException as e:
                        chain_stats["errs"].append(repr(e))
                chain = []
                for _ in range(SVT_T):
                    prev = threading.Thread(target=link, args=(prev,))
                    prev.start()
                    chain.append(prev)
                for t in chain:
                    t.join()
                chain_stats["reps"] += 1
            results[i] = (threading.get_ident(), threading.get_native_id())
        except BaseException as e:
            import traceback
            traceback.print_exception(e)
            failures.append(repr(e))
    def check():
        return f"chain reps {chain_stats['reps']}, errors {len(chain_stats['errs'])} {chain_stats['errs'][:2]}"
if os.environ.get("SVT_NOGC"):
    gc.disable()
`) != 0 {
		os.Exit(1)
	}
	mainMod := pyImportAddModule("__main__")
	body := pyObjectGetAttrStr(mainMod, "body")

	stop := make(chan struct{})
	prof := os.Getenv("SVT_PROF") != ""
	if prof {
		profStart(stop)
	}
	peakThreads, peakRSS := sample(stop)
	// SVT_STW=1: a plain OS thread stops the world (gc.collect) in a loop, so VTs often
	// reach a blocking call while a stop-the-world is pending and may not park.
	var stwCount atomic.Int64
	var stwWG sync.WaitGroup
	if os.Getenv("SVT_STW") != "" {
		pause := 200 * time.Microsecond // SVT_STW=<µs between collections>, default 200
		if us, err := strconv.Atoi(os.Getenv("SVT_STW")); err == nil && us > 1 {
			pause = time.Duration(us) * time.Microsecond
		}
		var ensure func() int32
		var release func(int32)
		pyapi.Register(&ensure, "PyGILState_Ensure")
		pyapi.Register(&release, "PyGILState_Release")
		stwWG.Add(1)
		go func() {
			defer stwWG.Done()
			runtime.LockOSThread()
			for {
				select {
				case <-stop:
					return
				default:
				}
				g := ensure()
				pyRunSimpleString("import gc; gc.collect()")
				release(g)
				stwCount.Add(1)
				time.Sleep(pause)
			}
		}()
	}
	start := time.Now()
	mode := fmt.Sprintf("vt (%d carriers)", carriers)
	if os.Getenv("SVT_LEAK") != "" {
		var setLeak func(int32)
		pyapi.Register(&setLeak, "pythago_vt_set_leak_tstate")
		setLeak(1)
		mode += " leaking tstates"
	}
	if os.Getenv("SVT_BASELINE") != "" {
		mode = "threading.Thread"
		pyRunSimpleString(fmt.Sprintf(`
ts = [threading.Thread(target=body, args=(i,)) for i in range(%d)]
[t.start() for t in ts]; [t.join() for t in ts]`, n))
	} else {
		pool := os.Getenv("SVT_POOL") != ""
		capO := 0 // per-carrier admission cap on thread states (0: unbounded)
		if v := os.Getenv("SVT_O"); v != "" {
			capO, _ = strconv.Atoi(v)
		}
		if pool {
			mode += " Go-pooled tstates"
			if capO > 0 {
				mode += fmt.Sprintf(" (O=%d per carrier)", capO)
			}
		}
		var tstateNew func() uintptr
		var tstateFree func(ts uintptr)
		var vtSetTstate func(vt, ts uintptr)
		var vtIsRaw func(vt uintptr) int32
		var vtTstateOf func(vt uintptr) uintptr
		pyapi.Register(&tstateNew, "pythago_vt_tstate_new")
		pyapi.Register(&tstateFree, "pythago_vt_tstate_free")
		pyapi.Register(&vtSetTstate, "pythago_vt_set_tstate")
		pyapi.Register(&vtIsRaw, "pythago_vt_is_raw")
		pyapi.Register(&vtTstateOf, "pythago_vt_tstate")
		var peakTstates atomic.Int64
		idleTrim := time.Second
		go func() { // wake carriers periodically so idle pools get trimmed
			for range time.Tick(250 * time.Millisecond) {
				for c := range carriers {
					vtKick(int32(c))
				}
			}
		}()
		waves := 1
		if v := os.Getenv("SVT_WAVES"); v != "" {
			waves, _ = strconv.Atoi(v)
		}
		mainTS := pyEvalSaveThread()
		var wg, trimWG sync.WaitGroup
		var trimmed atomic.Int64
		var trimTime atomic.Int64
		trimPending := make([]atomic.Int32, carriers) // trim requests (one per GC cycle)
		var allMu sync.Mutex
		all := map[uintptr]bool{}
		inRun := make([]atomic.Uintptr, carriers)
		// Nested runs: a pinned VT (in a Go callback) that would park runs its carrier's
		// next runnable VT through this hook, on its carrier's thread and goroutine.
		nestRun := make([]func() bool, carriers)
		var vtTakeLocal func(c int32, out *uintptr, max uintptr) uintptr
		pyapi.Register(&vtTakeLocal, "pythago_vt_take_local")
		if os.Getenv("SVT_NONEST") == "" {
			var setNestHook func(fn uintptr)
			pyapi.Register(&setNestHook, "pythago_vt_set_nest_hook")
			setNestHook(purego.NewCallback(func(c uintptr) uintptr {
				if nestRun[c]() {
					return 1
				}
				return 0
			}))
		}

		for c := range carriers {
			go func(cid int) {
				live := 0 // VTs of this carrier not yet finished
				// A VT stays on its carrier's OS thread for life.
				runtime.LockOSThread()
				carrierMu.Lock()
				carrierTIDs = append(carrierTIDs, syscall.Gettid())
				carrierMu.Unlock()
				// This carrier's thread-state pool (the O of M:N:O). Only this goroutine
				// touches it, and thread states never leave the carrier that created them
				// (their mimalloc heaps belong to its OS thread).
				// free: idle thread states, most recently returned last, with the time each
				// came back. Entries idle longer than idleTrim are destroyed, a batch per
				// scheduler pass, so a large trim never stalls the carrier. (Go's GC cycles
				// are no signal for this: they follow Go allocation, not Python idleness.)
				type idleTS struct {
					ts    uintptr
					since time.Time
				}
				var free []idleTS
				var deferred []uintptr
				inUse := 0
				timers := map[uintptr]*time.Timer{} // pending park deadlines, by VT
				var run func(vt uintptr)
				run = func(vt uintptr) {
					if t, ok := timers[vt]; ok {
						t.Stop()
						delete(timers, vt)
					}
					if vtStarted(vt) == 0 {
						if pool && vtIsRaw(vt) == 0 {
							// Admission: a VT that has not started has no Python frames, so
							// it can wait for a thread state without holding anything.
							var ts uintptr
							switch {
							case len(free) > 0:
								ts, free = free[len(free)-1].ts, free[:len(free)-1]
							case capO > 0 && inUse >= capO:
								deferred = append(deferred, vt)
								return
							default:
								ts = tstateNew()
							}
							inUse++
							if t := int64(inUse + len(free)); t > peakTstates.Load() {
								peakTstates.Store(t) // per carrier; good enough for a peak
							}
							vtSetTstate(vt, ts)
						}
						live++
					}
					inRun[cid].Store(vt)
					st := vtRun(vt)
					inRun[cid].Store(0)
					switch st {
					case vtSleep:
						time.AfterFunc(time.Duration(max(vtSleepRemaining(vt), 0)), func() { vtPush(vt) })
					case vtPark:
						// Woken through the local queue or the I/O goroutine, or here by
						// its deadline. The timer holds this park's token, not the VT: if
						// the VT was woken first (and moved on, finished or was recycled),
						// the token no longer matches and the wake does nothing. The timer
						// is also stopped when the VT next runs.
						if d := vtTimeout(vt); d >= 0 {
							tok := vtParkToken(vt)
							timers[vt] = time.AfterFunc(time.Duration(d), func() { vtWakeTok(tok) })
						}
					case vtHung:
						live-- // never runs again (daemon thread at shutdown)
					case vtDone:
						live--
						if pool && vtIsRaw(vt) == 0 {
							inUse--
							free = append(free, idleTS{vtTstateOf(vt), time.Now()}) // reset by the VT on exit
							if len(deferred) > 0 {
								next := deferred[0]
								deferred = deferred[1:]
								vtPush(next)
							}
						}
						if vtIsSpawned(vt) != 0 {
							vtRelease(vt) // threading.Thread: the handle keeps it until joined
							break
						}
						allMu.Lock()
						delete(all, vt)
						allMu.Unlock()
						vtFree(vt)
						wg.Done()
					case vtRunning:
						panic("vt returned while running")
					default:
						if st != 0 {
							panic(fmt.Sprintf("vt state %d", st))
						}
					}
				}
				// The batch taken from the C queue and the next one to run. Nested runs
				// take from it too: the VT a pinned VT waits for may be in it.
				var batch []uintptr
				next := 0
				nestRun[cid] = func() bool {
					var vt uintptr
					if next < len(batch) {
						vt = batch[next]
						next++
					} else if vtTakeLocal(int32(cid), &vt, 1) == 0 {
						return false
					}
					outer := inRun[cid].Load()
					run(vt)
					inRun[cid].Store(outer)
					return true
				}
				local := make([]uintptr, 256)
				for {
					// Sleeps in C on this carrier's futex until VTs are pushed to it.
					var kicked int32
					n := vtCarrierWait(int32(cid), &local[0], uintptr(len(local)), &kicked)
					batch, next = local[:n], 0
					for next < len(batch) {
						vt := batch[next]
						next++
						run(vt)
					}
					batch = nil
					for range trimPending[cid].Swap(0) {
						trimWG.Done()
					}
					// Oldest entries are first. Destroy on this carrier (it owns their heaps).
					if k := 0; len(free) > 0 && time.Since(free[0].since) > idleTrim {
						t := time.Now()
						for k < len(free) && k < 32 && time.Since(free[k].since) > idleTrim {
							tstateFree(free[k].ts)
							k++
						}
						free = free[k:]
						trimmed.Add(int64(k))
						trimTime.Add(int64(time.Since(t)))
						if len(free) > 0 && time.Since(free[0].since) > idleTrim {
							vtKick(int32(cid)) // more to trim; continue after runnable VTs
						}
					}
				}
			}(c)
		}
		// I/O goroutine: epoll events and wakes posted from C (semaphores).
		if vtIOInit() != 0 {
			panic("vt io init")
		}
		go func() {
			for {
				var k uintptr
				vtIOWait(&k) // pushes ready VTs to their carriers itself
			}
		}()
		if v := os.Getenv("SVT_THREADS"); v != "" {
			vtSetThreads(int32(carriers), 1<<20) // pthread_create in libpython now starts VTs
			if v == "py" {
				// threading.Thread on VTs with pooled thread states (no CPython changes)
				pyEvalRestoreThread(mainTS)
				goaio.InstallVTThreads()
				mainTS = pyEvalSaveThread()
				mode += ", threading.Thread on VTs (pooled thread states)"
			} else {
				mode += ", threading.Thread on VTs (CPython thread states)"
			}
		}
		// Re-armed cleanup on a sentinel: runs once per Go GC cycle.
		type sentinel struct{ _ [16]byte }
		var arm func()
		arm = func() {
			runtime.AddCleanup(new(sentinel), func(int) {
				trimWG.Add(carriers)
				for c := range carriers {
					trimPending[c].Add(1)
					vtKick(int32(c))
				}
				arm()
			}, 0)
		}
		if pool {
			arm()
		}
		// Watchdog: dump carrier and VT states and exit 3 if the run takes over
		// SVT_WATCHDOG seconds (default 20).
		go func() {
			wd := 20
			if v, err := strconv.Atoi(os.Getenv("SVT_WATCHDOG")); err == nil && v > 0 {
				wd = v
			}
			time.Sleep(time.Duration(wd) * time.Second)
			var st [7]int64
			vtioStats(&st)
			fmt.Printf("WATCHDOG: sem park %d, sem multi-waiter fallback %d, sem VT could not park %d, io park %d, io fail %d, ev park %d\n",
				st[0], st[1], st[2], st[3], st[4], st[5])
			for c := range carriers {
				fmt.Printf("  carrier %d: in vtRun=%#x\n", c, inRun[c].Load())
			}
			hist := map[int32]int{}
			allMu.Lock()
			for vt := range all {
				hist[vtState(vt)]++
			}
			allMu.Unlock()
			fmt.Printf("  live VT states (state|parked<<8): %v\n", hist)
			var stwInfo func(out *[8]uintptr)
			var vtTstate func(vt uintptr) uintptr
			pyapi.Register(&stwInfo, "pythago_vt_stw_info")
			pyapi.Register(&vtTstate, "pythago_vt_tstate")
			var stw [8]uintptr
			stwInfo(&stw)
			fmt.Printf("  stop-the-world: interp requested=%d stopped=%d countdown=%d requester=%#x; global requested=%d stopped=%d countdown=%d requester=%#x\n",
				stw[0], stw[1], stw[2], stw[3], stw[4], stw[5], stw[6], stw[7])
			var vtBacktrace func(vt uintptr, out *uintptr, max uintptr) uintptr
			pyapi.Register(&vtBacktrace, "pythago_vt_backtrace")
			var vtPyframes func(vt uintptr, buf *byte, n uintptr) uintptr
			pyapi.Register(&vtPyframes, "pythago_vt_pyframes")
			name := symbolizer()
			shown := map[string]int{}
			allMu.Lock()
			for vt := range all {
				var bt [32]uintptr
				nb := vtBacktrace(vt, &bt[0], 32)
				var parts []string
				for _, pc := range bt[:nb] {
					parts = append(parts, name(pc))
				}
				k := strings.Join(parts, " <- ")
				if os.Getenv("SVT_BT") == "py" { // Python frames too (SVT_BT=py)
					var pb [512]byte
					k += "\n      py: " + string(pb[:vtPyframes(vt, &pb[0], uintptr(len(pb)))])
				}
				if os.Getenv("SVT_BT") != "" || strings.Contains(k, "stop_the_world") || strings.Contains(k, "StopTheWorld") {
					shown[fmt.Sprintf("state|parked<<8=%d: %s", vtState(vt), k)]++
				}
			}
			for k, c := range shown {
				fmt.Printf("  %d suspended VT(s): %s\n", c, k) // SVT_BT=1: all of them
			}
			for vt := range all {
				if ts := vtTstate(vt); ts != 0 && (ts == stw[3] || ts == stw[7]) {
					fmt.Printf("  requester is VT %#x (state|parked<<8 = %d)\n", vt, vtState(vt))
				}
			}
			allMu.Unlock()
			if os.Getenv("SVT_TRAP") != "" {
				syscall.Kill(syscall.Getpid(), syscall.SIGTRAP) // stop under gdb for backtraces
			}
			if !prof {
				profStart(make(chan struct{}))
			}
			time.Sleep(300 * time.Millisecond)
			profDumpStacks(10)
			os.Exit(3)
		}()
		for w := range waves {
			wstart := time.Now()
			// One reference per VT, released when it finishes. Refcounting needs an attached
			// thread state: body is owned by this thread, whose local count other threads may
			// merge while it is detached.
			pyEvalRestoreThread(mainTS)
			for range n {
				pyIncRef(body)
			}
			mainTS = pyEvalSaveThread()
			for i := range n {
				vt := vtNew(body, int64(i), 512<<10, int32(i%carriers))
				if vt == 0 {
					panic("vt alloc")
				}
				allMu.Lock()
				all[vt] = true
				allMu.Unlock()
				wg.Add(1)
				vtPush(vt)
			}
			wg.Wait()
			if waves > 1 {
				fmt.Printf("  wave %d: %v\n", w+1, time.Since(wstart).Round(time.Millisecond))
			}
		}
		if pool {
			runtime.GC()
			runtime.GC()
			trimWG.Wait()
			fmt.Printf("  idle trim so far: %d thread states destroyed in %v (carrier CPU); peak thread states on one carrier %d\n",
				trimmed.Load(), time.Duration(trimTime.Load()).Round(time.Millisecond), peakTstates.Load())
		}
		pyEvalRestoreThread(mainTS)
	}
	elapsed := time.Since(start)
	close(stop)
	if os.Getenv("SVT_STW") != "" {
		ts := pyEvalSaveThread() // let the collector finish its last pass
		stwWG.Wait()
		pyEvalRestoreThread(ts)
		mode += fmt.Sprintf(", %d concurrent gc.collect", stwCount.Load())
	}
	fmt.Printf("%s: %d tasks x 3 sleeps of 50 ms in %v; peak OS threads %d, peak RSS %d MiB; Go callbacks %d\n",
		mode, n, elapsed.Round(time.Millisecond), peakThreads.Load(), peakRSS.Load()>>10, goCalls.Load())
	pyRunSimpleString(fmt.Sprintf(`
import gc; gc.collect()
idents = {v[0] for v in results.values()}
natives = {v[1] for v in results.values()}
print(f"  completed {len(results)}/%d, failures {len(failures)} {failures[:3]}, "
      f"distinct idents {len(idents)}, distinct OS threads {len(natives)}"
      + (f"; {check()}" if "check" in globals() else ""))
`, n))
	if prof {
		profReport()
	}
	if os.Getenv("SVT_POOL") != "" || os.Getenv("SVT_STATS") != "" {
		var st [7]int64
		vtioStats(&st)
		fmt.Printf("  parks: semaphore %d, socket %d, asyncio loop %d; carrier-blocking waits: attached %d, runtime lock %d, shared semaphore %d, io registration %d\n",
			st[0], st[3], st[5], st[2], st[6], st[1], st[4])
	}
	pyDecRef(body)
	if pyFinalizeEx() != 0 {
		fmt.Fprintln(os.Stderr, "Py_FinalizeEx failed")
		os.Exit(1)
	}
}
