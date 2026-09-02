#!/usr/bin/env bash

# disk-summary.sh - print a summary of the ci-search jobs cache on disk.
#
# Reports directory/file counts, how many empty directories remain (the target
# of the OCPCRT-639 empty-directory GC), and the total size on disk. By default
# it runs the measurement inside the search-0 pod via `oc rsh`; pass --local to
# measure a path on the current host instead.
#
# Usage:
#   hack/disk-summary.sh                       # oc rsh crt-argocd/search-0
#   hack/disk-summary.sh -n NS -p POD          # different namespace/pod
#   hack/disk-summary.sh --path /some/jobs     # override the jobs path
#   hack/disk-summary.sh --local --path DIR    # measure a local directory

set -euo pipefail

NAMESPACE="crt-argocd"
POD="search-0"
JOBS_PATH="/var/lib/ci-search/jobs"
LOCAL=false
GRACE_MIN=30   # matches emptyDirGracePeriod in cmd/search/pathindex.go
MAX_AGE_DAYS=14 # matches the --max-age default

while [[ $# -gt 0 ]]; do
  case "$1" in
    -n|--namespace) NAMESPACE="$2"; shift 2 ;;
    -p|--pod)       POD="$2"; shift 2 ;;
    --path)         JOBS_PATH="$2"; shift 2 ;;
    --local)        LOCAL=true; shift ;;
    -h|--help)      sed -n '3,20p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

# MEASURE runs on the target (pod or local host) and emits a single line of
# space-separated raw numbers that the caller formats into a table. The jobs
# path and thresholds are passed as positional arguments ($1 path, $2 grace
# minutes, $3 max-age days) rather than interpolated into the shell text, so a
# path with shell metacharacters can never be executed. It fails closed: any
# find/du/pipeline failure aborts (set -e plus pipefail where supported) so the
# caller sees empty output instead of a bogus zero.
read -r -d '' MEASURE <<'MEASURE_EOF' || true
set -eu
if (set -o pipefail) 2>/dev/null; then set -o pipefail; else echo "pipefail unsupported" >&2; exit 1; fi
B="$1"; grace_min="$2"; max_age_days="$3"
if [ ! -d "$B" ]; then echo "MISSING"; exit 0; fi
total_dirs=$(find "$B" -type d | wc -l)
empty_dirs=$(find "$B" -type d -empty | wc -l)
empty_stale=$(find "$B" -type d -empty -mmin +$((grace_min - 1)) | wc -l)
total_files=$(find "$B" -type f | wc -l)
stale_files=$(find "$B" -type f -mtime +$((max_age_days - 1)) | wc -l)
size_bytes=$(du -s --block-size=1 "$B" | cut -f1)
# Use awk 'NR==1' rather than 'head -1' to grab the first line: head closes the
# pipe after one line, which makes sort exit with SIGPIPE (141) and, under
# pipefail, aborts the whole script before the echo below.
oldest=$(find "$B" -type d -empty -printf '%TY-%Tm-%Td\n' | sort | awk 'NR==1')
echo "$total_dirs $empty_dirs $empty_stale $total_files $stale_files $size_bytes ${oldest:-none}"
MEASURE_EOF

# Clear RAW on failure so the error-reporting logic below stays reachable:
# without the `|| RAW=""`, a failing command substitution would abort the whole
# script via set -e before we can print a useful message.
if [[ "$LOCAL" == true ]]; then
  RAW=$(bash -c "$MEASURE" measure "$JOBS_PATH" "$GRACE_MIN" "$MAX_AGE_DAYS") || RAW=""
else
  RAW=$(oc -n "$NAMESPACE" rsh "$POD" sh -c "$MEASURE" measure "$JOBS_PATH" "$GRACE_MIN" "$MAX_AGE_DAYS" 2>/dev/null) || RAW=""
fi

# `oc rsh` allocates a PTY, so the target's output comes back with CRLF line
# endings. Strip carriage returns before validating; otherwise fields like
# `oldest` arrive as "2026-08-18\r" and fail the strict format checks below.
RAW=${RAW//$'\r'/}

if [[ "$RAW" == "MISSING" || -z "$RAW" ]]; then
  echo "error: jobs path '$JOBS_PATH' not found or could not be measured on target" >&2
  exit 1
fi

read -r total_dirs empty_dirs empty_stale total_files stale_files size_bytes oldest extra <<<"$RAW"

# Fail closed: require exactly seven fields, with the five counts and the byte
# size being non-negative integers and oldest being "none" or a valid ISO date,
# before formatting. This prevents empty/garbled measurement output from being
# rendered as "0.00 GiB" or "none". The trailing `extra` field captures any
# surplus tokens; if it is non-empty the output had too many fields and is
# rejected.
for v in "$total_dirs" "$empty_dirs" "$empty_stale" "$total_files" "$stale_files" "$size_bytes"; do
  if ! [[ "$v" =~ ^[0-9]+$ ]]; then
    echo "error: invalid measurement output from target: '$RAW'" >&2
    exit 1
  fi
done
if [[ -n "$extra" ]]; then
  echo "error: invalid measurement output from target: '$RAW'" >&2
  exit 1
fi
if ! [[ "$oldest" == "none" || "$oldest" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
  echo "error: invalid measurement output from target: '$RAW'" >&2
  exit 1
fi

# Convert bytes to GiB with two decimals.
size_gb=$(awk -v b="$size_bytes" 'BEGIN { printf "%.2f", b/1024/1024/1024 }')

# Group large integers with thousands separators for readability.
group() { printf "%'d" "$1" 2>/dev/null || echo "$1"; }

if [[ "$LOCAL" == true ]]; then
  src="local:${JOBS_PATH}"
else
  src="${NAMESPACE}/${POD}:${JOBS_PATH}"
fi

printf '\n  ci-search jobs cache summary — %s\n\n' "$src"
printf '  %-32s %14s\n' "Metric" "Count"
printf '  %-32s %14s\n' "--------------------------------" "--------------"
printf "  %-32s %14s\n" "Total directories"                 "$(group "$total_dirs")"
printf "  %-32s %14s\n" "Empty directories"                 "$(group "$empty_dirs")"
printf "  %-32s %14s\n" "Empty dirs older than ${GRACE_MIN}m"       "$(group "$empty_stale")"
printf "  %-32s %14s\n" "Total files"                       "$(group "$total_files")"
printf "  %-32s %14s\n" "Files older than ${MAX_AGE_DAYS} days"          "$(group "$stale_files")"
printf '  %-32s %14s\n' "--------------------------------" "--------------"
printf "  %-32s %14s\n" "Total size on disk"                "${size_gb} GiB"
printf "  %-32s %14s\n" "Oldest empty directory"            "$oldest"
printf '\n'
