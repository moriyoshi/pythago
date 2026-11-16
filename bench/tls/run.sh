#!/usr/bin/env bash
# TLS on GoEventLoop: Go's crypto/tls (goaio's Go path) against Python's ssl over the same
# loop (asyncio SSLProtocol) and over asyncio's own loop, as a client of one Go TLS peer:
#
#   bench/tls/run.sh [config-filter-regex]
#
# The client (internal/tools/pyrun bench/tls/client.py) is pinned to one core, the peer
# (bench/tls/peer) to others; configurations are interleaved, every round runs each once.
# Needs the test image (.work/testimage) for this host. Results: one JSON line per run in
# $BENCH_OUT.
#
#   BENCH_CLIENT_CPU   core for the client (default 5)
#   BENCH_PEER_CPUS    cores for the peer (default 6-9)
#   BENCH_DURATION     measured seconds per run (default 5)
#   BENCH_ROUNDS       rounds (default 3)
#   BENCH_OUT          results file (default $CACHE/bench-tls.jsonl)
. "$(dirname "$0")/../../ci/lib.sh"
cd "$ROOT"
filter=${1:-.}
client_cpu=${BENCH_CLIENT_CPU:-5}
peer_cpus=${BENCH_PEER_CPUS:-6-9}
dur=${BENCH_DURATION:-5}
rounds=${BENCH_ROUNDS:-3}
out=${BENCH_OUT:-$CACHE/bench-tls.jsonl}
port=18600
work=$(mktemp -d)
trap 'kill $peer 2>/dev/null; rm -rf "$work"' EXIT

GOWORK=off go build -o "$CACHE/bin/tlspeer" ./bench/tls/peer
go build -o "$CACHE/bin/pyrun" ./internal/tools/pyrun
taskset -c "$peer_cpus" "$CACHE/bin/tlspeer" -addr "127.0.0.1:$port" -ca "$work/ca.pem" &
peer=$!
for _ in $(seq 100); do [ -s "$work/ca.pem" ] && break; sleep 0.1; done

# workload|connections
loads=("bulk|1" "rr|1" "rr|64" "handshake|1" "handshake|64")
configs=(go py stock plain)
for round in $(seq "$rounds"); do
  for l in "${loads[@]}"; do
    IFS='|' read -r workload conns <<< "$l"
    for c in "${configs[@]}"; do
      [[ $c =~ $filter ]] || continue
      [ "$c" = plain ] && [ "$workload" = handshake ] && continue
      taskset -c "$client_cpu" "$CACHE/bin/pyrun" bench/tls/client.py "$c" "$workload" "$dur" "$conns" \
        "$work/ca.pem" "$port" 2>&1 | grep '^{' | sed "s/}\$/, \"round\": $round}/" | tee -a "$out"
    done
  done
done
