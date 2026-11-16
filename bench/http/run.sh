#!/usr/bin/env bash
# HTTP servers, plain and TLS: package asgi (internal/tools/asgiserve) against uvicorn,
# and package wsgi (internal/tools/wsgiserve) against uvicorn (a2wsgi) and gunicorn, all
# on CPython 3.13 (the stock servers on the PBS python3.13 that pythago-env provisions
# with, the same version and build as the native image):
#
#   bench/http/run.sh [config-filter-regex]
#
# Every configuration is one process pinned to one core; internal/tools/httpload drives
# it from other cores. Configurations are interleaved: every round runs each once, plain
# and then TLS (ECDSA P-256 certificate; clients offer only HTTP/1.1). The workloads are
# in "loads" below. Every result also records the host's SYN retransmissions during the
# run (nstat TcpExtTCPSynRetrans): a stalled run with some lost a connect's SYN. And the
# share of the server's core other processes used meanwhile (foreign_cpu_pct): a shared
# host's other work there makes the run invalid. And how full netfilter's connection
# tracking table was at the highest of the run's two ends (conntrack_pct): at 100 the
# kernel drops every new connection's SYN, loopback too, and a run completes nothing
# without an error. Results: a "meta" JSON line, then one JSON line per run, in
# $BENCH_OUT; bench/http/summarize.py turns them into tables (and README.md's).
#
# The test image (.work/testimage) is regenerated for CPython 3.13 on this host first,
# unless BENCH_IMAGE=keep; don't run this while something else tests against the image.
#
#   BENCH_SERVER_CPU   core for the server, one (default 5)
#   BENCH_LOAD_CPUS    cores for the load generator (default 15-19)
#   BENCH_DURATION     measured seconds per run (default 5)
#   BENCH_WARMUP       warm-up seconds per run (default 2)
#   BENCH_ROUNDS       rounds (default 3)
#   BENCH_OUT          results file (default $CACHE/bench-http.jsonl; appended to)
#   BENCH_IMAGE        "keep" to use the test image as it is
. "$(dirname "$0")/../../ci/lib.sh"
cd "$ROOT"
filter=${1:-.}
server_cpu=${BENCH_SERVER_CPU:-5}
load_cpus=${BENCH_LOAD_CPUS:-15-19}
dur=${BENCH_DURATION:-5}
warm=${BENCH_WARMUP:-2}
rounds=${BENCH_ROUNDS:-3}
out=${BENCH_OUT:-$CACHE/bench-http.jsonl}
# httpload's GOMAXPROCS: the number of load cores (a list such as 15-18 or 15,17).
load_procs=$(python3 -c 'import sys; print(sum(len(range(int(a), int(b or a) + 1)) for a, _, b in (p.partition("-") for p in sys.argv[1].split(","))))' "$load_cpus")
venv=$CACHE/venv-http
port=18531
work=$ROOT/.agents-workspace/tmp/bench-http
mkdir -p "$work"
pid=
trap '[ -n "$pid" ] && kill $pid 2>/dev/null; true' EXIT

[ "${BENCH_IMAGE:-}" = keep ] || gen_image gil "$HOST_ARCH"
[ -x "$venv/bin/gunicorn" ] || go run ./cmd/pythago-env -env "$venv" -- install uvicorn uvloop httptools a2wsgi gunicorn
go build -o "$CACHE/bin/asgiserve" ./internal/tools/asgiserve
go build -o "$CACHE/bin/wsgiserve" ./internal/tools/wsgiserve
GOWORK=off go build -o "$CACHE/bin/httpload" ./internal/tools/httpload

# A CA and a server certificate for 127.0.0.1 (ECDSA P-256), new for every run.
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 \
  -subj /CN=bench-http-ca -keyout "$work/ca.key" -out "$work/ca.pem" 2>/dev/null
openssl req -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -subj /CN=localhost \
  -keyout "$work/key.pem" -out "$work/server.csr" 2>/dev/null
printf 'subjectAltName=IP:127.0.0.1,DNS:localhost\n' >"$work/san.ext"
openssl x509 -req -in "$work/server.csr" -CA "$work/ca.pem" -CAkey "$work/ca.key" -CAcreateserial \
  -days 2 -extfile "$work/san.ext" -out "$work/cert.pem" 2>/dev/null

meta=$("$venv/bin/python" -I -c '
import json, platform, sys
from importlib.metadata import version
print(json.dumps({"python": sys.version.split()[0], **{p: version(p) for p in
      ("uvicorn", "uvloop", "httptools", "a2wsgi", "gunicorn")},
      "ssl": __import__("ssl").OPENSSL_VERSION, "kernel": platform.release()}))')
echo "{\"meta\": $meta, \"go\": \"$(go env GOVERSION)\", \"commit\": \"$(git rev-parse --short HEAD)\", \"date\": \"$(date -Iseconds)\", \"server_cpu\": \"$server_cpu\", \"cpu_model\": \"$(lscpu -e=CPU,MODELNAME | awk -v c="$server_cpu" '$1 == c {$1 = ""; sub(/^ /, ""); print}')\", \"load_cpus\": \"$load_cpus\", \"duration_s\": $dur, \"rounds\": $rounds}" | tee -a "$out"

# name|interface|server|options. The WSGI servers get 10 threads: uvicorn's a2wsgi pool.
configs=(
  "uvicorn uvloop+httptools|asgi|uvicorn|--loop uvloop --http httptools"
  "uvicorn asyncio+h11|asgi|uvicorn|--loop asyncio --http h11"
  "pythago asgi|asgi|asgiserve|"
  "pythago asgi GOMAXPROCS=2|asgi|asgiserve|"
  "uvicorn a2wsgi uvloop+httptools|wsgi|uvicorn|--interface wsgi --loop uvloop --http httptools"
  "gunicorn gthread|wsgi|gunicorn|-k gthread --threads 10"
  "pythago wsgi|wsgi|wsgiserve|-threads 10"
  "pythago wsgi GOMAXPROCS=2|wsgi|wsgiserve|-threads 10"
)
# name|path|connections|httpload options
loads=(
  "hello c1|/|1|"
  "hello c64|/|64|"
  "64 KiB response c64|/big|64|"
  "16 KiB upload c64|/upload|64|-body 16384"
  "sleep c64|/sleep|64|"
  "new connection c16|/|16|-close"
)

# The host's SYN retransmissions so far: a run that has some lost a connect's SYN.
synretrans() { nstat -az TcpExtTCPSynRetrans | awk '/TcpExtTCPSynRetrans/ {print $2}'; }
# Nanoseconds tasks ran on the server core (/proc/schedstat rq_cpu_time), and that the
# server's threads (gunicorn's worker too) ran; the difference is someone else's.
corebusy() { awk -v c="cpu$server_cpu" '$1 == c {print $8}' /proc/schedstat; }
# Percent of netfilter's connection tracking table in use (0 without conntrack).
conntrack() {
  local d=/proc/sys/net/netfilter
  [ -r $d/nf_conntrack_count ] && echo $(($(<$d/nf_conntrack_count) * 100 / $(<$d/nf_conntrack_max))) || echo 0
}
servercpu() {
  local q
  for q in $pid $(pgrep -P "$pid"); do cat /proc/$q/task/*/schedstat 2>/dev/null; done |
    awk '{t += $1} END {printf "%d\n", t}'
}

start() { # interface server options scheme
  local iface=$1 server=$2 opts=$3 scheme=$4 dir=bench/uvicorn app=app:app tls=() gmp=
  [ "$iface" = wsgi ] && dir=bench/wsgi
  [[ $name == *GOMAXPROCS=2 ]] && gmp=2
  # shellcheck disable=SC2206
  case $server in
  uvicorn)
    [ "$scheme" = https ] && tls=(--ssl-certfile "$work/cert.pem" --ssl-keyfile "$work/key.pem")
    taskset -c "$server_cpu" "$venv/bin/python" -I -m uvicorn "$app" --app-dir "$dir" --port $port \
      --no-access-log --log-level warning $opts "${tls[@]}" &
    ;;
  gunicorn)
    [ "$scheme" = https ] && tls=(--certfile "$work/cert.pem" --keyfile "$work/key.pem")
    taskset -c "$server_cpu" "$venv/bin/python" -I -m gunicorn -w 1 $opts --chdir "$dir" \
      -b "127.0.0.1:$port" --log-level warning "${tls[@]}" "$app" &
    ;;
  *)
    [ "$scheme" = https ] && tls=(-cert "$work/cert.pem" -key "$work/key.pem")
    PYRUN_ENV=$venv GOMAXPROCS=$gmp taskset -c "$server_cpu" "$CACHE/bin/$server" $opts \
      -addr "127.0.0.1:$port" -app-dir "$dir" "${tls[@]}" "$app" &
    ;;
  esac
  pid=$!
  for _ in $(seq 200); do (exec 3<>/dev/tcp/127.0.0.1/$port) 2>/dev/null && return; sleep 0.1; done
  echo "server $name did not start" >&2
  return 1
}

for round in $(seq "$rounds"); do
  for c in "${configs[@]}"; do
    IFS='|' read -r name iface server opts <<<"$c"
    [[ $name =~ $filter ]] || continue
    for scheme in http https; do
      start "$iface" "$server" "$opts" "$scheme"
      for l in "${loads[@]}"; do
        IFS='|' read -r load path conns lopts <<<"$l"
        syn0=$(synretrans) core0=$(corebusy) srv0=$(servercpu) ct0=$(conntrack) t0=$(date +%s%N)
        # shellcheck disable=SC2086
        res=$(GOMAXPROCS=$load_procs taskset -c "$load_cpus" "$CACHE/bin/httpload" -c "$conns" -d "${dur}s" -w "${warm}s" \
          -ca "$work/ca.pem" $lopts "$scheme://127.0.0.1:$port$path")
        syn1=$(synretrans) core1=$(corebusy) srv1=$(servercpu) ct1=$(conntrack) t1=$(date +%s%N)
        # Percent of the server core that other processes used during the run.
        foreign=$(( ((core1 - core0) - (srv1 - srv0)) * 100 / (t1 - t0) ))
        python3 -c 'import json, sys; d = json.loads(sys.argv[1]); d.update(zip(("config", "interface", "scheme", "load", "round"), sys.argv[2:7])); d["round"] = int(d["round"]); d["host_syn_retransmits"] = int(sys.argv[7]); d["foreign_cpu_pct"] = int(sys.argv[8]); d["conntrack_pct"] = int(sys.argv[9]); print(json.dumps(d))' \
          "$res" "$name" "$iface" "$scheme" "$load" "$round" $((syn1 - syn0)) "$foreign" $((ct0 > ct1 ? ct0 : ct1)) | tee -a "$out"
      done
      kill -TERM $pid; wait $pid 2>/dev/null || true
      pid=
    done
  done
done
