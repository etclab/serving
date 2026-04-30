#!/usr/bin/env python3
"""Extract mean deployment time per variant from the latest benchmark run
and emit Table 5 ("Average time (sec) to deploy function configurations")
both to stdout and to ./table5.dat.
"""

import argparse
import csv
import os
import re
import statistics as stat
import sys
from pathlib import Path


VARIANT_LABELS = {
    "knative":          "Knative Function",
    "efunction":        "EFunction",
    "leader-efunction": "\\SystemName Leader EFunction",
    "member-efunction": "\\SystemName Follower EFunction",
}

VARIANT_ORDER = ["knative", "efunction", "leader-efunction", "member-efunction"]


def is_timestamp_folder(name):
    return bool(re.match(r"^\d{4}-\d{2}-\d{2}_\d{2}:\d{2}:\d{2}$", name))


def latest_timestamp(run_dir: Path):
    folders = [p.name for p in run_dir.iterdir()
               if p.is_dir() and is_timestamp_folder(p.name)]
    if not folders:
        return None
    folders.sort(reverse=True)
    return folders[0]


def find_data_file(variant_dir: Path):
    for f in variant_dir.iterdir():
        if f.suffix == ".data":
            return f
    return None


def mean_deploy_time(data_file: Path):
    diffs = []
    with data_file.open(newline="") as fh:
        for row in csv.DictReader(fh):
            diffs.append(int(row["pod_ready_time"]) - int(row["pod_scheduled_time"]))
    if not diffs:
        return None
    # Trim 5 lowest + 5 highest only when there's enough room (matches transform.py).
    if len(diffs) >= 11:
        diffs.sort()
        diffs = diffs[5:-5]
    return stat.mean(diffs)


def render_table(rows, timestamp):
    lines = []
    lines.append(f"# Table 5: Average time (sec) to deploy function configurations")
    lines.append(f"# Source run: {timestamp}")
    lines.append(f"# {'Function Configuration':<32} {'Duration (s)':>12}")
    for label, mean in rows:
        if mean is None:
            lines.append(f"  {label:<32} {'N/A':>12}")
        else:
            lines.append(f"  {label:<32} {mean:>12.3f}")
    return "\n".join(lines) + "\n"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--run-dir", default=None,
                        help="path to run/ dir (default: ../run relative to this script)")
    parser.add_argument("--timestamp", default=None,
                        help="timestamp folder to process (default: most recent)")
    parser.add_argument("--out-file", default=None,
                        help="output .dat path (default: ./table5.dat next to this script)")
    args = parser.parse_args()

    script_dir = Path(__file__).resolve().parent
    run_dir = Path(args.run_dir) if args.run_dir else script_dir.parent / "run"
    out_file = Path(args.out_file) if args.out_file else script_dir / "table5.dat"

    if not run_dir.is_dir():
        print(f"error: run dir '{run_dir}' not found", file=sys.stderr)
        sys.exit(1)

    timestamp = args.timestamp or latest_timestamp(run_dir)
    if not timestamp:
        print(f"error: no timestamp folders in '{run_dir}'", file=sys.stderr)
        sys.exit(1)

    ts_path = run_dir / timestamp
    # AKS runs nest variant dirs under an extra "aks/" subfolder.
    variant_root = ts_path / "aks" if (ts_path / "aks").is_dir() else ts_path
    rows = []
    for variant in VARIANT_ORDER:
        variant_dir = variant_root / variant
        label = VARIANT_LABELS[variant]
        if not variant_dir.is_dir():
            rows.append((label, None))
            continue
        data_file = find_data_file(variant_dir)
        if not data_file:
            rows.append((label, None))
            continue
        rows.append((label, mean_deploy_time(data_file)))

    table = render_table(rows, timestamp)
    sys.stdout.write(table)
    out_file.write_text(table)
    print(f"\nWrote {out_file}")


if __name__ == "__main__":
    main()
