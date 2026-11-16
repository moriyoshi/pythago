package aio_test

import "testing"

// subprocessScenarios run on asyncio's stock loop, GoEventLoop and GoSelectorEventLoop;
// each loop must give the stock loop's results. On Windows the commands are Python (this
// test binary as sys.executable), the stock loop is the proactor, asyncio's selector loop
// has no subprocesses, and os.pipe pipes (which the proactor cannot serve) are checked on
// GoEventLoop alone.
const subprocessScenarios = `
import os, signal

TIMEOUT = 10
WINDOWS = sys.platform == "win32"
PY = [sys.executable, "-c"]
CAT = PY + ["import shutil, sys; shutil.copyfileobj(sys.stdin.buffer, sys.stdout.buffer)"] if WINDOWS else ["cat"]
SLEEP = PY + ["import time; time.sleep(30)"] if WINDOWS else ["sleep", "30"]
EXIT5 = PY + ["raise SystemExit(5)"] if WINDOWS else ["sh", "-c", "exit 5"]

async def cat_roundtrip():
    proc = await asyncio.create_subprocess_exec(*CAT, stdin=asyncio.subprocess.PIPE,
                                                stdout=asyncio.subprocess.PIPE)
    payload = bytes(range(256)) * 8192  # 2 MiB: both pipes fill and drain
    async def feed():
        proc.stdin.write(payload)
        await proc.stdin.drain()
        proc.stdin.close()
        await proc.stdin.wait_closed()
    got, _ = await asyncio.gather(proc.stdout.read(), feed())
    return got == payload, await proc.wait()

async def communicate():
    pipes = dict(stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE,
                 stderr=asyncio.subprocess.PIPE)
    if WINDOWS:
        proc = await asyncio.create_subprocess_exec(*PY, "import sys; "
            "sys.stdout.buffer.write(sys.stdin.buffer.read()); sys.stderr.buffer.write(b'err\\n'); "
            "sys.exit(3)", **pipes)
    else:
        proc = await asyncio.create_subprocess_shell("cat; echo err >&2; exit 3", **pipes)
    out, err = await proc.communicate(b"hello")
    # Under qemu-user the children inherit the loader's LD_PRELOAD and say so on stderr.
    err = b"".join(l for l in err.splitlines(True) if not l.startswith(b"ERROR: ld.so:"))
    return out, err, proc.returncode

async def killed():
    proc = await asyncio.create_subprocess_exec(*SLEEP)
    proc.kill()
    return await proc.wait()

class Recorder(asyncio.SubprocessProtocol):
    def __init__(self):
        self.lost = asyncio.get_running_loop().create_future()
    def connection_lost(self, exc):
        self.lost.set_result(exc)

async def stdin_left_open():
    # The child exits with its stdin pipe still open on our side: the transport finishes
    # only when the write pipe sees the reader go away.
    loop = asyncio.get_running_loop()
    transport, proto = await loop.subprocess_exec(Recorder, *EXIT5, stdin=asyncio.subprocess.PIPE)
    await proto.lost
    code = transport.get_returncode()
    transport.close()
    return code

class Reader(asyncio.Protocol):
    def __init__(self):
        self.data, self.done = bytearray(), asyncio.get_running_loop().create_future()
    def data_received(self, data):
        self.data += data
    def connection_lost(self, exc):
        self.done.set_result(bytes(self.data))

class Writer(asyncio.BaseProtocol):
    def __init__(self):
        self.lost = asyncio.get_running_loop().create_future()
    def connection_lost(self, exc):
        self.lost.set_result(type(exc).__name__ if exc else None)

async def raw_pipes():
    loop = asyncio.get_running_loop()
    r, w = os.pipe()
    _, reader = await loop.connect_read_pipe(Reader, os.fdopen(r, "rb", 0))
    wt, _ = await loop.connect_write_pipe(Writer, os.fdopen(w, "wb", 0))
    payload = b"pipe" * 300000
    wt.write(payload)
    wt.close()
    return (await reader.done) == payload

async def reader_goes_away():
    loop = asyncio.get_running_loop()
    r, w = os.pipe()
    wt, writer = await loop.connect_write_pipe(Writer, os.fdopen(w, "wb", 0))
    os.close(r)
    return await writer.lost

SUBPROCESS = (cat_roundtrip, communicate, killed, stdin_left_open)
PIPES = (raw_pipes, reader_goes_away)

async def all_scenarios(scenarios=SUBPROCESS + PIPES):
    out = {}
    for f in scenarios:
        try:
            out[f.__name__] = await asyncio.wait_for(f(), TIMEOUT)
        except TimeoutError:
            out[f.__name__] = "timed out"
    return out

def run_on(factory, scenarios=SUBPROCESS + PIPES):
    return asyncio.run(all_scenarios(scenarios), loop_factory=factory)

if WINDOWS:
    stock = run_on(asyncio.new_event_loop, SUBPROCESS)
    assert stock == {
        "cat_roundtrip": (True, 0), "communicate": (b"hello", b"err\n", 3),
        "killed": 1,  # TerminateProcess(handle, 1)
        "stdin_left_open": 5,
    }, stock
    got = run_on(goaio.GoEventLoop)
    assert got == dict(stock, raw_pipes=True, reader_goes_away=None), got
else:
    stock = run_on(asyncio.new_event_loop)
    assert stock == {
        "cat_roundtrip": (True, 0), "communicate": (b"hello", b"err\n", 3),
        "killed": -signal.SIGKILL, "stdin_left_open": 5, "raw_pipes": True,
        "reader_goes_away": None,
    }, stock
    for name, factory in LOOPS.items():
        got = run_on(factory)
        assert got == stock, (name, got, stock)
`

// TestTransportAfterClose: calls asyncio allows after close() (StreamReader resumes
// reading once its buffer drains) are no-ops, for connections and pipes alike. They
// reached Go with an unregistered handle and panicked (nil connection).
func TestTransportAfterClose(t *testing.T) {
	run(t, `
import os

async def after_close():
    loop = asyncio.get_running_loop()
    server = await asyncio.start_server(lambda r, w: None, "127.0.0.1", 0)
    port = server.sockets[0].getsockname()[1]
    tcp, _ = await loop.create_connection(asyncio.Protocol, "127.0.0.1", port)
    r, w = os.pipe()
    rp, _ = await loop.connect_read_pipe(asyncio.Protocol, os.fdopen(r, "rb", 0))
    wp, _ = await loop.connect_write_pipe(asyncio.BaseProtocol, os.fdopen(w, "wb", 0))
    for tr in (tcp, rp, wp):
        tr.close()
        await asyncio.sleep(0.05)  # EV_CLOSED: the Go handle is gone
        tr.pause_reading()
        tr.resume_reading()
        tr.set_write_buffer_limits(1024)
        tr.write(b"late")
        tr.write_eof()
        tr.close()
        tr.get_extra_info("peername")
    server.close()

asyncio.run(after_close(), loop_factory=goaio.GoEventLoop)
`)
}

// TestSubprocessAndPipes: asyncio subprocesses and pipe transports on the Go-backed
// loops behave as on asyncio's own Unix loop.
func TestSubprocessAndPipes(t *testing.T) {
	run(t, subprocessScenarios)
}
