"""Summarize repeated Go benchmark samples; times are per completed message."""
import collections
import re
import statistics
import sys

samples: collections.defaultdict[str, list[dict[str, float]]] = collections.defaultdict(list)
for filename in sys.argv[1:]:
    for line in open(filename):
        match = re.match(r"^(Benchmark\S+)\s+(\d+)\s+(.*)$", line)
        if not match:
            continue
        fields = match[3].split()
        metrics = {fields[i + 1]: float(fields[i]) for i in range(0, len(fields) - 1, 2)}
        if "ns/op" in metrics:
            samples[match[1]].append(metrics)

print("benchmark\tsamples\tmedian_ns/msg\tmin_ns/msg\tmax_ns/msg\tmedian_B/msg\tmedian_allocs/msg")
for name, values in samples.items():
    ns = [v["ns/op"] for v in values]
    memory = statistics.median(v.get("B/op", 0) for v in values)
    allocations = statistics.median(v.get("allocs/op", 0) for v in values)
    print(f"{name}\t{len(values)}\t{statistics.median(ns):.2f}\t{min(ns):.2f}\t{max(ns):.2f}\t{memory:.0f}\t{allocations:.0f}")
