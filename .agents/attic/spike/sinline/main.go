// Command sinline measures the ceiling of running an ASGI application inline on the
// net/http handler goroutine: the handler enters Python with py.Do on its own OS thread
// and steps the application's coroutine itself, with receive() and send() completing
// at once. There is no event loop and no handoff to another thread. An application that
// awaits anything else is rejected (a real server would fall back to the loop).
//
//	sinline [-addr 127.0.0.1:8000] [-app-dir dir] module:app
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	_ "github.com/moriyoshi/pythago/internal/testimage"
	"github.com/moriyoshi/pythago/py"
)

const driver = `
import importlib
from itertools import chain
_mod, _, _attr = TARGET.partition(":")
app = getattr(importlib.import_module(_mod), _attr)
_ASGI = {"version": "3.0"}
_join = b"\0".join
_chain = chain.from_iterable

def run(rid, method, path, query, packed, n, host, port, body):
    if n:
        h = packed.split(b"\0")
        headers = list(zip(h[0::2], h[1::2]))
    else:
        headers = []
    scope = {"type": "http", "asgi": _ASGI, "http_version": "1.1", "method": method,
             "scheme": "http", "path": path, "raw_path": None, "query_string": query,
             "root_path": "", "headers": headers, "client": (host, port), "server": None}
    async def receive():
        return {"type": "http.request", "body": body, "more_body": False}
    start = None
    async def send(m):
        nonlocal start
        if m["type"] == "http.response.start":
            start = m  # held until the first body, which goes with it in one call
        elif start is not None:
            hs = start.get("headers", ())
            go_respond(rid, start["status"], _join(_chain(hs)), len(hs), m.get("body", b""))
            start = None
        else:
            go_body(rid, m.get("body", b""))
    coro = app(scope, receive, send)
    try:
        coro.send(None)
    except StopIteration:
        return
    coro.close()
    raise RuntimeError("the application awaited something other than receive() or send()")

import struct
_record = struct.Struct("<HII").pack  # status (0: failed), header bytes, body bytes

def _serve(method, path, query, packed, n, host, port, body):
    if n:
        h = packed.split(b"\0")
        headers = list(zip(h[0::2], h[1::2]))
    else:
        headers = []
    scope = {"type": "http", "asgi": _ASGI, "http_version": "1.1", "method": method,
             "scheme": "http", "path": path, "raw_path": None, "query_string": query,
             "root_path": "", "headers": headers, "client": (host, port), "server": None}
    async def receive():
        return {"type": "http.request", "body": body, "more_body": False}
    start = None
    chunks = []
    async def send(m):
        nonlocal start
        if m["type"] == "http.response.start":
            start = m
        else:
            chunks.append(m.get("body", b""))
    coro = app(scope, receive, send)
    try:
        coro.send(None)
    except StopIteration:
        hs = start.get("headers", ())
        return start["status"], _join(_chain(hs)), b"".join(chunks)
    coro.close()
    raise RuntimeError("the application awaited something other than receive() or send()")

def run_batch(*a):
    # One call per batch, with no calls back into Go: the responses come back packed.
    out = []
    for j in range(len(a) // 8):
        try:
            status, hdr, body = _serve(*a[8 * j:8 * j + 8])
        except BaseException as e:
            status, hdr, body = 0, b"", repr(e).encode()
        out += (_record(status, len(hdr), len(body)), hdr, body)
    return b"".join(out)
`

// reqs maps a request id to its ResponseWriter while the application runs inline.
var (
	reqMu sync.Mutex
	reqs  = map[int64]http.ResponseWriter{}
	reqID int64
)

func writer(a *py.Object) http.ResponseWriter {
	id, _ := a.Int64()
	reqMu.Lock()
	defer reqMu.Unlock()
	return reqs[id]
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8000", "listen address")
	appDir := flag.String("app-dir", ".", "directory the application is imported from")
	cpuprofile := flag.String("cpuprofile", "", "write a Go CPU profile until SIGINT")
	slots := flag.Int("slots", 0, "py.Config.Slots (0: the default)")
	cres := flag.Bool("cres", false, "the C-resident combiner (needs -tags pythago_spike)")
	completers = flag.Int("completers", runtime.GOMAXPROCS(0), "-cres: completer goroutines")
	batch := flag.Bool("batch", false, "flat combining with one Python call per batch, and the response formatted outside the lock")
	combine := flag.Bool("combine", false, "flat combining: one goroutine at a time runs the queued requests' Python steps on its own thread state")
	flag.Parse()
	if *cpuprofile != "" {
		f, _ := os.Create(*cpuprofile)
		pprof.StartCPUProfile(f)
		go func() {
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, os.Interrupt)
			<-ch
			pprof.StopCPUProfile()
			os.Exit(0)
		}()
	}
	cfg := py.Config{Environment: os.Getenv("PYRUN_ENV"), Files: os.DirFS(*appDir), Slots: *slots}
	if err := py.Init(cfg); err != nil {
		log.Fatal(err)
	}
	var run *py.Object
	if err := py.Do(func() error {
		ns, err := py.From(map[string]any{
			"TARGET": flag.Arg(0),
			"go_start": py.Func(func(a []*py.Object, _ map[string]*py.Object) (any, error) {
				w := writer(a[0])
				status, _ := a[1].Int64()
				packed, _ := a[2].UnsafeBytes()
				h := w.Header()
				f := bytes.Split(packed, []byte{0})
				for i := 0; i+1 < len(f); i += 2 {
					h[http.CanonicalHeaderKey(string(f[i]))] = []string{string(f[i+1])}
				}
				w.WriteHeader(int(status))
				return nil, nil
			}),
			"go_respond": py.Func(func(a []*py.Object, _ map[string]*py.Object) (any, error) {
				w := writer(a[0])
				status, _ := a[1].Int64()
				packed, _ := a[2].UnsafeBytes()
				h := w.Header()
				f := bytes.Split(packed, []byte{0})
				for i := 0; i+1 < len(f); i += 2 {
					h[http.CanonicalHeaderKey(string(f[i]))] = []string{string(f[i+1])}
				}
				w.WriteHeader(int(status))
				b, _ := a[4].UnsafeBytes()
				w.Write(b)
				return nil, nil
			}),
			"go_bresp": py.Func(func(a []*py.Object, _ map[string]*py.Object) (any, error) {
				i, _ := a[0].Int64()
				j := curBatch[i]
				st, _ := a[1].Int64()
				j.status = int(st)
				h, _ := a[2].UnsafeBytes()
				j.hdr = append(j.hdr[:0], h...)
				b, _ := a[4].UnsafeBytes()
				j.body = append(j.body, b...)
				return nil, nil
			}),
			"go_bbody": py.Func(func(a []*py.Object, _ map[string]*py.Object) (any, error) {
				i, _ := a[0].Int64()
				b, _ := a[1].UnsafeBytes()
				curBatch[i].body = append(curBatch[i].body, b...)
				return nil, nil
			}),
			"go_bfail": py.Func(func(a []*py.Object, _ map[string]*py.Object) (any, error) {
				i, _ := a[0].Int64()
				msg, _ := a[1].GoString()
				curBatch[i].err = msg
				return nil, nil
			}),
			"go_body": py.Func(func(a []*py.Object, _ map[string]*py.Object) (any, error) {
				b, _ := a[1].UnsafeBytes()
				writer(a[0]).Write(b)
				return nil, nil
			}),
		})
		if err != nil {
			return err
		}
		if err := py.ExecIn("import builtins\n__builtins__ = builtins\n"+driver, ns); err != nil {
			return err
		}
		if run, err = ns.Item("run"); err != nil {
			return err
		}
		if runBatch, err = ns.Item("run_batch"); err != nil {
			return err
		}
		if *cres {
			comb, err = newCombiner(runBatch)
		}
		return err
	}); err != nil {
		log.Fatal(err)
	}
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGUSR1, syscall.SIGUSR2)
		for sig := range ch {
			if sig == syscall.SIGUSR2 { // a 2 s execution trace
				f, _ := os.Create(os.Getenv("SINLINE_TRACE"))
				trace.Start(f)
				time.Sleep(2 * time.Second)
				trace.Stop()
				f.Close()
				log.Println("trace written")
				continue
			}
			var line []string
			for i, n := range batchSizes {
				if n > 0 {
					line = append(line, strconv.Itoa(i)+":"+strconv.FormatInt(n, 10))
				}
			}
			log.Println("batch sizes", strings.Join(line, " "), "held", time.Duration(heldNs.Load()))
			if comb != nil {
				log.Println("combiner", combStats())
			}
		}
	}()
	http.Serve(listen(*addr), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var packed []byte
		n := 0
		for k, vs := range r.Header {
			for _, v := range vs {
				packed = append(packed, strings.ToLower(k)...)
				packed = append(packed, 0)
				packed = append(packed, v...)
				packed = append(packed, 0)
				n++
			}
		}
		if len(packed) > 0 {
			packed = packed[:len(packed)-1]
		}
		host, port, _ := net.SplitHostPort(r.RemoteAddr)
		p, _ := strconv.Atoi(port)
		if *cres {
			status, hdr, out, e := comb.Submit(r.Method, r.URL.Path, []byte(r.URL.RawQuery), packed, n, host, p, body)
			if e != "" {
				http.Error(w, e, 500)
				return
			}
			h := w.Header()
			f := bytes.Split(hdr, []byte{0})
			for i := 0; i+1 < len(f); i += 2 {
				h[http.CanonicalHeaderKey(string(f[i]))] = []string{string(f[i+1])}
			}
			w.WriteHeader(status)
			w.Write(out)
			return
		}
		if *batch {
			j := &bjob{args: []any{r.Method, r.URL.Path, []byte(r.URL.RawQuery), packed, n, host, p, body},
				done: make(chan struct{})}
			batched(j)
			if j.err != "" {
				http.Error(w, j.err, 500)
				return
			}
			h := w.Header()
			f := bytes.Split(j.hdr, []byte{0})
			for i := 0; i+1 < len(f); i += 2 {
				h[http.CanonicalHeaderKey(string(f[i]))] = []string{string(f[i+1])}
			}
			w.WriteHeader(j.status)
			w.Write(j.body)
			return
		}
		reqMu.Lock()
		reqID++
		id := reqID
		reqs[id] = w
		reqMu.Unlock()
		step := func() error {
			r, err := run.Call(id, r.Method, r.URL.Path, []byte(r.URL.RawQuery), packed, n, host, p, body)
			r.Close()
			return err
		}
		var err error
		if *combine {
			err = combined(step)
		} else {
			err = py.Do(step)
		}
		reqMu.Lock()
		delete(reqs, id)
		reqMu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), 500)
		}
	}))
}

// The combiner: requests queue their Python steps; the goroutine that finds no combiner
// running becomes it and runs every queued step, its own included, inside one py.Do, so
// the GIL is taken once per batch and never handed between threads. Waiters park in Go.
type job struct {
	step func() error
	err  error
	done chan struct{}
}

var (
	combMu    sync.Mutex
	combQueue []*job
	combBusy  bool
)

func combined(step func() error) error {
	j := &job{step: step, done: make(chan struct{})}
	combMu.Lock()
	combQueue = append(combQueue, j)
	if combBusy {
		combMu.Unlock()
		<-j.done
		return j.err
	}
	combBusy = true
	combMu.Unlock()
	py.Do(func() error {
		var batch []*job
		for {
			combMu.Lock()
			batch, combQueue = combQueue, batch[:0]
			if len(batch) == 0 {
				combBusy = false
				combMu.Unlock()
				return nil
			}
			combMu.Unlock()
			for _, q := range batch {
				q.err = q.step()
				close(q.done)
			}
		}
	})
	<-j.done
	return j.err
}

// Batched combining: as combined, but the combiner makes one Python call for the whole
// batch (run_batch), and Python only records each response (go_bresp copies it into the
// job); the waiting goroutine formats and writes it outside the lock.
type bjob struct {
	args   []any
	status int
	hdr    []byte
	body   []byte
	err    string
	done   chan struct{}
}

var completers *int

var (
	runBatch   *py.Object
	bMu        sync.Mutex
	bQueue     []*bjob
	bBusy      bool
	curBatch   []*bjob // the batch run_batch is serving; only the combiner touches it
	batchSizes [64]int64
	heldNs     atomic.Int64 // time combiners spent inside py.Do
)

func batched(j *bjob) {
	bMu.Lock()
	bQueue = append(bQueue, j)
	if bBusy {
		bMu.Unlock()
		<-j.done
		return
	}
	bBusy = true
	bMu.Unlock()
	t0 := time.Now()
	defer func() { heldNs.Add(int64(time.Since(t0))) }()
	py.Do(func() error {
		var b []*bjob
		var flat []any
		for {
			bMu.Lock()
			b, bQueue = bQueue, b[:0]
			if len(b) == 0 {
				bBusy = false
				bMu.Unlock()
				return nil
			}
			bMu.Unlock()
			batchSizes[min(len(b), len(batchSizes)-1)]++
			flat = flat[:0]
			for _, q := range b {
				flat = append(flat, q.args...)
			}
			r, err := runBatch.Call(flat...)
			if err == nil {
				var data []byte
				if data, err = r.UnsafeBytes(); err == nil {
					for _, q := range b { // status u16, header length u32, body length u32
						st := int(binary.LittleEndian.Uint16(data))
						hl := int(binary.LittleEndian.Uint32(data[2:]))
						bl := int(binary.LittleEndian.Uint32(data[6:]))
						data = data[10:]
						q.hdr = append(q.hdr[:0], data[:hl]...)
						q.body = append(q.body[:0], data[hl:hl+bl]...)
						data = data[hl+bl:]
						if q.status = st; st == 0 {
							q.err = string(q.body)
						}
					}
				}
				r.Close()
			}
			for _, q := range b {
				if err != nil && q.err == "" {
					q.err = err.Error()
				}
				close(q.done)
			}
		}
	})
	<-j.done
}

func listen(addr string) net.Listener {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	return l
}
