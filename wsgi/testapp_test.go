package wsgi_test

// testapp is a framework-free WSGI application covering PEP 3333's corners.
const testapp = `
import hashlib, io, json, os, sys, threading, time, warnings
from urllib.parse import parse_qsl
import wsgiref.validate
from pythago_wsgi import ClientDisconnected

closed = []  # names of results whose close() ran
late_results = []  # what /late's threads' writes did ("gone <id>", "sent <id>")
late_gate = threading.Event()

def take_late():
    """Removes and returns the late results so far (threads may append meanwhile)."""
    n = len(late_results)
    out = late_results[:n]
    del late_results[:n]
    return out
gate = threading.Event()
import contextvars
current_user = contextvars.ContextVar("current_user", default="nobody")

def sha(b):
    return hashlib.sha256(b).hexdigest()

def read_all(f, environ):
    """read() with a size, as wsgiref.validate insists, and not past CONTENT_LENGTH
    unless the server terminates the input (wsgiref's is the socket)."""
    left = None if environ.get("wsgi.input_terminated") else int(environ.get("CONTENT_LENGTH") or 0)
    parts = []
    while left is None or left > 0:
        b = f.read(65536 if left is None else min(left, 65536))
        if not b:
            break
        parts.append(b)
        if left is not None:
            left -= len(b)
    return b"".join(parts)

T = [("Content-Type", "text/plain")]

def env_view(environ):
    out = {k: v for k, v in environ.items() if type(v) is str}
    for k in ("wsgi.version", "wsgi.multithread", "wsgi.multiprocess", "wsgi.run_once", "wsgi.input_terminated"):
        out[k] = environ.get(k)
    out["wsgi.version"] = list(out["wsgi.version"])
    return out

class Closing:
    """A result that records close() and can fail after some chunks."""
    def __init__(self, name, chunks, fail_at=None, delay=0):
        self.name, self.chunks, self.fail_at, self.delay = name, chunks, fail_at, delay
    def __iter__(self):
        for i, c in enumerate(self.chunks):
            if i == self.fail_at:
                raise ValueError("boom after start")
            if self.delay:
                time.sleep(self.delay)
            yield c
    def close(self):
        closed.append(self.name)

def pattern(seed, i, n):
    """n bytes of chunk i of a /held response: sha256("seed-i") repeated."""
    unit = hashlib.sha256(b"%d-%d" % (seed, i)).digest()
    return (unit * (n // 32 + 1))[:n]

SHARED = bytes(range(256)) * 300  # 76,800 bytes

def forever():
    chunk = b"x" * 65536
    while True:
        yield chunk

# Malformed responses: each must give a 500 with nothing injected.
BAD = {
    "name-space": ("200 OK", [("Bad Name", "v")]),
    "name-colon": ("200 OK", [("X-A:", "v")]),
    "value-crlf": ("200 OK", [("X-A", "v\r\nSet-Cookie: evil=1")]),
    "value-lf": ("200 OK", [("X-A", "v\nSet-Cookie: evil=1")]),
    "value-nul": ("200 OK", [("X-A", "1\0Set-Cookie\0evil=1")]),
    "value-del": ("200 OK", [("X-A", "v\x7f")]),
    "non-latin1": ("200 OK", [("X-A", "€")]),
    "bytes-value": ("200 OK", [("X-A", b"v")]),
    "list-item": ("200 OK", [["X-A", "v"]]),
    "shifted-pairs": ("200 OK", [("X-A", "v", "Set-Cookie"), ("evil=1",)]),
    "tuple-headers": ("200 OK", (("X-A", "v"),)),
    "hop-connection": ("200 OK", [("Connection", "close")]),
    "hop-te": ("200 OK", [("Transfer-Encoding", "chunked")]),
    "hop-upgrade": ("200 OK", [("Upgrade", "websocket")]),
    "hop-keepalive": ("200 OK", [("Keep-Alive", "timeout=5")]),
    "cl-twice": ("200 OK", [("Content-Length", "2"), ("Content-Length", "2")]),
    "cl-bad": ("200 OK", [("Content-Length", "+2")]),
    "cl-space": ("200 OK", [("Content-Length", "2 ")]),
    "status-short": ("200", []),
    "status-nospace": ("200XOK", []),
    "status-digits": ("2x0 OK", []),
    "status-99": ("099 Low", []),
    "status-101": ("101 Switching Protocols", []),
    "status-600": ("600 High", []),
    "status-crlf": ("200 OK\r\nSet-Cookie: evil=1", []),
    "status-bytes": (b"200 OK", []),
}

def app(environ, start_response):
    path = environ["PATH_INFO"]
    q = dict(parse_qsl(environ["QUERY_STRING"]))
    if path == "/env" or path.startswith("/env/"):
        body = read_all(environ["wsgi.input"], environ)
        info = env_view(environ)
        info["body_len"], info["body_sha256"] = len(body), sha(body)
        start_response("200 OK", [("Content-Type", "application/json")])
        return [json.dumps(info).encode()]
    if path == "/read":  # the body in pieces: read(n), readline(), iteration, read()
        f = environ["wsgi.input"]
        h = hashlib.sha256()
        parts = []
        first = f.read(int(q.get("n", "7")))
        h.update(first); parts.append(len(first))
        line = f.readline()
        h.update(line); parts.append(len(line))
        lines = 0
        for line in f:
            h.update(line); lines += 1
            if lines == 3:
                break
        rest = read_all(f, environ)
        h.update(rest)
        again = f.read(10)  # past the end
        start_response("200 OK", [("Content-Type", "application/json")])
        return [json.dumps({"sha": h.hexdigest(), "parts": parts, "lines": lines, "again": len(again)}).encode()]
    if path == "/stream":  # n chunks of 64 KiB from a generator
        n = int(q.get("n", "4"))
        start_response("200 OK", [("Content-Type", "application/octet-stream")])
        return (bytes(range(256)) * 256 for _ in range(n))
    if path == "/list":  # n chunks of 64 KiB in a list
        n = int(q.get("n", "4"))
        start_response("200 OK", [("Content-Type", "application/octet-stream")])
        return [bytes(range(256)) * 256] * n
    if path == "/gate":  # the first chunk must reach the client before the second exists
        gate.clear()
        def gen():
            yield b"first"
            if not gate.wait(10):
                yield b"timeout"
                return
            yield b"second"
        start_response("200 OK", T)
        return gen()
    if path == "/open-gate":
        gate.set()
        start_response("200 OK", T)
        return [b"ok"]
    if path == "/write":
        write = start_response("200 OK", [("Content-Type", "text/plain")])
        write(b"a")
        write(b"b")
        return [b"c"]
    if path == "/error":
        raise ValueError("boom")
    if path == "/error-after":
        start_response("200 OK", T)
        return Closing("error-after", [b"partial", b"more"], fail_at=1)
    if path == "/close-ok":
        start_response("200 OK", T)
        return Closing("close-ok", [b"a", b"b"])
    if path == "/closed":
        start_response("200 OK", T)
        return [json.dumps(closed).encode()]
    if path == "/disconnect":
        start_response("200 OK", T)
        return Closing("disconnect", forever(), delay=0.001)
    if path == "/bad":
        status, headers = BAD[q["case"]]
        start_response(status, headers)
        return [b"ok"]
    if path == "/cl":  # declared Content-Length against the body actually sent
        start_response("200 OK", [("Content-Length", q["declared"])])
        body = [b"x" * int(q["actual"])]
        return body if q.get("mode") == "list" else iter(body)
    if path == "/file":
        f = open(q["path"], "rb")
        if "seek" in q:
            f.seek(int(q["seek"]))
        headers = [("Content-Type", "application/octet-stream")]
        if "cl" in q:
            headers.append(("Content-Length", q["cl"]))
        start_response("200 OK", headers)
        return environ["wsgi.file_wrapper"](f, 65536)
    if path == "/file-bytesio":
        start_response("200 OK", T)
        return environ["wsgi.file_wrapper"](io.BytesIO(b"from memory" * 1000), 1000)
    if path == "/file-pipe":
        r, w = os.pipe()
        def feed():
            with os.fdopen(w, "wb") as out:
                out.write(b"from a pipe" * 1000)
        threading.Thread(target=feed).start()
        start_response("200 OK", T)
        return environ["wsgi.file_wrapper"](os.fdopen(r, "rb"), 1000)
    if path == "/exc-info":  # replace the response before anything was sent
        start_response("200 OK", T + [("X-First", "1")])
        try:
            raise KeyError("x")
        except KeyError:
            start_response("503 Unavailable", T + [("X-Second", "1")], sys.exc_info())
        return [b"replaced"]
    if path == "/exc-info-late":  # after the head is out: start_response re-raises
        write = start_response("200 OK", T)
        write(b"sent")
        try:
            raise KeyError("late")
        except KeyError:
            start_response("500 Error", T, sys.exc_info())
        return [b"unreachable"]
    if path == "/twice":
        start_response("200 OK", T)
        start_response("200 OK", T)
        return [b"x"]
    if path == "/nostart":
        return [b"x"]
    if path == "/notbytes":
        start_response("200 OK", T)
        return ["text"]
    if path == "/none":
        start_response("200 OK", T)
        return None
    if path == "/nocontent":
        start_response("204 No Content", [])
        return [q.get("body", "").encode()]
    if path == "/many":  # n small chunks in a list: written by Go, never while attached
        start_response("200 OK", T)
        return [b"y" * 1000] * int(q["n"])
    if path == "/duplex":  # a helper thread reads the body while this one writes
        inp, got = environ["wsgi.input"], []
        t = threading.Thread(target=lambda: got.append(inp.read(5)))
        t.start()
        time.sleep(0.3)  # t is now waiting in Go for the client's body, holding the request
        write = start_response("200 OK", T)
        write(b"main ")
        t.join()
        return [b"got " + got[0]]
    if path == "/ctx-set":  # set and never reset
        current_user.set(q["user"])
        start_response("200 OK", T)
        return [str(threading.get_ident()).encode()]
    if path == "/ctx-get":
        start_response("200 OK", T)
        return [("%d %s" % (threading.get_ident(), current_user.get())).encode()]
    if path == "/sleep":
        time.sleep(float(q["s"]))
        start_response("200 OK", T)
        return [str(threading.get_ident()).encode()]
    if path == "/held":  # fresh chunks of the given sizes in a tuple, freed when serve returns
        seed = int(q.get("seed", "0"))
        start_response("200 OK", T)
        return tuple(pattern(seed, i, int(n)) for i, n in enumerate(q["sizes"].split(",")))
    if path == "/shared":  # one module-level object, n times in a list
        start_response("200 OK", T)
        return [SHARED] * int(q["n"])
    if path == "/shared-refs":
        start_response("200 OK", T)
        return [str(sys.getrefcount(SHARED)).encode()]
    if path == "/hello":
        start_response("200 OK", [("Content-Type", "text/plain"), ("Content-Length", "13")])
        return [b"Hello, world!"]
    if path == "/late":  # a thread keeps write() and writes after the response
        write = start_response("200 OK", T)
        rid = q.get("id", "")
        write(rid.encode())
        def late():
            if q.get("sleep"):
                time.sleep(float(q["sleep"]))
            try:
                write(b"LATE" + rid.encode())
            except ClientDisconnected:
                late_results.append("gone " + rid)
            else:
                late_results.append("sent " + rid)
        threading.Thread(target=late).start()
        if q.get("wait"):  # until /open-late: the test holds the late write meanwhile
            late_gate.wait(10)
            late_gate.clear()
        return []
    if path == "/open-late":
        late_gate.set()
        start_response("200 OK", T)
        return [b"ok"]
    if path.startswith("/latin1"):
        start_response("200 OK", [("X-Path", environ["PATH_INFO"]), ("X-Utf8", environ["PATH_INFO"].encode("latin-1").decode("utf-8"))])
        return []
    start_response("404 Not Found", [("Content-Type", "text/plain")])
    return [("not found: %s" % path).encode("latin-1")]

# Anything the validator merely warns about is an error too.
warnings.simplefilter("error", wsgiref.validate.WSGIWarning)
validated = wsgiref.validate.validator(app)

def run_wsgiref(host):
    """The stock counterpart: wsgiref's server on host, on a Python thread of this process."""
    import socket
    from wsgiref.simple_server import make_server, WSGIRequestHandler, WSGIServer
    class Quiet(WSGIRequestHandler):
        def log_message(self, *a):
            pass
    class Server(WSGIServer):
        address_family = socket.AF_INET6 if ":" in host else socket.AF_INET
    srv = make_server(host, 0, app, server_class=Server, handler_class=Quiet)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv.server_port
`
