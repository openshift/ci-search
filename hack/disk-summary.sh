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

# measure.sh runs on the target (pod or local host) and emits a single line of
# space-separated raw numbers that the caller formats into a table.
read -r -d '' MEASURE <<MEASURE_EOF || true
B="${JOBS_PATH}"
if [ ! -d "\$B" ]; then echo "MISSING"; exit 0; fi
total_dirs=\$(find "\$B" -type d | wc -l)
empty_dirs=\$(find "\$B" -type d -empty | wc -l)
empty_stale=\$(find "\$B" -type d -empty -mmin +${GRACE_MIN} | wc -l)
total_files=\$(find "\$B" -type f | wc -l)
stale_files=\$(find "\$B" -type f -mtime +${MAX_AGE_DAYS} | wc -l)
size_bytes=\$(du -sb "\$B" 2>/dev/null | cut -f1)
oldest=\$(find "\$B" -type d -empty -printf '%TY-%Tm-%Td\n' 2>/dev/null | sort | head -1)
echo "\$total_dirs \$empty_dirs \$empty_stale \$total_files \$stale_files \$size_bytes \${oldest:-none}"
MEASURE_EOF

if [[ "$LOCAL" == true ]]; then
  RAW=$(bash -c "$MEASURE")
else
  RAW=$(oc -n "$NAMESPACE" rsh "$POD" sh -c "$MEASURE" 2>/dev/null)
fi

if [[ "$RAW" == "MISSING" || -z "$RAW" ]]; then
  echo "error: jobs path '$JOBS_PATH' not found on target" >&2
  exit 1
fi

read -r total_dirs empty_dirs empty_stale total_files stale_files size_bytes oldest <<<"$RAW"

# Convert bytes to GiB with two decimals.
size_gb=$(awk -v b="${size_bytes:-0}" 'BEGIN { printf "%.2f", b/1024/1024/1024 }')

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
