#!/usr/bin/env bash
# Run the ncircle and cryptofun Go benchmarks, extract fig5.dat, and render
# fig5.pdf. All artifacts land next to this script.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MICRO_BENCH="$(cd "$HERE/.." && pwd)"
NCIRCLE_DIR="$MICRO_BENCH/ncircle"
CRYPTOFUN_DIR="$MICRO_BENCH/cryptofun"

NCIRCLE_OUT="$HERE/ncircle-bench.txt"
CRYPTOFUN_OUT="$HERE/cryptofun-bench.txt"
DAT="$HERE/fig5.dat"
PDF="$HERE/fig5.pdf"

for dir in "$NCIRCLE_DIR" "$CRYPTOFUN_DIR"; do
    if [[ ! -d "$dir" ]]; then
        echo "error: expected directory $dir" >&2
        exit 1
    fi
done

echo "==> running ncircle benchmarks (pre/... and ecc/elgamal)"
(
    cd "$NCIRCLE_DIR"
    go test -bench=. -benchmem -run='^$' ./pre/... ./ecc/elgamal
) | tee "$NCIRCLE_OUT"

echo "==> running cryptofun RSA benchmarks"
(
    cd "$CRYPTOFUN_DIR"
    go test -bench=RSA -benchmem -run='^$'
) | tee "$CRYPTOFUN_OUT"

echo "==> extracting fig5.dat"
python3 "$HERE/extract_fig5.py" "$NCIRCLE_OUT" "$CRYPTOFUN_OUT" -o "$DAT"

echo "==> rendering fig5.pdf with gnuplot"
(cd "$HERE" && gnuplot fig5.gpi)

echo "done. wrote:"
echo "  $DAT"
echo "  $PDF"
