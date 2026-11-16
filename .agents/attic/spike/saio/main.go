// Command saio benchmarks the Go-driven asyncio loops from .agents/attic/spike/goaio against
// asyncio's default epoll loop with an asyncio-streams echo workload.
//
//	PYTHONHOME=<pbs install> saio [connections] [bytes]
package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"

	"github.com/moriyoshi/pythago/internal/spike/goaio"
	"github.com/moriyoshi/pythago/internal/spike/pyapi"
	"github.com/moriyoshi/pythago/internal/spike/s1/pyembed"
)

func init() { runtime.LockOSThread() }

func main() {
	var (
		initialize func(int32)
		run        func(string) int32
		finalize   func() int32
	)
	pyapi.Load(pyembed.SymbolAddr0())
	pyapi.Register(&initialize, "Py_InitializeEx")
	pyapi.Register(&run, "PyRun_SimpleString")
	pyapi.Register(&finalize, "Py_FinalizeEx")
	conns, size := 1000, 65536
	if len(os.Args) > 1 {
		conns, _ = strconv.Atoi(os.Args[1])
	}
	if len(os.Args) > 2 {
		size, _ = strconv.Atoi(os.Args[2])
	}

	initialize(0)
	goaio.Install()
	rc := run(fmt.Sprintf(`
import asyncio, threading, time, goaio

async def handle(reader, writer):
    while data := await reader.read(65536):
        writer.write(data)
        await writer.drain()
    writer.close()
    await writer.wait_closed()

async def client(port, payload):
    reader, writer = await asyncio.open_connection("localhost", port)   # name resolution included
    writer.write(payload)
    await writer.drain()
    got = await reader.readexactly(len(payload))
    assert got == payload
    writer.write_eof()
    assert await reader.read() == b""
    writer.close()
    await writer.wait_closed()

async def bench(n, size):
    server = await asyncio.start_server(handle, "127.0.0.1", 0, backlog=4096)
    port = server.sockets[0].getsockname()[1]
    payload = bytes(range(256)) * (size // 256)
    async with server:
        t0 = time.perf_counter()
        await asyncio.gather(*(client(port, payload) for _ in range(n)))
        dt = time.perf_counter() - t0
        loop = asyncio.get_running_loop()
        fut = loop.create_future()
        threading.Timer(0.01, lambda: loop.call_soon_threadsafe(fut.set_result, "woken")).start()
        t1 = time.perf_counter()
        await asyncio.sleep(0.05)
        assert await fut == "woken"
        slept = time.perf_counter() - t1
    return dt, slept, threading.active_count()

n, size = %d, %d
for name, factory in (("GoEventLoop", goaio.GoEventLoop),
                      ("GoSelector", goaio.GoSelectorEventLoop),
                      ("default epoll loop", None)):
    kw = {"loop_factory": factory} if factory else {}
    dt, slept, nthreads = asyncio.run(bench(n, size), **kw)
    mb = 2 * n * size / dt / 1e6
    print(f"{name:20s}: {n} connections x {size} B echo in {dt*1000:.0f} ms ({mb:.0f} MB/s); "
          f"sleep(0.05) took {slept*1000:.0f} ms; Python threads {nthreads}")
`, conns, size))
	if finalize() != 0 {
		fmt.Fprintln(os.Stderr, "Py_FinalizeEx failed")
	}
	os.Exit(int(rc))
}
