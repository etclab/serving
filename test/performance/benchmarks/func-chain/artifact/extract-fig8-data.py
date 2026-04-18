#!/usr/bin/env python3
"""
Collect the *_traces.data files produced by run-benchmark.sh for every
strategy and copy them next to artifact/fig8.gpi so gnuplot can render
fig8.pdf.

Expected source layout (produced by run-benchmark.sh):
    run/<timestamp>/[aks/]<strategy>/<strategy>_<rate>rps_<duration>_traces.data

Destination:
    artifact/<strategy>_<rate>rps_<duration>_traces.data
"""

import argparse
import os
import shutil
import sys
from pathlib import Path

STRATEGIES = [
    "knative",
    "efunction",
    "rsa-efunction",
    "member-efunction",
    "leader-efunction",
    "both",
    "both-sig",
    "both-hash-chain-sig",
]


def latest_run_dir(run_root: Path) -> Path:
    if not run_root.is_dir():
        sys.exit(f"error: run root does not exist: {run_root}")
    candidates = [p for p in run_root.iterdir() if p.is_dir()]
    if not candidates:
        sys.exit(f"error: no run directories found under {run_root}")
    # Use mtime rather than name — ad-hoc folders (e.g. "tue-mar3") coexist
    # with timestamped ones and would otherwise win a lexicographic sort.
    return max(candidates, key=lambda p: p.stat().st_mtime)


def resolve_base(run_dir: Path) -> Path:
    """run-benchmark.sh places artifacts under run/<ts>/aks when USE_AKS=true."""
    aks = run_dir / "aks"
    return aks if aks.is_dir() else run_dir


def parse_args():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    here = Path(__file__).resolve().parent
    bench_dir = here.parent  # artifact/ lives inside func-chain/
    parser.add_argument("--run-root", type=Path, default=bench_dir / "run",
                        help="Directory containing timestamped run folders (default: %(default)s)")
    parser.add_argument("--run", type=Path, default=None,
                        help="Specific run folder to use (default: most recent under --run-root)")
    parser.add_argument("--artifact-dir", type=Path, default=here,
                        help="Destination directory for the .data files (default: %(default)s)")
    parser.add_argument("--rate", default="100", help="Request rate used for the run (default: %(default)s)")
    parser.add_argument("--duration", default="5m", help="Run duration (default: %(default)s)")
    parser.add_argument("--strategies", nargs="+", default=STRATEGIES,
                        help="Strategies to collect (default: all)")
    return parser.parse_args()


def main():
    args = parse_args()
    run_dir = args.run.resolve() if args.run else latest_run_dir(args.run_root.resolve())
    base = resolve_base(run_dir)

    args.artifact_dir.mkdir(parents=True, exist_ok=True)
    print(f"Source run:   {run_dir}")
    print(f"Source base:  {base}")
    print(f"Destination:  {args.artifact_dir}")

    filename = lambda s: f"{s}_{args.rate}rps_{args.duration}_traces.data"

    missing = []
    copied = []
    for strategy in args.strategies:
        src = base / strategy / filename(strategy)
        dst = args.artifact_dir / filename(strategy)
        if not src.is_file():
            missing.append(str(src))
            continue
        shutil.copy2(src, dst)
        copied.append((src, dst))
        print(f"  copied {strategy}: {src.name}")

    print()
    print(f"Copied {len(copied)}/{len(args.strategies)} data files.")
    if missing:
        print("Missing sources:")
        for m in missing:
            print(f"  {m}")
        sys.exit(1)


if __name__ == "__main__":
    main()
