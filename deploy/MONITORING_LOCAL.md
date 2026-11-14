# Sub2API 监控本地启动指南（dev compose 叠加）

本地基线为 `docker-compose.dev.yml`（源码构建 sub2api + postgres + redis，网络 `sub2api-network`）。监控栈以**同项目 `-f` 叠加**方式接入：Prometheus + Grafana + Node Exporter + 备份 cron 容器。

> 叠加文件会为 sub2api 注入 `MONITORING_METRICS_ENABLED=true`（9091 指标端点），去掉叠加文件后业务行为完全复原。

## 前置条件

- Docker Desktop（或等价 daemon）已运行：`docker info` 有输出
- 渲染配置需 `envsubst`（macOS：`brew install gettext`；脚本亦给出 docker 替代）
- **Docker Engine ≥26.0（API ≥1.45）和 Docker Compose ≥2.35.0**（`docker version` / `docker compose version` 确认）。这是本项目的保守支持基线，不表示 `volume.subpath` 首次在 Compose 2.35 引入；本机已实测 Engine 29.7.2 / Compose 5.5.0。
- 备份容器用 `volume.subpath: snapshots` 只挂同一 TSDB 卷的子目录；不能改成独立 snapshots 卷，因为已有持久块的快照需要 hard link，跨卷会报 `invalid cross-device link`。

## 启动（三条命令）

```bash
cd deploy

# 1. 复制并编辑环境文件（GRAFANA_ADMIN_PASSWORD、POSTGRES_PASSWORD 均必填）
cp monitoring.env.example monitoring.env
# 编辑 monitoring.env，设置 GRAFANA_ADMIN_PASSWORD，并取消 POSTGRES_PASSWORD 注释填值

# 2. 渲染 Prometheus 采集配置（含 promtool 校验）
./monitoring/render-prometheus-config.sh

# 3. 叠加启动（注意 --env-file：compose 默认只读 .env，
#    monitoring.env 必须显式传递，否则 GRAFANA_ADMIN_PASSWORD 缺失报错）
docker compose -f docker-compose.dev.yml -f docker-compose.monitoring.yml \
  --env-file ./monitoring.env up -d --build
```

首次执行会拉取/构建镜像，较慢（约数分钟，取决于网络）。`snapshots-init` 一次性创建同一 TSDB 卷的 `snapshots/` 并设置目录属主为 Prometheus 的 65534:65534；backup 依赖初始化成功及 Prometheus healthy 后才启动并挂载子目录。在上述实测版本的全新空卷上，单次 `up -d` 即可完成，不需手工先建目录。旧版 Docker/Compose 不支持时请升级，不得回退成跨卷快照。

备份 cron 固定使用 UTC（`SNAPSHOT_SCHEDULE=0 4 * * *` 为每日 04:00 UTC，即北京时间 12:00），因为 Alpine 不预装 tzdata。

若从此前独立 `prometheus_snapshots` 卷的试验版升级，应重建 `snapshots-init`、Prometheus 和 backup，以去掉错误的跨卷挂载。旧独立快照卷不会自动迁移或删除；保留旧备份并确认新链路成功后再人工处理，禁止直接删除 TSDB 活跃块。

## 验证

1. **Prometheus 采集正常**：
   ```bash
   docker compose -f docker-compose.dev.yml -f docker-compose.monitoring.yml \
     --env-file ./monitoring.env exec prometheus wget -qO- 'http://localhost:9090/api/v1/query?query=up' | grep -o '"value":[^,]*,[^]]*]'
   ```
   三目标 `up == 1`：`sub2api`、`node`、`prometheus`。

2. **Grafana 登录**：浏览器打开 <http://localhost:3000>（仅绑 127.0.0.1），用户名 `admin` / 密码为 `GRAFANA_ADMIN_PASSWORD`。

3. **看板出数**：Grafana 左侧 Dashboards → folder **Sub2API** → **Sub2API Overview**，无需手工导入。访问 <http://localhost:8080> 产生业务请求后，请求速率/延迟面板出数；Node Exporter 面板即时有数据。

4. **备份手动触发一次**（不等定时）：
   ```bash
   docker compose -f docker-compose.dev.yml -f docker-compose.monitoring.yml \
     --env-file ./monitoring.env exec backup /usr/local/bin/snapshot.sh
   ls -lh monitoring/backup-data/   # 应见 prometheus-snapshot-*.tar.gz
   ```

## 常见问题

**端口冲突**
- Grafana 用 `127.0.0.1:3000`。冲突时改 `monitoring.env` 无法生效（端口写死在 compose 里），临时改用 compose override 或停用占用进程。
- Prometheus 9090 / Node Exporter 9100 / sub2api 9091 均不映射宿主机端口，**不会**与宿主端口冲突。

**首次 build 慢**
- 叠加 `up --build` 会重建业务镜像（dev 基线本身即源码构建），首次拉取 prometheus/grafana/node-exporter/alpine 镜像也慢，属预期。

**9091 未暴露 / `up{job="sub2api"}` 为 0**
- 指标端点在容器网内，宿主机不可直接访问，属设计（安全边界 = 不发布端口）。本地验证指标内容：
  ```bash
  docker compose -f docker-compose.dev.yml -f docker-compose.monitoring.yml \
    --env-file ./monitoring.env exec prometheus wget -qO- http://sub2api:9091/metrics
  ```
  注意 targets 用**服务名** `sub2api`（compose 网内 DNS），容器名是 `sub2api-dev`，两者不同。
- 叠加 compose 已默认注入 `MONITORING_METRICS_ENABLED=true`；若 `up{job="sub2api"}` 仍为 0，确认启动时带了 `-f docker-compose.monitoring.yml`（env 注入靠叠加文件）。

**Grafana 无法访问 localhost:3000**
- 端口仅绑 127.0.0.1；远程机器上需 SSH 隧道：`ssh -L 3000:127.0.0.1:3000 <host>`。

**修改监控配置后不生效**
- 修改 `prometheus.yml`（渲染脚本校验后原子替换）：bind mount 可能仍指向旧 inode，执行 `docker compose ... up -d --force-recreate prometheus`（需带双 `-f` 和 `--env-file`），不能仅依赖 restart/SIGHUP
- 修改 `monitoring.env` 或 compose command：**必须重新 `up -d`** 重建相应容器，`restart` 不会应用新容器环境

**临时调试 Prometheus UI**
- compose 中 prometheus 服务注释掉的 `ports: ["127.0.0.1:9090:9090"]` 取消注释后 `up -d prometheus` 即可在 <http://localhost:9090> 查看 targets/status；生产保持注释。

## 临时关闭监控

**先显式停止监控容器（不动业务容器，保留数据卷）**：

```bash
docker compose -f docker-compose.dev.yml -f docker-compose.monitoring.yml \
  --env-file ./monitoring.env stop prometheus grafana node-exporter backup
```

仅从命令中去掉叠加文件，不会停止已有监控容器：它们会变成 orphan 并继续运行。完成上面的 stop 后，后续业务启动可不再叠加监控：

```bash
docker compose -f docker-compose.dev.yml --env-file ./monitoring.env up -d --build
```

注意：去掉叠加后应用指标环境变量也会移除，因此该业务 `up` 可能重建 sub2api（并非零重启）；如果只是临时停监控，执行 stop 即可，无需再 up 业务。不要运行 `down -v`，以免删除数据卷。

## 下一步

- 生产部署与更新回退：`docs/MONITORING_DEPLOY.md`（阶段 2 交付）
- 指标字典与口径：`docs/MONITORING_API.md`
