#!/usr/bin/env python3
"""Extract `go test -bench` output into fig5.dat.

Reads one or more benchmark output files (from `go test -bench=. -benchmem` in
ncircle/pre, ncircle/ecc/elgamal, and cryptofun) and emits a table in the
format expected by fig5.gpi. Each cell is an ns/op value; 0 means the scheme
does not support that API function.

Column source (matching fig5-sample.dat):
  - ElGamal: ncircle/ecc/elgamal, P-256 curve
  - BBS98:   ncircle/pre/bbs98,   P-256 curve
  - RSA:     cryptofun,           3072-bit keys
  - AFGH05, CH07, LV08: ncircle/pre/* (no subtests)
"""

import argparse
import re
import sys

PKG_RE = re.compile(r"^pkg:\s*(\S+)")
BENCH_RE = re.compile(r"^(Benchmark\S+?)-\d+\s+\d+\s+([\d.eE+\-]+)\s+ns/op")

ELGAMAL_PKG = "github.com/etclab/ncircl/ecc/elgamal"
BBS98_PKG = "github.com/etclab/ncircl/pre/bbs98"
AFGH05_PKG = "github.com/etclab/ncircl/pre/afgh05"
CH07_PKG = "github.com/etclab/ncircl/pre/ch07"
LV08_PKG = "github.com/etclab/ncircl/pre/lv08"
CRYPTOFUN_PKG = "github.com/etclab/cryptofun"

# For the sample, ElGamal and BBS98 used P-256 (~128-bit); RSA used 3072-bit.
ELGAMAL_CURVE = "P-256"
BBS98_CURVE = "P-256"
RSA_KEYLEN = 3072

# Row layout matches fig5-sample.dat. Each row lists the benchmark lookup key
# per column, or None when the scheme lacks that operation (printed as 0).
COLUMNS = ["ElGamal", "RSA", "BBS98", "AFGH05", "CH07", "LV08"]

ROWS = [
    ("KeyGen", {
        "ElGamal": (ELGAMAL_PKG, f"BenchmarkKeyGen/{ELGAMAL_CURVE}"),
        "RSA":     (CRYPTOFUN_PKG, f"BenchmarkGenerateRSAKeyPair/keyLength:{RSA_KEYLEN}"),
        "BBS98":   (BBS98_PKG, f"BenchmarkKeyGen/{BBS98_CURVE}"),
        "AFGH05":  (AFGH05_PKG, "BenchmarkKeyGen"),
        "CH07":    (CH07_PKG, "BenchmarkKeyGen"),
        "LV08":    (LV08_PKG, "BenchmarkKeyGen"),
    }),
    ('"ReEncryption\\nKeyGen"', {
        "BBS98":  (BBS98_PKG, f"BenchmarkReEncryptKeyGen/{BBS98_CURVE}"),
        "AFGH05": (AFGH05_PKG, "BenchmarkReEncryptionKeyGen"),
        "CH07":   (CH07_PKG, "BenchmarkReEncryptKeyGen"),
        "LV08":   (LV08_PKG, "BenchmarkReEncryptionKeyGen"),
    }),
    ("Encrypt", {
        "ElGamal": (ELGAMAL_PKG, f"BenchmarkEncrypt/{ELGAMAL_CURVE}"),
        "RSA":     (CRYPTOFUN_PKG, f"BenchmarkRSAEncrypt/keyLength:{RSA_KEYLEN}"),
        "BBS98":   (BBS98_PKG, f"BenchmarkEncrypt/{BBS98_CURVE}"),
        "AFGH05":  (AFGH05_PKG, "BenchmarkEncrypt"),
        "CH07":    (CH07_PKG, "BenchmarkEncrypt"),
    }),
    ("Encrypt1", {
        "LV08": (LV08_PKG, "BenchmarkEncrypt1"),
    }),
    ("Encrypt2", {
        "LV08": (LV08_PKG, "BenchmarkEncrypt2"),
    }),
    ("ReEncrypt", {
        "BBS98":  (BBS98_PKG, f"BenchmarkReEncrypt/{BBS98_CURVE}"),
        "AFGH05": (AFGH05_PKG, "BenchmarkReEncrypt"),
        "CH07":   (CH07_PKG, "BenchmarkReEncrypt"),
        "LV08":   (LV08_PKG, "BenchmarkReEncrypt"),
    }),
    ("Decrypt", {
        "ElGamal": (ELGAMAL_PKG, f"BenchmarkDecrypt/{ELGAMAL_CURVE}"),
        # cryptofun's RSA decrypt benchmark uses a different sub-name format.
        "RSA":     (CRYPTOFUN_PKG, f"BenchmarkRSADecrypt/RSADecrypt-{RSA_KEYLEN}"),
        "BBS98":   (BBS98_PKG, f"BenchmarkDecrypt/{BBS98_CURVE}"),
        "CH07":    (CH07_PKG, "BenchmarkDecrypt"),
    }),
    ("Decrypt1", {
        "AFGH05": (AFGH05_PKG, "BenchmarkDecrypt1"),
        "LV08":   (LV08_PKG, "BenchmarkDecrypt1"),
    }),
    ("Decrypt2", {
        "AFGH05": (AFGH05_PKG, "BenchmarkDecrypt2"),
        "LV08":   (LV08_PKG, "BenchmarkDecrypt2"),
    }),
]


def parse(paths):
    """Return {(pkg, bench_name): ns_per_op_int}."""
    results = {}
    current_pkg = None
    for path in paths:
        with open(path) as f:
            for line in f:
                m = PKG_RE.match(line)
                if m:
                    current_pkg = m.group(1)
                    continue
                m = BENCH_RE.match(line)
                if m and current_pkg is not None:
                    name = m.group(1)
                    val = int(round(float(m.group(2))))
                    results[(current_pkg, name)] = val
    return results


def lookup(results, key):
    if key is None:
        return 0
    pkg, name = key
    if (pkg, name) not in results:
        print(f"warning: missing benchmark {name} in {pkg}", file=sys.stderr)
        return 0
    return results[(pkg, name)]


def build_table(results):
    header = ["Operation"] + COLUMNS
    rows = [header]
    for op_label, mapping in ROWS:
        row = [op_label]
        for col in COLUMNS:
            row.append(str(lookup(results, mapping.get(col))))
        rows.append(row)
    return rows


def format_table(rows):
    # Fixed column widths; operation column wider to fit quoted labels.
    widths = [24] + [12] * len(COLUMNS)
    out = []
    for row in rows:
        parts = []
        for i, cell in enumerate(row):
            if i == len(row) - 1:
                parts.append(cell)
            else:
                parts.append(cell.ljust(widths[i]))
        out.append("".join(parts).rstrip())
    return "\n".join(out) + "\n"


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("inputs", nargs="+", help="go test benchmark output files")
    ap.add_argument("-o", "--output", default="fig5.dat", help="output .dat file")
    args = ap.parse_args()

    results = parse(args.inputs)
    rows = build_table(results)
    text = format_table(rows)

    header_comment = (
        "# Generated by extract_fig5.py from go benchmark output.\n"
        "# Values are in ns/op. 0 means the scheme does not support that API.\n"
        "# - ElGamal: P-256 curve (~128-bit security)\n"
        "# - BBS98:   P-256 curve (~128-bit security)\n"
        "# - RSA:     3072-bit keys (128-bit security)\n"
        "#--------------------------------------------------------------------\n"
    )

    with open(args.output, "w") as f:
        f.write(header_comment)
        f.write(text)
    print(f"wrote {args.output}")


if __name__ == "__main__":
    main()
