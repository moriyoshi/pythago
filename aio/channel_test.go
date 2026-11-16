package aio_test

import (
	"fmt"
	"testing"

	"github.com/moriyoshi/pythago/py"
)

func runChannels(t *testing.T, attrs map[string]any, script string) {
	t.Helper()
	if err := py.Do(func() error {
		m, err := py.NewModule("_channel_test", attrs)
		if err != nil {
			return err
		}
		defer m.Close()
		return py.Exec(common + "\nimport _channel_test as host\n" + script)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestChannelRendezvous(t *testing.T) {
	requests, replies := make(chan int), make(chan int)
	stop := make(chan struct{})
	defer close(stop)
	done := make(chan error, 1)
	go func() {
		for i := range 100 {
			select {
			case requests <- i:
			case <-stop:
				return
			}
			select {
			case n := <-replies:
				if n != i*2 {
					done <- fmt.Errorf("reply = %d, want %d", n, i*2)
					return
				}
			case <-stop:
				return
			}
		}
		close(requests)
		done <- nil
	}()
	runChannels(t, map[string]any{"requests": requests, "replies": replies}, `
async def main():
    count = 0
    async for n in host.requests:
        assert n == count
        await host.replies.send(n * 2)
        count += 1
    assert count == 100
    assert await host.requests.recv_ok() == (0, False)
goaio.run(asyncio.wait_for(main(), 10))
`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestChannelBuffering(t *testing.T) {
	ch := make(chan int, 2)
	ch <- 1
	ch <- 2
	runChannels(t, map[string]any{"ch": ch}, `
async def main():
    assert await host.ch.recv() == 1
    await host.ch.send(3)
    pending = asyncio.create_task(host.ch.send(4))
    await asyncio.sleep(0.02)
    assert not pending.done(), "send to full channel completed"
    assert await host.ch.recv() == 2
    await pending
    host.ch.close()
    assert [n async for n in host.ch] == [3, 4]
    assert await host.ch.recv_ok() == (0, False)
    try:
        await host.ch.send(5)
    except RuntimeError as e:
        assert "closed channel" in str(e)
    else:
        raise AssertionError("send on closed channel succeeded")
goaio.run(asyncio.wait_for(main(), 5))
`)
	if _, ok := <-ch; ok {
		t.Fatal("Python did not close the Go channel")
	}
}

func TestChannelCancellation(t *testing.T) {
	var nilCh chan int
	ch := make(chan int)
	runChannels(t, map[string]any{"nil_ch": nilCh, "ch": ch}, `
async def main():
    for operation in (host.nil_ch.recv(), host.nil_ch.send(1),
                      host.ch.recv(), host.ch.send(1)):
        try:
            await asyncio.wait_for(operation, 0.02)
        except TimeoutError:
            pass
        else:
            raise AssertionError("unmatched operation completed")
    loop = asyncio.get_running_loop()
    assert not loop._channel_ops
    sender = asyncio.create_task(host.ch.send(42))
    assert await host.ch.recv() == 42
    await sender
    # Concurrent waiters on one channel, with no executor threads.
    senders = [asyncio.create_task(host.ch.send(n)) for n in range(50)]
    assert sorted([await host.ch.recv() for _ in senders]) == list(range(50))
    await asyncio.gather(*senders)
goaio.run(asyncio.wait_for(main(), 5))
`)
	select {
	case v := <-ch:
		t.Fatalf("cancelled sender transferred %d", v)
	default:
	}
}

func TestChannelDirectionAndConversion(t *testing.T) {
	read := make(chan int, 1)
	read <- 7
	write := make(chan int, 1)
	ints := make(chan []int, 1)
	small := make(chan int8, 1)
	unsigned := make(chan uint64, 1)
	bad := make(chan any, 1)
	bad <- struct{}{}
	runChannels(t, map[string]any{
		"read": (<-chan int)(read), "write": (chan<- int)(write),
		"ints": ints, "small": small, "unsigned": unsigned, "bad": bad,
	}, `
async def fails(operation):
    try:
        await operation
    except RuntimeError:
        return
    raise AssertionError("invalid operation succeeded")

async def main():
    assert await host.read.recv() == 7
    await host.write.send(9)
    await fails(host.read.send(1))
    await fails(host.write.recv())
    try:
        host.read.close()
    except RuntimeError:
        pass
    else:
        raise AssertionError("closed receive-only channel")
    await host.ints.send([1, 2, 3])
    await fails(host.small.send(128))
    await fails(host.small.send(-129))
    await fails(host.small.send("wrong"))
    await fails(host.small.send(object()))
    await fails(host.ints.send([object()]))
    await host.small.send(-128)
    await host.unsigned.send(2**64 - 1)
    await fails(host.bad.recv())
    await asyncio.sleep(0)  # conversion failure leaves the event loop usable
goaio.run(asyncio.wait_for(main(), 5))
`)
	if n := <-write; n != 9 {
		t.Fatalf("write = %d", n)
	}
	if v := <-ints; fmt.Sprint(v) != "[1 2 3]" {
		t.Fatalf("ints = %v", v)
	}
	if n := <-small; n != -128 {
		t.Fatalf("small = %d", n)
	}
	if n := <-unsigned; n != ^uint64(0) {
		t.Fatalf("unsigned = %d", n)
	}
}

func TestChannelGoCloseWakesPython(t *testing.T) {
	ch := make(chan string)
	runChannels(t, map[string]any{
		"ch": ch,
		"close": py.Func(func([]*py.Object, py.Kwargs) (any, error) {
			close(ch)
			return nil, nil
		}),
	}, `
async def main():
    pending = asyncio.create_task(host.ch.recv_ok())
    await asyncio.sleep(0.02)
    assert not pending.done()
    host.close()
    assert await pending == ("", False)
goaio.run(asyncio.wait_for(main(), 5))
`)
}

func TestChannelInConfig(t *testing.T) {
	run(t, `
import _channel_config_test
async def main():
    await _channel_config_test.ch.send(17)
    assert await _channel_config_test.ch == 17
goaio.run(asyncio.wait_for(main(), 5))
`)
}

type channelPacket struct{ Number int64 }

func decodeChannelPacket(v any) (channelPacket, error) {
	if fields, ok := v.(map[any]any); ok {
		if n, ok := fields["number"].(int64); ok {
			return channelPacket{Number: n}, nil
		}
	}
	return channelPacket{}, fmt.Errorf("expected a dictionary with an integer number")
}

func encodeChannelPacket(v channelPacket) any {
	return map[string]any{"number": v.Number}
}

func TestChannelTypeVariants(t *testing.T) {
	runChannels(t, map[string]any{
		"bytes":   make(chan []byte, 1),
		"mapping": make(chan map[string]any, 1),
		"float":   make(chan float32, 1),
		"bool":    make(chan bool, 1),
		"any":     make(chan any, 1),
		"strings": make(chan []string, 1),
	}, `
async def main():
    await host.bytes.send(bytes([0, 255]))
    assert await host.bytes == bytes([0, 255])
    await host.bytes.send([0, 255])
    assert await host.bytes == bytes([0, 255])
    await host.mapping.send({"answer": 42, "nested": [None, True]})
    assert await host.mapping == {"answer": 42, "nested": [None, True]}
    await host.float.send(1.5)
    assert await host.float == 1.5
    await host.bool.send(True)
    assert await host.bool is True
    await host.any.send(None)
    assert await host.any.recv_ok() == (None, True)
    await host.strings.send(["one", "two"])
    assert await host.strings == ["one", "two"]
    try:
        await host.bytes.send([256])
    except RuntimeError:
        pass
    else:
        raise AssertionError("byte overflow accepted")
goaio.run(asyncio.wait_for(main(), 5))
`)
}

func TestChannelRegisteredType(t *testing.T) {
	runChannels(t, map[string]any{"custom": make(chan channelPacket, 1)}, `
async def main():
    channel = host.custom
    await channel.send({"number": 23})
    assert await channel == {"number": 23}
    channel.close()
    assert await channel.recv_ok() == ({"number": 0}, False)
goaio.run(asyncio.wait_for(main(), 5))
`)
}

func TestChannelReadyAvoidsFuture(t *testing.T) {
	runChannels(t, map[string]any{"ch": make(chan any, 1)}, `
import _goaio
async def main():
    loop = asyncio.get_running_loop()
    create_future = loop.create_future
    cancel = _goaio.channel_cancel
    def forbidden(*args):
        raise AssertionError("ready channel allocated a Future or cancelled an operation")
    loop.create_future = forbidden
    _goaio.channel_cancel = forbidden
    initial_token = loop._next_token
    try:
        for value in (None, 42, b"bytes", "text"):
            await host.ch.send(value)
            assert await host.ch.recv_ok() == (value, True)
        host.ch.close()
        assert await host.ch.recv_ok() == (None, False)
        assert not loop._channel_ops
        assert loop._next_token == initial_token
    finally:
        loop.create_future = create_future
        _goaio.channel_cancel = cancel
goaio.run(main())
`)
}

func TestChannelSuccessAvoidsCancelCallback(t *testing.T) {
	ch := make(chan int, 1)
	runChannels(t, map[string]any{
		"ch":   ch,
		"give": py.Func(func([]*py.Object, py.Kwargs) (any, error) { ch <- 42; return nil, nil }),
	}, `
import _goaio
async def main():
    original = _goaio.channel_cancel
    calls = []
    def cancel(operation):
        calls.append(operation)
        return original(operation)
    _goaio.channel_cancel = cancel
    try:
        receive = asyncio.create_task(host.ch.recv())
        await asyncio.sleep(0)
        assert asyncio.get_running_loop()._channel_ops
        host.give()
        assert await receive == 42
        assert not calls, calls
        receive = asyncio.create_task(host.ch.recv())
        await asyncio.sleep(0)
        receive.cancel()
        try:
            await receive
        except asyncio.CancelledError:
            pass
        assert len(calls) == 1, calls
        assert not asyncio.get_running_loop()._channel_ops
    finally:
        _goaio.channel_cancel = original
goaio.run(asyncio.wait_for(main(), 5))
`)
}

func TestChannelMatchCancellationRace(t *testing.T) {
	runChannels(t, map[string]any{"ch": make(chan int)}, `
async def main():
    for i in range(200):
        send = asyncio.create_task(host.ch.send(i))
        recv = asyncio.create_task(host.ch.recv())
        await asyncio.sleep(0)
        if i % 2:
            send.cancel()
        else:
            recv.cancel()
        # Native transfer may have committed; both outcomes are allowed.
        await asyncio.sleep(0)
        send.cancel()
        recv.cancel()
        await asyncio.gather(send, recv, return_exceptions=True)
        assert not asyncio.get_running_loop()._channel_ops
    send = asyncio.create_task(host.ch.send(999))
    assert await host.ch.recv() == 999
    await send
goaio.run(asyncio.wait_for(main(), 10))
`)
}
