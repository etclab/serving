#!/usr/bin/env python3

import argparse
import csv
import os
import re
import statistics as stat
from pathlib import Path


# appender-00001-deployment-6f87fcbbb-xwvrh
# removes the last two unique strings
def get_func_name(pod_name):
    parts = pod_name.split('-')
    parts = parts[:-2]
    return "-".join(parts)


def is_timestamp_folder(name):
    """Check if folder name matches timestamp pattern YYYY-MM-DD_HH:MM:SS"""
    pattern = r'^\d{4}-\d{2}-\d{2}_\d{2}:\d{2}:\d{2}$'
    return bool(re.match(pattern, name))


def get_most_recent_timestamp(run_dir):
    """Find the most recent timestamp folder in the run directory"""
    timestamp_folders = []
    for item in os.listdir(run_dir):
        item_path = os.path.join(run_dir, item)
        if os.path.isdir(item_path) and is_timestamp_folder(item):
            timestamp_folders.append(item)

    if not timestamp_folders:
        return None

    # Sort timestamps (they're lexicographically sortable due to format)
    timestamp_folders.sort(reverse=True)
    return timestamp_folders[0]


def find_data_file(variant_path):
    """Find the .data file in a variant folder"""
    for item in os.listdir(variant_path):
        if item.endswith('.data'):
            return os.path.join(variant_path, item)
    return None


def process_data_file(data_file):
    """Process a data file and return metrics"""
    with open(data_file, mode="r", newline='') as file:
        reader = csv.DictReader(file)

        all_diffs = []
        func_name = 'func-name'
        for row in reader:
            func_name = get_func_name(row['pod_name'])[:25]

            ready_time = row['pod_ready_time']
            scheduled_time = row['pod_scheduled_time']

            diff = int(ready_time) - int(scheduled_time)
            all_diffs.append(diff)

        tall = None
        if len(all_diffs) < 11:
            # Not enough data points to trim
            tall = all_diffs
        else:
            all_diffs.sort()
            # ignore the first and last five
            tall = all_diffs[5:-5]

        if not tall:
            return None
        
        print(tall)

        min_val = min(tall)
        max_val = max(tall)
        mean = stat.mean(tall)
        std = stat.stdev(tall) if len(tall) > 1 else 0
        median = stat.median(tall)

        return {
            'func_name': func_name,
            'min': min_val,
            'max': max_val,
            'mean': mean,
            'median': median,
            'std': std
        }


def main():
    parser = argparse.ArgumentParser(
        description="Process deployment benchmark data from run/{timestamp}/{variant}/ structure"
    )

    parser.add_argument(
        "--run-dir",
        type=str,
        default="run",
        help="path to run directory containing timestamp folders (default: run)"
    )

    parser.add_argument(
        "--out-file",
        type=str,
        default="deployment_metrics.txt",
        help="path to output file (default: deployment_metrics.txt)"
    )

    parser.add_argument(
        "--timestamp",
        type=str,
        default=None,
        help="specific timestamp folder to process (default: most recent)"
    )

    args = parser.parse_args()

    run_dir = Path(args.run_dir)
    if not run_dir.exists():
        print(f"Error: run directory '{run_dir}' does not exist")
        return

    # Find the timestamp folder to process
    if args.timestamp:
        timestamp = args.timestamp
        if not is_timestamp_folder(timestamp):
            print(f"Error: '{timestamp}' is not a valid timestamp format (YYYY-MM-DD_HH:MM:SS)")
            return
    else:
        timestamp = get_most_recent_timestamp(run_dir)

    if not timestamp:
        print(f"Error: no timestamp folders found in '{run_dir}'")
        return

    timestamp_path = run_dir / timestamp
    if not timestamp_path.exists():
        print(f"Error: timestamp folder '{timestamp_path}' does not exist")
        return

    print(f"Processing timestamp: {timestamp}")

    # Find all variant folders inside the timestamp folder
    variants = []
    for item in os.listdir(timestamp_path):
        item_path = timestamp_path / item
        if os.path.isdir(item_path):
            variants.append(item)

    if not variants:
        print(f"Error: no variant folders found in '{timestamp_path}'")
        return

    variants.sort()
    print(f"Found variants: {variants}")

    results = []
    for variant in variants:
        variant_path = timestamp_path / variant

        # Find data file
        data_file = find_data_file(variant_path)
        if not data_file:
            print(f"Warning: no .data file found in '{variant_path}'")
            continue

        print(f"Processing {variant}...")

        # Process data file
        metrics = process_data_file(data_file)
        if metrics:
            metrics['variant'] = variant
            results.append(metrics)
            print(f"  min: {metrics['min']}, max: {metrics['max']}, "
                  f"mean: {metrics['mean']:.2f}, median: {metrics['median']}, "
                  f"std: {metrics['std']:.2f}")

    # Write output file
    if results:
        with open(args.out_file, "w") as f:
            f.write(f"# pod deployment time (seconds)\n")
            f.write(f"# timestamp: {timestamp}\n")
            f.write(f"{'# variant':<25} {'min':<10} {'max':<10} {'mean':<15} "
                    f"{'median':<10} {'std':<15}\n")
            for r in results:
                f.write(f"{r['variant']:<25} {r['min']:<10} {r['max']:<10} "
                        f"{r['mean']:<15.2f} {r['median']:<10} {r['std']:<15.2f}\n")
        print(f"\nResults written to {args.out_file}")
    else:
        print("No results to write")


if __name__ == "__main__":
    main()
