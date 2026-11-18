#!/usr/bin/env bash
# Remote half of deploy-us1.sh. Do not invoke against a server during local tests.
set -Eeuo pipefail
umask 077
VERSION="${1:?version required}"
STAGING="${2:?staging directory required}"
MODE="${3:-monitoring}"
APP="${4:-sub2api}"
DB_CONTAINER="${5:-sub2api-postgres}"
IMAGE_NAME="${6:-sun/sub2api}"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$ ]] || exit 1
case "$MODE" in monitoring|app-only) ;; *) echo 'Invalid deployment mode' >&2; exit 1 ;; esac
FULL_IMAGE="${IMAGE_NAME}:${VERSION}"
OLD_IMAGE="$(docker inspect "$APP" --format '{{.Config.Image}}')"
OLD_IMAGE_ID="$(docker inspect "$APP" --format '{{.Image}}')"
WORKDIR="$(docker inspect "$APP" --format '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}')"
PROJECT="$(docker inspect "$APP" --format '{{ index .Config.Labels "com.docker.compose.project" }}')"
CONFIG_FILES="$(docker inspect "$APP" --format '{{ index .Config.Labels "com.docker.compose.project.config_files" }}')"
ENV_FILES="$(docker inspect "$APP" --format '{{ index .Config.Labels "com.docker.compose.project.environment_file" }}')"
[ -d "$WORKDIR" ] && [ -n "$PROJECT" ] && [ "$PROJECT" != '<no value>' ] || {
  echo 'Cannot locate the existing Compose project; no deployment changes applied.' >&2; exit 1;
}
cd "$WORKDIR"
ARGS=(--no-start --image "$FULL_IMAGE" --project-name "$PROJECT")
IFS=',' read -r -a EXISTING_FILES <<< "$CONFIG_FILES"
BASE_COUNT=0
for file in "${EXISTING_FILES[@]}"; do
  case "$(basename "$file")" in
    docker-compose.release.yml) continue ;;
    docker-compose.monitoring.yml)
      # --app-only preserves monitoring on an already monitored deployment.
      ARGS+=(--monitoring)
      continue ;;
  esac
  [ -f "$file" ] || { echo "Missing active Compose file: $file" >&2; exit 1; }
  ARGS+=(--compose-file "$file")
  BASE_COUNT=$((BASE_COUNT + 1))
done
if [ -n "$ENV_FILES" ] && [ "$ENV_FILES" != '<no value>' ]; then
  IFS=',' read -r -a EXISTING_ENV_FILES <<< "$ENV_FILES"
  for file in "${EXISTING_ENV_FILES[@]}"; do ARGS+=(--env-file "$file"); done
fi
[ "$BASE_COUNT" -gt 0 ] || { echo 'No base Compose file found' >&2; exit 1; }
[ "$MODE" != monitoring ] || ARGS+=(--monitoring)
RETIRED_DASHBOARD=monitoring/grafana/dashboards/02-chain.json
if [[ -L "$RETIRED_DASHBOARD" || ( -e "$RETIRED_DASHBOARD" && ! -f "$RETIRED_DASHBOARD" ) ]]; then
  echo "Cannot archive unexpected file type: $WORKDIR/$RETIRED_DASHBOARD" >&2
  exit 1
fi
STAMP="$(date +%Y%m%dT%H%M%S)-$$"
ROLLBACK_IMAGE="${IMAGE_NAME}:rollback-${STAMP}"
# Tag the running image ID before loading: redeploying the same tag must not
# accidentally tag the newly loaded image as the rollback image.
docker tag "$OLD_IMAGE_ID" "$ROLLBACK_IMAGE"
echo "==> Loading $FULL_IMAGE (previous image: $OLD_IMAGE)"
docker load < "$STAGING/image.tar.gz"
echo '==> Database backup'
DUMP="${SUB2API_BACKUP_DIR:-/root}/sub2api-backup-${STAMP}.dump"
docker exec "$DB_CONTAINER" sh -ec 'exec pg_dump -U "${POSTGRES_USER:-sub2api}" -d "${POSTGRES_DB:-sub2api}" -Fc' > "$DUMP"
[ -s "$DUMP" ] || { echo 'Empty database backup; deployment aborted' >&2; exit 1; }

# Back up only managed configuration, never runtime TSDB/business data.
CONFIG_BACKUP="$WORKDIR/sub2api-deploy-config-${STAMP}.tar.gz"
SAVED=()
for path in compose-deploy.sh docker-compose.release.yml sub2api-compose monitoring.env docker-compose.monitoring.yml monitoring/render-prometheus-config.sh monitoring/prometheus.yml.tmpl monitoring/prometheus.yml monitoring/prometheus/rules monitoring/alertmanager monitoring/sql monitoring/backup/snapshot.sh monitoring/grafana; do
  [ ! -e "$path" ] || SAVED+=("$path")
done
if [ "${#SAVED[@]}" -gt 0 ]; then
  COPYFILE_DISABLE=1 tar --exclude='._*' --exclude='.DS_Store' -czf "$CONFIG_BACKUP" "${SAVED[@]}"
fi
if [ -f "$RETIRED_DASHBOARD" ]; then
  RETIRED_DIR="$(mktemp -d "$WORKDIR/sub2api-retired-dashboards-${STAMP}.XXXXXX")"
  mv -- "$RETIRED_DASHBOARD" "$RETIRED_DIR/"
  printf 'Archived retired dashboard: %s/02-chain.json\n' "$RETIRED_DIR"
fi
# Reject platform metadata on extraction as well, for older uploaded bundles.
COPYFILE_DISABLE=1 tar --exclude='._*' --exclude='.DS_Store' -xzf "$STAGING/monitoring-assets.tar.gz" -C "$WORKDIR"
chmod u+x compose-deploy.sh monitoring/backup/snapshot.sh
./compose-deploy.sh "${ARGS[@]}"
ROLLBACK_SCRIPT="$WORKDIR/sub2api-rollback-${STAMP}.sh"
ROLLBACK_ARGS=("${ARGS[@]}")
ROLLBACK_ARGS[2]="$ROLLBACK_IMAGE"
{
  printf '#!/usr/bin/env bash\nset -euo pipefail\ncd -- %q\n' "$WORKDIR"
  printf './compose-deploy.sh '; printf '%q ' "${ROLLBACK_ARGS[@]}"; printf '\n'
  printf './sub2api-compose up -d --no-deps --wait --wait-timeout 180 sub2api\n'
} > "$ROLLBACK_SCRIPT"
chmod 700 "$ROLLBACK_SCRIPT"
deployment_failed() {
  local status=$?
  trap - ERR
  echo 'Deployment health check failed.' >&2
  docker logs --tail 30 "$APP" || true
  printf 'Rollback application: bash %q\n' "$ROLLBACK_SCRIPT"
  echo "Configuration backup: $CONFIG_BACKUP; database backup: $DUMP"
  exit "$status"
}
trap deployment_failed ERR
echo '==> Recreating application; existing PostgreSQL/Redis/Caddy are untouched'
ACTIVE_SERVICES="$(./sub2api-compose config --services)"
./sub2api-compose up -d --no-deps --wait --wait-timeout 180 --force-recreate sub2api
if grep -qx grafana <<< "$ACTIVE_SERVICES"; then
  echo '==> Recreating monitoring services and waiting for readiness'
  SERVICES=(prometheus alertmanager)
  if grep -qx node-exporter <<< "$ACTIVE_SERVICES"; then SERVICES+=(node-exporter); fi
  if grep -qx backup <<< "$ACTIVE_SERVICES"; then
    ./sub2api-compose run --rm --no-deps -T snapshots-init
  fi
  ./sub2api-compose up -d --no-deps --wait --wait-timeout 180 --force-recreate "${SERVICES[@]}"
  SERVICES=(grafana)
  if grep -qx backup <<< "$ACTIVE_SERVICES"; then SERVICES+=(backup); fi
  ./sub2api-compose up -d --no-deps --wait --wait-timeout 180 --force-recreate "${SERVICES[@]}"
  ./compose-deploy.sh --check-monitoring
  ./compose-deploy.sh --setup-grafana-db
  echo 'Grafana listens on server loopback only. Use an SSH tunnel; password is in monitoring.env.'
fi
trap - ERR
rm -f "$STAGING/image.tar.gz" "$STAGING/monitoring-assets.tar.gz"
printf 'Rollback application: bash %q\n' "$ROLLBACK_SCRIPT"
echo "Deployment complete. Rollback image: $ROLLBACK_IMAGE; database backup: $DUMP"
