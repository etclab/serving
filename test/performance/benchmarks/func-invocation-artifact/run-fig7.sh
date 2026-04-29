#!/usr/bin/env bash
# Run the func-invocation benchmark across every strategy that appears in
# fig7, parse each strategy's vegeta logs into artifact/<strategy>.data,
# then render fig7.pdf with gnuplot.
#
# Usage:
#   ./run-fig7.sh [STRATEGY]
#     STRATEGY: one of stock|enclave|rsa|samba|lambada-member, or "all"
#               (default: all)
#
# Environment:
#   RATES               Space-separated list of vegeta rates to run
#                       (default: "250 500 750 1000 1250 1500"). Forwarded to
#                       the underlying run-appender*.sh scripts, which iterate
#                       these rates and produce one <rate>.log per entry.
#   DURATION            Per-rate vegeta duration (default: "5m"). Forwarded
#                       through envsubst into each Job YAML's --duration arg.
#   RSA_SK_FILE         Path to a PEM file containing the RSA private key
#                       used by the rsa strategy. If unset, the key is
#                       extracted from func-chain/pre-config.sh.
#   SKIP_BENCHMARK      If "true", skip running benchmarks AND skip
#                       re-extracting from run/<ts>/ — re-plot fig7.pdf
#                       directly from the *.data files already present
#                       in this artifact directory.
#   STRATEGY_RUN_DIR_<NAME>
#                       Override the run dir picked for a strategy. NAME is
#                       the strategy in upper case with '-' replaced by '_'
#                       (e.g. STRATEGY_RUN_DIR_LAMBADA_MEMBER=...).
#
# Produces:
#   artifact/<strategy>.data   (one per strategy)
#   artifact/fig7.pdf
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BENCH_DIR="$(cd "$HERE/.." && pwd)"

# When USE_AKS=true, child run-appender*.sh scripts write logs into
# run/<ts>/aks/ instead of run/<ts>/. Export so child shells inherit.
export USE_AKS="${USE_AKS:-false}"

# RATES and DURATION are read by each run-appender*.sh. Export so they
# propagate through both `( cd … && ./run-appender.sh )` and
# `bash -c "cd … && ./run-appender-ego.sh"` invocations below.
export RATES="${RATES:-250 500 750 1000 1250 1500}"
export DURATION="${DURATION:-5m}"

STRATEGY_ARG="${1:-all}"
ALL_STRATEGIES=(stock enclave rsa samba lambada-member)

case "$STRATEGY_ARG" in
  all) STRATEGIES=("${ALL_STRATEGIES[@]}") ;;
  stock|enclave|rsa|samba|lambada-member) STRATEGIES=("$STRATEGY_ARG") ;;
  *) echo "error: unknown strategy: $STRATEGY_ARG" >&2; exit 2 ;;
esac

if ! command -v gnuplot >/dev/null 2>&1; then
  echo "error: gnuplot not found on PATH" >&2
  exit 1
fi

# strategy -> source folder
folder_for() {
  case "$1" in
    stock)          echo "func-invocation" ;;
    enclave|rsa|samba) echo "func-invocation-ego" ;;
    lambada-member) echo "func-invocation-ego-member" ;;
  esac
}

# Pull the RSA_SK PEM out of an existing pre-config.sh that has it
# uncommented, so we don't have to duplicate the test key here.
extract_rsa_sk_from_funcchain() {
  python3 - "$BENCH_DIR/func-chain/pre-config.sh" <<'PY'
import re, sys
src = open(sys.argv[1]).read()
m = re.search(r"^RSA_SK='([^']+)'", src, re.M)
if not m:
    sys.exit("error: could not locate RSA_SK assignment in func-chain/pre-config.sh")
sys.stdout.write(m.group(1))
PY
}

# Stash the most-recent run dir for a folder (used to detect what this
# invocation produced even when multiple strategies share the same folder).
snapshot_latest_run() {
  local folder=$1
  ls -1dt "$BENCH_DIR/$folder/run/"*/ 2>/dev/null | head -n1 | sed 's:/$::'
}

declare -A RUN_DIR_FOR_STRATEGY=()

run_one() {
  local strategy=$1
  local folder
  folder=$(folder_for "$strategy")
  local folder_path="$BENCH_DIR/$folder"

  echo ""
  echo "=========================================="
  echo "[$strategy] folder: $folder"
  echo "=========================================="

  local before_latest
  before_latest=$(snapshot_latest_run "$folder" || true)

  local override_var="STRATEGY_RUN_DIR_$(echo "$strategy" | tr '[:lower:]-' '[:upper:]_')"
  if [[ -n "${!override_var:-}" ]]; then
    RUN_DIR_FOR_STRATEGY[$strategy]="${!override_var}"
    echo "[$strategy] using override run dir: ${!override_var}"
    return
  fi

  case "$strategy" in
    stock)
      ( cd "$folder_path" && ./run-appender.sh )
      ;;
    enclave)
      FUNCTION_MODE='' RSA_SK='' \
        bash -c "cd '$folder_path' && ./run-appender-ego.sh"
      ;;
    rsa)
      local rsa_sk="${RSA_SK:-}"
      if [[ -z "$rsa_sk" ]]; then
        if [[ -n "${RSA_SK_FILE:-}" ]]; then
          rsa_sk=$(cat "$RSA_SK_FILE")
        else
          rsa_sk=$(extract_rsa_sk_from_funcchain)
        fi
      fi
      FUNCTION_MODE='' RSA_SK="$rsa_sk" \
        bash -c "cd '$folder_path' && ./run-appender-ego.sh"
      ;;
    samba)
      FUNCTION_MODE='SINGLE' RSA_SK='' \
        bash -c "cd '$folder_path' && ./run-appender-ego.sh"
      ;;
    lambada-member)
      ( cd "$folder_path" && ./run-appender-ego-member.sh )
      ;;
  esac

  local after_latest
  after_latest=$(snapshot_latest_run "$folder" || true)
  if [[ -z "$after_latest" || "$after_latest" == "$before_latest" ]]; then
    echo "[$strategy] error: no new run directory appeared under $folder/run" >&2
    return 1
  fi
  # On AKS, the run-appender*.sh scripts write logs into run/<ts>/aks/, so
  # point extract-fig7-data.py at that subfolder rather than the timestamp dir.
  if [[ "$USE_AKS" == "true" || "$USE_AKS" == "1" ]]; then
    after_latest="$after_latest/aks"
  fi
  RUN_DIR_FOR_STRATEGY[$strategy]="$after_latest"
  echo "[$strategy] produced $after_latest"
}

if [[ "${SKIP_BENCHMARK:-false}" == "true" ]]; then
  echo "==> SKIP_BENCHMARK=true; using existing $HERE/*.data, skipping benchmark and extraction"
  missing=()
  for s in "${STRATEGIES[@]}"; do
    [[ -f "$HERE/${s}.data" ]] || missing+=("$HERE/${s}.data")
  done
  if (( ${#missing[@]} > 0 )); then
    echo "error: SKIP_BENCHMARK=true but these data files are missing:" >&2
    printf '  %s\n' "${missing[@]}" >&2
    exit 1
  fi
else
  for s in "${STRATEGIES[@]}"; do
    run_one "$s"
  done

  echo ""
  echo "==> extracting .data files into $HERE"
  EXTRACT_ARGS=(--strategies "${STRATEGIES[@]}" --artifact-dir "$HERE")
  for s in "${STRATEGIES[@]}"; do
    EXTRACT_ARGS+=(--runs "${s}=${RUN_DIR_FOR_STRATEGY[$s]}")
  done
  python3 "$HERE/extract-fig7-data.py" "${EXTRACT_ARGS[@]}"
fi

echo ""
echo "==> rendering fig7.pdf with gnuplot"
( cd "$HERE" && gnuplot fig7.gpi )

echo ""
echo "done. wrote:"
for s in "${STRATEGIES[@]}"; do
  echo "  $HERE/${s}.data"
done
echo "  $HERE/fig7.pdf"
