# Run by pyrun with PYRUN_GO=16: Go goroutines call outer(g) concurrently through py.Do.
import torch
dev = torch.device("cuda")
def outer(g: int) -> None:
    for _ in range(50):
        x = torch.full((64, 512), float(g), device=dev)
        assert (x @ torch.eye(512, device=dev)).sum().item() == 64 * 512 * g
        torch.relu(torch.randn(128, 128) @ torch.randn(128, 128)).mean().item()
