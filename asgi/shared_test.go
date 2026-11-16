package asgi_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/moriyoshi/pythago/asgi"
	"github.com/moriyoshi/pythago/py"
)

// sharedapp reports the scope values every request of a server shares, and which worker
// it ran on (the lifespan startup numbers the workers through their state).
const sharedapp = `
import itertools, sys

_workers = itertools.count()

def _immortal(v):
    f = getattr(sys, "_is_immortal", None)  # 3.14+
    return None if f is None else f(v)

async def app(scope, receive, send):
    if scope["type"] == "lifespan":
        while True:
            m = await receive()
            if m["type"] == "lifespan.startup":
                scope["state"]["worker"] = next(_workers)
                await send({"type": "lifespan.startup.complete"})
            else:
                await send({"type": "lifespan.shutdown.complete"})
                return
    shared = {"method": scope["method"], "http_version": scope["http_version"],
              "scheme": scope["scheme"], "server_host": scope["server"][0],
              "root_path": scope["root_path"]}
    asgi = scope["asgi"]
    out = {"worker": scope["state"]["worker"],
           "values": shared,
           "immortal": {k: _immortal(v) for k, v in shared.items()},
           "port": scope["server"][1], "port_id": id(scope["server"][1]),
           "asgi_id": id(asgi), "asgi": dict(asgi)}
    if scope["path"] == "/mutate":
        asgi["mutated_by"] = out["worker"]
    body = __import__("json").dumps(out).encode()
    await send({"type": "http.response.start", "status": 200, "headers": []})
    await send({"type": "http.response.body", "body": body})
`

type sharedReport struct {
	Worker   int               `json:"worker"`
	Values   map[string]string `json:"values"`
	Immortal map[string]*bool  `json:"immortal"`
	Port     int               `json:"port"`
	PortID   int64             `json:"port_id"`
	AsgiID   int64             `json:"asgi_id"`
	Asgi     map[string]any    `json:"asgi"`
}

// TestSharedScopeValues checks what the requests of different workers share: on
// free-threaded builds every shared scope value must be immortal (interned strings), or
// each touch is an atomic reference count update contended by every worker; the server
// port, which cannot be made immortal, and the asgi dict are each worker's own. On GIL
// builds the values are unchanged.
func TestSharedScopeValues(t *testing.T) {
	for _, loopThread := range []bool{false, true} {
		t.Run("LoopThread="+strconv.FormatBool(loopThread), func(t *testing.T) {
			testSharedScopeValues(t, loopThread)
		})
	}
}

func testSharedScopeValues(t *testing.T, loopThread bool) {
	s, err := asgi.New("sharedapp:app", asgi.Options{Workers: 2, RootPath: "/mounted", LoopThread: loopThread})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	defer s.Close(context.Background())
	defer ts.Close()
	_, portStr, _ := net.SplitHostPort(ts.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	get := func(path string) sharedReport {
		resp, err := ts.Client().Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var r sharedReport
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatalf("%s: %v: %s", path, err, b)
		}
		return r
	}
	want := map[string]string{"method": "GET", "http_version": "1.1", "scheme": "http",
		"server_host": "127.0.0.1", "root_path": "/mounted"}
	byWorker := map[int][]sharedReport{}
	for range 8 { // round-robin: both workers
		r := get("/")
		byWorker[r.Worker] = append(byWorker[r.Worker], r)
		for k, v := range want {
			if r.Values[k] != v {
				t.Errorf("worker %d: scope %s is %q, want %q", r.Worker, k, r.Values[k], v)
			}
		}
		if r.Port != port {
			t.Errorf("worker %d: server port %d, want %d", r.Worker, r.Port, port)
		}
		if r.Asgi["version"] != "3.0" || r.Asgi["spec_version"] != "2.4" || len(r.Asgi) != 2 {
			t.Errorf("worker %d: scope asgi %v", r.Worker, r.Asgi)
		}
		for k, imm := range r.Immortal {
			switch {
			case !py.FreeThreaded():
			case imm == nil:
				t.Errorf("%s: sys._is_immortal is missing on a free-threaded build", k)
			case k == "server_host" && loopThread:
				// Posted requests carry a server tuple of their own, made on the loop
				// thread: not shared.
			case !*imm:
				t.Errorf("worker %d: scope %s (%q) is not immortal: shared by every worker's requests",
					r.Worker, k, r.Values[k])
			}
		}
	}
	if len(byWorker) != 2 {
		t.Fatalf("requests ran on workers %v, want 2 workers", byWorker)
	}
	var firsts []sharedReport // a request of each worker (numbered across servers)
	for _, rs := range byWorker {
		firsts = append(firsts, rs[0])
	}
	r0, r1 := firsts[0], firsts[1]
	if r0.AsgiID == r1.AsgiID {
		t.Errorf("both workers' requests share one scope asgi dict (id %d)", r0.AsgiID)
	}
	if !loopThread {
		for w, rs := range byWorker {
			for _, r := range rs {
				if r.PortID != rs[0].PortID {
					t.Errorf("worker %d: server port objects %d and %d, want the worker's one", w, rs[0].PortID, r.PortID)
				}
			}
		}
		if r0.PortID == r1.PortID {
			t.Errorf("both workers' requests share one server port object (id %d)", r0.PortID)
		}
	}

	// A request that mutates its scope's asgi dict changes only its own worker's.
	m := get("/mutate")
	for range 4 {
		r := get("/")
		_, seen := r.Asgi["mutated_by"]
		if r.Worker != m.Worker && seen {
			t.Errorf("worker %d sees worker %d's change to scope asgi: %v", r.Worker, m.Worker, r.Asgi)
		}
	}
}
