# Sub2API 监控接口与指标字典

- 文档状态：v1（配套开发计划 `docs/MONITORING_DEV_PLAN.md`）
- 编写日期：2026-11-14
- 范围：应用指标端点、指标字典（名称/类型/单位/标签/口径/与现有 ops 体系的关系）、Prometheus 采集接口、备份 snapshot 接口

## 1. 应用指标端点

### 1.1 端点定义

| 项 | 值 |
| --- | --- |
| 路径 | `/metrics` |
| 监听 | **独立内部端口**，默认 `0.0.0.0:9091`（与业务端口 8080 完全分离） |
| 方法 | GET |
| 格式 | Prometheus exposition format，经 `promhttp` 按 Accept 协商（Prometheus text / OpenMetrics）；具体版本随 client_golang 演进，消费方不应锁死 `version=0.0.4` |
| 鉴权 | 无应用层鉴权；安全边界 = 不映射宿主机端口 + 仅 `sub2api-network` 容器网内可达 |
| 与 /health 的关系 | 完全独立；/metrics 不替代 /health，/health 不产生指标 |
| 配置 | `monitoring.metrics_enabled`（默认 false）/ `monitoring.metrics_bind`（env: `MONITORING_METRICS_ENABLED` / `MONITORING_METRICS_BIND`） |

### 1.2 端口选择决策记录

- **采用独立端口**：主端口不新增任何公网暴露面；无须在 Caddy 增加 `/metrics` 阻断规则（生产 Caddy 泛代理主端口，主端口注册 /metrics 有被透出的风险）；无须管理 Prometheus 侧 token 凭据（token 方案有 token 进日志的风险面）。
- 备选（主端口 + token）记录但不采用：需要 Caddy 显式阻断、Prometheus bearer token 配置、token 轮换管理，复杂度更高而安全性不更优。
- **独立端口 ≠ 自动安全**：默认绑定 `0.0.0.0` 仅为容器网内互通；`docker-compose.monitoring.yml` 不为 9091 发布宿主机端口。若未来跨主机采集，需另行设计 TLS/鉴权，不在本版范围。
- 启用 `metrics_enabled` 时服务启动流程：`net.Listen` 失败必须使进程启动失败（fail-fast），并纳入 DI 与 Shutdown 生命周期（幂等关闭）。setup 模式下行为一并明确。

### 1.3 排除路径（不计入指标）

以下路径不产生 HTTP 指标。口径来源：`/health`、`/setup/status` 复用 `logger.go:26` 现有日志排除清单；`POST /api/event_logging/batch` 为指标**新增**排除（该假端点在日志侧仍走现有逻辑，两清单不强求一致）：

- `/health`（容器健康检查）
- `/setup/status`（前端重启探测）
- `POST /api/event_logging/batch`（Claude Code 遥测假端点，直接 200）
- `/metrics` 自身（独立端口，天然不在业务链路）

静态资源/SPA 由前端嵌入中间件服务，**纳入指标但归入有界分类**：已注册路由按模板聚合；未匹配路由统一 `unmatched`。排除路径表是有限白名单，不能把任意路径转为新标签值。

## 2. HTTP 指标定义

实现要点（对应开发计划决策）：

- 中间件注册于 **Recovery 之前**（`http.go`），完成记录用 `defer`；panic 时内层 Recovery 先写最终状态码，再由 defer 记录 → 不误记 200。in_flight 同样在 defer 中归还（panic/取消/正常结束三路均不泄漏）。
- 不读取请求体，不包装/缓冲 ResponseWriter，不改变 Flush/Hijack/Unwrap 行为（不破坏 SSE 及时输出）。
- 自建 `prometheus.Registry`，`promauto.With(registry)` 注册；go/process 指标用标准 collector 显式加入同一 registry，不为 gopsutil 重复造进程指标。

### 2.1 sub2api_http_requests_total

| 项 | 值 |
| --- | --- |
| 类型 | Counter |
| 单位 | 次（累计） |
| 标签 | `method`（有限归一集，未知归 OTHER）、`route`（c.FullPath() 路由模板；空 = `unmatched`）、`status_code`（实际写出状态码）、`response_mode`（`sse` \| `other`） |
| 计数时机 | 请求完全结束（写入响应完成/panic 恢复后）时 +1 |
| PromQL 示例 | 总 QPS：`sum(rate(sub2api_http_requests_total[5m]))`（先 sum 聚合各标签序列）；5xx 率：`sum(rate(sub2api_http_requests_total{status_code=~"5.."}[5m])) / sum(rate(sub2api_http_requests_total[5m]))` |

### 2.2 sub2api_http_request_duration_seconds

| 项 | 值 |
| --- | --- |
| 类型 | Histogram |
| 单位 | 秒 |
| 标签 | `method`、`route`、`response_mode`（无 status_code，控制基数） |
| 计时区间 | 请求进入中间件 → 处理器全生命周期结束（**非首字节时间**） |
| 桶 | client_golang 默认秒桶 `0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10` + 追加 `30, 60, 120, 300, 600`（显式全量列表，实现须用 `prometheus.ExponentialBuckets` 之外的自定义 `[]float64`，防止默认值漂移；超最大桶的分位数查询为截顶近似，看板注明） |
| PromQL 示例 | 普通请求 P95：`histogram_quantile(0.95, sum by (le) (rate(sub2api_http_request_duration_seconds_bucket{response_mode="other"}[5m])))` |

**流式口径声明（看板必须标注）**：

1. `response_mode=sse` 仅表示**最终响应 Content-Type 为 `text/event-stream`**；不代表识别全部流式协议，也不表征"客户端想要流式但失败返回 JSON"的请求。
2. SSE 请求 duration 是整个连接生命周期，值远大于普通请求，**不等于 TTFT**；普通接口延迟面板必须过滤 `response_mode="other"`，且口径是"非 SSE"而非"全部非流式"。
3. HTTP 2xx ≠ 业务成功（SSE 开始输出后业务失败不改状态码）；业务成功/失败以 ops 体系（usage_logs / ops_error_logs）为准。

### 2.3 sub2api_http_requests_in_flight

| 项 | 值 |
| --- | --- |
| 类型 | Gauge（一期无标签） |
| 单位 | 个 |
| 语义 | 当前正在处理的请求数（进入中间件 +1，defer −1） |
| 验证要求 | 正常结束、panic 两路径下归零；取消路径的精确口径：中间件 defer 在**处理器观察到取消并退出**后归还（客户端断开本身不保证处理器立即退出，归还时机绑定在服务端处理结束）；长请求进行中仅体现于 in_flight，不提前计入 requests_total |

### 2.4 运行时指标（标准 collector）

`go_*`（goroutine、GC、内存等）与 `process_*`（CPU、RSS、启动时间）直接复用 client_golang 标准 collector。注意语义边界：

- `process_*` 是**应用进程**资源，与 Node Exporter 的**宿主机**资源不同，百分比不可直接对比。
- 与 ops 体系 CPU/内存的关系见 2.5。

## 2.5 与现有 ops 体系的口径对账（不可对账，仅概念重叠）

现有 `ops_system_metrics`（`ops_metrics_collector.go`，每分钟聚合入 DB）为业务口径权威，两者**概念重叠但严禁在对账场景混用**：

| 维度 | Prometheus 指标 | ops_system_metrics | 关系 |
| --- | --- | --- | --- |
| 请求量 | `sub2api_http_requests_total`：外层 HTTP 请求（含认证/管理接口，排除白名单路径） | SuccessCount = usage_logs 行数（成功样本）；错误数 = ops_error_logs 中非 count_tokens 且 status≥400 | **概念重叠，不可对账**：认证失败、管理接口、重试、业务日志写入时序都会造成差异 |
| 延迟 | HTTP 处理全生命周期，秒，直方图桶近似 | usage_logs.duration_ms 样本，毫秒，SQL `percentile_cont` 精确分位 | 概念重叠，样本集与统计方法不同 |
| TTFT / TPS / token / 业务限流 / 供应商 429/529 | 一期无对应 | ops 权威 | 一期不复制；后续业务埋点单独设计 |
| 并发 | in_flight（HTTP 在途请求） | ConcurrencyQueueDepth（调度队列深度） | **互补，不等同** |
| goroutine | `go_goroutines` | GoroutineCount | 同概念、采样窗口与实例不同 |
| CPU/内存 | process_*（进程）+ Node Exporter（宿主机） | cgroup 优先、宿主回退 | 三者语义各不相同，不可直接对比 |

设计约束（由上表推出）：

1. 新增 Prometheus 开关与现有 `OpsMonitoringEnabled` **独立命名、互不依赖**：关闭业务 Ops 采集不应停掉基础 HTTP 指标，反之亦然。
2. `/metrics` 端点**不查询 DB**（不把 ops 聚合表在每次 scrape 查一遍）；两体系各自独立采集。

## 3. Prometheus 采集接口

配置以**模板 + 渲染**方式管理：仓库保存 `prometheus.yml.tmpl`，部署/启动前由 `render-prometheus-config.sh`（POSIX awk）渲染为实际配置文件。原因：Prometheus 不会对配置文件中的 `${VAR}` 做环境变量展开（该语法仅在部分字段有限支持），Compose 也不会替换 bind-mount 的文件内容——直接写 `${PROM_SCRAPE_INTERVAL}` 不生效。渲染产物不进 Git，`promtool check config` 校验渲染后的文件。

模板内容（渲染后）：

```yaml
global:
  scrape_interval: 15s          # 渲染自 PROM_SCRAPE_INTERVAL
  scrape_timeout: 10s

scrape_configs:
  - job_name: sub2api
    static_configs:
      - targets: ["sub2api:9091"]            # 容器网内服务名，非 localhost
  # 以下 node job 仅在 --host-metrics 启用后渲染，地址由脚本从 Docker bridge 获取：
  # - job_name: node
  #   static_configs:
  #     - targets: ["<Docker 私有网关 IPv4>:9100"]
  - job_name: prometheus
    static_configs:
      - targets: ["localhost:9090"]          # 自监控，辅助排障
```

`up` 指标语义：仅代表该抓取目标可采集，不代表业务健康（业务健康看 /health 与业务面板）。

配置生效规则：

- 渲染内容不变时保留文件 inode；内容改变时一键部署脚本重建 Prometheus。原子替换的单文件 bind mount 不能仅靠 restart/SIGHUP 更新。
- **修改环境变量或 compose command → 必须重新 `docker compose up -d prometheus` 重建容器**；restart 不会应用新的容器环境。
- 发布前 `promtool check config` 校验渲染产物。

基础部署为 sub2api/postgres/redis/grafana/prometheus 五个常驻容器，默认只抓取 sub2api 与 prometheus。`--host-metrics` 添加独立 Node Exporter：使用 host 网络命名空间，绑定 Docker 私有网关的 9100；Prometheus 按该地址采集，不再使用 bridge 容器的网络计数或服务 DNS。`--backup` 添加独立 cron 和一次性卷初始化容器。入口与参数见 `deploy/MONITORING_LOCAL.md`。

## 4. 备份 snapshot 接口

| 项 | 值 |
| --- | --- |
| 端点 | `POST http://prometheus:9090/api/v1/admin/tsdb/snapshot` |
| 前提 | Prometheus 启动参数 `--web.enable-admin-api`；端口不映射宿主机，仅容器网内可达 |
| 行为 | 在数据卷 `prometheus_data` 的 `snapshots/<name>-<unix>` 下创建一致性快照 |
| 后续 | 备份容器 cron 执行 snapshot.sh：调 API → 等 `{"status":"success"}` → tar.gz 快照目录到备份区 → 删除源快照目录 → 清理超期备份与失败残留。备份镜像须含验证过的工具链（cron、HTTP 客户端、JSON 解析、tar/gzip），不假设 prometheus 镜像自带 |
| 挂载边界 | Prometheus 挂 `prometheus_data` 根目录，备份容器只通过 `volume.subpath: snapshots` 挂该卷的快照子目录为 `/snapshots`（可写），看不到 WAL/活跃块；一次性 init 负责首启目录和属主。快照必须与 TSDB 块在同一卷，**独立 snapshots 卷会导致已落盘块硬链接 EXDEV**。源快照由脚本在受限子目录直接清理，不存在 delete_snapshot API。支持基线 Engine≥26.0 / API≥1.45、Compose≥2.35.0 |
| 恢复验证 | tar.gz 可解压 ≠ 可恢复；验收须用隔离临时 Prometheus 实例加载备份并查询已知历史样本 |
| 清理 | 备份包按 `SNAPSHOT_KEEP_DAYS` 独立清理（`find -mtime +N`）；快照目录不受 TSDB retention 管理，必须由脚本显式回收，成功与失败路径都受控 |

红线（重复强调）：备份只走 snapshot API，**不 tar 活跃 TSDB 目录**；**不 cron 删除活跃块**，TSDB 数据删除只由 `--storage.tsdb.retention.time` / `.size` 自动执行。

## 5. Grafana 自动配置

- 数据源 provisioning：Prometheus，URL `http://prometheus:9090`（容器服务名，非 localhost），稳定 UID `sub2api-prometheus`。
- 看板 provisioning：`sub2api-overview.json`，稳定 UID `sub2api-overview`，标记 folder `Sub2API`，与用户自建看板隔离。
- 面板覆盖：up 状态、请求速率（总/按路由）、4xx/5xx 速率与占比（分母为零处理为 no-data 而非 0）、P50/P95/P99（`response_mode` 分组，SSE 面板独立）、in_flight、go/process 面板、Node Exporter 整机面板。
- 看板迭代流程：本地页面调整 → 导出 JSON → 更新仓库文件 → 重启加载；provisioned 看板以 Git 文件为权威；导出内容不得含密码或环境特定地址。

## 6. 变更与兼容性约定

- 指标名、标签集、直方图桶任何变更须评估查询与看板兼容性，并在指标字典记录变更。
- 应用版本与监控配置版本在发布记录中对应（compose 文件与镜像 tag 一起进 release 说明）。
- 本字典为活文档：新增指标先补字典条目再进代码评审。
