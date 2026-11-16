# torch + CUDA: device, matmul vs CPU, autograd training, autocast, C++ errors, CPU intra-op
# threads, Python threads on CUDA streams, DataLoader without workers.
import threading, torch
assert torch.cuda.is_available(), "no CUDA device"
dev = torch.device("cuda")
a, b = torch.randn(1024, 1024), torch.randn(1024, 1024)
assert torch.allclose(a @ b, (a.to(dev) @ b.to(dev)).cpu(), atol=1e-3, rtol=1e-3)
torch.manual_seed(0)
model = torch.nn.Sequential(torch.nn.Linear(1, 64), torch.nn.Tanh(), torch.nn.Linear(64, 1)).to(dev)
opt = torch.optim.Adam(model.parameters(), lr=1e-2)
x = torch.linspace(-3, 3, 512, device=dev).unsqueeze(1)
y = torch.sin(x)
first: float
for step in range(300):
    opt.zero_grad()
    loss = torch.nn.functional.mse_loss(model(x), y)
    loss.backward()
    opt.step()
    first = loss.item() if step == 0 else first
assert loss.item() < first / 10, (first, loss.item())
with torch.autocast("cuda", dtype=torch.bfloat16):
    assert model(x).dtype == torch.bfloat16
try:
    torch.randn(2, 3, device=dev) @ torch.randn(4, 5, device=dev)
    raise AssertionError("no error")
except RuntimeError as e:
    assert "cannot be multiplied" in str(e)
torch.randn(1024, 1024) @ torch.randn(1024, 1024)
results: list[float | None] = [None] * 8
def worker(i: int) -> None:
    s = torch.cuda.Stream()
    with torch.cuda.stream(s):
        m = torch.full((512, 512), float(i), device=dev)
        for _ in range(20):
            m = m @ torch.eye(512, device=dev)
        s.synchronize()
        results[i] = m[0, 0].item()
ts = [threading.Thread(target=worker, args=(i,)) for i in range(8)]
for t in ts:
    t.start()
for t in ts:
    t.join()
assert results == [float(i) for i in range(8)], results
ds = torch.utils.data.TensorDataset(torch.arange(100.0))
assert sum(b[0].sum().item() for b in torch.utils.data.DataLoader(ds, batch_size=10)) == 4950
print("ok", torch.__version__)
