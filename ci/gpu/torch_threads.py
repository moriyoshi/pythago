# Free-threaded builds: torch CPU work scales across Python threads; GPU work from 16 threads.
import sys, threading, time, torch
if getattr(sys, "_is_gil_enabled", lambda: True)():
    print("skip: GIL build")
    raise SystemExit(0)
torch.set_num_threads(1)
x = torch.randn(256, 256)
def work(n: int) -> None:
    y = x
    for _ in range(n):
        y = torch.tanh(y @ x) * 0.01
def timed(threads: int, n: int = 400) -> float:
    ts = [threading.Thread(target=work, args=(n,)) for _ in range(threads)]
    t0 = time.perf_counter()
    for t in ts:
        t.start()
    for t in ts:
        t.join()
    return time.perf_counter() - t0
one, eight = timed(1), timed(8)
assert 8 * one / eight > 2.5, ("no parallel speedup", one, eight)
out: list[float | None] = [None] * 16
def gpu(i: int) -> None:
    s = torch.cuda.Stream()
    with torch.cuda.stream(s):
        m = torch.full((256, 256), float(i), device="cuda")
        for _ in range(200):
            m = m @ torch.eye(256, device="cuda")
        s.synchronize()
    out[i] = m[0, 0].item()
ts = [threading.Thread(target=gpu, args=(i,)) for i in range(16)]
for t in ts:
    t.start()
for t in ts:
    t.join()
assert out == [float(i) for i in range(16)]
assert not sys._is_gil_enabled()
print("ok speedup %.1fx" % (8 * one / eight))
