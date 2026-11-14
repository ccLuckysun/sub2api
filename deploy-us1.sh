#!/usr/bin/env bash
# =============================================================================
# Sub2API 一键部署脚本（Mac → 服务器 us1）
#
# 用法：
#   cd /Users/cc/Desktop/sub2api
#   ./deploy-us1.sh 0.0.2
#   或直接运行 ./deploy-us1.sh 后按提示输入版本号
#
# 自动完成：
#   1. 本机构建 linux/amd64 镜像 sun/sub2api:<版本号>
#   2. 导出 gzip 压缩包并 scp 上传到服务器 /tmp
#   3. 服务器端 docker load 加载镜像
#   4. 数据库备份到 /root/sub2api-backup-<日期时间>.dump
#   5. 旧镜像按其原版本号打 rollback tag 备份
#   6. 修改服务器 compose 文件的 sub2api 镜像行
#   7. docker compose up -d sub2api 只重建应用容器（数据卷不动）
#   8. 健康检查，失败时打印回滚指引
#
# 前提条件（详见脚本下方注释）：
#   - 本机 ~/.ssh/config 已配置 us1 主机别名，且 `ssh us1 true` 能免密执行
#   - Docker Desktop 正在运行
#   - 服务器上应用容器名为 sub2api，数据库容器名为 sub2api-postgres
# =============================================================================
set -euo pipefail

REMOTE_HOST="us1"                 # ~/.ssh/config 里的主机别名
IMAGE_NAME="sun/sub2api"          # 镜像名
APP_CONTAINER="sub2api"           # 服务器上应用容器名
DB_CONTAINER="sub2api-postgres"   # 服务器上数据库容器名
DB_USER="sub2api"                 # 数据库用户
DB_NAME="sub2api"                 # 数据库名
APP_PORT="8080"                   # 应用端口（服务器本机访问）

banner() { echo; echo "========== $* =========="; }

# -----------------------------------------------------------------------------
# 0. 前置检查
# -----------------------------------------------------------------------------
banner "前置检查"

if ! docker info >/dev/null 2>&1; then
  echo "❌ Docker 未运行，请先启动 Docker Desktop"; exit 1
fi

if ! ssh -o BatchMode=yes -o ConnectTimeout=10 "$REMOTE_HOST" true 2>/dev/null; then
  echo "❌ 无法免密 SSH 到 $REMOTE_HOST"
  echo "   请确认：① ~/.ssh/config 里配置了 us1；② 已上传公钥（ssh-copy-id us1）"
  exit 1
fi
echo "✅ Docker 运行中，SSH $REMOTE_HOST 可用"

# -----------------------------------------------------------------------------
# 1. 输入版本号
# -----------------------------------------------------------------------------
VERSION="${1:-}"
if [ -z "$VERSION" ]; then
  read -r -p "请输入版本号（例如 0.0.2）: " VERSION
fi
# 去除粘贴/输入法可能带入的空白和回车，避免格式校验误判
VERSION="$(printf '%s' "$VERSION" | tr -d '[:space:]')"
if ! [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$ ]]; then
  echo "❌ 版本号格式不合法（应为 x.y.z 或 x.y.z-后缀，如 0.0.2-dev）"; exit 1
fi

FULL_IMAGE="${IMAGE_NAME}:${VERSION}"
COMMIT="$(git rev-parse --short HEAD)"
LOCAL_TAR="/tmp/sun-sub2api-${VERSION}.tar.gz"
REMOTE_TAR="/tmp/sun-sub2api-${VERSION}.tar.gz"

echo "部署目标：${FULL_IMAGE}（commit ${COMMIT} → ${REMOTE_HOST}）"

# -----------------------------------------------------------------------------
# 2. 本机构建镜像（linux/amd64）
# -----------------------------------------------------------------------------
banner "构建镜像 $FULL_IMAGE"

# 优先使用 Docker Desktop 自带的 buildx builder：它继承 daemon 的
# registry mirror，能正常拉取 base image；docker-container builder 不走
# mirror，在受限网络下会卡在 auth.docker.io。
BUILDER="desktop-linux"
if ! docker buildx inspect "$BUILDER" >/dev/null 2>&1; then
  BUILDER="multiarch"
  if ! docker buildx inspect "$BUILDER" >/dev/null 2>&1; then
    docker buildx create --use --name "$BUILDER" >/dev/null
  fi
fi
docker buildx use "$BUILDER"

docker buildx build --platform linux/amd64 \
  --build-arg GOPROXY="https://goproxy.cn,direct" \
  --build-arg GOSUMDB="sum.golang.google.cn" \
  --build-arg VERSION="$VERSION" \
  --build-arg COMMIT="$COMMIT" \
  -t "$FULL_IMAGE" \
  --load \
  -f Dockerfile .

# -----------------------------------------------------------------------------
# 3. 导出镜像
# -----------------------------------------------------------------------------
banner "导出镜像"
docker save "$FULL_IMAGE" | gzip > "$LOCAL_TAR"
ls -lh "$LOCAL_TAR"

# -----------------------------------------------------------------------------
# 4. 上传服务器
# -----------------------------------------------------------------------------
banner "上传到 $REMOTE_HOST:/tmp"
scp -q "$LOCAL_TAR" "$REMOTE_HOST:/tmp/"
echo "✅ 上传完成"

# -----------------------------------------------------------------------------
# 5. 服务器端：加载 / 备份 / 改 compose / 重建容器 / 健康检查
# -----------------------------------------------------------------------------
banner "服务器端部署"

ssh "$REMOTE_HOST" REMOTE_VERSION="$VERSION" 'bash -s' <<'REMOTE_SCRIPT'
set -euo pipefail
VERSION="$REMOTE_VERSION"
IMAGE_NAME="sun/sub2api"
APP="sub2api"
DB_CONTAINER="sub2api-postgres"
DB_USER="sub2api"
DB_NAME="sub2api"

echo "==> 当前运行镜像"
OLD_IMAGE="$(docker inspect "$APP" --format '{{.Config.Image}}')"
echo "    $OLD_IMAGE"

echo "==> docker load 新镜像"
docker load < "/tmp/sun-sub2api-${VERSION}.tar.gz"

echo "==> 数据库备份"
DUMP="/root/sub2api-backup-$(date +%F-%H%M).dump"
docker exec "$DB_CONTAINER" pg_dump -U "$DB_USER" -d "$DB_NAME" -Fc > "$DUMP"
ls -lh "$DUMP"

echo "==> 旧镜像按原版本号备份 tag"
OLD_TAG="${OLD_IMAGE##*:}"
docker tag "$OLD_IMAGE" "sun/sub2api:rollback-${OLD_TAG}"
echo "    已备份为 sun/sub2api:rollback-${OLD_TAG}"

echo "==> 更新 compose 文件"
WORKDIR="$(docker inspect "$APP" --format '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}')"
echo "    compose 目录: $WORKDIR"
cd "$WORKDIR"
cp docker-compose.yml "docker-compose.yml.bak.$(date +%F-%H%M)"
sed -i "s|image:.*sub2api:[^ ]*|image: ${IMAGE_NAME}:${VERSION}|" docker-compose.yml
grep -n "image:" docker-compose.yml || true

echo "==> 重建应用容器（只重建 sub2api，数据卷不动）"
docker compose up -d "$APP"

echo "==> 健康检查（最长等 3 分钟）"
HEALTH_OK=0
for i in $(seq 1 36); do
  if curl -sf "http://127.0.0.1:8080/health" >/dev/null 2>&1; then
    echo "✅ 健康检查通过（第 ${i} 次探测）"
    HEALTH_OK=1
    break
  fi
  sleep 5
done

if [ "$HEALTH_OK" -ne 1 ]; then
  echo "❌ 健康检查超时！最近 30 行日志："
  docker logs --tail 30 "$APP" || true
  echo
  echo "如需回滚："
  echo "  cd $WORKDIR"
  echo "  sed -i 's|image:.*sub2api:[^ ]*|image: ${OLD_IMAGE}|' docker-compose.yml"
  echo "  docker compose up -d $APP"
  echo "数据库如有异常："
  echo "  docker exec -i $DB_CONTAINER pg_restore -U $DB_USER -d $DB_NAME --clean < $DUMP"
  exit 1
fi

echo "==> 清理服务器上的临时压缩包"
rm -f "/tmp/sun-sub2api-${VERSION}.tar.gz"
REMOTE_SCRIPT

# -----------------------------------------------------------------------------
# 6. 完成
# -----------------------------------------------------------------------------
banner "🎉 部署完成：$FULL_IMAGE 已在 $REMOTE_HOST 上运行"
echo "后验证证（建议手动确认）："
echo "  1. 浏览器打开站点，确认首页/登录正常"
echo "  2. 管理后台查看版本号应为 $VERSION"
echo
echo "回滚方法（如需要）："
echo "  ssh $REMOTE_HOST"
echo "  cd \$(docker inspect $APP_CONTAINER --format '{{ index .Config.Labels \"com.docker.compose.project.working_dir\" }}')"
echo "  把 docker-compose.yml 的 image 行改回 sun/sub2api:rollback-<旧版本号> 或旧镜像名"
echo "  docker compose up -d $APP_CONTAINER"
