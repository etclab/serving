#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

$SCRIPT_DIR/collect-queue-logs.sh
$SCRIPT_DIR/collect-user-logs.sh

