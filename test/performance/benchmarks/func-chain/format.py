#!/usr/bin/env python3
"""
Format parsed trace CSV files into statistical summary.

Usage:
    python3 format.py <input_csv> <output_data> [--limit N]

Arguments:
    input_csv   - Path to the parsed traces CSV file
    output_data - Path to the output .data file
    --limit N   - Number of records to process (default: 300)
"""

import argparse
import csv
import statistics as stat
import sys


def find_stat(all_values):
    """Calculate statistics after trimming 10% from both ends."""
    if len(all_values) < 3:
        return {
            'min': 0,
            'max': 0,
            'mean': 0,
            'std': 0,
            'median': 0
        }

    all_values.sort()

    skip = int(len(all_values) * 0.1)
    # Ignore the first and last 10%
    if skip > 0 and len(all_values) > 2 * skip:
        trimmed = all_values[skip:-skip]
    else:
        trimmed = all_values

    if len(trimmed) < 2:
        trimmed = all_values

    min_val = min(trimmed)
    max_val = max(trimmed)
    mean = stat.mean(trimmed)
    std = stat.stdev(trimmed) if len(trimmed) >= 2 else 0
    median = stat.median(trimmed)

    return {
        'min': min_val,
        'max': max_val,
        'mean': mean,
        'std': std,
        'median': median
    }


def latency_stat_writer(file_ptr, data, func_name):
    """Write a single row of statistics to the output file."""
    file_ptr.write(
        f"{func_name:<25} "
        f"{data['min']:<25.2f} "
        f"{data['max']:<25.2f} "
        f"{data['mean']:<25.2f} "
        f"{data['median']:<25.2f} "
        f"{data['std']:<25.2f}\n"
    )


def parse_trace_file(in_file, out_file, limit=300):
    """Parse trace CSV and write statistics to output file."""
    try:
        with open(in_file, 'r') as file:
            csv_reader = csv.reader(file)

            header = next(csv_reader)

            # Take up to 'limit' records
            count = 0
            rows = []
            for row in csv_reader:
                rows.append(row)
                count += 1
                if count >= limit:
                    break

            if not rows:
                print(f"Warning: No data rows found in {in_file}")
                return False

            # Extract timing columns (assuming columns: trace_id, validate, vote, count_vote, display)
            validate_timings = [float(row[1]) for row in rows if len(row) > 1]
            vote_timings = [float(row[2]) for row in rows if len(row) > 2]
            count_vote_timings = [float(row[3]) for row in rows if len(row) > 3]
            display_timings = [float(row[4]) for row in rows if len(row) > 4]

            validate_stat = find_stat(validate_timings)
            vote_stat = find_stat(vote_timings)
            count_vote_stat = find_stat(count_vote_timings)
            display_stat = find_stat(display_timings)

            print(f"Processed {len(rows)} records from {in_file}")
            print(f"  validate-fun: min={validate_stat['min']:.2f}, mean={validate_stat['mean']:.2f}, median={validate_stat['median']:.2f}")
            print(f"  vote-fun: min={vote_stat['min']:.2f}, mean={vote_stat['mean']:.2f}, median={vote_stat['median']:.2f}")
            print(f"  count-vote-fun: min={count_vote_stat['min']:.2f}, mean={count_vote_stat['mean']:.2f}, median={count_vote_stat['median']:.2f}")
            print(f"  display-fun: min={display_stat['min']:.2f}, mean={display_stat['mean']:.2f}, median={display_stat['median']:.2f}")

            with open(out_file, "w") as f:
                f.write(f"# time spent (milli-seconds)\n")
                f.write(f"# Records processed: {len(rows)} (limit: {limit})\n")
                f.write(f"{'# function name':<25} {'min':<25} {'max':<25} {'mean':<25} {'median':<25} {'std':<25}\n")

                latency_stat_writer(f, validate_stat, "validate-fun")
                latency_stat_writer(f, vote_stat, "vote-fun")
                latency_stat_writer(f, count_vote_stat, "count-vote-fun")
                latency_stat_writer(f, display_stat, "display-fun")

            print(f"Statistics written to {out_file}")
            return True

    except FileNotFoundError:
        print(f"Error: Input file not found: {in_file}")
        return False
    except Exception as e:
        print(f"Error processing {in_file}: {e}")
        return False


def main():
    parser = argparse.ArgumentParser(
        description='Format parsed trace CSV files into statistical summary.'
    )
    parser.add_argument('input_csv', help='Path to the parsed traces CSV file')
    parser.add_argument('output_data', help='Path to the output .data file')
    parser.add_argument('--limit', type=int, default=300,
                        help='Number of records to process (default: 300)')

    args = parser.parse_args()

    success = parse_trace_file(args.input_csv, args.output_data, args.limit)
    sys.exit(0 if success else 1)


if __name__ == '__main__':
    main()
