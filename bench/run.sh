#!/usr/bin/env bash
#
# Run the soak harness over a case directory.
#
#   ./bench/run.sh bench/cases
#
# This is the entry point required by CONTRIBUTING.md: a prompt change is only
# mergeable with a benchmark delta, and this script produces it.
set -euo pipefail

cd "$(dirname "$0")/.."
exec go run ./cmd/cite soak "${1:-bench/cases}"
