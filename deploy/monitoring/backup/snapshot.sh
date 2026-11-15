#!/bin/sh
# Prometheus 一致性快照备份。只挂载 TSDB 卷的 snapshots 子目录，不接触活跃 TSDB。
# 失败与成功都会清理已创建的快照、半成品、过期包和过期孤儿目录。
set -euo pipefail

PROMETHEUS_URL="http://prometheus:9090"
SNAPSHOT_DIR="/snapshots"
BACKUP_DIR="/backup"
KEEP_DAYS="${SNAPSHOT_KEEP_DAYS:-3}"
TMP_PREFIX=".snapshot-tmp-"
SNAP_NAME=""
TMP_TARBALL=""

log() { printf '[snapshot %s] %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$*"; }
fail() { log "ERROR: $*" >&2; exit 1; }

case "$KEEP_DAYS" in
    ''|*[!0-9]*) fail "SNAPSHOT_KEEP_DAYS must be a positive integer: $KEEP_DAYS" ;;
esac
[ "$KEEP_DAYS" -gt 0 ] || fail "SNAPSHOT_KEEP_DAYS must be greater than zero"
[ -d "$SNAPSHOT_DIR" ] || fail "snapshot volume missing: $SNAPSHOT_DIR"
[ -d "$BACKUP_DIR" ] || fail "backup directory missing: $BACKUP_DIR"

KEEP_MINUTES=$((KEEP_DAYS * 1440))

# 只处理本次 API 返回的快照及超期（而非正在创建）的孤儿目录。
# 即使 tar/mv/rm 失败，也要运行保留策略并以非零状态退出。
cleanup() {
    result=$1
    trap - EXIT
    if [ -n "$TMP_TARBALL" ] && [ -f "$TMP_TARBALL" ]; then
        if ! rm -f "$TMP_TARBALL"; then
            log "ERROR: cannot remove partial archive $TMP_TARBALL" >&2
            result=1
        fi
    fi
    if [ -n "$SNAP_NAME" ] && [ -d "$SNAPSHOT_DIR/$SNAP_NAME" ]; then
        if ! rm -rf "$SNAPSHOT_DIR/$SNAP_NAME"; then
            log "ERROR: cannot remove current snapshot $SNAP_NAME" >&2
            result=1
        else
            log "removed source snapshot $SNAP_NAME"
        fi
    fi
    # BusyBox find 支持 -mmin；不用 -mtime +N（整数天向下取整，多留近 1 天）。
    # 只清超过 KEEP_DAYS 天的历史文件，绝不删除新建/正在创建的快照。
    for pattern in 'prometheus-snapshot-*.tar.gz' '.snapshot-tmp-*'; do
        if ! find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type f \
            -name "$pattern" -mmin "+$KEEP_MINUTES" -print -exec rm -f {} +; then
            log "ERROR: failed pruning $pattern from $BACKUP_DIR" >&2
            result=1
        fi
    done
    if ! find "$SNAPSHOT_DIR" -mindepth 1 -maxdepth 1 -type d \
        -name '20??????T*' -mmin "+$KEEP_MINUTES" -print -exec rm -rf {} +; then
        log "ERROR: failed pruning expired orphan snapshots" >&2
        result=1
    fi
    [ "$result" -eq 0 ] && log "cleanup complete" || log "ERROR: backup or cleanup failed (exit $result)" >&2
    exit "$result"
}
trap 'cleanup $?' EXIT

log "POST ${PROMETHEUS_URL}/api/v1/admin/tsdb/snapshot"
RESPONSE="$(wget -q -T 120 -O - --post-data='' \
    "${PROMETHEUS_URL}/api/v1/admin/tsdb/snapshot")" \
    || fail "snapshot API request failed (check --web.enable-admin-api)"
# Prometheus API 响应固定为 JSON {"status":"success","data":{"name":"..."}}。
printf '%s\n' "$RESPONSE" | grep -Eq '"status"[[:space:]]*:[[:space:]]*"success"' \
    || fail "snapshot API returned non-success: $RESPONSE"
CANDIDATE_NAME="$(printf '%s\n' "$RESPONSE" | grep -oE '"name"[[:space:]]*:[[:space:]]*"[^"]*"' | cut -d '"' -f 4)" \
    || fail "cannot parse snapshot name: $RESPONSE"
# 先验证候选名，再赋给 EXIT trap 使用的变量；异常响应不能污染清理目标。
case "$CANDIDATE_NAME" in
    ''|*[!a-zA-Z0-9._-]*|.|..) fail "unsafe snapshot name: $CANDIDATE_NAME" ;;
esac
SNAP_NAME="$CANDIDATE_NAME"
[ -d "$SNAPSHOT_DIR/$SNAP_NAME" ] || fail "source snapshot missing: $SNAP_NAME"

STAMP="$(date '+%Y%m%dT%H%M%S%z')"
TMP_TARBALL="$BACKUP_DIR/${TMP_PREFIX}${STAMP}-${SNAP_NAME}.tar.gz"
ARCHIVE="$BACKUP_DIR/prometheus-snapshot-${STAMP}-${SNAP_NAME}.tar.gz"
log "archiving $SNAP_NAME"
if ! COPYFILE_DISABLE=1 tar --exclude='._*' --exclude='.DS_Store' -czf "$TMP_TARBALL" -C "$SNAPSHOT_DIR" "$SNAP_NAME"; then
    fail "tar failed: $TMP_TARBALL"
fi
if [ -e "$ARCHIVE" ]; then
    fail "archive name already exists: $ARCHIVE"
fi
if ! mv "$TMP_TARBALL" "$ARCHIVE"; then
    fail "cannot publish archive $ARCHIVE"
fi
TMP_TARBALL=""
log "archive complete: $ARCHIVE"
# 清理由 EXIT trap 执行，成功也必须删掉本次的源快照。
