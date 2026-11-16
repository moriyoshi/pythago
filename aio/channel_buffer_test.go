package aio_test

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/moriyoshi/pythago/py"
)

func TestChannelBufferGoMemory(t *testing.T) {
	data := []byte{1, 2, 3, 4}
	lease := py.NewOwnedBuffer(data, false)
	ch := make(chan *py.OwnedBuffer, 1)
	ch <- lease
	runChannels(t, map[string]any{
		"ch":   ch,
		"gc":   py.Func(func([]*py.Object, py.Kwargs) (any, error) { runtime.GC(); return nil, nil }),
		"pins": py.Func(func([]*py.Object, py.Kwargs) (any, error) { return py.ReadStats().GoBuffers, nil }),
	}, `
async def main():
    before = host.pins()
    view = await host.ch.recv()
    assert isinstance(view, memoryview)
    assert view.tolist() == [1, 2, 3, 4]
    assert not view.readonly
    assert host.pins() == before + 1
    host.gc()
    view[0] = 42
    assert view[0] == 42
    view.release()
    assert host.pins() == before
    host.ch.close()
    assert await host.ch.recv_ok() == (None, False)
goaio.run(main())
`)
	if data[0] != 42 {
		t.Fatalf("buffer was copied: %v", data)
	}
	if _, err := lease.Bytes(); err == nil {
		t.Fatal("transferred lease is still open")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestChannelBufferPythonRoundTrip(t *testing.T) {
	requests, replies := make(chan *py.OwnedBuffer), make(chan *py.OwnedBuffer)
	stop, done := make(chan struct{}), make(chan error, 1)
	defer close(stop)
	go func() {
		var lease *py.OwnedBuffer
		select {
		case lease = <-requests:
		case <-stop:
			return
		}
		data, err := lease.Bytes()
		if err != nil {
			done <- err
			return
		}
		data[0] = 99
		runtime.GC()
		select {
		case replies <- lease:
		case <-stop:
			_ = lease.Close()
			return
		}
		done <- nil
	}()
	runChannels(t, map[string]any{"requests": requests, "replies": replies}, `
async def main():
    original = bytearray(b"abcd")
    await host.requests.send(original)
    view = await host.replies.recv()
    assert view[0] == 99 and original[0] == 99
    try:
        original.append(0)
    except BufferError:
        pass
    else:
        raise AssertionError("export was not retained")
    view.release()
    original.append(0)  # both the Go lease and returned Python view released
    assert len(original) == 5
goaio.run(asyncio.wait_for(main(), 5))
`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestChannelBufferCloseFromGoroutine(t *testing.T) {
	ch := make(chan *py.OwnedBuffer)
	done := make(chan error, 1)
	go func() {
		lease := <-ch
		if !lease.ReadOnly() {
			done <- fmt.Errorf("immutable bytes were exported writable")
			return
		}
		done <- lease.Close()
	}()
	runChannels(t, map[string]any{"ch": ch}, `
async def main():
    await host.ch.send(b"readonly")
goaio.run(asyncio.wait_for(main(), 5))
`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestChannelBufferCancelledExport(t *testing.T) {
	var nilChannel chan *py.OwnedBuffer
	closed := make(chan *py.OwnedBuffer)
	close(closed)
	runChannels(t, map[string]any{"nil": nilChannel, "closed": closed}, `
async def main():
    data = bytearray(b"exported")
    pending = asyncio.create_task(getattr(host, "nil").send(data))
    await asyncio.sleep(0)
    try:
        data.append(0)
    except BufferError:
        pass
    else:
        raise AssertionError("pending send did not hold an export")
    pending.cancel()
    try:
        await pending
    except asyncio.CancelledError:
        pass
    data.append(0)
    try:
        await host.closed.send(data)
    except RuntimeError:
        pass
    else:
        raise AssertionError("send to closed channel succeeded")
    data.append(0)
    try:
        await getattr(host, "nil").send(memoryview(data)[::2])
    except RuntimeError:
        pass
    else:
        raise AssertionError("noncontiguous buffer accepted")
    data.append(0)
goaio.run(asyncio.wait_for(main(), 5))
`)
}

func TestChannelBufferKeepsPythonExporterAlive(t *testing.T) {
	ch := make(chan *py.OwnedBuffer)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		lease := <-ch
		defer lease.Close()
		<-release
		data, err := lease.Bytes()
		if err == nil && string(data) != "kept alive" {
			err = fmt.Errorf("exporter lost: %q", data)
		}
		if closeErr := lease.Close(); err == nil {
			err = closeErr
		}
		done <- err
	}()
	runChannels(t, map[string]any{
		"ch":      ch,
		"release": py.Func(func([]*py.Object, py.Kwargs) (any, error) { close(release); return nil, nil }),
	}, `
import gc
async def main():
    data = bytearray(b"kept alive")
    await host.ch.send(data)
    del data
    gc.collect()
    host.release()
goaio.run(asyncio.wait_for(main(), 5))
`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestChannelBufferLoopShutdown(t *testing.T) {
	var ch chan *py.OwnedBuffer
	runChannels(t, map[string]any{"ch": ch}, `
data = bytearray(b"shutdown")
pending = None
async def setup():
    global pending
    # Suspend a coroutine without creating a Task, so loop.close can be tested
    # without deliberately leaving a destroyed-pending-Task warning.
    pending = host.ch.send(data)
    pending.send(None)
    assert asyncio.get_running_loop()._channel_ops
loop = goaio.GoEventLoop()
loop.run_until_complete(setup())
loop.close()
pending.close()
data.append(0)
assert not loop._channel_ops
`)
}

func TestChannelBufferNilAndFutureFailure(t *testing.T) {
	var nilChannel chan *py.OwnedBuffer
	runChannels(t, map[string]any{"ready": make(chan *py.OwnedBuffer, 1), "blocked": nilChannel}, `
async def main():
    await host.ready.send(None)
    assert await host.ready.recv_ok() == (None, True)
    host.ready.close()
    assert await host.ready.recv_ok() == (None, False)
    data = bytearray(b"failure")
    loop = asyncio.get_running_loop()
    original = loop.create_future
    def fail():
        raise ValueError("future allocation failed")
    loop.create_future = fail
    try:
        try:
            await host.blocked.send(data)
        except ValueError:
            pass
        else:
            raise AssertionError("failure was swallowed")
    finally:
        loop.create_future = original
    data.append(0)
    assert not loop._channel_ops
goaio.run(main())
`)
}

func TestChannelBufferTypedView(t *testing.T) {
	input, output := make(chan *py.OwnedBuffer, 1), make(chan *py.OwnedBuffer, 1)
	done := make(chan struct{})
	go func() { output <- <-input; close(done) }()
	runChannels(t, map[string]any{"input": input, "output": output}, `
import array
async def main():
    original = array.array("I", [0x01020304])
    await host.input.send(original)
    view = await host.output.recv()
    assert view.format == "B" and view.ndim == 1
    assert view.tobytes() == b"\x04\x03\x02\x01"
    view.release()
    original.append(5)
goaio.run(asyncio.wait_for(main(), 5))
`)
	<-done
}

func TestChannelBufferReadOnlyView(t *testing.T) {
	ch := make(chan *py.OwnedBuffer, 1)
	ch <- py.NewOwnedBuffer([]byte{1, 2}, true)
	runChannels(t, map[string]any{"ch": ch}, `
async def main():
    view = await host.ch.recv()
    assert view.readonly
    try:
        view[0] = 9
    except TypeError:
        pass
    else:
        raise AssertionError("read-only lease accepted a write")
    view.release()
goaio.run(main())
`)
}
