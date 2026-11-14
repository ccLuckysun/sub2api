#!/bin/sh
# 将 prometheus.yml.tmpl 渲染到同目录临时文件，校验后原子替换 prometheus.yml。
# 失败时保留上次有效配置；本地需 envsubst（macOS: brew install gettext），
# 无本机 envsubst 时可用 Docker + alpine:3.20 安装 gettext 后渲染；
# 无本机 promtool 时用 prom/prometheus:v3.16.0 镜像校验。
# 手工校验命令：
# docker run --rm -v "$PWD/monitoring/prometheus.yml:/prometheus.yml:ro" \
#   --entrypoint promtool prom/prometheus:v3.16.0 check config /prometheus.yml
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TEMPLATE="$SCRIPT_DIR/prometheus.yml.tmpl"
OUTPUT="$SCRIPT_DIR/prometheus.yml"
ENV_FILE="$SCRIPT_DIR/../monitoring.env"
TMP=""
cleanup() { [ -z "$TMP" ] || rm -f "$TMP"; }
trap cleanup EXIT

: "${PROM_SCRAPE_INTERVAL:=30s}"
: "${PROM_SCRAPE_TIMEOUT:=10s}"
# 只从 env 文件读取两个已知键；不执行（source）任意 shell 内容。
if [ -f "$ENV_FILE" ]; then
    while IFS= read -r line || [ -n "$line" ]; do
        case "$line" in
            PROM_SCRAPE_INTERVAL=*) PROM_SCRAPE_INTERVAL="${line#*=}" ;;
            PROM_SCRAPE_TIMEOUT=*) PROM_SCRAPE_TIMEOUT="${line#*=}" ;;
        esac
    done < "$ENV_FILE"
    echo "[render] loaded scrape settings from $ENV_FILE"
fi
export PROM_SCRAPE_INTERVAL PROM_SCRAPE_TIMEOUT
[ -f "$TEMPLATE" ] || { echo "[render] ERROR: template not found: $TEMPLATE" >&2; exit 1; }
TMP="$(mktemp "$OUTPUT.tmp.XXXXXX")"
echo "[render] $TEMPLATE -> $OUTPUT (temporary: $TMP)"
if command -v envsubst >/dev/null 2>&1; then
    envsubst '${PROM_SCRAPE_INTERVAL} ${PROM_SCRAPE_TIMEOUT}' < "$TEMPLATE" > "$TMP"
elif command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    echo "[render] envsubst unavailable: install gettext in temporary alpine:3.20 container"
    docker run --rm -i \
        -e PROM_SCRAPE_INTERVAL -e PROM_SCRAPE_TIMEOUT \
        --entrypoint /bin/sh alpine:3.20 \
        -ec 'apk add --no-cache gettext >/dev/null; envsubst '\''${PROM_SCRAPE_INTERVAL} ${PROM_SCRAPE_TIMEOUT}'\''' \
        < "$TEMPLATE" > "$TMP"
else
    echo '[render] ERROR: envsubst unavailable (brew install gettext), Docker fallback unavailable' >&2
    exit 1
fi

# Docker 镜像中的 promtool 以 nobody(65534) 运行，mktemp 默认 0600 不可读。
chmod 644 "$TMP"
if command -v promtool >/dev/null 2>&1; then
    echo '[render] promtool check config (local)'
    promtool check config "$TMP"
elif command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    echo '[render] promtool check config (docker)'
    docker run --rm -v "$TMP:/prometheus.yml:ro" \
        --entrypoint promtool prom/prometheus:v3.16.0 \
        check config /prometheus.yml
else
    echo '[render] ERROR: promtool unavailable and Docker fallback unavailable; configuration NOT replaced' >&2
    exit 1
fi
# mktemp 在目标目录内，mv 为同文件系统原子替换。
chmod 644 "$TMP"
mv -f "$TMP" "$OUTPUT"
TMP=""
echo "[render] OK: $OUTPUT"
