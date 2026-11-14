# Sub2API 监控系统开发计划

- 文档状态：开发计划 v1，已补充五容器部署与复审修正（基于需求文档 `docs/MONITORING_REQUIREMENTS.md`）
- 编写日期：2026-11-14
- 配套接口文档：`docs/MONITORING_API.md`（指标字典与端点定义）

当前入口：`deploy-us1.sh VERSION` 默认远程部署五个核心服务；`deploy/docker-deploy.sh --monitoring` 一键准备并启动独立五容器栈。原构建/上传/数据库备份/回滚保留，Node Exporter 与快照 cron 为可选独立容器。实际操作以 `deploy/MONITORING_LOCAL.md` 为准；下文容量预算包含可选扩展，不代表默认常驻数量。

## 1. 输入与变更说明

相对需求文档的输入变更：

1. **用户追加要求**：监控覆盖常规需求；数据"定时存入硬盘 → 定时压缩 → 定时删除"的基础链路作为第一阶段目标。
2. **一期纳入 Node Exporter**：需求文档第 4.2 节将宿主机指标列为后续扩展，本计划按用户"覆盖常规需求"的要求将其提前到一期。
3. **服务器规格已确认：2 核 CPU / 2GB 内存 / 50GB 磁盘**（2026-11-14）。小规格机器，2GB 内存为最紧约束（业务栈 sub2api/postgres/redis/caddy + 监控栈共存），容量预算见第 5.1 节。
4. 生产服务器 Compose 以服务器实际文件为准（仓库 `deploy/docker-compose.yml` 仅含 sub2api/postgres/redis，生产另有 caddy 容器与独立 compose 目录，`deploy-us1.sh` 通过容器 label 定位）。

### 1.1 数据生命周期的准确表述

"定时落盘、定时压缩、定时删除"在 Prometheus 架构下的真实语义（避免错误实现）：

| 用户要求 | 实际机制 | 层级 |
| --- | --- | --- |
| 定时存入硬盘 | **无需自研定时落盘任务**：TSDB 自身负责 WAL 先行持久化与 head block 定期切割为磁盘块（后台压实内建）。注意：WAL 持久化 ≠ 每样本 fsync，断电场景的潜在丢失边界遵循 Prometheus 自身机制 | Prometheus 内建，配好持久化卷即达成 |
| 定时压缩 | ① TSDB 后台压实（compaction）内建，无需配置；② **定期快照备份**：cron 触发 `POST /api/v1/admin/tsdb/snapshot`（需 `--web.enable-admin-api`，仅容器网内可达），快照落备份目录后 tar.gz 压缩 | 本计划新增 cron 任务 |
| 定时删除 | ① TSDB retention（`--storage.tsdb.retention.time`）自动删除超期块；② 备份目录独立 retention，cron `find -mtime +N -delete` 清理旧快照包 | 两层独立配置 |

红线约束：

- 不得 tar 活跃 TSDB 数据目录（WAL 状态不一致，产物不可用）；备份一律走 snapshot API。
- 不得 cron 直接删除 TSDB 活跃块目录；删除只交给 retention。
- 备份目录不在 TSDB 数据目录内，不受其 retention 影响，需独立清理任务。

## 2. 调研结论（已核实代码）

| 事实 | 位置 | 对方案的影响 |
| --- | --- | --- |
| Gin v1.9.1，`gin.New()` 后立即 `r.Use(Recovery())` | `backend/internal/server/http.go:51-52` | metrics 中间件必须注册在 Recovery **之前**，否则 panic 时 defer 读到的是外层 Recovery 写 500 之前的默认状态（误记 200） |
| 无任何 Prometheus 客户端依赖 | `backend/go.mod` | 新增 `github.com/prometheus/client_golang` |
| 全局中间件链 | `backend/internal/server/router.go:59-71` | 埋点不插入该链，改在 http.go 包裹（见上） |
| 已有自研 DB 侧监控：`ops_metrics_collector` 每分钟聚合业务口径（QPS/TPS/耗时分位/TTFT/CPU/内存）入 `ops_system_metrics`，小时/日预聚合表 + cron retention 清理 | `backend/internal/service/ops_metrics_collector.go`、`ops_aggregation_service.go`、`ops_cleanup_service.go` | Prometheus 方案与其**互补**：有界标签的实时 HTTP 指标 + 整机指标归 Prometheus；业务口径（usage_logs / ops_error_logs 样本，按 DB 记录）保留在 ops 体系，两者**概念重叠但不可对账**（口径差异见 MONITORING_API.md 指标字典"与 ops 体系关系"列）。新增 Prometheus 开关与现有 OpsMonitoringEnabled 独立命名、互不隐式依赖；scrape 时不查询 DB 聚合表 |
| `/health`、`/setup/status` 已被日志排除；`POST /api/event_logging/batch` 为直接 200 的假端点 | `internal/server/middleware/logger.go:26`、`routes/common.go:17` | 同样从指标中排除 |
| 生产部署：`deploy-us1.sh` 构建上传，`deploy/remote-deploy.sh` 读取 Compose labels 中项目名、配置与环境文件 | `deploy-us1.sh` / `deploy/remote-deploy.sh` | 默认更新 sub2api 并部署 Grafana/Prometheus，数据库和 Redis 不重建；保留 pg_dump、运行中旧镜像和应用回滚脚本，业务 Compose 不做正则改写 |
| Docker 网络：`sub2api-network`（bridge） | `deploy/docker-compose.yml:331-333` | 同项目 `-f` 叠加模式下监控容器自然加入该网络（叠加文件复用网络声明，不标 external）；若生产采用独立监控项目部署，才用 `external: true` 且填写运行时实际网络名（带 compose 项目前缀，逻辑 key ≠ 运行时名） |

## 3. 总体架构（一期）

```
                          ┌─ sub2api 容器 ──────────────┐
sub2api:9091 /metrics ◄───┤ 业务端口 8080（不动）          │
（不映射宿主机端口）        └──────────────────────────────┘
        ▲ 拉取(15s)                ▲ 拉取
┌───────┴────────┐   ┌────────────┴───┐
│  Prometheus    │   │  Node Exporter │ 可选 host 网络 + 私有网关监听
│  TSDB 持久化卷   │   │  9100          │ /proc /sys /rootfs
└───────┬────────┘   └────────────────┘
        │ PromQL
┌───────┴────────┐        ┌────────────────┐
│  Grafana       │        │ 备份 cron 容器   │ snapshot → tar.gz → 旧包删除
│  provisioning  │        │（选定工具链镜像）  │
└────────────────┘        └────────────────┘
```

关键决策：

| 决策点 | 结论 | 理由 |
| --- | --- | --- |
| /metrics 暴露方式 | **独立内部端口 9091**，容器内 `0.0.0.0:9091`，不映射宿主机端口；Prometheus 经 `sub2api-network` 以 `sub2api:9091` 访问 | 主端口不新增公网面；无须经 Caddy 加阻断规则；主端口+token 为备选，涉及 token 进日志与 Prometheus 凭据管理，复杂且不更安全 |
| 中间件注册位置 | http.go 中包裹在 Recovery 之前；完成记录用 defer（保证 in_flight 归还），panic 场景 Recovery 在内层先写最终状态码 | http.go:51 的 Recovery 在最外层，见第 2 节 |
| Registry | 自建 `prometheus.Registry`，显式注册，`promauto.With(registry)` | 避免默认全局 registry 与 go/process collector 重复注册 |
| 流式口径 | `response_mode ∈ {sse, other}`，响应结束后按最终 Content-Type 判定 `text/event-stream`；duration 记请求全生命周期 | 不缓冲 writer、不改 Flush/Hijack/Unwrap；SSE ≠ 全部流式，文档明示 |
| 指标基数 | 所有标签均为**有界有限集**（路由模板、有限 method 集、有限 status 来源、sse/other），绝不引入高基数无界标签 | 需求文档 6.4 |
| Node Exporter | 可选 `host-metrics` profile；host 网络 + 只读宿主挂载，绑定 Docker bridge 的 RFC1918 私有网关 IPv4:9100。Prometheus 使用同一 IP，不依赖服务 DNS。默认五容器基础栈不启动 Exporter，也不渲染 node job | bridge 网络下 netlink 与 `/proc/net` 读到容器网络；host 网络下限定私有网关监听，保持公网不可达。Desktop 指标属于 Linux VM |
| Grafana 访问 | 一期不暴露公网；本地/生产均绑 `127.0.0.1`，生产经 SSH 隧道访问 | 需求文档 10.1 |
| Compose 组合 | **同一 Compose 项目 `-f` 叠加为基线**（叠加文件复用原网络声明，不标 external）；服务器若需要"独立监控项目"方式部署，才用 `external: true` 且写**运行时实际网络名**（通常带 compose 项目前缀，逻辑 key ≠ 运行时名） | external 网络在本地首次启动不存在会直接失败；两种模式择一写清基线，不混用 |
| Prometheus 配置渲染 | 仓库保存 `prometheus.yml.tmpl` 模板 + 渲染脚本（POSIX awk，无额外包依赖）生成实际配置；Prometheus 不会对 bind-mount 的配置文件做环境变量展开，`${VAR}` 直接写不生效。渲染产物不进 Git | promtool 校验渲染后的文件 |
| 配置变更生效 | 渲染内容不变时保留 inode，改变时脚本重建 Prometheus；环境变量/command 变化也需重新 up | 原子替换的单文件 bind mount 可能继续指向旧 inode，不能只 restart/SIGHUP |
| 备份容器 | 使用固定版本 Alpine 的 crond/wget/tar 工具链；Prometheus 挂完整 `prometheus_data` 卷，备份容器通过 `volume.subpath: snapshots` **只挂同卷的 snapshots 子目录**为 `/snapshots`（可写），无法访问 WAL/活跃块。一次性 init 创建目录并设置属主，不递归更改活跃块。**不能另挂独立 snapshots 卷**，因为已落盘块快照使用硬链接，跨挂载会 EXDEV；不调用不存在的 delete_snapshot API | 本地已验证空卷首次初始化、已有落盘块硬链接（同 device/inode）、完整脚本打包/清源与隔离恢复。最低支持基线 Engine≥26.0 / API≥1.45、Compose≥2.35.0；定时任务明确 UTC |
| 备份清理 | 快照目录不受 TSDB retention 管理；打包成功与失败路径都要受控清理，防双倍堆积 | 见 1.1 节红线 |
| 镜像版本 | Prometheus `v3.16.0`、Grafana `13.3.1`、Node Exporter `v1.12.1`、Alpine `3.20`（以 Compose 固定 tag 为准） | 禁止 latest；Grafana 从原先已 EOL 的 11.x 调整为仍维护的版本，已验证 provisioning 与看板兼容 |

## 4. 交付物文件布局

```
backend/
  internal/monitoring/            # 指标定义与 registry（纯定义，可单测）
    metrics.go                    # counter/histogram/gauge 定义 + NewRegistry()
    server.go                     # 9091 端口 http server 生命周期（listen/shutdown/DI）
  internal/server/middleware/
    metrics.go                     # HTTP 指标中间件（注册于 Recovery 之前）
    metrics_test.go
  internal/config/config.go       # monitoring 配置段
deploy/
  docker-compose.monitoring.yml   # prometheus + grafana + node-exporter + 备份 cron（叠加层）
  monitoring/
    prometheus.yml.tmpl            # 采集配置模板（渲染脚本 envsubst 生成 prometheus.yml）
    render-prometheus-config.sh    # 模板渲染 + promtool check
    alerts/                         # 一期仅采集状态规则（可选，告警后置）
    grafana/provisioning/
      datasources/prometheus.yml   # 数据源 provisioning（稳定 UID）
      dashboards/dashboards.yml    # 看板 provisioning provider
    grafana/dashboards/
      sub2api-overview.json        # 基础看板（稳定 UID）
    backup/
      snapshot.sh                  # snapshot → tar.gz → 清理旧备份
      crontab.example
  monitoring.env.example           # 端口/retention/备份策略/管理员密码等变量
docs/
  MONITORING_API.md               # 接口与指标字典
  MONITORING_DEPLOY.md            # 本地启动、生产部署、更新回退、排障（阶段 2 交付）
```

## 5. 配置接口（详见 MONITORING_API.md）

应用侧新增配置段（默认关闭，向后兼容——不配置则行为与现状完全一致）：

```yaml
monitoring:
  metrics_enabled: false        # env: MONITORING_METRICS_ENABLED
  metrics_bind: "0.0.0.0:9091"  # env: MONITORING_METRICS_BIND
```

### 5.1 容量预算（按 2 核 / 2GB / 50GB 回填的**初始值**，待实测校准）

内存是本机最紧资源（业务栈 + 监控栈共存）。监控容器统一设 `mem_limit`，防止单容器失控拖垮业务。**以下 mem_limit 为部署初始值而非验证结论**：四容器合计 896MiB，约占 2GB 的 43.75%，与业务栈（Go 应用 + PostgreSQL + Redis + Caddy + OS）能否共存取决于双方真实用量，必须部署后实测校准；limit 之和也不构成"实际可用容量"的证明。快照压缩任务运行时还会额外消耗 CPU 与页缓存。

| 容器 | mem_limit（初始值） | 说明 |
| --- | --- | --- |
| prometheus | 384MB | TSDB head 内存与活跃序列数相关，实测后校准 |
| grafana | 384MiB（`GRAFANA_MEMORY_LIMIT` 可调） | 本地 Grafana 13.3.1 冷启动实测约 255MiB，原 256MiB 几乎无余量，故上调；2GB 生产主机仍需单独评估 |
| node-exporter | 64MB | 采集器预期极轻 |
| backup | 64MB | cron 任务瞬时运行 |

另有一次性 `snapshots-init` 容器 `mem_limit=32MiB`，初始化完成即退出；896MiB 是上述四个容器限额之和，不包含该短时峰值。

磁盘（50GB）：业务数据（PostgreSQL + Redis + 应用数据 + 日志）预留 40GB，监控栈**预算** ≤ 5GB（TSDB 块 1GB + WAL/快照备份区 ≤ 2GB + 安全垫）。注意此预算的两个不确定性：① `retention.size` 非配额、删除异步，WAL/head/快照另占空间；② 每日快照是**整个保留窗口**的复制，3 天保留即最多 3 份全窗口数据 + 压缩临时产物 + 源快照目录瞬时共存，实际占用需以运行余量判定而非纸面估算。`retention.time=15d` 是删除上界（size 先到则不足 15 天），不保证至少保留 15 天；1GB size 与 15 天哪个先到，取决于真实活跃序列数、采样率、块压缩率与 WAL 开销，部署后以实测数据复核。

### 5.2 Prometheus 侧变量（`monitoring.env`）

| 变量 | 值（2C/2G/50G） | 说明 |
| --- | --- | --- |
| PROM_SCRAPE_INTERVAL | 30s | 2 核机器取需求文档 15~30s 建议区间的上限，降低采集开销 |
| PROM_RETENTION_TIME | 15d | 指标保留目标上界（time 与 size 先到者生效；size 先到时实际保留不足 15 天） |
| PROM_RETENTION_SIZE | 1GB | 块容量上限（先到先删）。注意：它不是数据目录硬上限，WAL/head/快照/备份需额外空间，删除为异步过程，不能当磁盘配额依赖 |
| SNAPSHOT_SCHEDULE | `0 4 * * *` | 每日快照压缩时刻 |
| SNAPSHOT_KEEP_DAYS | 3 | 备份包保留天数（独立于 TSDB retention；每日快照含整个保留窗口，多份瞬时共存，50GB 磁盘下取小值） |
| GRAFANA_ADMIN_PASSWORD | 必填 | 禁默认 admin |

容量复核（一期为**人工检查**而非自动告警——告警通知属后续迭代）：部署前核对当前磁盘/内存余量足够容纳预算；上线后首周每日人工检查磁盘用量、监控容器 RSS 与 limit 距离、TSDB 实际块大小、备份区实际占用，据此校准 retention/size/mem_limit；此后磁盘 >80% 或任一容器 OOM/接近 limit 时随时重估。

## 6. 阶段划分与任务分解

### 阶段 1：数据采集与生命周期基础链路（本次核心，用户指定优先）

| # | 任务 | 负责 | 验证方式 |
| --- | --- | --- | --- |
| 1.1 | 引入 client_golang；`internal/monitoring` 定义指标 + 独立 registry；9091 metrics server 生命周期接入 DI/shutdown | 后端 | 单测：registry 注册、重复注册防护、shutdown 幂等 |
| 1.2 | HTTP 指标中间件（Recovery 之前注册；defer 归还 in_flight；排除路径表；route=FullPath 空归 unmatched；method 归一有限集） | 后端 | 单测：panic 场景状态码、取消、动态路径 404 基数、SSE/other 判定 |
| 1.3 | 监控 Compose 叠加文件（同项目 `-f` 叠加，复用原网络声明）+ node-exporter + Prometheus 配置 + 持久化卷 + retention/size | 基建 | `docker compose -f docker-compose.yml -f docker-compose.monitoring.yml config` 校验；promtool check config |
| 1.4 | Grafana provisioning（数据源 + 基础看板，稳定 UID） | 基建 | 空环境启动后零手工配置可见看板 |
| 1.5 | 备份 cron：snapshot → tar.gz → 清理旧备份与失败残留（打包成功/失败路径均受控清理；备份容器只对 snapshots 子路径有写权限） | 基建 | 手动触发一次，验证快照包可解压、旧包与失败临时目录被清、TSDB 活跃数据未被动；**恢复演练**：用隔离的临时 Prometheus 实例加载备份 tar.gz，查询已知历史样本确认可恢复（可解压 ≠ 可恢复） |
| 1.6 | 验证 TSDB retention 自动删除（隔离实例、受控历史块） | 基建 | 已以 promtool 生成 12 小时前及近期两个块，在独立临时 Prometheus 中设置 1h retention：旧块由两个减为一个、旧历史查询为空、近期已知样本仍可查询。正常实时采集的 head 块切割约 2h 且删除异步，**1h retention 不保证每个样本一小时即删** |

### 阶段 2：联调验证与生产部署

| # | 任务 | 验证方式 |
| --- | --- | --- |
| 2.1 | 本地全链路联调：发请求 → 指标增量 → 看板展示；4xx/5xx 受控构造验证 | 需求文档 12.2 逐项 |
| 2.2 | 验收映射：需求文档第 12 节 → 可执行步骤清单（并入 MONITORING_DEPLOY.md） | 人工验证记录 |
| 2.3 | 生产部署：上传 compose 叠加文件 + .env 至服务器 compose 目录，`up -d` 仅监控栈；核对实际网络名与 caddy 路由 | 需求文档 12.5 |
| 2.4 | 生产安全验证：公网扫描 9091/9090/9100/3000 不可达；Caddy 域名不透出 /metrics | 外部探测 |
| 2.5 | 服务器规格已回填（2C/2G/50G，见 5.1）：部署后首周实测监控容器 RSS、TSDB 实际块大小、备份区实际占用，据实校准 mem_limit 与 retention/size（5.1 全部为待验证初始值） | 容量记录文档 |

### 后续迭代（不在本次范围）

告警规则与通知渠道；PostgreSQL/Redis Exporter；cAdvisor；TTFT/业务层流式埋点（与 ops_system_metrics 融合的看板）。

## 7. 风险与应对（本计划新增）

| 风险 | 应对 |
| --- | --- |
| 9091 端口与容器内现存监听冲突 | 9091 在 sub2api 容器命名空间内，宿主 `ss` 查不到；核对容器内监听（`docker exec sub2api ss -ltnp` 或 /proc/net/tcp）并确认 Prometheus scrape 目标地址一致；冲突时经 MONITORING_METRICS_BIND 改端口 |
| snapshot admin API 被滥用 | `--web.enable-admin-api` 配合 Prometheus 端口不映射宿主机（仅容器网内可达）；Grafana/Prometheus 均绑 127.0.0.1 或不发布 |
| 备份容器工具链缺失 | 选定并验证过工具链的镜像（cron/HTTP/JSON 解析/tar/gzip），不假设 prometheus 镜像自带 curl/cron；任务 1.5 现场演练兜底 |
| macOS 本地 node-exporter 指标语义与 Linux 不同 | 本地仅验证"可采集"；数据语义在生产 Linux 复验（需求文档 5.2 已有此约束） |
| retention/size 误配吃满磁盘 | PROM_RETENTION_SIZE 必设但**不是磁盘配额**（WAL/head/快照/备份额外占空间、删除异步）；容量参数为初始值，靠首周人工检查 + 部署后实测校准兜底，自动水位告警属后续迭代 |
| 2GB 内存下监控栈挤占业务内存 | 监控容器统一 mem_limit 初始值（合计 896MiB，约 43.75%），部署后实测与业务栈真实用量共同校准；limit 之和不代表实际可用容量证明 |
| 生产 compose 与仓库示例漂移 | 一切以服务器实际文件为准；同项目叠加不引用业务服务定义；若生产改用独立项目 + external 网络，须填运行时实际网络名（`docker network ls` 查带项目前缀的名称） |
| 备份失败残留占用磁盘 | snapshot 成功才 tar.gz；tar 失败删除半成品；临时目录统一前缀，清理任务兜底 find 删除超期残留 |

## 8. 验收标准（阶段 1 完成 = 用户指定基础要求达成）

1. `docker compose -f docker-compose.yml -f docker-compose.monitoring.yml up -d` 后，Prometheus 采集 sub2api 与 node-exporter 成功（`up == 1`）。
2. 发送已知数量请求后 `sub2api_http_requests_total` 增量一致；4xx/5xx 分列；SSE 与普通请求 response_mode 正确分桶。
3. Prometheus 重启不丢数据（持久化卷）；in_flight 在请求结束/panic/处理器观察到取消并退出后归零。
4. 快照定时任务产出 tar.gz 且旧备份按天数被删；备份经隔离实例恢复演练确认可查历史样本；TSDB 数据目录从未被 tar/cron 直接操作。
5. 隔离实例用受控历史块验证超期块自动删除、旧查询无数据而新样本仍可查；不以 1h retention 推断实时样本一小时即删。
6. Grafana 打开即有数据源与基础看板，无手工导入。
7. 停止全部监控容器，业务接口（含流式）行为正常。
