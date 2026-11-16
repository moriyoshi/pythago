# DataLoader worker processes with each multiprocessing start method (and the default).
# spawn and forkserver re-import __main__ in each worker, hence the guard.
import multiprocessing as mp, torch

if __name__ == "__main__":
    ds = torch.utils.data.TensorDataset(torch.arange(100.0))
    for method in (None, "fork", "spawn", "forkserver"):
        dl = torch.utils.data.DataLoader(ds, batch_size=10, num_workers=2, multiprocessing_context=method)
        assert sum(b[0].sum().item() for b in dl) == 4950, method
    print("ok", mp.get_start_method())
