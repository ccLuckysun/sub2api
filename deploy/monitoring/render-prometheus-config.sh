#!/bin/sh
# POSIX shell; no envsubst or package installation required.
# Validate a temporary file with promtool before atomically replacing the config.
set -eu

# Container-only SMTP renderer: allowlisted substitutions, no eval/source.
if [ "${1:-}" = --alertmanager ]; then
    umask 077
    config=/alertmanager/alertmanager.yml
    if [ -z "${ALERT_SMTP_TO:-}" ]; then
        printf 'route:\n  receiver: disabled\nreceivers:\n  - name: disabled\n' > "$config"
    else
        : "${ALERT_SMTP_SMARTHOST:?required}" "${ALERT_SMTP_FROM:?required}"
        case "${ALERT_SMTP_REQUIRE_TLS:-true}" in true|false) ;; *) exit 1 ;; esac
        export ALERT_SMTP_REQUIRE_TLS="${ALERT_SMTP_REQUIRE_TLS:-true}"
        awk '
      function quote(v) { gsub(/\047/, "\047\047", v); return "\047" v "\047" }
      /\$\{ALERT_SMTP_/ {
        line=$0
        rendered=""
        while (match(line, /\$\{ALERT_SMTP_[A-Z_]+\}/)) {
          key=substr(line,RSTART+2,RLENGTH-3)
          value=ENVIRON[key]
          if (value ~ /[\r\n]/) exit 2
          if (key != "ALERT_SMTP_REQUIRE_TLS") value=quote(value)
          rendered=rendered substr(line,1,RSTART-1) value
          line=substr(line,RSTART+RLENGTH)
        }
        print rendered line; next
      }
      {print}
        ' /etc/alertmanager/alertmanager.yml.tmpl > "$config"
    fi
    chmod 600 "$config"
    amtool check-config "$config"
    [ "${ALERTMANAGER_RENDER_ONLY:-false}" != true ] || exit 0
    exec /bin/alertmanager --config.file="$config" --storage.path=/alertmanager
fi

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
ENV_FILE="${MONITORING_ENV_FILE:-$SCRIPT_DIR/../monitoring.env}"
TEMPLATE="$SCRIPT_DIR/prometheus.yml.tmpl"
OUTPUT="$SCRIPT_DIR/prometheus.yml"
TMP=""
cleanup() { [ -z "$TMP" ] || rm -f "$TMP"; }
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

# Parse only known scalar keys; never source an environment file as shell code.
# Shell environment takes precedence over the file, just as in Compose.
read_setting() {
    [ -f "$ENV_FILE" ] || return 0
    awk -v key="$1" '
        index($0, key "=") == 1 {
            value = substr($0, length(key) + 2)
            sub(/\r$/, "", value)
            if (value ~ /^".*"$/ || value ~ /^\047.*\047$/)
                value = substr(value, 2, length(value) - 2)
            else sub(/[ \t]+#.*/, "", value)
            result = value
        }
        END { print result }
    ' "$ENV_FILE"
}
PROM_SCRAPE_INTERVAL="${PROM_SCRAPE_INTERVAL:-$(read_setting PROM_SCRAPE_INTERVAL)}"
PROM_SCRAPE_TIMEOUT="${PROM_SCRAPE_TIMEOUT:-$(read_setting PROM_SCRAPE_TIMEOUT)}"
MONITORING_HOST_METRICS_ENABLED="${MONITORING_HOST_METRICS_ENABLED:-$(read_setting MONITORING_HOST_METRICS_ENABLED)}"
NODE_EXPORTER_HOST_IP="${NODE_EXPORTER_HOST_IP:-$(read_setting NODE_EXPORTER_HOST_IP)}"
: "${PROM_SCRAPE_INTERVAL:=15s}"
: "${PROM_SCRAPE_TIMEOUT:=10s}"
: "${MONITORING_HOST_METRICS_ENABLED:=false}"
case "$MONITORING_HOST_METRICS_ENABLED" in
    true)
        # Host exporter must listen on an RFC1918 address, not a public interface.
        awk -v ip="$NODE_EXPORTER_HOST_IP" 'BEGIN {
            n = split(ip, a, "."); if (n != 4) exit 1
            for (i=1; i<=4; i++) if (a[i] !~ /^[0-9]+$/ || a[i] > 255) exit 1
            exit !(a[1] == 10 || (a[1] == 172 && a[2] >= 16 && a[2] <= 31) || (a[1] == 192 && a[2] == 168))
        }' || { echo '[render] ERROR: host metrics require a private Docker gateway IPv4 address' >&2; exit 1; }
        ;;
    false) ;;
    *) echo '[render] ERROR: MONITORING_HOST_METRICS_ENABLED must be true or false' >&2; exit 1 ;;
esac
export PROM_SCRAPE_INTERVAL PROM_SCRAPE_TIMEOUT MONITORING_HOST_METRICS_ENABLED NODE_EXPORTER_HOST_IP
[ -f "$TEMPLATE" ] || { echo "[render] ERROR: template not found: $TEMPLATE" >&2; exit 1; }
TMP="$(mktemp "$OUTPUT.tmp.XXXXXX")"
awk '
    /# OPTIONAL_NODE_EXPORTER_JOB/ {
        if (ENVIRON["MONITORING_HOST_METRICS_ENABLED"] == "true") {
            print "  - job_name: node"
            print "    static_configs:"
            print "      - targets: [\"" ENVIRON["NODE_EXPORTER_HOST_IP"] ":9100\"]"
        }
        next
    }
    {
        gsub(/\$\{PROM_SCRAPE_INTERVAL\}/, ENVIRON["PROM_SCRAPE_INTERVAL"])
        gsub(/\$\{PROM_SCRAPE_TIMEOUT\}/, ENVIRON["PROM_SCRAPE_TIMEOUT"])
        print
    }
' "$TEMPLATE" > "$TMP"
if grep -q '\${' "$TMP"; then
    echo '[render] ERROR: unresolved template variable' >&2; exit 1
fi
chmod 644 "$TMP"
if command -v promtool >/dev/null 2>&1; then
    promtool check config --syntax-only "$TMP"
    promtool check rules "$SCRIPT_DIR"/prometheus/rules/*.yml
elif command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    docker run --rm --network none -v "$TMP:/prometheus.yml:ro" \
        -v "$SCRIPT_DIR/prometheus/rules:/rules:ro" \
        --entrypoint /bin/sh prom/prometheus:v3.16.0 -ec \
        'promtool check config --syntax-only /prometheus.yml && promtool check rules /rules/*.yml'
else
    echo '[render] ERROR: promtool and Docker unavailable; configuration NOT replaced' >&2
    exit 1
fi
if [ -f "$OUTPUT" ] && cmp -s "$TMP" "$OUTPUT"; then
    echo "[render] unchanged: $OUTPUT"
    exit 0
fi
mv -f "$TMP" "$OUTPUT"
TMP=""
echo "[render] OK: $OUTPUT"
