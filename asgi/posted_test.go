package asgi_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/moriyoshi/pythago/asgi"
	"github.com/moriyoshi/pythago/py"
)

// heldbody's /block holds the loop thread (a blocking sleep), so that posted requests
// wait unconverted; /record keeps the body it received, by id.
const heldbody = `
import time
seen = {}
async def app(scope, receive, send):
    if scope["type"] == "lifespan":
        while True:
            m = await receive()
            if m["type"] == "lifespan.startup":
                await send({"type": "lifespan.startup.complete"})
            else:
                await send({"type": "lifespan.shutdown.complete"})
                return
    if scope["path"] == "/block":
        time.sleep(0.4)
    else:
        m = await receive()
        seen[scope["query_string"].decode()] = m["body"]
    await send({"type": "http.response.start", "status": 200, "headers": []})
    await send({"type": "http.response.body", "body": b"ok"})
`

// A posted request's first body chunk lives in the request's memory until the loop
// thread has converted it, even when the request is over before that (its client
// gone): request B is posted while the loop thread is held, B's client goes away (its
// handler returns and drops its reference), and request C, which would take B's memory
// if B were recycled, reads its own body. B's application must still see B's body.
// GOMAXPROCS is 1, so that C takes from the pool what B would have put back.
func TestPostedBodyHeldUntilConverted(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	s, err := asgi.New("heldbody:app", asgi.Options{Workers: 1, LoopThread: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	ts := httptest.NewServer(s)
	defer ts.Close()
	blocked := make(chan struct{})
	go func() {
		resp, err := http.Get(ts.URL + "/block")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		close(blocked)
	}()
	time.Sleep(100 * time.Millisecond) // the loop thread is asleep in /block
	bodyB, bodyC := bytes.Repeat([]byte("B"), 1000), bytes.Repeat([]byte("C"), 1000)
	ctx, cancel := context.WithCancel(context.Background())
	reqB, _ := http.NewRequestWithContext(ctx, "POST", ts.URL+"/record?B", bytes.NewReader(bodyB))
	doneB := make(chan struct{})
	go func() {
		if resp, err := http.DefaultClient.Do(reqB); err == nil {
			resp.Body.Close()
		}
		close(doneB)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for asgi.Requests(s) < 2 && time.Now().Before(deadline) { // B registered (and posted)
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-doneB
	for asgi.Requests(s) > 1 && time.Now().Before(deadline) { // B's handler returned
		time.Sleep(time.Millisecond)
	}
	resp, err := http.Post(ts.URL+"/record?C", "application/octet-stream", bytes.NewReader(bodyC))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	<-blocked
	var got map[any]any
	asgi.WaitIdle(s, 5*time.Second)
	if err := py.Do(func() error {
		o, err := py.Eval("__import__('heldbody').seen")
		if err != nil {
			return err
		}
		defer o.Close()
		v, err := o.Go()
		got, _ = v.(map[any]any)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if b, ok := got["B"].([]byte); !ok || !bytes.Equal(b, bodyB) {
		t.Errorf("request B's application saw %s; want its own body (B's memory was reused while its post waited)", describe(got["B"]))
	}
	if b, ok := got["C"].([]byte); !ok || !bytes.Equal(b, bodyC) {
		t.Errorf("request C's application saw %s", describe(got["C"]))
	}
}

func describe(v any) string {
	if b, ok := v.([]byte); ok && len(b) > 0 {
		return fmt.Sprintf("%d bytes starting %q", len(b), b[:1])
	}
	return fmt.Sprintf("%v", v)
}

// Posted uploads (LoopThread) of 16 and 64 KiB, declared or not, read their chunks into
// the request's memory (first chunk: the bytes lane; later ones: the receive buffer):
// the body costs no Go allocation. It cost about 35 and 160 KB per request read into
// heap memory.
func TestLoopThreadUploadNotAllocated(t *testing.T) {
	for _, size := range []int{16 << 10, 64 << 10, 200 << 10} {
		per := uploadBytes(t, true, size)
		t.Logf("Go bytes allocated per posted %d B upload: %d", size, per)
		if per > 8<<10 {
			t.Errorf("a posted %d-byte upload allocates %d bytes; its chunks are not read into the request's memory", size, per)
		}
	}
}
