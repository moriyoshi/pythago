#!/usr/bin/env bash
# GPU tier (needs an NVIDIA GPU): torch, and vLLM on the GIL build, in environments
# provisioned by pythago-env. Each check runs twice, under pythago (internal/tools/pyrun,
# embedded stdlib, Config.Environment) and with the environment's stock Python as the
# control, so failures caused by packages or the machine are told apart from pythago's.
#
#   ci/gpu.sh gil|ft
#
#   PYTHAGO_GPU_VLLM    set to 0 to skip vLLM checks (default: 1)
#   PYTHAGO_GPU_WHEELS  space-separated wheel directories for offline installs (pip --no-index
#                       --find-links); default: the PyTorch and PyPI indexes
#
# Exit status 1 if a check fails under pythago while its control passes, or a
# pythago-only check fails.
. "$(dirname "$0")/lib.sh"
variant=${1:?usage: ci/gpu.sh gil|ft}
nvidia-smi -L >/dev/null 2>&1 || { echo "no NVIDIA GPU" >&2; exit 1; }
case $variant in
  gil) envflags=(-python 3.13) ;;
  ft) envflags=(-python 3.14 -freethreaded) ;;
esac
gen_image "$variant" "$HOST_ARCH"
cd "$ROOT"
go build -o "$CACHE/bin/pyrun-$variant" ./internal/tools/pyrun
go build -o "$CACHE/bin/pythago-env" ./cmd/pythago-env
pyrun=$CACHE/bin/pyrun-$variant

# provision NAME PIP-REQUIREMENTS... -- ONLINE-INDEX-ARGS...
provision() {
  local name=$1 venv reqs=() idx=()
  venv=$CACHE/venv-$name-$variant
  shift
  while [ $# -gt 0 ] && [ "$1" != -- ]; do reqs+=("$1"); shift; done
  [ $# -gt 0 ] && shift && idx=("$@")
  if [ -n "${PYTHAGO_GPU_WHEELS:-}" ]; then
    idx=(--no-index)
    for d in $PYTHAGO_GPU_WHEELS; do idx+=(--find-links "$d"); done
  fi
  log "provision $name ($variant): ${reqs[*]}"
  "$CACHE/bin/pythago-env" "${envflags[@]}" -env "$venv" install -q "${idx[@]}" "${reqs[@]}" >&2
  echo "$venv"
}

# The stock interpreter's prefix: its headers serve torch.compile and vLLM's JIT kernels.
stock_home() { sed -n 's/^home = \(.*\)\/bin$/\1/p' "$1/pyvenv.cfg"; }

results=() status=0
record() { # NAME PYTHAGO-RC STOCK-RC ("-" when not run)
  local verdict=ok
  if [ "$2" != 0 ] && [ "$3" = 0 -o "$3" = - ]; then verdict="FAIL (pythago)"; status=1
  elif [ "$2" != 0 ]; then verdict="both fail (environment)"; fi
  results+=("$(printf '%-28s pythago=%-3s stock=%-3s %s' "$1" "$2" "$3" "$verdict")")
}

# check NAME VENV SCRIPT [ENV=VALUE...]: under pythago and with the venv's python.
check() {
  local name=$1 venv=$2 script=$3; shift 3
  local home p s
  home=$(stock_home "$venv")
  log "check $name"
  env "$@" PYRUN_ENV="$venv" PYTHONHOME="$home" timeout 1800 "$pyrun" "ci/gpu/$script" >"$CACHE/gpu-$name.pythago.log" 2>&1 && p=0 || p=$?
  env "$@" timeout 1800 "$venv/bin/python" "ci/gpu/$script" >"$CACHE/gpu-$name.stock.log" 2>&1 && s=0 || s=$?
  record "$name" "$p" "$s"
}

# server NAME VENV PYTHON-CMD...: OpenAI server smoke test (health, chat, SIGTERM exit 0).
server() {
  local name=$1 venv=$2; shift 2
  local port=$((18000 + RANDOM % 1000)) pid rc=1
  "$@" -m vllm.entrypoints.openai.api_server --model Qwen/Qwen2.5-0.5B-Instruct \
    --gpu-memory-utilization 0.25 --max-model-len 2048 --port "$port" >"$CACHE/gpu-$name.log" 2>&1 &
  pid=$!
  for _ in $(seq 120); do curl -sf "localhost:$port/health" >/dev/null && break; kill -0 $pid 2>/dev/null || break; sleep 5; done
  if curl -sf "localhost:$port/v1/chat/completions" -H 'Content-Type: application/json' \
      -d '{"model":"Qwen/Qwen2.5-0.5B-Instruct","messages":[{"role":"user","content":"Reply with one word: what color is the sky on a clear day?"}],"max_tokens":8,"temperature":0}' |
      grep -qi blue; then rc=0; fi
  kill -TERM $pid 2>/dev/null; wait $pid || rc=1
  echo $rc
}

torch_venv=$(provision torch "torch==2.14.1+cu130" -- --index-url https://download.pytorch.org/whl/cu130 --extra-index-url https://pypi.org/simple)
check torch_basic "$torch_venv" torch_basic.py
check torch_workers "$torch_venv" torch_workers.py
check torch_compile "$torch_venv" torch_compile.py
check torch_threads "$torch_venv" torch_threads.py
log "check torch_goroutines (pythago only)"
PYRUN_GO=16 PYRUN_ENV="$torch_venv" timeout 900 "$pyrun" ci/gpu/torch_goroutines.py >"$CACHE/gpu-torch_goroutines.log" 2>&1 && g=0 || g=$?
record torch_goroutines "$g" -

if [ "$variant" = gil ] && [ "${PYTHAGO_GPU_VLLM:-1}" = 1 ]; then # vLLM requires the GIL build
  vllm_venv=$(provision vllm "vllm==0.32.0" "torch==2.13.0+cu132" "torchaudio==2.11.0+cpu" -- \
    --extra-index-url https://download.pytorch.org/whl/cu132 --extra-index-url https://download.pytorch.org/whl/cpu)
  # flashinfer's JIT sampler fails to compile with this package set under stock Python too.
  check vllm_generate "$vllm_venv" vllm_generate.py VLLM_USE_FLASHINFER_SAMPLER=0
  check vllm_embed "$vllm_venv" vllm_embed.py VLLM_USE_FLASHINFER_SAMPLER=0
  log "check vllm_server"
  home=$(stock_home "$vllm_venv")
  p=$(VLLM_USE_FLASHINFER_SAMPLER=0 PYRUN_ENV="$vllm_venv" PYTHONHOME="$home" server vllm_server.pythago "$vllm_venv" "$pyrun" --python)
  s=$(VLLM_USE_FLASHINFER_SAMPLER=0 server vllm_server.stock "$vllm_venv" "$vllm_venv/bin/python")
  record vllm_server "$p" "$s"
fi

printf '%s\n' "${results[@]}"
exit $status
