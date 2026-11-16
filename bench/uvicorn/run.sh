#!/usr/bin/env bash
# uvicorn on pythago against uvicorn on the stock interpreter (the PBS python3.13 that
# pythago-env provisions with; same version and build as the native image):
#
#   bench/uvicorn/run.sh [config-filter-regex]
#
# Each configuration serves bench/uvicorn/app.py with one uvicorn worker pinned to one
# core; internal/tools/httpload drives it from other cores. Needs the test image
# (.work/testimage) with CPython 3.13 for this host (e.g. ci/unit.sh gil). Results: one JSON line per run
# in $BENCH_OUT.
#
#   BENCH_SERVER_CPU   core for the server (default 5)
#   BENCH_LOAD_CPUS    cores for the load generator (default 15-19)
#   BENCH_DURATION     measured seconds per run (default 8)
#   BENCH_OUT          results file (default $CACHE/bench-uvicorn.jsonl)
#   BENCH_APP          the application (default app:app; fastapi_app:app needs fastapi)
#
# "asgi" configurations serve the application with package asgi behind net/http
# (internal/tools/asgiserve) instead of uvicorn.
. "$(dirname "$0")/../../ci/lib.sh"
cd "$ROOT"
filter=${1:-.}
server_cpu=${BENCH_SERVER_CPU:-5}
load_cpus=${BENCH_LOAD_CPUS:-15-19}
dur=${BENCH_DURATION:-8}
venv=$CACHE/venv-uvicorn
port=18731
out=${BENCH_OUT:-$CACHE/bench-uvicorn.jsonl}

app=${BENCH_APP:-app:app}
[ -x "$venv/bin/uvicorn" ] || go run ./cmd/pythago-env -env "$venv" -- install uvicorn uvloop httptools fastapi
go build -o "$CACHE/bin/pyrun" ./internal/tools/pyrun
go build -o "$CACHE/bin/httpload" ./internal/tools/httpload
go build -o "$CACHE/bin/asgiserve" ./internal/tools/asgiserve
"$venv/bin/python" -I -c 'import sys, uvicorn, uvloop, httptools; print(sys.version.split()[0], "uvicorn", uvicorn.__version__, "uvloop", uvloop.__version__, "httptools", httptools.__version__)'

# name|interpreter|uvicorn options
configs=(
  "stock/asyncio/h11|stock|--loop asyncio --http h11"
  "stock/asyncio/httptools|stock|--loop asyncio --http httptools"
  "stock/uvloop/httptools|stock|--loop uvloop --http httptools"
  "pythago/asyncio/httptools|pythago|--loop asyncio --http httptools"
  "pythago/uvloop/httptools|pythago|--loop uvloop --http httptools"
  "pythago/GoEventLoop/httptools|pythago|--loop goaio:GoEventLoop --http httptools"
  "pythago/GoSelectorEventLoop/httptools|pythago|--loop goaio:GoSelectorEventLoop --http httptools"
  "pythago/GoEventLoop/h11|pythago|--loop goaio:GoEventLoop --http h11"
  "asgi/net-http|asgi|"
)
# path|connections
loads=("/|1" "/|64" "/|256" "/sleep|256" "/big|64")

for c in "${configs[@]}"; do
  IFS='|' read -r name interp opts <<< "$c"
  [[ $name =~ $filter ]] || continue
  # shellcheck disable=SC2206
  args=(-m uvicorn "$app" --app-dir bench/uvicorn --port $port --no-access-log --log-level warning $opts)
  if [ "$interp" = stock ]; then
    taskset -c "$server_cpu" "$venv/bin/python" "${args[@]}" &
  elif [ "$interp" = asgi ]; then
    PYRUN_ENV=$venv taskset -c "$server_cpu" "$CACHE/bin/asgiserve" -addr "127.0.0.1:$port" -app-dir bench/uvicorn "$app" &
  else
    PYRUN_ENV=$venv taskset -c "$server_cpu" "$CACHE/bin/pyrun" --python "${args[@]}" &
  fi
  pid=$!
  for _ in $(seq 100); do (exec 3<>/dev/tcp/127.0.0.1/$port) 2>/dev/null && break; sleep 0.1; done
  rss0=$(ps -o rss= -p $pid)
  for l in "${loads[@]}"; do
    IFS='|' read -r path conns <<< "$l"
    GOMAXPROCS=5 taskset -c "$load_cpus" "$CACHE/bin/httpload" -c "$conns" -d "${dur}s" -w 2s \
      -label "$name $path" "http://127.0.0.1:$port$path" | tee -a "$out"
  done
  echo "{\"label\": \"$name rss\", \"rss_kb_start\": $rss0, \"rss_kb_end\": $(ps -o rss= -p $pid), \"threads\": $(ps -o nlwp= -p $pid)}" | tee -a "$out"
  kill -TERM $pid; wait $pid 2>/dev/null || true
done
