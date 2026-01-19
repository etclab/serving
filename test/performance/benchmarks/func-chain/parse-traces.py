#!/usr/bin/env python3
"""
Parse Zipkin traces for func-chain benchmark.

This script:
1. Checks if Zipkin port is forwarded, starts port-forward if needed
2. Fetches traces from Zipkin API
3. Parses traces to extract per-service latencies
4. Saves raw JSON traces and parsed CSV to the artifacts folder
5. Clears Zipkin data by restarting the Zipkin pod
"""

import argparse
import csv
import json
import os
import signal
import socket
import subprocess
import sys
import time
from contextlib import contextmanager

import requests

ZIPKIN_NAMESPACE = os.environ.get("ZIPKIN_NAMESPACE", "zipkin-monitoring")
ZIPKIN_LOCAL_PORT = int(os.environ.get("ZIPKIN_LOCAL_PORT", "9411"))
ZIPKIN_URL = f"http://localhost:{ZIPKIN_LOCAL_PORT}"

# Service names per strategy
# Base function names in the chain
BASE_SERVICES = ["validate-fun", "vote-fun", "count-vote-fun", "display-fun"]

# Strategy to suffix mapping
STRATEGY_SUFFIX = {
    "knative": "-stock",
    "efunction": "",
    "rsa-efunction": "",
    "leader-efunction": "-leader",
    "member-efunction": "-member",
    "both": "",
    "both-sig": ""
}


def get_services_for_strategy(strategy):
    """Get the list of service names for a given strategy."""
    if strategy not in STRATEGY_SUFFIX:
        print(f"Error: Unknown strategy '{strategy}'")
        print(f"Available strategies: {', '.join(STRATEGY_SUFFIX.keys())}")
        sys.exit(1)

    suffix = STRATEGY_SUFFIX[strategy]
    return [f"{svc}{suffix}" for svc in BASE_SERVICES]


def is_port_open(port, host="localhost"):
    """Check if a port is open on the given host."""
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.settimeout(1)
    try:
        sock.connect((host, port))
        sock.close()
        return True
    except (socket.timeout, ConnectionRefusedError, OSError):
        return False


def start_port_forward():
    """Start kubectl port-forward for Zipkin in the background."""
    print(f"Starting port-forward for Zipkin on port {ZIPKIN_LOCAL_PORT}...")

    # Start port-forward in background
    proc = subprocess.Popen(
        ["kubectl", "port-forward", "deployment/zipkin", "-n", ZIPKIN_NAMESPACE,
         f"{ZIPKIN_LOCAL_PORT}:9411"],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        preexec_fn=os.setpgrp  # Create new process group
    )

    # Wait for port to become available
    max_attempts = 30
    for i in range(max_attempts):
        if is_port_open(ZIPKIN_LOCAL_PORT):
            print(f"Port-forward established on port {ZIPKIN_LOCAL_PORT}")
            return proc
        time.sleep(0.5)

    proc.terminate()
    print("Error: Failed to establish port-forward to Zipkin")
    sys.exit(1)


@contextmanager
def ensure_zipkin_connection():
    """Context manager to ensure Zipkin port is forwarded."""
    port_forward_proc = None
    started_by_us = False

    if not is_port_open(ZIPKIN_LOCAL_PORT):
        port_forward_proc = start_port_forward()
        started_by_us = True
    else:
        print(f"Zipkin already accessible on port {ZIPKIN_LOCAL_PORT}")

    try:
        yield
    finally:
        # Only kill port-forward if we started it
        if started_by_us and port_forward_proc:
            print("Cleaning up port-forward...")
            try:
                os.killpg(os.getpgid(port_forward_proc.pid), signal.SIGTERM)
            except ProcessLookupError:
                pass


def fetch_traces(lookback_ms=604800000, limit=100000):
    """Fetch traces from Zipkin API.

    Args:
        lookback_ms: How far back to look for traces (default: 7 days)
        limit: Maximum number of traces to fetch

    Returns:
        List of traces from Zipkin
    """
    service_name = "activator-service"
    end_timestamp = int(time.time() * 1000)

    url = f"{ZIPKIN_URL}/api/v2/traces"
    params = {
        "serviceName": service_name,
        "lookback": lookback_ms,
        "endTs": end_timestamp,
        "limit": limit
    }

    print(f"Fetching traces from Zipkin...")
    print(f"  URL: {url}")
    print(f"  Lookback: {lookback_ms}ms ({lookback_ms / 86400000:.1f} days)")

    try:
        response = requests.get(url, params=params, timeout=60)
        response.raise_for_status()
        traces = response.json()
        print(f"  Found {len(traces)} traces")
        return traces
    except requests.exceptions.RequestException as e:
        print(f"Error fetching traces: {e}")
        return []


def parse_trace_timings(trace, services):
    """Parse timing information from a single trace.

    Args:
        trace: List of spans in a trace
        services: List of service names to look for

    Returns:
        Dict with service latencies and trace_id, or None if invalid
    """
    # Sort spans by timestamp
    sorted_spans = sorted(trace, key=lambda x: x.get('timestamp', 0))

    # Filter for SERVER kind spans
    server_spans = [x for x in sorted_spans if x.get('kind') == 'SERVER']
    matched_services = [x for x in server_spans if x['localEndpoint']['serviceName'] == 'activator-service']

    row = {}
    for span in matched_services:
        tags = span.get('tags', {})
        host = tags.get('http.host', '')
        status_code = tags.get('http.status_code', '')

        if status_code and status_code != "200":
            print(f"Warning: non-200 status code {status_code} for host {host}")
            return None

        # Extract service name from http.host (format: service-name.namespace.svc.cluster.local)
        if host:
            service_name = host.split('.')[0]
        else:
            # Fallback: extract from localEndpoint.serviceName
            local_svc = span.get('localEndpoint', {}).get('serviceName', '')
            # Format: service-name-00001-deployment-xxx
            parts = local_svc.split('-00')
            if parts:
                service_name = parts[0]
            else:
                continue

        # Duration is in microseconds, convert to milliseconds
        duration_ms = span.get('duration', 0) / 1000
        row[service_name] = duration_ms

    if not row:
        return None

    row['trace_id'] = trace[0].get('traceId', 'unknown')
    return row


def all_keys_exist(row, services):
    """Check if all required service keys exist in the row."""
    for service in services:
        if service not in row:
            return False
    return True


def save_traces(traces, services, artifacts_dir, strategy, rate=None, duration=None):
    """Save traces to JSON and CSV files.

    Args:
        traces: Raw traces from Zipkin
        services: List of service names for this strategy
        artifacts_dir: Directory to save output files
        strategy: Strategy name for file naming
        rate: Request rate for file naming (optional)
        duration: Test duration for file naming (optional)
    """
    os.makedirs(artifacts_dir, exist_ok=True)

    # Build file name prefix with optional rate and duration
    if rate and duration:
        file_prefix = f"{strategy}_{rate}rps_{duration}"
    elif rate:
        file_prefix = f"{strategy}_{rate}rps"
    else:
        file_prefix = strategy

    # Save raw JSON traces
    json_file = os.path.join(artifacts_dir, f"{file_prefix}_traces.json")
    with open(json_file, "w") as f:
        json.dump(traces, f, indent=2)
    print(f"Saved raw traces to: {json_file}")

    # Parse and save CSV
    rows = []
    skipped = 0
    for trace in traces:
        row = parse_trace_timings(trace, services)
        if row:
            rows.append(row)
        else:
            skipped += 1

    print(f"Parsed {len(rows)} valid traces ({skipped} skipped)")

    csv_file = os.path.join(artifacts_dir, f"{file_prefix}_traces.csv")
    with open(csv_file, 'w', newline='') as f:
        writer = csv.writer(f)

        # Header row
        header = ["trace_id"] + services
        writer.writerow(header)

        # Data rows
        complete_rows = 0
        for row in rows:
            if all_keys_exist(row, services):
                ordered_row = [row.get('trace_id', '')] + [row.get(svc, '') for svc in services]
                writer.writerow(ordered_row)
                complete_rows += 1

    print(f"Saved {complete_rows} complete traces to: {csv_file}")
    return len(rows), complete_rows


def clear_zipkin_data():
    """Clear Zipkin data by restarting the Zipkin pod."""
    print(f"Clearing Zipkin data by restarting pod in {ZIPKIN_NAMESPACE}...")

    try:
        # Delete the zipkin pod to restart it (deployment will recreate it)
        result = subprocess.run(
            ["kubectl", "rollout", "restart", "deployment/zipkin", "-n", ZIPKIN_NAMESPACE],
            capture_output=True,
            text=True,
            timeout=30
        )

        if result.returncode != 0:
            print(f"Warning: Failed to restart Zipkin: {result.stderr}")
            return False

        print("Zipkin pod restarted. Waiting for it to be ready...")

        # Wait for the pod to be ready again
        result = subprocess.run(
            ["kubectl", "rollout", "status", "deployment/zipkin", "-n", ZIPKIN_NAMESPACE,
             "--timeout=120s"],
            capture_output=True,
            text=True,
            timeout=150
        )

        if result.returncode == 0:
            print("Zipkin is ready")
            return True
        else:
            print(f"Warning: Zipkin may not be fully ready: {result.stderr}")
            return False

    except subprocess.TimeoutExpired:
        print("Warning: Timeout waiting for Zipkin restart")
        return False
    except Exception as e:
        print(f"Error restarting Zipkin: {e}")
        return False


def parse_args():
    """Parse command line arguments."""
    parser = argparse.ArgumentParser(
        description='Parse Zipkin traces for func-chain benchmark.',
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog='''
Examples:
  %(prog)s knative ./artifacts
  %(prog)s efunction ./artifacts --rate 10 --duration 1m
  %(prog)s both ./artifacts --rate 100 --duration 5m --clear
        '''
    )

    parser.add_argument(
        'strategy',
        choices=list(STRATEGY_SUFFIX.keys()),
        help='Benchmark strategy (determines service name suffixes)'
    )
    parser.add_argument(
        'artifacts_dir',
        help='Directory to save traces and CSV output'
    )
    parser.add_argument(
        '--rate',
        type=str,
        default=None,
        help='Request rate (e.g., 10) for file naming'
    )
    parser.add_argument(
        '--duration',
        type=str,
        default=None,
        help='Test duration (e.g., 1m) for file naming'
    )
    parser.add_argument(
        '--clear',
        action='store_true',
        help='Restart Zipkin pod to clear traces after extraction'
    )

    return parser.parse_args()


def main():
    args = parse_args()

    services = get_services_for_strategy(args.strategy)
    print(f"Strategy: {args.strategy}")
    print(f"Services: {services}")
    print(f"Artifacts directory: {args.artifacts_dir}")
    if args.rate:
        print(f"Rate: {args.rate} rps")
    if args.duration:
        print(f"Duration: {args.duration}")
    print(f"Clear after extraction: {args.clear}")
    print()

    with ensure_zipkin_connection():
        # Fetch traces
        traces = fetch_traces()

        if not traces:
            print("No traces found")
            sys.exit(0)

        # Save traces
        total, complete = save_traces(
            traces, services, args.artifacts_dir,
            args.strategy, args.rate, args.duration
        )

        print()
        print("Summary:")
        print(f"  Total traces fetched: {len(traces)}")
        print(f"  Traces with service matches: {total}")
        print(f"  Complete traces (all services): {complete}")

    # Clear Zipkin data if requested
    if args.clear:
        print()
        clear_zipkin_data()


if __name__ == "__main__":
    main()
