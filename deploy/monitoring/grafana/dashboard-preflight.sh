#!/bin/sh
# Read-only checks, shared by deployment preparation and Grafana's entrypoint.
# Do not delete volume files here: an operator must inspect historical artifacts.
set -eu
fail() { printf '[dashboard-preflight] ERROR: %s\n' "$*" >&2; exit 1; }
[ "$#" -eq 2 ] || fail 'usage: dashboard-preflight.sh --source|--volume|--metadata DIRECTORY'
mode="$1"
directory="$2"
case "$mode" in --source|--volume|--metadata) ;; *) fail "unknown mode: $mode" ;; esac
[ -d "$directory" ] || fail "Grafana asset directory is missing: $directory"
# Grafana's readers can parse nonempty AppleDouble .json/.yml as config.
# Packaging exclusions do not sanitize artifacts already persisted in a volume.
metadata="$(find "$directory" \( -name '._*' -o -name '.DS_Store' \) -print)" \
  || fail "cannot scan Grafana metadata: $directory"
[ -z "$metadata" ] || fail "macOS metadata artifact ($mode); refusing Grafana startup:
$metadata
Inspect and explicitly clean the source or mounted volume. No files were deleted."
if [ "$mode" = --metadata ]; then
  printf '[dashboard-preflight] PASS: no macOS metadata in %s.\n' "$directory"
  exit 0
fi
empty_files="$(find "$directory" -type f -name '*.json' -size 0c -print)" \
  || fail "cannot scan dashboard directory: $directory"
[ -z "$empty_files" ] || fail "zero-byte dashboard JSON ($mode); refusing Grafana startup:
$empty_files
Inspect the source and persistent grafana_data volume. No files were deleted; see MONITORING_RELEASE.md."
# 02-chain was superseded by 02-requests, not an optional seventh dashboard.
# File bind mounts do not hide unrelated artifacts in the persistent volume.
historical="$(find "$directory" -name '02-chain.json' -print)" \
  || fail "cannot scan historical dashboards: $directory"
[ -z "$historical" ] || fail "retired 02-chain.json artifact ($mode):
$historical
Expected six canonical dashboards; inspect and explicitly remove/migrate the historical artifact (MONITORING_RELEASE.md)."
for name in 01-overview 02-requests 03-nodes 04-users 05-host 06-request; do
  file="$directory/$name.json"
  [ -f "$file" ] && [ -r "$file" ] && [ -s "$file" ] \
    || fail "required dashboard JSON is missing, unreadable or empty: $file"
done
printf '[dashboard-preflight] PASS: six canonical dashboards; no zero-byte JSON or retired chain artifact (%s).\n' "$mode"
