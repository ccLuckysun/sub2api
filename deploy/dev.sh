#!/usr/bin/env bash
# Local Docker development entrypoint: one-key start/stop for the dev stack.
#
# Stack: docker-compose.dev.yml (builds sub2api from ../ source, binds ./data)
#        + docker-compose.monitoring.yml (Grafana/Prometheus/Alertmanager).
#        Environment comes from .env and monitoring.env, in that order.
#
# Typical use:
#   ./dev.sh up                 # build + start everything, then print URLs
#   ./dev.sh up --no-build      # reuse the existing image
#   ./dev.sh status             # containers, health, published ports
#   ./dev.sh logs sub2api -f    # follow one service
#   ./dev.sh rebuild sub2api    # recompile after Go changes
#   ./dev.sh stop               # stop containers, keep data
#   ./dev.sh down               # remove containers, keep volumes
set -euo pipefail
umask 022

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
cd "$SCRIPT_DIR"

DEV_FILE="docker-compose.dev.yml"
MON_FILE="docker-compose.monitoring.yml"
ENV_FILE="${SUB2API_DEV_ENV_FILE:-monitoring.env}"
APP_PORT="${SERVER_PORT:-8080}"
GRAFANA_PORT_VALUE="${GRAFANA_PORT:-3000}"
MONITORING=true
PROFILES=()
ENV_FILES=()
FILES=(-f "$DEV_FILE" -f "$MON_FILE")

usage() {
  cat <<'USAGE'
Usage: ./dev.sh <command> [options]

  up [--no-build] [--app-only] [--foreground] [--ignore-ports]
        Build (unless --no-build) and start the stack in the background.
        --app-only      only sub2api + postgres + redis (no Grafana/Prometheus)
        --foreground    use `up` without -d, stream logs, Ctrl+C stops it
        --ignore-ports  start even if 8080/3000 are already held (they will fail)
  stop                  Stop containers and keep them (fast to start again)
  down                  Remove containers and networks; volumes are preserved
  restart [service...]  Restart containers as-is (no rebuild); default: all
  rebuild [service...]  Rebuild image(s) and recreate (use after Go changes)
  status                Containers, health and published ports
  logs [service] [-f] [--lines N]
                        Read logs; default service is sub2api
  shell [service]       Open /bin/sh inside a container (default: sub2api)
  config                Validate the merged Compose configuration
  reset --yes           Remove containers AND volumes (deletes local data)
  url                   Print panel URLs and where the credentials live
  help                  Show this help

Environment overrides: SUB2API_DEV_ENV_FILE, SERVER_PORT, GRAFANA_PORT,
NO_PROXY/PROXY values are read from the Compose env file as usual.
USAGE
}

die() { printf '[dev] ERROR: %b\n' "$*" >&2; exit 1; }
note() { printf '[dev] %s\n' "$*"; }

need_docker() {
  command -v docker >/dev/null 2>&1 || die "docker not found; start Docker Desktop first"
  docker info >/dev/null 2>&1 || die "Docker daemon is not running; start Docker Desktop first"
  if [ -f .env ]; then ENV_FILES+=(--env-file .env); fi
  if [ -f "$ENV_FILE" ]; then
    ENV_FILES+=(--env-file "$ENV_FILE")
  elif $MONITORING || [ -n "${SUB2API_DEV_ENV_FILE:-}" ]; then
    die "missing $ENV_FILE; bootstrap the local stack once with:\n    cd deploy && ./compose-deploy.sh --monitoring --compose-file docker-compose.dev.yml --project-name deploy"
  fi
  if $MONITORING; then
    local enabled profile
    enabled="$(compose config --environment | awk '
      $0 == "MONITORING_HOST_METRICS_ENABLED=true" {print "host-metrics"}
      $0 == "MONITORING_BACKUP_ENABLED=true" {print "backup"}
    ')"
    while IFS= read -r profile; do
      [ -z "$profile" ] || PROFILES+=(--profile "$profile")
    done <<< "$enabled"
  fi
}

compose() { docker compose ${ENV_FILES[@]+"${ENV_FILES[@]}"} "${FILES[@]}" ${PROFILES[@]+"${PROFILES[@]}"} "$@"; }

port_owner() { lsof -nP -iTCP:"$1" -sTCP:LISTEN 2>/dev/null | awk 'NR==2{print $1" ("$2")"}' || true; }

# Refuse to start when something outside this stack holds a published port: the
# container would fail halfway through and leave a half-started stack behind.
# A stale `ssh -L 3000:...` tunnel is the usual culprit.
ensure_ports() {
  local port owner
  for port in "$APP_PORT" "$GRAFANA_PORT_VALUE"; do
    owner="$(port_owner "$port")"
    [ -n "$owner" ] || continue
    case "$owner" in
      *docker*|*Docker*|*vpnkit*|*OrbStack*|*colima*) continue ;;
    esac
    die "port $port is held by $owner; stop it (an ssh -L tunnel?) or start on other ports:\n    SERVER_PORT=8081 GRAFANA_PORT=3001 ./dev.sh up"
  done
}

wait_http() {
  local url="$1" label="$2" attempts="${3:-90}" i=0
  command -v curl >/dev/null 2>&1 || return 0
  while [ "$i" -lt "$attempts" ]; do
    if curl -fsS -m 3 -o /dev/null "$url" 2>/dev/null; then
      note "$label ready: $url"
      return 0
    fi
    i=$((i + 1))
    sleep 2
  done
  note "warning: $label did not answer on $url yet; run ./dev.sh status / ./dev.sh logs"
  return 1
}

print_urls() {
  cat <<EOF
[dev] Application   http://127.0.0.1:${APP_PORT}
[dev] Grafana       http://127.0.0.1:${GRAFANA_PORT_VALUE}   (login: admin / \$GRAFANA_ADMIN_PASSWORD)
[dev] Prometheus    inside the Compose network only (no host port)
[dev] Credentials   ${ENV_FILE}: ADMIN_EMAIL / ADMIN_PASSWORD / GRAFANA_ADMIN_PASSWORD
EOF
}

cmd_up() {
  local build=1 app_only=0 foreground=0 ignore_ports=0
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --no-build) build=0 ;;
      --app-only) app_only=1 ;;
      --foreground) foreground=1 ;;
      --ignore-ports) ignore_ports=1 ;;
      *) die "up: unknown option $1" ;;
    esac
    shift
  done
  if [ "$app_only" -eq 1 ]; then
    MONITORING=false
    FILES=(-f "$DEV_FILE")
  fi
  need_docker
  [ "$ignore_ports" -eq 1 ] || ensure_ports
  mkdir -p data
  local args=(up)
  [ "$foreground" -eq 1 ] || args+=(-d)
  [ "$build" -eq 1 ] || args+=(--no-build)
  note "starting: ${FILES[*]} ${PROFILES[*]}"
  compose "${args[@]}"
  [ "$foreground" -eq 1 ] && return 0
  wait_http "http://127.0.0.1:${APP_PORT}/health" "application" || true
  [ "$app_only" -eq 1 ] || wait_http "http://127.0.0.1:${GRAFANA_PORT_VALUE}/api/health" "grafana" 30 || true
  print_urls
}

cmd_stop() { need_docker; compose stop; note "containers stopped; data and containers kept (./dev.sh up to resume)"; }
cmd_down() { need_docker; compose down; note "containers removed; volumes preserved (./dev.sh reset --yes deletes them)"; }

cmd_restart() {
  need_docker
  if [ "$#" -eq 0 ]; then compose restart; note "all services restarted"; else compose restart "$@"; fi
  note "restart keeps the existing image; use ./dev.sh rebuild <service> after source changes"
}

cmd_rebuild() {
  need_docker
  if [ "$#" -eq 0 ]; then
    compose up -d --build --force-recreate
  else
    compose up -d --build --force-recreate --no-deps "$@"
  fi
}

cmd_status() { need_docker; compose ps -a --format 'table {{.Service}}\t{{.Status}}\t{{.Ports}}'; }

cmd_logs() {
  need_docker
  local service=sub2api follow=() lines=(--tail 100)
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -f|--follow) follow=(-f) ;;
      --lines) shift; [ "$#" -ge 1 ] || die "logs: --lines needs a value"; lines=(--tail "$1") ;;
      -*) die "logs: unknown option $1" ;;
      *) service="$1" ;;
    esac
    shift
  done
  compose logs "${lines[@]}" ${follow[@]+"${follow[@]}"} "$service"
}

cmd_shell() { need_docker; local service="${1:-sub2api}"; compose exec "$service" /bin/sh; }

cmd_config() { need_docker; compose config -q; note "compose configuration is valid"; }

cmd_reset() {
  [ "${1:-}" = "--yes" ] || die "reset deletes local Postgres/Redis/Grafana data; re-run with --yes"
  need_docker
  compose down -v
  note "containers and volumes removed"
}

cmd_url() { print_urls; }

[ "$#" -ge 1 ] || { usage; exit 1; }
command="$1"; shift
case "$command" in
  up) cmd_up "$@" ;;
  stop) cmd_stop "$@" ;;
  down) cmd_down "$@" ;;
  restart) cmd_restart "$@" ;;
  rebuild) cmd_rebuild "$@" ;;
  status|ps) cmd_status "$@" ;;
  logs) cmd_logs "$@" ;;
  shell) cmd_shell "$@" ;;
  config) cmd_config "$@" ;;
  reset) cmd_reset "$@" ;;
  url|urls) cmd_url "$@" ;;
  help|-h|--help) usage ;;
  *) usage >&2; die "unknown command: $command" ;;
esac
