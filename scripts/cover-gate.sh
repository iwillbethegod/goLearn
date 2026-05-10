#!/usr/bin/env bash
# cover-gate.sh — fail if total coverage drops below the threshold.
#
# Usage:
#   scripts/cover-gate.sh [threshold]      # default 70
#
# Inputs (read from env, with defaults):
#   COVER_FILE  Path to a `go test -coverprofile` output file.
#               Default: cover.out
#   EXCLUDE_RE  Extended regex of paths to exclude from coverage.
#               Default drops generated code (proto/gen, sqlc pgdb).
#
# Behavior:
#   - Strips lines matching EXCLUDE_RE from the profile (so generated
#     code with 0% doesn't drag the average).
#   - Runs `go tool cover -func` and parses the "total" line.
#   - Exits 0 if total >= threshold, 1 otherwise.
#   - Always prints the per-file table on failure for fast triage.

set -euo pipefail

threshold="${1:-70}"
cover_file="${COVER_FILE:-cover.out}"
exclude_re="${EXCLUDE_RE:-(proto/gen/|/pgdb/)}"

if [[ ! -f "$cover_file" ]]; then
    echo "cover-gate: $cover_file not found" >&2
    echo "  generate it with: go test -coverprofile=$cover_file -coverpkg=./internal/...,./pkg/...,./cmd/api/... ./..." >&2
    exit 2
fi

filtered="$(mktemp -t cover-filtered)"
trap 'rm -f "$filtered"' EXIT

# Keep the "mode:" header line always, then drop matching paths.
head -n1 "$cover_file" > "$filtered"
grep -E -v "$exclude_re" "$cover_file" | tail -n +2 >> "$filtered" || true

total_line="$(go tool cover -func="$filtered" | tail -n1)"
total_pct="$(awk '{print $NF}' <<<"$total_line" | tr -d '%')"

if [[ -z "$total_pct" ]]; then
    echo "cover-gate: failed to parse total coverage from: $total_line" >&2
    exit 2
fi

# Use bc for float comparison; threshold and total may both be floats.
ok="$(awk -v t="$total_pct" -v th="$threshold" 'BEGIN { print (t+0 >= th+0) ? 1 : 0 }')"

if [[ "$ok" == "1" ]]; then
    printf 'cover-gate: %s%% >= %s%% ✓\n' "$total_pct" "$threshold"
    exit 0
fi

printf 'cover-gate: %s%% < %s%% ✗\n\n' "$total_pct" "$threshold" >&2
echo "Per-file breakdown (filtered):" >&2
go tool cover -func="$filtered" >&2
exit 1
