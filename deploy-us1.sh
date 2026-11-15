#!/usr/bin/env bash
# Build locally, upload, back up the database and deploy the existing us1 project.
# Default: five core services (existing sub2api/postgres/redis + grafana/prometheus).
# --app-only keeps legacy application-only upgrades, preserving existing monitoring.
set -euo pipefail
REPO_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$REPO_DIR"
REMOTE_HOST="${REMOTE_HOST:-us1}"
IMAGE_NAME="${IMAGE_NAME:-sun/sub2api}"
APP_CONTAINER="${APP_CONTAINER:-sub2api}"
DB_CONTAINER="${DB_CONTAINER:-sub2api-postgres}"
MODE=monitoring
VERSION=""
usage() {
  cat <<'USAGE'
Usage: ./deploy-us1.sh [VERSION] [--app-only]
Default: build linux/amd64, upload, pg_dump backup, retain rollback image, deploy
Sub2API + Prometheus + Grafana into the existing Compose project and check health.
Existing PostgreSQL/Redis/Caddy containers, data and .env credentials are preserved.
--app-only: update the application, keeping any already-enabled monitoring.
Environment overrides: REMOTE_HOST, IMAGE_NAME, APP_CONTAINER, DB_CONTAINER.
USAGE
}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --app-only) MODE=app-only ;;
    --help|-h) usage; exit 0 ;;
    -*) echo "Unknown option: $1" >&2; exit 1 ;;
    *) [ -z "$VERSION" ] || { echo 'Only one version is allowed' >&2; exit 1; }; VERSION="$1" ;;
  esac
  shift
done
if [ -z "$VERSION" ]; then read -r -p '请输入版本号（例如 0.0.4）: ' VERSION; fi
VERSION="$(printf '%s' "$VERSION" | tr -d '[:space:]')"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$ ]] || { echo 'Invalid version: expected x.y.z[-suffix]' >&2; exit 1; }
# SSH runs a remote shell: reject shell metacharacters in remotely passed values.
[[ "$REMOTE_HOST" =~ ^[A-Za-z0-9_][A-Za-z0-9_@.-]*$ && "$IMAGE_NAME" =~ ^[A-Za-z0-9][A-Za-z0-9./_:-]*$ && "$APP_CONTAINER" =~ ^[A-Za-z0-9_.-]+$ && "$DB_CONTAINER" =~ ^[A-Za-z0-9_.-]+$ ]] || { echo 'Invalid deployment target' >&2; exit 1; }
docker info >/dev/null 2>&1 || { echo 'Docker daemon is unavailable' >&2; exit 1; }
ssh -o BatchMode=yes -o ConnectTimeout=10 "$REMOTE_HOST" true
FULL_IMAGE="${IMAGE_NAME}:${VERSION}"
COMMIT="$(git rev-parse --short HEAD)"
LOCAL_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sub2api-deploy.XXXXXX")"
cleanup() { rm -rf "$LOCAL_DIR"; }
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
BUILDER=desktop-linux
if ! docker buildx inspect "$BUILDER" >/dev/null 2>&1; then
  BUILDER=multiarch
  docker buildx inspect "$BUILDER" >/dev/null 2>&1 || docker buildx create --name "$BUILDER" >/dev/null
fi
docker buildx build --builder "$BUILDER" --platform linux/amd64 \
  --build-arg GOPROXY=https://goproxy.cn,direct \
  --build-arg GOSUMDB=sum.golang.google.cn \
  --build-arg VERSION="$VERSION" --build-arg COMMIT="$COMMIT" \
  -t "$FULL_IMAGE" --load -f Dockerfile .
docker save "$FULL_IMAGE" | gzip > "$LOCAL_DIR/image.tar.gz"
# Enumerated files ensure private monitoring.env, generated config and backup data
# can never be accidentally uploaded from the developer's workstation.
tar -czf "$LOCAL_DIR/monitoring-assets.tar.gz" -C deploy \
  compose-deploy.sh docker-compose.monitoring.yml monitoring.env.example \
  monitoring/render-prometheus-config.sh monitoring/prometheus.yml.tmpl \
  monitoring/prometheus/rules monitoring/alertmanager monitoring/sql \
  monitoring/backup/snapshot.sh monitoring/backup/crontab.example \
  monitoring/grafana/provisioning monitoring/grafana/dashboards
REMOTE_DIR="$(ssh "$REMOTE_HOST" mktemp -d /tmp/sub2api-deploy.XXXXXX)"
[[ "$REMOTE_DIR" =~ ^/tmp/sub2api-deploy\.[A-Za-z0-9]+$ ]] || { echo 'Unexpected remote staging path' >&2; exit 1; }
scp -q "$LOCAL_DIR/image.tar.gz" "$LOCAL_DIR/monitoring-assets.tar.gz" deploy/remote-deploy.sh "$REMOTE_HOST:$REMOTE_DIR/"
ssh "$REMOTE_HOST" bash "$REMOTE_DIR/remote-deploy.sh" "$VERSION" "$REMOTE_DIR" "$MODE" "$APP_CONTAINER" "$DB_CONTAINER" "$IMAGE_NAME"
printf '\nDeployed %s to %s.\n' "$FULL_IMAGE" "$REMOTE_HOST"
printf 'Grafana tunnel: ssh -L 3000:127.0.0.1:3000 %s\n' "$REMOTE_HOST"
echo 'Manage updates with ./sub2api-compose in the remote Compose directory.'
