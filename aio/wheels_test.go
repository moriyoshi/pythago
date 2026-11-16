package aio_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/moriyoshi/pythago/internal/testwheels"
	"github.com/moriyoshi/pythago/py"
)

// Async Python awaiting native code, from real binary wheels (internal/testwheels;
// ci/test-wheels.txt pins them, internal/tools/fetchwheels fetches them per target):
//
//   - pyreqwest (Rust on tokio): its awaitables are asyncio futures made with
//     loop.create_future() and completed from tokio's own threads through
//     loop.call_soon_threadsafe, so on the Go loops the wake-up is a Go call from an OS
//     thread that neither Python nor Go created.
//   - pyzmq's zmq.asyncio: Python coroutines over libzmq, woken by the readiness of the
//     socket's ZMQ_FD through loop.add_reader (edge-triggered in ZMQ's sense: pyzmq
//     re-checks ZMQ_EVENTS after each operation).
//
// Every script runs on asyncio's own loop in pythago (the baseline) and on each Go loop,
// and with PYTHAGO_TEST_STOCK_PYTHON (a stock python of the image's version and variant)
// again under that interpreter with the same site directory, on asyncio's loops only.
// Every await has a timeout, so a lost wake-up fails with a diagnostic instead of hanging.

// wheelsCommon is prepended to every wheel script (after common in pythago; alone under
// the stock interpreter, which has no goaio). RUN ("pythago" or "stock"), SITE, URL and
// CLOSED_URL come first.
const wheelsCommon = `
import asyncio, hashlib, os, sys, sysconfig, threading, time, traceback
from datetime import timedelta
if RUN == "stock":
    sys.path.insert(0, SITE)
else:  # from Config.SearchPaths (aio_test.go's config)
    assert SITE in sys.path, ("the site directory is not on sys.path", SITE, sys.path)

T = 60  # seconds: the bound on every await (qemu-user is slow)

import pyreqwest, pyreqwest._pyreqwest
from pyreqwest.client import ClientBuilder
from pyreqwest import exceptions as rqx
import zmq, zmq.asyncio, zmq.backend.cython._zmq

def _under_site(path):
    return os.path.realpath(path).startswith(os.path.realpath(SITE) + os.sep)
NATIVE = {"pyreqwest": pyreqwest._pyreqwest.__file__, "zmq": zmq.backend.cython._zmq.__file__}
for _name, _file in NATIVE.items():
    assert _under_site(_file), (_name, _file, SITE)

FT = bool(sysconfig.get_config_var("Py_GIL_DISABLED"))
GIL = sys._is_gil_enabled() if hasattr(sys, "_is_gil_enabled") else True

try:
    import goaio
    GO_LOOPS = {"GoEventLoop": goaio.GoEventLoop, "GoSelectorEventLoop": goaio.GoSelectorEventLoop}
except ImportError:  # the stock interpreter
    GO_LOOPS = {}
# asyncio.EventLoop is the platform's default loop (the proactor on Windows).
ALL_LOOPS = {"asyncio.EventLoop": asyncio.EventLoop, **GO_LOOPS}
# Loops with add_reader, which zmq.asyncio needs (asyncio's proactor has none).
SELECTOR_LOOPS = {"asyncio.SelectorEventLoop": asyncio.SelectorEventLoop, **GO_LOOPS}

def pattern(n):
    return (bytes(range(256)) * (n // 256 + 1))[:n]

def run_threads(target, args_list):
    """Runs target(*args) on one Python thread each, every thread with its own loop;
    raises with every thread's traceback if any failed or hung."""
    results = [None] * len(args_list)
    def main(i, args):
        try:
            target(*args)
            results[i] = "ok"
        except BaseException:
            results[i] = traceback.format_exc()
    threads = [threading.Thread(target=main, args=(i, a)) for i, a in enumerate(args_list)]
    for th in threads:
        th.start()
    deadline = time.monotonic() + 4 * T
    for th in threads:
        th.join(max(0, deadline - time.monotonic()))
    failed = ["thread %d %r: %s" % (i, args_list[i], "hung" if th.is_alive() else results[i])
              for i, th in enumerate(threads) if th.is_alive() or results[i] != "ok"]
    assert not failed, "\n".join(failed)
`

// runWheels runs a wheel script in pythago, and under PYTHAGO_TEST_STOCK_PYTHON if set.
func runWheels(t *testing.T, url, script string) {
	t.Helper()
	site := testwheels.Skip(t)
	head := "SITE = " + strconv.Quote(site) + "\nURL = " + strconv.Quote(url) +
		"\nCLOSED_URL = " + strconv.Quote(testwheels.ClosedURL(t)) + "\n" + wheelsCommon
	t.Run("pythago", func(t *testing.T) {
		// run, with the traceback.
		if err := py.Do(func() error { return py.Exec(common + "RUN = 'pythago'\n" + head + script) }); err != nil {
			var pe *py.Error
			if errors.As(err, &pe) {
				t.Fatalf("%v\n%s", err, pe.Traceback)
			}
			t.Fatal(err)
		}
	})
	stock := os.Getenv("PYTHAGO_TEST_STOCK_PYTHON")
	if stock == "" {
		return
	}
	t.Run("stock", func(t *testing.T) {
		// The stock interpreter must match the image: same version, same GIL variant.
		match := `import sys, sysconfig; print(sys.version_info[:2], bool(sysconfig.get_config_var("Py_GIL_DISABLED")))`
		file := filepath.Join(t.TempDir(), "wheels_script.py")
		if err := os.WriteFile(file, []byte("RUN = 'stock'\n"+head+"\n"+match+"\n"+script), 0o644); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(ctx, stock, "-I", file).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", stock, err, out)
		}
		t.Logf("%s:\n%s", stock, out)
	})
}

// skipZmq skips a test that opens zmq sockets where libzmq cannot run (Wine on arm64).
func skipZmq(t *testing.T) {
	if why := testwheels.ZmqUnsupported(runtime.GOOS, runtime.GOARCH); why != "" {
		t.Skip(why)
	}
}

// TestWheelsLoad: both packages load their native modules from the extracted wheels,
// and on a free-threaded build the GIL stays off after the imports (as under stock 3.14t).
func TestWheelsLoad(t *testing.T) {
	runWheels(t, "", `
print("native modules:", NATIVE)
print("zmq", zmq.zmq_version(), "pyzmq", zmq.pyzmq_version(), "pyreqwest", pyreqwest.__version__)
print("free-threaded:", FT, "GIL enabled after the imports:", GIL)
import sys as _sys
assert _sys.version_info[:2] >= (3, 13)
if FT:
    assert not GIL, "importing pyreqwest and zmq enabled the GIL"
assert zmq.pyzmq_version() == "27.3.0" and pyreqwest.__version__ == "0.15.1"
`)
}

// pyreqwestSuite exercises one client on the running loop: many concurrent requests,
// large bodies, a cancelled request, a client timeout and a refused connection.
const pyreqwestSuite = `
async def get_json(c, path):
    r = await asyncio.wait_for(c.get(URL + path).build().send(), T)
    assert r.status == 200, (path, r.status)
    return await asyncio.wait_for(r.json(), T)

async def get_text(c, path):
    r = await asyncio.wait_for(c.get(URL + path).build().send(), T)
    assert r.status == 200, (path, r.status)
    return await asyncio.wait_for(r.text(), T)

async def wait_stats(c, tag, want, what):
    deadline = time.monotonic() + T
    while True:
        st = await get_json(c, "/stats?tag=" + tag)
        if want(st):
            return st
        if time.monotonic() > deadline:
            raise AssertionError("%s: %s after %ds" % (what, st, T))
        await asyncio.sleep(0.01)

async def pyreqwest_suite(tag, multithreaded, n=300, big=8 << 20):
    loop = type(asyncio.get_running_loop()).__name__
    def client(**kw):
        b = ClientBuilder().runtime_multithreaded(multithreaded)
        if "timeout" in kw:
            b = b.timeout(kw["timeout"])
        return b.build()
    async with client() as c:
        # Many requests at once: each future completes from a tokio thread.
        rs = await asyncio.wait_for(asyncio.gather(
            *(c.get("%s/hello?i=%d" % (URL, i)).build().send() for i in range(n))), T)
        texts = await asyncio.wait_for(asyncio.gather(*(r.text() for r in rs)), T)
        assert texts == ["hello %d" % i for i in range(n)], (loop, texts[:3])

        # Large bodies, both ways.
        r = await asyncio.wait_for(c.get("%s/bytes?n=%d" % (URL, big)).build().send(), T)
        body = bytes(await asyncio.wait_for(r.bytes(), T))
        assert len(body) == big and hashlib.sha256(body).digest() == hashlib.sha256(pattern(big)).digest(), (loop, len(body))
        payload = os.urandom(big // 2)
        r = await asyncio.wait_for(c.post(URL + "/echo").body_bytes(payload).build().send(), T)
        assert bytes(await asyncio.wait_for(r.bytes(), T)) == payload, loop

        # Cancel a request the server holds. The task ends cancelled at once. pyreqwest
        # leaves the request running in tokio (stock CPython shows the same: the server
        # never sees the client go, not even at client.close()), so the server then
        # answers it, and that late completion must not disturb the loop: no error reaches
        # its exception handler, and the loop and client still work.
        errors = []
        asyncio.get_running_loop().set_exception_handler(lambda _, ctx: errors.append(ctx))
        task = asyncio.ensure_future(c.get("%s/slow?tag=%s" % (URL, tag)).build().send())
        await wait_stats(c, tag, lambda s: s["inflight"] == 1, "%s: the slow request never reached the server" % loop)
        task.cancel()
        try:
            await asyncio.wait_for(task, T)
        except asyncio.CancelledError:
            pass
        else:
            raise AssertionError("%s: the cancelled request completed" % loop)
        await get_text(c, "/release?tag=" + tag)
        await wait_stats(c, tag, lambda s: s["inflight"] == 0, "%s: the released request is still held" % loop)
        await asyncio.sleep(0.05)
        assert await get_text(c, "/hello?i=after") == "hello after", loop
        assert not errors, (loop, errors)
        asyncio.get_running_loop().set_exception_handler(None)

        # A cancelled request still held when the loop closes: run_suite releases it after.
        left = asyncio.ensure_future(c.get("%s/slow?tag=%s-left" % (URL, tag)).build().send())
        await wait_stats(c, tag + "-left", lambda s: s["inflight"] == 1, "%s: the left request never reached the server" % loop)
        left.cancel()
        try:
            await asyncio.wait_for(left, T)
        except asyncio.CancelledError:
            pass

    # The client's own timeout.
    async with client(timeout=timedelta(milliseconds=200)) as c:
        t0 = time.monotonic()
        try:
            await asyncio.wait_for(c.get("%s/slow?tag=%s-timeout" % (URL, tag)).build().send(), T)
        except rqx.RequestTimeoutError:
            pass
        else:
            raise AssertionError("%s: no timeout" % loop)
        took = time.monotonic() - t0
        assert 0.15 <= took < T, (loop, took)
    async with client() as c:
        await get_text(c, "/release?tag=%s-timeout" % tag)
        await wait_stats(c, tag + "-timeout", lambda s: s["inflight"] == 0, "%s: the timed-out request is still held" % loop)

    # A refused connection.
    async with client() as c:
        try:
            await asyncio.wait_for(c.get(CLOSED_URL).build().send(), T)
        except rqx.ConnectError as e:
            assert isinstance(e, rqx.NetworkError), (loop, type(e).__mro__)
        else:
            raise AssertionError("%s: connected to a closed port" % loop)
    return loop

def run_suite(tag, mt, factory, **kw):
    """pyreqwest_suite on a new loop, then the request it left held is released with the
    loop closed (tokio completes it for a loop that is gone), and a new loop still works."""
    tag = "%s-%d-%s" % (RUN, time.monotonic_ns(), tag)  # unique on a shared server
    got = asyncio.run(pyreqwest_suite(tag, mt, **kw), loop_factory=factory)
    import json, urllib.request
    def get(path):
        with urllib.request.urlopen(URL + path, timeout=T) as r:
            return r.read()
    get("/release?tag=%s-left" % tag)
    deadline = time.monotonic() + T
    while json.loads(get("/stats?tag=%s-left" % tag))["inflight"]:
        assert time.monotonic() < deadline, "%s: the left request is still held" % got
        time.sleep(0.01)
    time.sleep(0.05)
    async def again():
        async with ClientBuilder().runtime_multithreaded(mt).build() as c:
            return await get_text(c, "/hello?i=again")
    assert asyncio.run(again(), loop_factory=factory) == "hello again", got
    return got
`

// TestPyreqwestLoops runs the pyreqwest suite on each loop, with tokio's default
// (current-thread) runtime and its multi-threaded one.
func TestPyreqwestLoops(t *testing.T) {
	s := testwheels.NewServer(t)
	runWheels(t, s.URL, pyreqwestSuite+`
for name, factory in ALL_LOOPS.items():
    for mt in (False, True):
        t0 = time.monotonic()
        got = run_suite("%s-%s" % (name, mt), mt, factory)
        print("%-20s multithreaded=%-5s %s %.2fs" % (name, mt, got, time.monotonic() - t0))
`)
}

// TestPyreqwestThreads runs the suite on several Python threads at once, each with its
// own loop and client (free-threaded builds run them in parallel).
func TestPyreqwestThreads(t *testing.T) {
	s := testwheels.NewServer(t)
	runWheels(t, s.URL, pyreqwestSuite+`
loops = list(ALL_LOOPS.items())
jobs = [(i, loops[i % len(loops)], i % 2 == 1) for i in range(2 * len(loops) + 2)]
def job(i, loop, mt):
    run_suite("thread%d" % i, mt, loop[1], n=100, big=1 << 20)
t0 = time.monotonic()
run_threads(job, jobs)
print("%d threads over %s: %.2fs" % (len(jobs), list(ALL_LOOPS), time.monotonic() - t0))
`)
}

// zmqSuite exercises zmq.asyncio on the running loop: REQ/REP and PUSH/PULL over tcp
// loopback and inproc, a poller, and a cancelled and a timed-out recv.
const zmqSuite = `
def bind(sock, addr):
    if addr == "tcp":
        return "tcp://127.0.0.1:%d" % sock.bind_to_random_port("tcp://127.0.0.1")
    sock.bind(addr)
    return addr

async def zmq_reqrep(ctx, addr, n):
    rep = ctx.socket(zmq.REP)
    addr = bind(rep, addr)
    req = ctx.socket(zmq.REQ)
    req.connect(addr)
    async def server():
        for _ in range(n):
            a, b = await rep.recv_multipart()
            await rep.send_multipart([a.upper(), b])
    srv = asyncio.ensure_future(server())
    for i in range(n):
        await asyncio.wait_for(req.send_multipart([b"x%d" % i, b"y" * (i % 300)]), T)
        got = await asyncio.wait_for(req.recv_multipart(), T)
        assert got == [b"X%d" % i, b"y" * (i % 300)], (addr, i, got)
    await asyncio.wait_for(srv, T)
    req.close(linger=0)
    rep.close(linger=0)

async def zmq_pushpull(ctx, addr, n):
    pull = ctx.socket(zmq.PULL)
    addr = bind(pull, addr)
    push = ctx.socket(zmq.PUSH)
    push.sndhwm = 100  # backpressure: sends wait for the receiver
    push.connect(addr)
    sizes = [i % 7 * 9000 for i in range(n)]
    async def sender():
        for i, size in enumerate(sizes):
            await push.send(b"%08d" % i + b"z" * size)
    snd = asyncio.ensure_future(sender())
    for i, size in enumerate(sizes):
        m = await asyncio.wait_for(pull.recv(), T)
        assert m[:8] == b"%08d" % i and len(m) == 8 + size, (addr, i, m[:8], len(m))
    await asyncio.wait_for(snd, T)
    push.close(linger=0)
    pull.close(linger=0)

async def zmq_poller(ctx):
    a, b = ctx.socket(zmq.PULL), ctx.socket(zmq.PULL)
    pa, pb = ctx.socket(zmq.PUSH), ctx.socket(zmq.PUSH)
    pa.connect(bind(a, "tcp"))
    pb.connect(bind(b, "tcp"))
    poller = zmq.asyncio.Poller()
    poller.register(a, zmq.POLLIN)
    poller.register(b, zmq.POLLIN)
    t0 = time.monotonic()
    assert await asyncio.wait_for(poller.poll(100), T) == []
    assert time.monotonic() - t0 >= 0.09
    await pb.send(b"to b")
    events = await asyncio.wait_for(poller.poll(T * 1000), T)
    assert events == [(b, zmq.POLLIN)], events
    assert await asyncio.wait_for(b.recv(), T) == b"to b"
    for s in (a, b, pa, pb):
        s.close(linger=0)

async def zmq_cancel(ctx, addr):
    pull = ctx.socket(zmq.PULL)
    addr = bind(pull, addr)
    push = ctx.socket(zmq.PUSH)
    push.connect(addr)
    # A cancelled recv takes no message: the next one gets it.
    task = asyncio.ensure_future(pull.recv())
    await asyncio.sleep(0.05)
    assert not task.done()
    task.cancel()
    try:
        await asyncio.wait_for(task, T)
    except asyncio.CancelledError:
        pass
    else:
        raise AssertionError("the cancelled recv completed")
    # A recv that times out, likewise.
    try:
        await asyncio.wait_for(pull.recv(), 0.1)
    except asyncio.TimeoutError:
        pass
    else:
        raise AssertionError("recv returned with nothing sent")
    await push.send(b"after")
    assert await asyncio.wait_for(pull.recv(), T) == b"after"
    push.close(linger=0)
    pull.close(linger=0)

async def zmq_suite(tag, n=2000):
    loop = type(asyncio.get_running_loop()).__name__
    ctx = zmq.asyncio.Context()
    try:
        await zmq_reqrep(ctx, "tcp", n)
        await zmq_reqrep(ctx, "inproc://reqrep-" + tag, n)
        await zmq_pushpull(ctx, "tcp", 2 * n)
        await zmq_pushpull(ctx, "inproc://pushpull-" + tag, 2 * n)
        await zmq_poller(ctx)
        await zmq_cancel(ctx, "tcp")
        await zmq_cancel(ctx, "inproc://cancel-" + tag)
    finally:
        ctx.destroy(linger=0)
    return loop
`

// TestZmqLoops runs the zmq.asyncio suite on each loop with add_reader.
func TestZmqLoops(t *testing.T) {
	skipZmq(t)
	runWheels(t, "", zmqSuite+`
for name, factory in SELECTOR_LOOPS.items():
    t0 = time.monotonic()
    got = asyncio.run(zmq_suite(name), loop_factory=factory)
    print("%-26s %s %.2fs" % (name, got, time.monotonic() - t0))
`)
}

// TestZmqThreads runs the suite on several Python threads at once, each with its own
// loop and context.
func TestZmqThreads(t *testing.T) {
	skipZmq(t)
	runWheels(t, "", zmqSuite+`
loops = list(SELECTOR_LOOPS.items())
jobs = [(i, loops[i % len(loops)]) for i in range(4 * len(loops))]
def job(i, loop):
    asyncio.run(zmq_suite("thread%d" % i, n=500), loop_factory=loop[1])
t0 = time.monotonic()
run_threads(job, jobs)
print("%d threads over %s: %.2fs" % (len(jobs), list(SELECTOR_LOOPS), time.monotonic() - t0))
`)
}
