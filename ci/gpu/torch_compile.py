# torch.compile on CUDA (triton) and CPU (inductor C++); needs Python's headers on disk.
import torch
def f(x: torch.Tensor, y: torch.Tensor) -> torch.Tensor:
    return torch.nn.functional.gelu(x @ y) * 2 + 1
x, y = torch.randn(256, 256, device="cuda"), torch.randn(256, 256, device="cuda")
assert torch.allclose(torch.compile(f)(x, y), f(x, y), atol=1e-3, rtol=1e-3)
xc, yc = x.cpu(), y.cpu()
assert torch.allclose(torch.compile(f)(xc, yc), f(xc, yc), atol=1e-3, rtol=1e-3)
print("ok")
