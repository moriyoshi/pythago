"""Tables from bench/http/run.sh results.

    python3 bench/http/summarize.py RESULTS.jsonl [--readme README.md] [--session N]

Takes the last session in RESULTS.jsonl (the runs after its last "meta" line; --session
picks another, counting from 1), and prints Markdown: per interface, the median requests
per second of every configuration for each workload, plain and TLS, and pythago's ratio
to uvicorn (the interface's first configuration). Runs with no request, errors, connects
over a second, other processes on the server's core, or a nearly full connection tracking
table are suspect and listed under the
tables; a cell is marked † when half its rounds or more are, since its median may then be
off. With --readme, the tables also replace what README.md has
between "<!-- bench-http:begin -->" and "<!-- bench-http:end -->".
"""

from __future__ import annotations

import argparse
import collections
import json
import re
import statistics
import sys
from typing import Any

Run = dict[str, Any]  # one JSONL line

BEGIN, END = "<!-- bench-http:begin -->", "<!-- bench-http:end -->"


def sessions(path: str) -> list[tuple[Run, list[Run]]]:
    out: list[tuple[Run, list[Run]]] = []
    for line in open(path):
        d = json.loads(line)
        if "meta" in d:
            out.append((d, []))
        elif out:
            out[-1][1].append(d)
    return out


FOREIGN_MAX = 5  # percent of the server's core other processes may use during a run
CONNTRACK_MAX = 90  # percent of netfilter's connection tracking table in use; full drops SYNs


def suspect(r: Run) -> Any:
    """No request completed, errors, a connect over a second, other processes on the
    server's core, or a nearly full connection tracking table."""
    return (not r["requests"] or r["errors"] or r.get("slow_connects")
            or r.get("foreign_cpu_pct", 0) > FOREIGN_MAX
            or r.get("conntrack_pct", 0) > CONNTRACK_MAX)


def rate(v: float) -> str:
    return f"{v / 1000:.1f}k" if v >= 1000 else f"{v:.0f}"


def tables(meta: Run, runs: list[Run], brief: bool = False) -> str:
    """Markdown tables; brief (for README.md) counts the suspect runs instead of listing
    them."""
    loads: dict[str, None] = {}
    configs: collections.defaultdict[str, dict[str, None]] = collections.defaultdict(dict)
    cells: collections.defaultdict[tuple[str, str, str, str], list[Run]] = collections.defaultdict(list)
    for r in runs:
        loads[r["load"]] = None
        configs[r["interface"]][r["config"]] = None
        cells[(r["interface"], r["config"], r["scheme"], r["load"])].append(r)
    lines = []
    m = meta["meta"]
    rounds = max((r["round"] for r in runs), default=0)
    lines.append(
        f"CPython {m['python']} (GIL), one server process pinned to one core (core "
        f"{meta['server_cpu']}{', a ' + meta['cpu_model'] if meta.get('cpu_model') else ''}), "
        f"load from cores {meta['load_cpus']} "
        f"(`internal/tools/httpload`, HTTP/1.1 keep-alive). Median requests per second of "
        f"{rounds} interleaved rounds of {meta['duration_s']} s. uvicorn {m['uvicorn']}, uvloop "
        f"{m['uvloop']}, httptools {m['httptools']}, a2wsgi {m['a2wsgi']}, gunicorn "
        f"{m['gunicorn']} on stock CPython ({m['ssl']}); pythago {meta['commit']} with "
        f"{meta['go']}; Linux {m['kernel']}; {meta['date'][:10]}.")
    lines.append("")
    flagged = False
    for iface, cfgs in configs.items():
        names = list(cfgs)
        ref = names[0]
        # The ratio column, when the interface has a pythago and another configuration.
        pythago = next((n for n in names if n.startswith("pythago") and n != ref), None)
        lines.append(f"**{iface.upper()}**")
        lines.append("")
        lines.append("| Workload | " + " | ".join(names) + (f" | {pythago} ÷ {ref}" if pythago else "") + " |")
        lines.append("|---" * (len(names) + 1 + bool(pythago)) + "|")
        for scheme in ("http", "https"):
            for load in loads:
                row, med = [], {}
                for n in names:
                    rs = cells.get((iface, n, scheme, load))
                    if not rs:
                        row.append("")
                        continue
                    med[n] = statistics.median(r["rps"] for r in rs)
                    # The median of n rounds holds while fewer than half of them are suspect.
                    bad = 2 * sum(bool(suspect(r)) for r in rs) >= len(rs)
                    flagged |= bad
                    row.append(rate(med[n]) + (" †" if bad else ""))
                if pythago:
                    row.append(f"{med[pythago] / med[ref]:.2f}" if med.get(ref) and pythago in med else "")
                label = f"{load}, {'TLS' if scheme == 'https' else 'plain'}"
                lines.append(f"| {label} | " + " | ".join(row) + " |")
        lines.append("")
    suspects = [r for r in runs if suspect(r)]
    if suspects and brief:
        foreign = sum(r.get("foreign_cpu_pct", 0) > FOREIGN_MAX for r in suspects)
        lines.append(("† half the rounds or more were suspect, so the median may be off. " if flagged else "")
                     + f"{len(suspects)} of {len(runs)} runs were suspect: {foreign} had other "
                     f"processes on over {FOREIGN_MAX}% of the server's core"
                     + (f", {len(suspects) - foreign} no request, errors, a connect over a second or a connection tracking "
                        f"table over {CONNTRACK_MAX}% full"
                        if foreign < len(suspects) else "")
                     + ". A cell's median comes from its other rounds unless marked †; "
                     "`bench/http/summarize.py` lists the runs.")
        lines.append("")
    elif suspects:
        lines.append(("† half the rounds or more were suspect, so the median may be off. " if flagged else "")
                     + "Suspect runs (no request completed, errors, a connect over a second, other "
                     f"processes using over {FOREIGN_MAX}% of the server's core, or a connection tracking "
                     f"table over {CONNTRACK_MAX}% full), whose cell's median "
                     "comes from its other rounds unless marked †:")
        lines.append("")
        for r in suspects:
            if suspect(r):
                slowest = f"{r['max_connect_ms']:.0f} ms" if "max_connect_ms" in r else "?"
                scheme = "TLS" if r["scheme"] == "https" else "plain"
                lines.append(f"- {r['config']}, {r['load']}, {scheme}, round {r['round']}: "
                             f"{r['rps']:.0f} req/s, {r['errors']} errors, "
                             f"{r.get('slow_connects', '?')} connects over a second (slowest "
                             f"{slowest}), {r.get('host_syn_retransmits', '?')} SYN "
                             f"retransmissions on the host, {r.get('foreign_cpu_pct', '?')}% of "
                             f"the server's core used by other processes, connection "
                             f"tracking table {r.get('conntrack_pct', '?')}% full")
        lines.append("")
    tls = sorted({f"{r['config']}: {r['tls']}" for r in runs if r.get("tls")})
    suites = collections.defaultdict(list)
    for t in tls:
        name, suite = t.split(": ", 1)
        suites[suite].append(name)
    if suites:
        lines.append("TLS 1.3 cipher suites negotiated: " + "; ".join(
            f"{s.removeprefix('TLS 1.3 ')} ({', '.join(n)})" for s, n in suites.items()) + ".")
        lines.append("")
    return "\n".join(lines).rstrip() + "\n"


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("results")
    ap.add_argument("--readme")
    ap.add_argument("--session", type=int)
    a = ap.parse_args()
    ss = sessions(a.results)
    if not ss:
        sys.exit("no session in " + a.results)
    meta, runs = ss[a.session - 1] if a.session else ss[-1]
    print(tables(meta, runs))
    if a.readme:
        md = tables(meta, runs, brief=True)
        text = open(a.readme).read()
        new, n = re.subn(re.escape(BEGIN) + ".*?" + re.escape(END),
                         lambda _: f"{BEGIN}\n{md}{END}", text, flags=re.S)
        if n != 1:
            sys.exit(f"{a.readme}: want one {BEGIN} ... {END} block, found {n}")
        open(a.readme, "w").write(new)


if __name__ == "__main__":
    main()
