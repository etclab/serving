#!/usr/bin/env python3
"""
Collect the per-rate vegeta logs produced by the func-invocation* runners
for each strategy, parse them into the gnuplot-friendly format used by
fig7.gpi, and write them next to fig7.gpi.

Expected source layout (produced by run-appender*.sh):
    <strategy_dir>/run/<timestamp>/<rate>.log

Strategy -> source folder mapping is fixed (see STRATEGIES below).
For folders that host more than one strategy (func-invocation-ego runs
samba/rsa/enclave), each strategy's run is identified by an explicit
run directory passed via --runs strategy=path or auto-picked as the
most recently modified run directory (latest mtime wins).

Destination:
    artifact/<strategy>.data
"""

import argparse
import os
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
BENCH_DIR = HERE.parent  # test/performance/benchmarks/

# strategy -> (folder under benchmarks/, label written into the .data header)
STRATEGIES = {
    "stock":          ("func-invocation",            "single-function-stock"),
    "enclave":        ("func-invocation-ego",        "enclave-sacmat"),
    "rsa":            ("func-invocation-ego",        "rsa-sacmat"),
    "samba":          ("func-invocation-ego",        "samba-sacmat"),
    "lambada-member": ("func-invocation-ego-member", "lambada-member"),
}


def parse_duration(duration: str) -> float:
    """Convert vegeta-style duration strings to milliseconds."""
    duration = duration.strip()
    if duration.endswith("ms"):
        return float(duration[:-2])
    if duration.endswith("µs") or duration.endswith("us"):
        return float(duration[:-2]) / 1000
    if duration.endswith("s"):
        if "m" in duration:
            minutes, _, rest = duration.partition("m")
            seconds = float(rest[:-1]) if rest.endswith("s") else float(rest)
            return (float(minutes) * 60 + seconds) * 1000
        return float(duration[:-1]) * 1000
    return 0.0


def latest_run_dir(folder: Path) -> Path | None:
    run_root = folder / "run"
    if not run_root.is_dir():
        return None
    candidates = [p for p in run_root.iterdir() if p.is_dir()]
    if not candidates:
        return None
    latest = max(candidates, key=lambda p: p.stat().st_mtime)
    # AKS runs nest the rate logs one level deeper (run/<ts>/aks/<rate>.log).
    # If the picked dir has no <rate>.log files but does have an aks/ subdir,
    # descend into it.
    aks_sub = latest / "aks"
    if aks_sub.is_dir() and not any(p.name.split(".")[0].isdigit() and p.suffix == ".log" for p in latest.iterdir()):
        return aks_sub
    return latest


def parse_run_dir(run_dir: Path) -> dict[int, dict[str, float]]:
    """Read every <rate>.log under run_dir and return {rate -> {metric -> ms}}."""
    data: dict[int, dict[str, float]] = {}
    for entry in os.listdir(run_dir):
        path = run_dir / entry
        if not path.is_file() or not entry.endswith(".log"):
            continue
        stem = entry.split(".")[0]
        try:
            rate = int(stem)
        except ValueError:
            # ignore non-rate logs that occasionally land in the run dir
            continue
        match = ""
        with open(path, "r") as f:
            for line in f:
                if "Latencies" in line:
                    match = line.strip()
        if not match:
            print(f"  warning: no Latencies line in {path}", file=sys.stderr)
            continue
        left = match.find("[")
        right = match.find("]")
        labels = [e.strip() for e in match[left + 1 : right].split(",")]
        values = [e.strip() for e in match[right + 1 :].split(",")]
        data[rate] = {labels[i]: parse_duration(values[i]) for i in range(len(labels))}
    return data


def write_data_file(out_path: Path, label: str, data: dict[int, dict[str, float]]) -> None:
    cols = ("50", "90", "95", "99", "min", "max", "mean")
    headers = ("p50", "p90", "p95", "p99", "min", "max", "mean")
    with open(out_path, "w") as f:
        f.write("# func-invocation time (ms)\n")
        f.write(f"#{label:<25} " + " ".join(f"{h:<25}" for h in headers) + "\n")
        for rate in sorted(data):
            row = data[rate]
            f.write(f"{rate:<25} " + " ".join(f"{row[c]:<25}" for c in cols) + "\n")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    parser.add_argument(
        "--strategies", nargs="+", default=list(STRATEGIES.keys()),
        choices=list(STRATEGIES.keys()),
        help="Strategies to extract (default: all)",
    )
    parser.add_argument(
        "--runs", action="append", default=[],
        metavar="STRATEGY=PATH",
        help=(
            "Override the run directory for a strategy. May be specified "
            "multiple times. Useful when the same folder hosts multiple "
            "strategies (e.g. func-invocation-ego)."
        ),
    )
    parser.add_argument(
        "--artifact-dir", type=Path, default=HERE,
        help="Destination directory for the .data files (default: %(default)s)",
    )
    return parser.parse_args()


def parse_run_overrides(items: list[str]) -> dict[str, Path]:
    out: dict[str, Path] = {}
    for it in items:
        if "=" not in it:
            sys.exit(f"error: --runs expects STRATEGY=PATH, got {it!r}")
        name, _, path = it.partition("=")
        if name not in STRATEGIES:
            sys.exit(f"error: unknown strategy {name!r} in --runs")
        out[name] = Path(path).expanduser().resolve()
    return out


def main() -> None:
    args = parse_args()
    overrides = parse_run_overrides(args.runs)
    args.artifact_dir.mkdir(parents=True, exist_ok=True)

    failed = []
    for strategy in args.strategies:
        folder_name, label = STRATEGIES[strategy]
        folder = BENCH_DIR / folder_name
        run_dir = overrides.get(strategy) or latest_run_dir(folder)
        if run_dir is None or not run_dir.is_dir():
            print(f"[{strategy}] no run dir under {folder}/run; skipping", file=sys.stderr)
            failed.append(strategy)
            continue
        print(f"[{strategy}] parsing {run_dir}")
        data = parse_run_dir(run_dir)
        if not data:
            print(f"[{strategy}] no usable <rate>.log files in {run_dir}", file=sys.stderr)
            failed.append(strategy)
            continue
        out_path = args.artifact_dir / f"{strategy}.data"
        write_data_file(out_path, label, data)
        print(f"[{strategy}] wrote {out_path} ({len(data)} rates)")

    if failed:
        sys.exit(f"failed strategies: {', '.join(failed)}")


if __name__ == "__main__":
    main()
