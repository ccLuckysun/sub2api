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
  retired_ds="$(find "$directory" \( -name 'postgres.yml' -o -name 'postgres.yaml' \) -print)" \
    || fail "cannot scan Grafana provisioning directory: $directory"
  [ -z "$retired_ds" ] || fail "retired PostgreSQL datasource artifact ($mode); refusing Grafana startup:
$retired_ds
Grafana is Prometheus-only; inspect and explicitly remove/archive the retired datasource file."
  printf '[dashboard-preflight] PASS: no macOS metadata or retired PostgreSQL datasource in %s.\n' "$directory"
  exit 0
fi
empty_files="$(find "$directory" -type f -name '*.json' -size 0c -print)" \
  || fail "cannot scan dashboard directory: $directory"
[ -z "$empty_files" ] || fail "zero-byte dashboard JSON ($mode); refusing Grafana startup:
$empty_files
Inspect the source and mounted dashboard directory. No files were deleted; see MONITORING_RELEASE.md."
# Retired V2 dashboards must not remain alongside the 3 canonical Prometheus dashboards.
historical="$(find "$directory" \( \
  -name '02-chain.json' -o \
  -name '02-requests.json' -o \
  -name '03-nodes.json' -o \
  -name '04-users.json' -o \
  -name '05-host.json' -o \
  -name '06-request.json' \
\) -print)" || fail "cannot scan historical dashboards: $directory"
[ -z "$historical" ] || fail "retired dashboard artifact ($mode):
$historical
Expected three canonical dashboards (01-overview, 02-nodes-chain, 03-host); inspect and explicitly remove/archive the historical artifact (MONITORING_RELEASE.md)."
for name in 01-overview 02-nodes-chain 03-host; do
  file="$directory/$name.json"
  [ -f "$file" ] && [ -r "$file" ] && [ -s "$file" ] \
    || fail "required dashboard JSON is missing, unreadable or empty: $file"
done
printf '[dashboard-preflight] PASS: three canonical dashboards; no zero-byte JSON or retired artifacts (%s).\n' "$mode"
