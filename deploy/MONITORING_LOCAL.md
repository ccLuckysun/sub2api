# Sub2API 五容器一键部署

默认监控部署包含五个独立的常驻容器：`sub2api`、`postgres`（PostgreSQL）、`redis`、`grafana`、`prometheus`。数据库、缓存、应用、可视化与指标存储均独立运行。Grafana 自动配置数据源和 Sub2API Overview 看板。

需要 Docker Compose ≥2.35。启用快照扩展时还需 Docker Engine ≥26（API ≥1.45）。部署脚本需要 Bash；Prometheus 配置渲染脚本兼容 POSIX `/bin/sh`，不再依赖 envsubst 或临时联网安装 gettext。

## 从本地构建并更新 us1

在仓库根目录执行：

```bash
./deploy-us1.sh 0.0.4
```

版本号按本次发布调整。脚本继续完成 linux/amd64 构建、压缩、上传、数据库 pg_dump、运行中旧镜像备份、应用更新与健康检查，并默认部署 Prometheus、Grafana。脚本从现有应用的 Compose labels 读取**项目名称、全部配置文件及环境文件**，保留 PostgreSQL、Redis、Caddy 及现有数据和业务 `.env`，不会重建它们。

若只更新应用：

```bash
./deploy-us1.sh 0.0.4 --app-only
```

已有监控的项目会继续保留监控配置。脚本结束后输出数据库备份位置和可直接执行的应用回滚脚本。应用回滚不自动回退数据库 schema；数据库备份用于必要时人工恢复。

服务器上的 Grafana 仅绑定 `127.0.0.1:3000`。在本机执行 `ssh -L 3000:127.0.0.1:3000 us1`，再打开 <http://127.0.0.1:3000>。用户名 `admin`，密码保存在**服务器 Compose 目录**的 `monitoring.env`（0600），首次自动生成，重复部署不重置。已有 Grafana 数据卷的管理员密码以卷内已保存的密码为准。

## 新服务器或独立目录部署

在目标部署目录运行仓库中的准备脚本：

```bash
/path/to/sub2api/deploy/docker-deploy.sh --monitoring --image sun/sub2api:0.0.4
```

`--image` 应指定包含监控埋点的已发布镜像。未指定时沿用原 Compose 的默认镜像。首次安装会生成 PostgreSQL/JWT/TOTP 等业务凭据、Grafana 密码及持久化目录；已有 `.env` 和 Compose 配置会保留。镜像通过 Docker 拉取，因此新服务器使用私有镜像时须先登录镜像仓库。

不传参数仍保留原脚本的三容器配置准备功能。加 `--start` 可启动三容器基础栈。`--monitoring --no-start` 可以只准备并校验五容器配置。

## 当前源码开发环境

已有本地业务 `.env` 时：

```bash
cd deploy
./compose-deploy.sh --monitoring --compose-file docker-compose.dev.yml --project-name deploy
```

没有 `.env` 时先从 `.env.example` 复制并设置 PostgreSQL 密码。不要覆盖已有私有 `monitoring.env`。旧版本曾把本地数据库密码放在 `monitoring.env`，脚本仍支持该文件；新的部署优先把业务变量保存在 `.env`，监控变量保存在 `monitoring.env`，Compose 按此顺序读取两者。

脚本生成 `sub2api-compose`，保存完整项目名、配置文件列表、扩展 profiles 及环境文件顺序。后续操作统一使用它：

```bash
./sub2api-compose ps
./sub2api-compose logs -f sub2api
./sub2api-compose up -d
./sub2api-compose stop grafana prometheus
```

更改采样间隔后，重新执行原来的 `compose-deploy.sh` 命令。渲染失败保留上一次有效文件；配置不变时保留文件 inode；内容改变时脚本会重建 Prometheus，使文件挂载生效。

## 可选扩展

五容器基础栈采集应用 HTTP、Go/process 和 Prometheus 自身指标。没有开启宿主机监控时，看板的宿主机面板显示无数据，Prometheus 不创建一个必定失败的 node 抓取任务。

```bash
# 增加独立 Node Exporter，采集宿主机 CPU/内存/磁盘/网络
./compose-deploy.sh --monitoring --host-metrics

# 增加独立快照 cron 容器及一次性卷初始化容器
./compose-deploy.sh --monitoring --backup

# 两种扩展一起启用；开发环境仍需带原 --compose-file / --project-name 参数
./compose-deploy.sh --monitoring --host-metrics --backup
```

后续升级会保留已经启用的扩展。Node Exporter 使用 Linux host 网络命名空间，解决 bridge 模式采到容器网络的问题。脚本从 Docker 默认 bridge 获取 RFC1918 私有网关地址，Exporter 只在该地址的 9100 监听，Prometheus 使用相同地址采集。它不监听公网地址，不依赖 host 网络下不存在的服务 DNS。默认 bridge 被禁用或使用非 RFC1918 地址的主机不支持此自动配置，脚本会报错。

Docker Desktop 下宿主指标属于 **Linux 虚拟机**。它们不是 macOS/Windows 物理主机的数据。Docker Desktop 的 host networking 能力受版本和设置影响；生产 Linux 仍需复核实际网卡、挂载点和可用资源。

快照扩展继续使用同一 TSDB 卷的 `snapshots` 子路径，避免跨卷硬链接失败；备份容器看不到活跃 TSDB。定时任务默认 UTC 04:00（北京时间 12:00），备份包默认保留 3 天，输出目录 `monitoring/backup-data`。

```bash
./sub2api-compose exec backup /usr/local/bin/snapshot.sh
```

## 验证与停用

```bash
./sub2api-compose exec prometheus wget -qO- 'http://localhost:9090/api/v1/query?query=up'
```

基础栈的 `sub2api`、`prometheus` 两个目标应为 1；开启宿主扩展时另有 `node=1`。`up=1` 表示可抓取，仍需验证指标口径。

从旧版七容器环境迁移到默认五容器时，先用原 Compose 命令停止 `node-exporter` 和 `backup`，再执行新脚本；未选中的旧容器不会自动删除。不要使用 `down -v`，它会删除命名数据卷。

Grafana 和 Prometheus 分别使用独立持久卷。Grafana 只绑定宿主回环；Prometheus 9090、应用指标 9091 不发布宿主端口。2GB 生产机器须按业务和监控的实测用量校准内存限额与 retention。

部署回归测试：`python3 deploy/tests/monitoring-deploy-test.py`（在仓库根目录执行）。测试不连接 us1，服务更新和 pg_dump 故障由本地桩模拟，配置合并和 promtool 校验使用真实 Docker。

完整隔离栈验收（需要一个已经构建、包含监控埋点的应用镜像；会创建并清理独立项目及其测试卷）：

```bash
python3 deploy/tests/monitoring-stack-smoke.py --image sun/sub2api:0.0.4 --extensions
```

这条测试通过真正的 `docker-deploy.sh --monitoring` 启动五容器，检查抓取目标和 Grafana 看板 provisioning；`--extensions` 还会验证宿主网络计数与快照压缩。测试不会连接远程服务器。
