#!/usr/bin/env bash
# Run the ncircle aggsig/multisig and cryptofun sign/verify benchmarks,
# extract fig6.dat, and render fig6.pdf.
#
# The ncircle aggsig/multisig benchmarks take a long time (~30+ min) because
# they sweep numSigs up to 1024 for three schemes. By default this script
# reuses an existing ncircle-bench.txt in this directory; pass --fresh-ncircle
# to force a rerun. cryptofun benchmarks are fast and always rerun.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MICRO_BENCH="$(cd "$HERE/.." && pwd)"
NCIRCLE_DIR="$MICRO_BENCH/ncircle"
CRYPTOFUN_DIR="$MICRO_BENCH/cryptofun"

NCIRCLE_OUT="$HERE/ncircle-bench.txt"
CRYPTOFUN_OUT="$HERE/cryptofun-bench.txt"
DAT="$HERE/fig6.dat"
PDF="$HERE/fig6.pdf"

FRESH_NCIRCLE=0
for arg in "$@"; do
    case "$arg" in
        --fresh-ncircle) FRESH_NCIRCLE=1 ;;
        -h|--help)
            sed -n '2,10p' "$0"
            exit 0
            ;;
        *) echo "unknown arg: $arg" >&2; exit 2 ;;
    esac
done

for dir in "$NCIRCLE_DIR" "$CRYPTOFUN_DIR"; do
    if [[ ! -d "$dir" ]]; then
        echo "error: expected directory $dir" >&2
        exit 1
    fi
done

if [[ $FRESH_NCIRCLE -eq 1 || ! -s "$NCIRCLE_OUT" ]]; then
    echo "==> running ncircle aggsig/multisig benchmarks (this is slow)"
    (
        cd "$NCIRCLE_DIR"
        go test -v -bench=. -benchmem -run='^$' \
            ./aggsig/bgls03 ./multisig/b03 ./multisig/bgoy07
    ) | tee "$NCIRCLE_OUT"
else
    echo "==> reusing existing $NCIRCLE_OUT (pass --fresh-ncircle to regenerate)"
fi

echo "==> running cryptofun RSA and Ed25519 sign/verify benchmarks"
(
    cd "$CRYPTOFUN_DIR"
    go test -v -bench='RSASignSHA256|RSAVerifySHA256|Ed25519phSign|Ed2519phVerify' \
        -benchmem -run='^$'
) | tee "$CRYPTOFUN_OUT"

echo "==> extracting fig6.dat"
python3 "$HERE/extract_fig6.py" "$NCIRCLE_OUT" "$CRYPTOFUN_OUT" -o "$DAT"

echo "==> rendering fig6.pdf with gnuplot"
(cd "$HERE" && gnuplot fig6.gpi)

echo "done. wrote:"
echo "  $DAT"
echo "  $PDF"
