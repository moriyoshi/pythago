#!/usr/bin/env bash
# package wsgi (internal/tools/wsgiserve, net/http) against gunicorn on the stock
# interpreter (the PBS python3.13 that pythago-env provisions with; same version and
# build as the native image):
#
#   bench/wsgi/run.sh [config-filter-regex]
#
# Each configuration serves bench/wsgi/app.py from one process pinned to one core, with
# 4 threads where it has threads (gunicorn gthread, wsgiserve's admission slots);
# internal/tools/httpload drives it from other cores. Configurations are interleaved:
# every round runs each once. Needs the test image (.work/testimage) with CPython 3.13 for
# this host (e.g. ci/unit.sh gil). Results: one JSON line per run in $BENCH_OUT.
#
#   BENCH_SERVER_CPU   core for the server (default 5)
#   BENCH_LOAD_CPUS    cores for the load generator (default 15-19)
#   BENCH_DURATION     measured seconds per run (default 8)
#   BENCH_ROUNDS       rounds (default 2)
#   BENCH_OUT          results file (default $CACHE/bench-wsgi.jsonl)
#   BENCH_APP          the application (default app:app; flask_app:app needs flask)
. "$(dirname "$0")/../../ci/lib.sh"
cd "$ROOT"
filter=${1:-.}
server_cpu=${BENCH_SERVER_CPU:-5}
load_cpus=${BENCH_LOAD_CPUS:-15-19}
dur=${BENCH_DURATION:-8}
rounds=${BENCH_ROUNDS:-2}
out=${BENCH_OUT:-$CACHE/bench-wsgi.jsonl}
app=${BENCH_APP:-app:app}
venv=$CACHE/venv-wsgi
port=18431

[ -x "$venv/bin/gunicorn" ] || go run ./cmd/pythago-env -env "$venv" -- install gunicorn flask
"$venv/bin/python" -I -c 'import sys, gunicorn; print(sys.version.split()[0], "gunicorn", gunicorn.__version__)'
go build -o "$CACHE/bin/wsgiserve" ./internal/tools/wsgiserve
go build -o "$CACHE/bin/httpload" ./internal/tools/httpload

# name|server|options
configs=(
  "stock/gunicorn-sync|gunicorn|-k sync"
  "stock/gunicorn-gthread-4|gunicorn|-k gthread --threads 4"
  "wsgi/net-http-4|wsgi|-threads 4"
)
# path|connections
loads=("/|1" "/|64" "/big|64" "/sleep|64")

for round in $(seq "$rounds"); do
  for c in "${configs[@]}"; do
    IFS='|' read -r name server opts <<< "$c"
    [[ $name =~ $filter ]] || continue
    # shellcheck disable=SC2086
    if [ "$server" = gunicorn ]; then
      taskset -c "$server_cpu" "$venv/bin/python" -m gunicorn -w 1 $opts --chdir bench/wsgi \
        -b "127.0.0.1:$port" --log-level warning "$app" &
    else
      PYRUN_ENV=$venv taskset -c "$server_cpu" "$CACHE/bin/wsgiserve" $opts -addr "127.0.0.1:$port" \
        -app-dir bench/wsgi "$app" &
    fi
    pid=$!
    for _ in $(seq 100); do (exec 3<>/dev/tcp/127.0.0.1/$port) 2>/dev/null && break; sleep 0.1; done
    for l in "${loads[@]}"; do
      IFS='|' read -r path conns <<< "$l"
      GOMAXPROCS=5 taskset -c "$load_cpus" "$CACHE/bin/httpload" -c "$conns" -d "${dur}s" -w 2s \
        -label "$name $path r$round" "http://127.0.0.1:$port$path" | tee -a "$out"
    done
    kill -TERM $pid; wait $pid 2>/dev/null || true
  done
done
