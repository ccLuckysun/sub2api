#!/usr/bin/env bash
# Configure a Compose deployment, preserving existing business secrets and volumes.
set -euo pipefail
umask 077
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"
MONITORING=false
HOST_METRICS=false
BACKUP=false
START=true
APP_IMAGE=""
PROJECT=""
FILES=()
ENV_FILES=()
usage() {
  cat <<'USAGE'
Usage: ./compose-deploy.sh [options]
  --monitoring             Start five core containers: sub2api/postgres/redis/grafana/prometheus
  --host-metrics           Also enable a separate Linux host Node Exporter
  --backup                 Also enable snapshot cron (and its one-shot volume initializer)
  --image IMAGE:TAG        Use a specific application image
  --compose-file PATH      Base Compose file, repeatable; defaults to docker-compose.yml
  --env-file PATH          Business environment file, repeatable; defaults to .env
  --project-name NAME      Preserve an existing Compose project name
  --no-start               Prepare and validate files without starting services
  --help                   Show this help
Existing .env credentials and data volumes are preserved. Commands after setup:
  ./sub2api-compose ps
  ./sub2api-compose up -d
USAGE
}
fail() { echo "[deploy] ERROR: $*" >&2; exit 1; }
while [ "$#" -gt 0 ]; do
  case "$1" in
    --monitoring) MONITORING=true ;;
    --host-metrics) MONITORING=true; HOST_METRICS=true ;;
    --backup) MONITORING=true; BACKUP=true ;;
    --no-start) START=false ;;
    --image|--compose-file|--env-file|--project-name)
      [ "$#" -ge 2 ] || fail "$1 requires a value"
      case "$1" in
        --image) APP_IMAGE="$2" ;;
        --compose-file) FILES+=("$2") ;;
        --env-file) ENV_FILES+=("$2") ;;
        --project-name) PROJECT="$2" ;;
      esac
      shift ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
  shift
done
# Reject damaged source assets before contacting Docker or starting any container.
# Existing wrappers also preserve monitoring when --monitoring is omitted.
if [ -f sub2api-compose ] && grep -q docker-compose.monitoring.yml sub2api-compose; then
  MONITORING=true
fi
if $MONITORING; then
  sh monitoring/grafana/dashboard-preflight.sh --source monitoring/grafana/dashboards
  sh monitoring/grafana/dashboard-preflight.sh --metadata monitoring/grafana/provisioning
fi
command -v docker >/dev/null || fail 'Docker is required'
docker info >/dev/null 2>&1 || fail 'Docker daemon is unavailable'
compose_version="$(docker compose version --short)"
compose_version="${compose_version#v}"
IFS=. read -r compose_major compose_minor _ <<< "$compose_version"
[[ "$compose_major" =~ ^[0-9]+$ && "$compose_minor" =~ ^[0-9]+$ ]] || fail "cannot parse Compose version: $compose_version"
if [ "$compose_major" -lt 2 ] || { [ "$compose_major" -eq 2 ] && [ "$compose_minor" -lt 35 ]; }; then
  fail 'Docker Compose >=2.35 is required'
fi
[ "${#FILES[@]}" -gt 0 ] || FILES=(docker-compose.yml)
for file in "${FILES[@]}"; do [ -f "$file" ] || fail "missing Compose file: $file"; done

# Only controlled, non-secret values are updated here. Never execute .env contents.
setting() {
  awk -F= -v key="$1" '$1 == key {
    v=substr($0,length(key)+2); sub(/\r$/, "", v)
    if (v ~ /^".*"$/ || v ~ /^\047.*\047$/) v=substr(v,2,length(v)-2)
    else sub(/[ \t]+#.*/, "", v)
  } END {print v}' monitoring.env 2>/dev/null || true
}
set_setting() {
  local temp
  temp="$(mktemp ./monitoring.env.tmp.XXXXXX)"
  awk -v key="$1" 'index($0,key "=") != 1 {print}' monitoring.env > "$temp"
  printf '%s=%s\n' "$1" "$2" >> "$temp"
  mv "$temp" monitoring.env
}
COMPOSE=(docker compose --project-directory "$SCRIPT_DIR")
[ -z "$PROJECT" ] || COMPOSE+=(-p "$PROJECT")
if [ "${#ENV_FILES[@]}" -eq 0 ] && [ -f .env ]; then ENV_FILES=(.env); fi
if [ "${#ENV_FILES[@]}" -gt 0 ]; then
  for file in "${ENV_FILES[@]}"; do
    [ -f "$file" ] || fail "missing environment file: $file"
    COMPOSE+=(--env-file "$file")
  done
fi
if $MONITORING; then
  # A provisioning bind hides the image's optional directories. Create them
  # for file-by-file installers too, avoiding Grafana's startup read errors.
  mkdir -p monitoring/grafana/provisioning/plugins monitoring/grafana/provisioning/alerting
  # Installers use umask 077 for secrets. Non-root Grafana/Prometheus must
  # still be able to read the explicitly managed public configuration assets.
  for dir in monitoring monitoring/grafana monitoring/grafana/provisioning \
      monitoring/grafana/provisioning/datasources monitoring/grafana/provisioning/dashboards \
      monitoring/grafana/provisioning/plugins monitoring/grafana/provisioning/alerting \
      monitoring/grafana/dashboards monitoring/backup monitoring/prometheus \
      monitoring/prometheus/rules monitoring/alertmanager monitoring/alertmanager/templates \
      monitoring/sql; do
    [ ! -d "$dir" ] || chmod 755 "$dir"
  done
  for file in monitoring/prometheus.yml.tmpl \
      monitoring/prometheus/rules/*.yml \
      monitoring/alertmanager/alertmanager.yml.tmpl \
      monitoring/alertmanager/templates/*.tmpl \
      monitoring/sql/*.sql \
      monitoring/grafana/provisioning/datasources/prometheus.yml \
      monitoring/grafana/provisioning/datasources/postgres.yml \
      monitoring/grafana/provisioning/dashboards/dashboards.yml \
      monitoring/grafana/dashboard-preflight.sh \
      monitoring/grafana/dashboards/*.json; do
    [ ! -f "$file" ] || chmod 644 "$file"
  done
  [ -f monitoring.env ] || : > monitoring.env
  chmod 600 monitoring.env
  if [ -z "$(setting GRAFANA_ADMIN_PASSWORD)" ]; then
    command -v openssl >/dev/null || fail 'openssl is required to generate the Grafana password'
    set_setting GRAFANA_ADMIN_PASSWORD "$(openssl rand -hex 24)"
  fi
  if [ -z "$(setting GRAFANA_PG_PASSWORD)" ]; then
    command -v openssl >/dev/null || fail 'openssl is required to generate the Grafana PG password'
    set_setting GRAFANA_PG_PASSWORD "$(openssl rand -hex 24)"
  fi
  # Keep previously enabled extensions on subsequent upgrades.
  [ "$(setting MONITORING_HOST_METRICS_ENABLED)" != true ] || HOST_METRICS=true
  [ "$(setting MONITORING_BACKUP_ENABLED)" != true ] || BACKUP=true
  if $HOST_METRICS; then
    [ "$(docker info --format '{{.OSType}}')" = linux ] || fail 'host metrics require a Linux Docker engine'
    NODE_EXPORTER_HOST_IP="$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}')"
    [ -n "$NODE_EXPORTER_HOST_IP" ] || fail 'Docker default bridge has no gateway'
    # On Desktop the host is the Linux VM, not the macOS/Windows physical host.
    set_setting NODE_EXPORTER_HOST_IP "$NODE_EXPORTER_HOST_IP"
    COMPOSE+=(--profile host-metrics)
  fi
  MONITORING_HOST_METRICS_ENABLED="$HOST_METRICS"
  export MONITORING_HOST_METRICS_ENABLED
  [ -z "${NODE_EXPORTER_HOST_IP:-}" ] || export NODE_EXPORTER_HOST_IP
  set_setting MONITORING_HOST_METRICS_ENABLED "$HOST_METRICS"
  set_setting MONITORING_BACKUP_ENABLED "$BACKUP"
  if $BACKUP; then
    api_version="$(docker version --format '{{.Server.APIVersion}}')"
    IFS=. read -r api_major api_minor <<< "$api_version"
    if [ "$api_major" -lt 1 ] || { [ "$api_major" -eq 1 ] && [ "$api_minor" -lt 45 ]; }; then
      fail 'backup volume.subpath requires Docker Engine >=26 (API >=1.45)'
    fi
    COMPOSE+=(--profile backup)
  fi
  COMPOSE+=(--env-file monitoring.env)
fi
for file in "${FILES[@]}"; do COMPOSE+=(-f "$file"); done
if [ -n "$APP_IMAGE" ]; then
  [[ "$APP_IMAGE" =~ ^[A-Za-z0-9][A-Za-z0-9./_:@-]*$ ]] || fail 'invalid application image reference'
  # A persistent override avoids regex edits to production Compose/Caddy files.
  temp="$(mktemp ./docker-compose.release.yml.tmp.XXXXXX)"
  printf 'services:\n  sub2api:\n    image: "%s"\n' "$APP_IMAGE" > "$temp"
  mv "$temp" docker-compose.release.yml
fi
[ ! -f docker-compose.release.yml ] || COMPOSE+=(-f docker-compose.release.yml)
PROM_CONFIG_CHANGED=false
if $MONITORING; then
  before="$(cksum monitoring/prometheus.yml 2>/dev/null || true)"
  [ -f docker-compose.monitoring.yml ] || fail 'missing monitoring overlay'
  MONITORING_ENV_FILE="$SCRIPT_DIR/monitoring.env" ./monitoring/render-prometheus-config.sh
  after="$(cksum monitoring/prometheus.yml)"
  [ "$before" = "$after" ] || PROM_CONFIG_CHANGED=true
  COMPOSE+=(-f docker-compose.monitoring.yml)
fi
"${COMPOSE[@]}" config --quiet

# Persist the exact files, env-file order and project. Plain `docker compose`
# would otherwise forget the monitoring overlay on the next application update.
wrapper="$(mktemp ./sub2api-compose.tmp.XXXXXX)"
{
  printf '#!/usr/bin/env bash\nset -euo pipefail\ncd -- %q\n' "$SCRIPT_DIR"
  if $MONITORING; then
    printf '%s\n' 'for arg in "$@"; do' \
      '  case "$arg" in up|start|restart)' \
      '    sh monitoring/grafana/dashboard-preflight.sh --source monitoring/grafana/dashboards' \
      '    sh monitoring/grafana/dashboard-preflight.sh --metadata monitoring/grafana/provisioning; break ;;' \
      '  esac' 'done'
  fi
  printf 'exec '
  printf '%q ' "${COMPOSE[@]}"
  printf '"$@"\n'
} > "$wrapper"
chmod 700 "$wrapper"
mv "$wrapper" sub2api-compose
if $START; then
  if $MONITORING && $PROM_CONFIG_CHANGED && [ -n "$(./sub2api-compose ps -q prometheus)" ]; then
    # A file bind mount otherwise continues to reference the replaced inode.
    ./sub2api-compose up -d --wait --wait-timeout 180 --force-recreate prometheus
  fi
  if $MONITORING; then
    # Installers/copy tools may replace file inodes. Keep volume data, but rebuild
    # Grafana's file bind mounts; the provider interval alone cannot refresh them.
    ./sub2api-compose up -d --wait --wait-timeout 180 --force-recreate grafana
  fi
  ./sub2api-compose up -d --wait --wait-timeout 180
  if $MONITORING; then
    # Provision/refresh the read-only Grafana role. psql runs as the application
    # superuser inside the postgres container; password never appears in argv.
    pg_user="$(./sub2api-compose exec -T postgres printenv POSTGRES_USER 2>/dev/null | tr -d '\r')"
    pg_db="$(./sub2api-compose exec -T postgres printenv POSTGRES_DB 2>/dev/null | tr -d '\r')"
    pg_user="${pg_user:-sub2api}"
    pg_db="${pg_db:-sub2api}"
    ./sub2api-compose exec -T postgres \
      psql -X -v ON_ERROR_STOP=1 -U "$pg_user" -d "$pg_db" \
      -v password="$(setting GRAFANA_PG_PASSWORD)" \
      < monitoring/sql/grafana-ro.sql
    echo '[deploy] Grafana read-only role provisioned (grafana_ro).'
  fi
  echo '[deploy] Services are ready. Use ./sub2api-compose for subsequent commands.'
else
  echo '[deploy] Configuration validated; services were not started.'
fi
if $MONITORING; then
  echo '[deploy] Grafana: http://127.0.0.1:<GRAFANA_PORT, default 3000> (admin)'
  echo '[deploy] Grafana password is stored in monitoring.env (mode 0600); existing passwords are preserved.'
fi
