package aio_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/moriyoshi/pythago/py"
)

func TestChannelStringPreservesUnicodeIdentity(t *testing.T) {
	input, output := make(chan *py.String), make(chan *py.String)
	stop, done := make(chan struct{}), make(chan struct{})
	defer close(stop)
	go func() {
		defer close(done)
		for range 4 {
			var value *py.String
			select {
			case value = <-input:
			case <-stop:
				return
			}
			select {
			case output <- value:
			case <-stop:
				_ = value.Close()
				return
			}
		}
	}()
	runChannels(t, map[string]any{"input": input, "output": output}, `
class Text(str):
    def __str__(self):
        raise AssertionError("text was coerced")
async def main():
    for original in ("plain", "日本語\x00🤖" * 1024, "\ud800", Text("subclass")):
        await host.input.send(original)
        returned = await host.output.recv()
        assert returned is original
        assert type(returned) is type(original)
goaio.run(asyncio.wait_for(main(), 5))
`)
	<-done
}

func TestChannelStringConstructorAndClone(t *testing.T) {
	text := strings.Repeat("Go → Python 日本語\x00", 100)
	value, err := py.NewString(text)
	if err != nil {
		t.Fatal(err)
	}
	defer value.Close()
	copy, err := value.GoString()
	if err != nil || copy != text {
		t.Fatalf("GoString: %q, %v", copy, err)
	}
	clone, err := value.Clone()
	if err != nil {
		t.Fatal(err)
	}
	defer clone.Close()
	ch := make(chan *py.String, 2)
	ch <- value
	ch <- clone
	runChannels(t, map[string]any{"ch": ch, "expected": text}, `
async def main():
    first = await host.ch.recv()
    second = await host.ch.recv()
    assert first is second
    assert first == host.expected
goaio.run(main())
`)
	if _, err := value.GoString(); err == nil {
		t.Fatal("consumed string stayed open")
	}
	if _, err := clone.Clone(); err == nil {
		t.Fatal("consumed string was cloned")
	}
	if err := value.Close(); err != nil {
		t.Fatal(err)
	}
	if copy != text {
		t.Fatal("Go copy did not outlive the lease")
	}
	if _, err := py.NewString("\xff"); err == nil {
		t.Fatal("invalid Go UTF-8 was accepted")
	}
}

func TestChannelStringSurrogateConversion(t *testing.T) {
	input, output := make(chan *py.String), make(chan *py.String)
	done := make(chan error, 1)
	go func() {
		value := <-input
		_, err := value.GoString()
		if err == nil {
			err = fmt.Errorf("unpaired surrogate was encoded as UTF-8")
		} else {
			err = nil
		}
		output <- value
		done <- err
	}()
	runChannels(t, map[string]any{"input": input, "output": output}, `
async def main():
    original = "surrogate\ud800"
    await host.input.send(original)
    assert await host.output.recv() is original
goaio.run(asyncio.wait_for(main(), 5))
`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestChannelStringTypeAndNil(t *testing.T) {
	ch := make(chan *py.String, 1)
	var blocked chan *py.String
	runChannels(t, map[string]any{"ch": ch, "blocked": blocked}, `
async def main():
    for invalid in (b"bytes", 12, object()):
        try:
            await host.ch.send(invalid)
        except RuntimeError:
            pass
        else:
            raise AssertionError("non-str accepted")
    await host.ch.send(None)
    assert await host.ch.recv_ok() == (None, True)
    for _ in range(20):
        try:
            await asyncio.wait_for(host.blocked.send("cancel me"), 0.001)
        except TimeoutError:
            pass
    assert not asyncio.get_running_loop()._channel_ops
    host.ch.close()
    assert await host.ch.recv_ok() == (None, False)
    try:
        await host.ch.send("closed")
    except RuntimeError:
        pass
    else:
        raise AssertionError("closed send succeeded")
goaio.run(asyncio.wait_for(main(), 5))
`)
	var zero py.String
	if _, err := zero.GoString(); err == nil {
		t.Fatal("uninitialized String converted")
	}
	if err := zero.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestChannelStringNativeCodePoints(t *testing.T) {
	input, output := make(chan *py.String), make(chan *py.String)
	done := make(chan error, 1)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		value := <-input
		defer value.Close()
		want := []rune{'A', 'é', 0, '🤖', 0xd800, 'e', 0x301}
		var failure error
		n, err := value.Len()
		if err != nil || n != len(want) {
			failure = fmt.Errorf("native Len = %d, %v", n, err)
		}
		all, err := value.Runes()
		if err != nil || !slices.Equal(all, want) {
			failure = fmt.Errorf("native Runes = %v, %v", all, err)
		}
		for i, expected := range want {
			r, err := value.Rune(i)
			if err != nil || r != expected {
				failure = fmt.Errorf("native Rune(%d) = %U, %v", i, r, err)
			}
		}
		part, err := value.Slice(2, 5)
		if err != nil {
			failure = err
		}
		value.Close() // the substring must independently own its Unicode storage
		select {
		case output <- part:
		case <-stop:
			part.Close()
		}
		done <- failure
	}()
	runChannels(t, map[string]any{"input": input, "output": output}, `
class Native(str):
    def __len__(self):
        return 999
    def __getitem__(self, key):
        raise AssertionError("Python __getitem__ override invoked")
async def main():
    await host.input.send(Native("Aé\x00🤖\ud800e\u0301"))
    assert await host.output.recv() == "\x00🤖\ud800"
goaio.run(asyncio.wait_for(main(), 5))
`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
