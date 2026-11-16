"""Median rate and CPU cost per configuration and workload, from bench/tls/run.sh's JSONL."""
import json, statistics, sys
from collections import defaultdict
from typing import Any

runs: defaultdict[tuple[str, int, str], list[dict[str, Any]]] = defaultdict(list)
for line in open(sys.argv[1]):
    r = json.loads(line)
    runs[(r["workload"], r["conns"], r["config"])].append(r)
order = ["go", "py", "stock", "plain"]
for (workload, conns) in sorted({k[:2] for k in runs}, key=lambda k: (["bulk", "rr", "handshake"].index(k[0]), k[1])):
    print(f"\n{workload}, {conns} connection(s)")
    for c in order:
        rs = runs.get((workload, conns, c))
        if not rs:
            continue
        rates = [r["rate"] for r in rs]
        cost_key = "cpu_s_per_GB" if workload == "bulk" else "cpu_us_per_op"
        costs = [r[cost_key] for r in rs if r.get(cost_key)]
        print(f"  {c:6} {statistics.median(rates):>10.1f} {rs[0]['unit']:13} (rounds {', '.join(f'{x:.0f}' for x in rates)})"
              f"   {cost_key} {statistics.median(costs):.3f}   cpu {statistics.median(r['cpu_util'] for r in rs):.2f}"
              f"   {rs[0].get('transport', '-')}")
