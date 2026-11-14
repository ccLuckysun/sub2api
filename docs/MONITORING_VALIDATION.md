# 监控一期本地验收记录

- 日期：2026-11-14
- 状态：**本地开发版本可交付体验，第三方复审无剩余阻塞项**；不是生产验收或生产部署记录。
- 实际环境：macOS + Docker Desktop，Engine 29.7.2 / API 1.55 / Compose 5.5.0。
- 最终 Compose 项目：`deploy`；本地服务继续运行，用户可直接打开页面。

## 1. 本地查看

- Grafana：<http://127.0.0.1:3000/d/sub2api-overview/sub2api-overview>。
- Grafana 用户名：`admin`；随机密码在未入 Git 的 `deploy/monitoring.env` 的 `GRAFANA_ADMIN_PASSWORD` 中。
- 应用：<http://127.0.0.1:8080>；本地管理员邮箱为 `admin@localhost.test`，密码在同一文件的 `ADMIN_PASSWORD` 中。
- `monitoring.env` 权限为 0600；请勿提交该文件或将其密码复制进正式配置。
- 当前环境已初始化，不要为查看效果再次执行 `cp monitoring.env.example monitoring.env` 覆盖现有凭据。
- 完整冷启动和停用说明见 `deploy/MONITORING_LOCAL.md`。

## 2. 已执行验证

| 层次 | 操作 | 实际结果 |
| --- | --- | --- |
| 后端构建 | `go build ./...` | PASS |
| DI 生成 | `go generate ./cmd/server` | 正式 Wire 生成成功；不再依赖手工维护 wire_gen |
| 并发测试 | `go test -race -count=1 ./internal/monitoring/... ./internal/server/middleware/... ./cmd/server/...` | 主会话和第三方均独立执行，全部 PASS |
| 配置/路由 | `go test -tags=unit ./internal/config/... ./internal/server/...` | 执行体运行 PASS |
| HTTP 语义 | 单测正常/异常计数、Recovery 500、排除清单、未知 method、模板路由、显式桶、真实取消与 SSE Flush | PASS；SSE 测试使用 httptest.Server 验证首帧在处理器结束前到达 |
| 模板渲染 | envsubst + Docker promtool | PASS；非法采集间隔被拒绝，原有效配置保持不变 |
| Compose | 双 `-f`、明确 `--env-file` 的 `config --quiet` | PASS；应用指标 env 已由叠加文件注入 |
| 实际启动 | 业务容器源码构建后启动；Prometheus/Grafana/Exporter/备份全部运行 | 应用与数据库 healthy，Prometheus healthy |
| 数据链路 | Grafana 数据源代理查询 `up` | sub2api、node、prometheus 三个目标均为 1 |
| HTTP 实测 | 首页成功请求 + `/v1/models` 未认证 401；六次 `/health` | 前两者按对应状态/模板计数，health 不增加业务请求计数，结束后 in_flight=0 |
| 指标端点 | 9091 上 `/anything`、POST `/metrics` | 分别 404、405；主端口不暴露 Prometheus 数据 |
| Grafana | 实际登录隔离 Chrome、打开看板并截图；经真实数据源执行全部 24 条面板查询 | 看板有 16 个面板，查询均成功；请求/延迟/运行时数据实际展示 |
| 备份 | 最终本地栈执行原版 snapshot.sh | exit 0，生成压缩包，源快照清空，cleanup complete |
| 最小挂载 | inspect + 容器内检查 | backup 只挂同一 TSDB 卷的 snapshots 子路径；看不到 `/prometheus`、WAL、chunks_head |
| 已落盘块边界 | 新隔离项目预置真实块，执行 skip_head 快照与完整备份 | API 成功，源与快照 meta 同 device/inode、硬链接数 2，压缩包包含真实持久块 |
| 恢复演练 | 已落盘 fixture 压缩包在 network=none 的隔离 Prometheus 加载 | 历史已知样本 43 可查询 |
| 最终业务包恢复 | 最终栈真实压缩包在另一个隔离 Prometheus 加载 | 历史 `up{job="sub2api"}=1`、总请求数 17 可查询；验证后已停止恢复容器 |
| 清理故障路径 | 人为令备份输出只读；制造过期包、临时文件、孤儿目录及新目录 | 错误非零退出仍清源/过期残留；新目录保留 |
| TSDB retention | promtool 生成 12 小时前与近期两个块，隔离实例 retention=1h | 块数 2→1，旧历史查询为空，近期已知值 2 仍可查 |
| 暴露边界 | 本机检查 3000/8080/9090/9091/9100 | 仅 3000/8080 发布在回环地址；指标相关端口不发布 |

恢复演练首次启动临时实例时，验证命令遗漏 `--config.file`，实例退出 2；补充显式 `/etc/prometheus/prometheus.yml` 后查询通过。该错误仅涉及验证命令，不涉及源监控数据或恢复产物。

## 3. 审核中修复的问题

- 移除不存在的 `delete_snapshot` API，失败路径也执行保留清理。
- 不能将 snapshots 放独立挂载卷：真实已落盘块快照会因硬链接 `EXDEV` 失败；现改为同一 TSDB 卷 + `volume.subpath`，且首启目录初始化已实测。
- 修正 Wire 非接口 Bind 及 provider 多非错误返回值问题，正式生成通过。
- `/metrics` 服务限制为精确 GET 路径；完善取消、SSE 和并发 Stop 测试。
- 修正本地 DB 密码/环境文件说明、配置渲染原子替换和失效保护。
- Go/process 面板添加 `job="sub2api"`，不再混入 Prometheus/Exporter 的自身指标。
- Grafana 从已 EOL 的 11.x 调整至 13.3.1；本地冷启动接近原 256MiB 限额，初始上限改为可配置的 384MiB。常驻容器限额合计 896MiB，另有 init 短时 32MiB；不构成 2GB 生产机器可承载的证明。

## 4. 当前无数据与范围限制

- 没有真实 SSE 流量或某类错误时，相应面板无样本属于预期，不伪造业务成功、错误或上游数据填图。
- macOS Docker Desktop 的 Node Exporter 反映 Linux 虚拟机，**不是远程 2 核/2GB 主机，也不是 macOS 物理宿主指标**。本地 `/` 根分区可能无样本（本机实际有 `/var/lib/docker` 等挂载）；生产 Linux 的根分区、网络 namespace 和权限仍需独立验收。
- retention 受控块试验验证删除机制，不意味着实时样本精确到期即删；head 切块和自动删除均有异步边界。
- 日历定时任务使用 UTC（默认 04:00 UTC，即北京时间 12:00）；本轮手动触发了同一脚本并检查 cron 配置，未等待跨日自然触发。
- 自动通知告警不在本轮交付范围；磁盘/内存保障暂为人工检查与实测校准。
- 隔离测试/恢复资源已在用户随后要求的清理中删除（见第 6 节）；未删除任何业务数据卷。

## 5. 生产前置（用户本地验收后再处理）

1. 服务器 Engine≥26.0 / API≥1.45、Compose≥2.35.0 的基线核验。
2. 实际业务 Compose 项目、网络与版本核对，不复用本地随机密码。
3. Linux 宿主机指标 namespace、磁盘挂载点与网络指标专项验证。
4. 2 核/2GB/50GB 的真实可用余量及业务+监控共存实测，校准内存限额、采样间隔、块与备份预算。
5. 部署前和首周人工磁盘水位检查；生产访问保护与回退验证。

**本轮未连接或部署远程服务器，也未创建 Git commit。**

## 6. 开发资源清理（2026-11-14）

按用户要求清理本轮开发产物，保留当前本地验收环境：

- 删除 11 个已退出的试验/恢复/一次性初始化容器、11 个试验或已淘汰数据卷、2 个试验网络。
- 删除不再使用的 Grafana 11.6.16 测试镜像及本轮构建使用的 Node 24 镜像；当前应用和监控运行镜像保留。
- 按具体缓存 ID 精确清理本轮新建的 27 条未共享构建缓存，BuildKit 报告释放 3.572GB；没有执行全局 prune，也没有删除更早已有的工具链缓存。
- 删除临时浏览器 profile、截图、恢复信息文件、历史块夹具/override 目录及三份早期试验备份；保留最终实际业务备份 `prometheus-snapshot-20261114T073125+0000-20261114T073125Z-6cd2c3bccb32f5f3.tar.gz`。
- 源码、文档、私有 `monitoring.env`、Grafana/Prometheus 正式本地卷、PostgreSQL/Redis/应用数据和 7 个正在运行的验收服务均保留。

因此上文试验容器、卷和临时夹具名称只是历史验证记录，不再是仍然存在的资源；需要重跑时应创建新的隔离环境。下一次完整镜像构建可能需要重新下载依赖并重建缓存。
