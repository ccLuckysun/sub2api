# Sub2API 观测看板 V2 - 开发计划

| 项 | 内容 |
| --- | --- |
| 文档状态 | **计划定稿 v1.0**（Codex 审核无阻塞，2026-11-14 定案） |
| 编写日期 | 2026-11-14 |
| 输入 | `Sub2API-观测看板-V2-需求文档.md` v0.3；`/tmp/v2-codex-review.md` |
| 配套文档 | `docs/MONITORING_V2_API.md`（接口与指标契约）、`docs/dashboard-v2-mockup.html`（5 张看板布局评审稿） |
| 基线 | Grafana 13.3.1 / Prometheus v3.16.0 / sub2api 0.0.4（不含 ops 埋点） |
| 交付形态 | 本地开发 → 打镜像 → 上传服务器；看板/告警 as code；**本次不执行线上部署** |

---

## 1. 目标与范围

### 1.1 目标

沿请求链路逐段量化：任意时刻看到 6 段互斥耗时、5 方向字节、账号/模型维度与失败根因，值班内 1 分钟回答“问题出在哪一段”。

### 1.2 范围（P0 + P1）

| 优先级 | 内容 |
| --- | --- |
| P0 | 9 个原始指标 + 2 个契约扩展（M-10/M-11）；Claude/兼容转换/原生 OpenAI 四条链路 |
| P0 | `ops_request_transfer_samples` 恢复写入；`usage_logs.body_bytes`/`gateway_latency_ms` 新增（源码当前没有） |
| P0 | Prometheus 15s + 18 条规则 + Alertmanager + node-exporter + PG 只读源 + 5 张看板 |
| P1 | Antigravity / Gemini / Bedrock 三条链路接入（见 M2b） |
| P0 | 构建自检、部署后受控烟测、回滚 tag、AC 记录 |
| 不做 | Caddy 边缘、cAdvisor、Mihomo、CF Analytics、Prometheus 用户维度、线上部署 |

> **2026-11-14 用户确认**：旧 `sub2api-overview` 备份后下线，由 5 张 V2 看板替换，不保留双轨运行。01 不保留路由 top10 / in_flight / 非 SSE 延迟；这些能力分别由 03 的账号矩阵、在途列与 02 的 phase/TTFT 承担。

### 1.3 验收边界

- AC-1~AC-10 在本地 compose 跑通并留命令输出；AC-7 的**外部收件箱投递**需用户授权后单独执行，未执行前不得写“通过”。
- AC-9 用固定假上游/录制回放，不以两次真实 LLM 调用对比。

---

## 2. 基线对齐（B1，1 天）

### 2.1 任务

| # | 任务 | 判定 |
| --- | --- | --- |
| B1.1 | 本地 HEAD 构建镜像，确认 V1 指标 | `curl :9091/metrics \| grep sub2api_http_requests_total` 有输出 |
| B1.2 | 记录基线 commit、镜像 digest、Go 版本、迁移集合 | 写入 `docs/MONITORING_V2_VALIDATION.md` 附录 |
| B1.3 | 核对迁移状态：文件名 + checksum + applied_at（`backend/migrations/README.md:117`），不是只看最大编号 | 本地与线上集合差异清单；同名异内容必须列红 |
| B1.4 | 只读核对线上 `ops_request_transfer_samples` 列类型/非空/索引，以及 `usage_logs` 是否已有 `body_bytes`/`gateway_latency_ms` | 产出旧表→目标表映射；不兼容项阻止发布 |
| B1.5 | 本地 compose 冷启动，灌 20 条请求 | `usage_logs` 有行、V1 看板有数据 |
| B1.6 | 固定镜像 tag `0.0.5-v2mon` | 部署脚本拒绝 0.0.4 |

> B1.4 只执行 `\d` 与 `select ... limit 100`，不写库、不重启。

### 2.2 基线风险

- 本地 HEAD 与线上 0.0.4 若不等价（0.0.4 可能是更晚的私有提交），先补齐差异再埋点，否则 AC-9 无意义。
- 线上迁移集合与仓库不一致时，新迁移编号/内容必须避让；`CREATE TABLE IF NOT EXISTS` 不能修复类型差异，只能阻止发布并人工核对。

---

## 3. 总体设计

### 3.1 数据流

```
客户端
  │ body
  ▼
OpsMiddleware（网关入口，认证前）──► body_read
  │
  ▼
认证/限流（拒绝 → M-10 rejected / gateway_ratelimit）
  │
  ▼
handler：wait → gateway → 上游 attempt
  │                         │
  │                   Do/DoWithTLS（可能内部多次物理发送）
  │                         ▼
  │                   headers → first byte → response body
  ▼                         │
客户端 ◄── resp_downstream ──┘
  │
  └─ usage 提交（finish）──► PG usage_logs / ops_request_transfer_samples
                                 │
Prometheus ◄─ /metrics ◄─ Recorder
Grafana ◄─ Prometheus + PG（只读）
```

### 3.2 关键决策

| 决策 | 结论 | 理由 |
| --- | --- | --- |
| Recorder 建立点 | `server/http.go` 的 `OpsMiddleware`，在 Recovery 之前、认证之前 | 覆盖 body_read、限流拒绝、panic；Finalize 在 Recovery 之后读到最终结果 |
| attempt 边界 | 应用层一次转发尝试，到响应体结束为止 | `Do` 返回只有响应头；内部物理重发不单独计数，字节仍计入 |
| phase 模型 | 6 个互斥段 + 1 个重叠 TTFT | 保证总时长闭合；TTFT 不参与堆叠 |
| 字节口径 | 一律实际读写 `n`，不用 Content-Length | 声明值在中断/压缩场景会失真 |
| 失败分类 | `FailureEvidence` 纯函数 | 12+1 类可单测；不额外消费 body |
| 请求级根因 | 新增 M-10 | M-4 只保留真实上游 attempt，避免 unknown 二义性 |
| 账号 P95 | 新增 M-11 | M-1 没有 account 标签，PG 采样不能算总体分位 |
| finish | usage 任务提交完成 | 异步写入下不延长请求生命周期；DB 失败另观测 |
| 样本写入 | 100% 网关请求 | 当前 ~0.15 QPS，30d 约 40 万行；避免采样偏差 |
| 看板生成 | Python 生成器 | 1 行 = 1 查询，禁止手改 JSON |

### 3.3 路径矩阵（详见 API §3.5）

- P0：Claude Messages、Anthropic passthrough、Chat Completions 兼容转换、Responses 兼容转换、**原生 OpenAI `OpenAIGatewayService.Forward` 与独立链**。
- P1：Antigravity、Gemini v1beta、Bedrock。
- P2：images/批量/WS 只计请求级结果。
- 排除：count_tokens、models、探测/余额、管理接口。

> 转发的“公共出口”不是唯一的：`ForwardAs*` 只覆盖兼容转换，原生 OpenAI 有独立链（`openai_gateway_handler.go:826`、`openai_chat_completions.go:262`）。M2a 必须先做路由→handler→service→usage 的清单，再动手。

---

## 4. 工作分解

估时按 1 人、含单测与自测，不含审核等待。

### M1 埋点基础设施（2.5 天）

| # | 任务 | 文件 | 验收 |
| --- | --- | --- | --- |
| 1.1 | 指标定义 M-1~M-11（显式桶） | `backend/internal/monitoring/ops_metrics.go` | 单测断言名称/标签/桶/注册无重复 |
| 1.2 | Recorder/Attempt 状态机（请求级单次、attempt 多实例、Finalize 输入） | `ops_recorder.go` | `-race`；重复/未 Done/跨 goroutine 用例 |
| 1.3 | 入口中间件 + 网关路由白名单（ctx 写回 `c.Request`） | `server/middleware/ops.go`、`server/http.go` | 认证失败、限流拒绝、body 读失败都能记录 |
| 1.4 | Recovery 打 panic 标记 | `server/middleware/recovery.go` | panic 最终落 `gateway_panic`，不误记 success |
| 1.5 | 标签/失败分类纯函数 + FailureEvidence | `ops_labels.go` | 13 类分支 + 优先级冲突 + success 空串 |
| 1.6 | M-9 分钟桶（单锁整体 Swap） | `ops_coverage.go` | 假时钟 3 分钟；无样本 = -1 |
| 1.7 | 配置 3 项 + 非法组合 fail-fast | `config/config.go`、测试 | 启动失败信息明确 |
| 1.8 | 注册到 registry | `monitoring/metrics.go`/wire | 启动后 `/metrics` 无重复 HELP |

### M2a P0 路径（5 天）

| # | 任务 | 验收 |
| --- | --- | --- |
| 2a.0 | 路由→handler→service→usage 清单（含原生 OpenAI/Gemini/Antigravity） | 清单进 `docs/dashboard-v2.md`，与 API §3.5 对齐 |
| 2a.1 | body_read + M-2/M-3（实际读取字节） | 正常/部分读/读失败三种；不用 Content-Length |
| 2a.2 | wait 计时（用户/账号槽） | 并发压测 wait 非 0，gateway 残差不为负 |
| 2a.3 | attempt 生命周期（应用层创建，HTTP 层只更新；send→body done） | 200 后读超时、SSE 中途失败、签名纠错重发、Grok fallback 计数正确 |
| 2a.4 | response 字节 / resp_downstream（实际 Write n） | 客户端 abort 时 n 之和与 broker 记录一致 |
| 2a.5 | 原生 OpenAI 两条链接入 | 与兼容链同一套 Recorder；M-10/M-4/M-11 有数据 |
| 2a.6 | 失败分类接入 13 类 | 每类 1 个构造用例；代理 5xx 与源站 5xx 可区分 |
| 2a.7 | switch/outcome/finish | 3 次重试：M-4=3、M-6=retried_success、M-7 正确 |
| 2a.8 | panic/DB/限流 | 不误记 success；`gateway_db` 只覆盖同步路径 |

### M2b P1 路径（2 天，可裁剪但需签字）

| # | 任务 | 验收 |
| --- | --- | --- |
| 2b.1 | Antigravity Forward | phase/字节/attempt 与 P0 同口径 |
| 2b.2 | Gemini v1beta | 同上；端点归 `responses`/`other` 按 API §4.2 |
| 2b.3 | Bedrock | 同上 |
| 2b.4 | 混合平台回归 | 三平台各 10 请求，M-9 `kind="request"` ≥0.9 |

> 裁剪时必须在看板 description 与验收记录写明“仅覆盖 P0”，否则 AC-1/AC-5 结论不成立。

### M3 PG 样本与 usage 字段（2.5 天）

| # | 任务 | 验收 |
| --- | --- | --- |
| 3.1 | 基线映射 → 幂等迁移（样本表） | 空库、旧表、回滚旧镜像三场景 |
| 3.2 | 100% 异步写入器（队列 8192、批 500/1s） | 500 QPS 压测不阻塞请求；满队列丢弃有日志 |
| 3.3 | request_id 保证（入站无 ID 生成内部 ID） | 空 ID 请求也能落表 |
| 3.4 | `usage_logs` 两字段：迁移 + ent schema + 写入 + 查询 | 新请求非空率 ≥95%，历史行可读 |
| 3.5 | 30d 清理接入 cleanup targets | 31d 数据清理后剩 30d；0 天 = TRUNCATE（现有语义） |
| 3.6 | 历史 3257 行脱敏回放 | 5 张看板不报错；Top 40 口径一致 |

### M4 监控栈 as code（3 天）

| # | 任务 | 验收 |
| --- | --- | --- |
| 4.1 | Prometheus 模板 + 渲染脚本（15s、规则、alertmanager、node） | `promtool check config` |
| 4.2 | 18 条规则（完整表达式） | `promtool check rules` + 触发/恢复/缺失单测 |
| 4.3 | Alertmanager 邮件模板 | 本地 SMTP 收到；外发另授权 |
| 4.4 | compose：alertmanager 64m、规则挂载、PG 环境、node profile | `docker compose config`；内存账 |
| 4.5 | `grafana_ro` 最小权限 SQL + PG 数据源 | 只读/DML/DDL/超时四条验收 |
| 4.6 | 看板生成器 + 5 张 JSON（02 刷新 10s，其余 30s） | 面板数 16/19/9/6/15；无 No data；description 齐全 |
| 4.7 | 旧看板下线（先备份） | 只剩 5 张 V2 |

### M5 部署契约与回滚（1 天）

| # | 任务 | 验收 |
| --- | --- | --- |
| 5.1 | 构建自检（二进制 grep 仅辅助） | 无埋点镜像构建中止 |
| 5.2 | 受控烟测：唯一 request_id → /metrics 具体标签 → PG 精确行 | 三处同时命中才通过 |
| 5.3 | 回滚 tag + 脚本 | 回滚后 V1 正常、样本停写不报错 |
| 5.4 | 迁移 checksum/版本校验 | 同名异内容中止 |

### M6 文档与验收（2 天）

| # | 任务 | 验收 |
| --- | --- | --- |
| 6.1 | `docs/dashboard-v2.md`：阈值、判定树、口径与不可信数字 | 覆盖需求 §7/§8 |
| 6.2 | AC-1~AC-10 记录（命令、输出、结论） | 每条可复现；未执行项标“未执行” |
| 6.3 | 序列预算实测（整机 head） | <8000，单族 <2000 |
| 6.4 | README/本地部署文档 | 冷启动、升级、回滚可照做 |

### 里程碑

| 里程碑 | 内容 | 累计 |
| --- | --- | --- |
| B1 | 基线对齐 | 1d |
| M1 | 埋点基础设施 | 3.5d |
| M2a | P0 路径 | 8.5d |
| M2b | P1 路径（可裁剪） | 10.5d |
| M3 | PG 样本/usage | 13d |
| M4 | 监控栈 | 16d |
| M5 | 部署契约 | 17d |
| M6 | 验收文档 | 19d |

> v1 估 14d 未含 B1 与 P1，不成立。不含 P1 为 17d；含 P1 为 19d。

---

## 5. 测试策略

### 5.1 单元

- 归一化：随机/恶意/冲突名，集合 ≤11。
- failure：13 类 + 优先级 + success 空串。
- Recorder：并发、重复、未 Done、Finalize 重入、panic 标记、async finish。
- phase 闭合：同一请求 6 段之和 = total ±1ms；headers_end 不参与。
- 覆盖率：单锁 Swap、跨分钟、无样本 -1。
- 配置：非法组合 fail-fast。

### 5.2 集成（httptest 假上游）

覆盖：200 普通、200 SSE、429、500、502（代理）、超时（connect/read）、连接拒绝、客户端提前断开、200 后读超时、签名纠错重发、Grok fallback。

断言：
- M-1 闭合；headers_end 存在且不参与堆叠。
- M-2 各方向与假上游实际收发一致；partial 状态正确。
- M-4/M-5/M-6/M-10/M-11 计数与预期一致。
- 不改变上游收到的请求体/头（逐字节对比）。

### 5.3 本地端到端

1. `compose-deploy.sh --monitoring --host-metrics` 冷启动。
2. 受控脚本：正常、慢、abort、429、500、转换协议各 1 条，另 50 条普通。
3. 断言：9+2 指标有值；PG 有唯一 request_id 行；看板无 No data；M-9 四 kind 合理。
4. 断流演练：换无埋点镜像 → 5 分钟内 `Sub2apiInstrumentationStalled`；恢复后 resolved。
5. `down && up -d` 自恢复。

### 5.4 非功能

| 项 | 方法 | 门槛 |
| --- | --- | --- |
| 单面板 | 6h 查询计时 | ≤2s |
| 首屏 | 浏览器 Network | ≤8s |
| 内存 | `docker stats` 30 分钟稳定 RSS + 峰值 | 监控栈稳定 RSS ≤640MB；容器 limit 合计 ≤896m；宿主留 ≥200MB 可用 |
| PG | `EXPLAIN (ANALYZE, BUFFERS)` 30d 规模 | 实际 ≤2s，扫描页数可解释；不强制无 Seq Scan |
| 基数 | 整机 `prometheus_tsdb_head_series` | <8000，单族 <2000 |

---

## 6. AC 造数与判定（修正 v1）

| AC | 造数/方法 | 判定 |
| --- | --- | --- |
| AC-1 | 假上游逐段注入延迟（body 慢、wait 满、upstream 慢、response 慢各 1 条）；**隔离造数窗口**（前后 2 分钟无其他网关请求）；02 看板 10s 刷新 + Prom 段堆叠/占比 + PG 单请求分解 | 从该请求完成起计时：10s 刷新 + 15s 抓取 + ≤2s 查询，≤30s 用 Prom 指出主导段；PG 单请求确认 ≤90s；不以各段 P95 相加断言 |
| AC-2 | 构造 abort、协议转换、重试失败体、网关截断四种 | 字节差异可见；能结合 failure_reason 区分 abort 与其他差异，不要求差额单独定性 |
| AC-3 | 构造 ≥3 类失败 | 根因出现 ≥3 种；`unknown` 占比 <10%（有最小样本门槛） |
| AC-4 | 切换 user/api_key 变量 | 全部适用面板跟随；空/All/单选 SQL 合法；<15s |
| AC-5 | 10 账号各 20 条受控请求 | QPS/失败率/P95 TTFT 三项同屏；M-11 有数据 |
| AC-6 | host-metrics | CPU/内存/磁盘/网卡/load 全有曲线 |
| AC-7 | 本地 SMTP + 授权后外发 | 本地路由通过；外发未授权则记录“未执行” |
| AC-8 | 持续造流下两种演练：换无埋点镜像（序列缺失）；保留镜像但停写埋点（序列存在不增长） | 均 5 分钟内 fire；恢复流量后 resolved；空闲时不误报 |
| AC-9 | 固定假上游/录制回放，相同 DB 初态；对比协议字节、状态/头、SSE 分帧/Flush、attempt、usage/cost；另验证埋点异常不重复执行 handler、不吞 panic、不改变 Close/Flush/取消 | 差异为空；等待计费完成；异常注入后业务行为不变 |
| AC-10 | 整机 head | <8000，单族 <2000 |

---

## 7. 风险与缓解

| # | 风险 | 影响 | 缓解 |
| --- | --- | --- | --- |
| R-1 | 无埋点镜像覆盖 | 分段面板静默失效 | tag 锁定 + 烟测 + 断流告警 + 回滚 |
| R-2 | 基线与 0.0.4 不一致 | AC-9 失败 | B1 对齐；差异先补 |
| R-3 | 埋点改变行为 | SSE/计费差异 | 只读观测、不包装 writer、字节对比测试 |
| R-4 | 路径遗漏 | 面板少算 | 路由清单 + M-9 request 覆盖 + description |
| R-5 | 基数爆炸 | Prom OOM | 账号快照 ≤11、白名单、单测、整机预算 |
| R-6 | 内存超限 | swap 抖动 | 64/384/384m；稳定 RSS 与峰值分开验收 |
| R-7 | 样本写放大 | IO/磁盘 | 100% 但队列/批量/30d 清理；峰值压测 |
| R-8 | 线上表结构不一致 | 迁移失败/错列 | 只读映射；不兼容阻止发布 |
| R-9 | detach context 丢 Recorder | 流式无数据 | context value + WithoutCancel 单测 |
| R-10 | abort 误判 | 面板失真 | 严格判定 + 正常 EOF 反例 |
| R-11 | headers_end 双计 | 占比 >100% | 统一 `phase!="headers_end"` + 生成器断言 |
| R-12 | 原生 OpenAI 漏接 | AC-1/5 失真 | M2a.0 清单 + 集成测试 |
| R-13 | attempt 内部重发 | 计数/字节不一致 | 明确 attempt=应用级；字节含全部物理发送 |
| R-14 | 异步 usage DB 失败 | 结果被覆写 | finish 只到入队；gateway_db 仅同步 |
| R-15 | 冷启动无序列 | 误回滚 | 受控烟测，不用行数断言 |
| R-16 | 规则标签不匹配 | 告警不触发 | 无标签聚合分支 + promtool 单测 + 演练 |

---

## 8. 交付物

```
backend/
  internal/monitoring/ops_metrics.go / ops_recorder.go / ops_labels.go
  internal/monitoring/ops_coverage.go / *_test.go
  internal/server/middleware/ops.go / ops_test.go     # 入口中间件
  internal/server/http.go                        # OpsMiddleware 注册
  internal/server/middleware/recovery.go         # panic 标记
  internal/config/config.go                      # 3 项配置
  internal/handler/*gateway*.go                  # 接入点
  internal/service/gateway_forward*.go           # attempt/phase
  internal/repository/http_upstream.go           # send/headers/字节
  internal/repository/ops_sample_repo.go         # 批量写入
  internal/repository/usage_log_repo_insert.go   # 两字段
  internal/service/usage_service.go
  ent/schema/usage_log.go                        # 两字段
  migrations/xxx_ops_v2_samples.sql
  migrations/xxx_usage_logs_v2_fields.sql
deploy/
  monitoring/prometheus.yml.tmpl / render-prometheus-config.sh
  monitoring/prometheus/rules/{slo,chain,infra}.yml
  monitoring/alertmanager/alertmanager.yml.tmpl
  monitoring/sql/grafana-ro.sql
  monitoring/grafana/provisioning/datasources/postgres.yml
  monitoring/grafana/build-dashboards.py
  monitoring/grafana/dashboards/0*.json
  docker-compose.monitoring.yml / monitoring.env.example
  deploy-us1.sh / remote-deploy.sh
docs/
  MONITORING_V2_API.md / MONITORING_V2_DEV_PLAN.md
  dashboard-v2-mockup.html                  # 布局评审稿（本 HTML）
  dashboard-v2.md / MONITORING_V2_VALIDATION.md
```

> 注意：需求文档引用的 `monitoring/design-v2/` 在工作树中不存在，实际路径为 `deploy/monitoring/`；参考实现需新建。

---

## 9. 待决策

| # | 决策 | 当前结论 | 仍需确认 |
| --- | --- | --- | --- |
| D1 | P1 是否首发 | 首发（+2d）；裁剪需签字并限定 AC 范围 | 需求方 |
| D2 | Recorder 生命周期 | 入口中间件，Finalize 在 Recovery 后 | 无 |
| D3 | finish | 任务提交完成 | 需求方接受 R7 |
| D4 | 样本 100% | 是；峰值需压测 | 峰值 QPS |
| D5 | 上游 5xx | 新增 `upstream_server_error` | 需求方接受 R2 |
| D6 | 告警 18 条 | 新增 unknown 根因 | 需求方接受 R8 |
| D7 | 开关 | 两个功能开关 + 保留天数 | 无 |

---

## 附录 A：需求路径映射

| 需求 | 实际 |
| --- | --- |
| `monitoring/prometheus.yml` | `deploy/monitoring/prometheus.yml.tmpl` |
| `monitoring/prometheus/rules/*` | `deploy/monitoring/prometheus/rules/*` |
| `monitoring/alertmanager/*` | `deploy/monitoring/alertmanager/*` |
| `monitoring/grafana/*` | `deploy/monitoring/grafana/*` |
| `monitoring/design-v2/*` | 不存在，需新建 |
| `docker-compose.monitoring.yml` | `deploy/docker-compose.monitoring.yml` |
| `monitoring.env` | `deploy/monitoring.env`（不入库） |

## 附录 B：Codex 审核落地索引

| 审核条目 | 本文位置 |
| --- | --- |
| 1 时间轴 | M1.2、API §2 |
| 2 入口/body_read | M1.3、API §3.2 |
| 3 Recorder/panic | M1.2/M1.4 |
| 4 路径/原生 OpenAI | M2a.0/M2a.5、M2b、API §3.5 |
| 5 attempt 边界 | M2a.3、API §1.3 |
| 6 finish | M2a.7、API §2.4 |
| 7 failure/5xx | M1.5/M2a.6、API §4.4 |
| 8 序列/账号上限 | M1.1、API §3.6 |
| 9 unknown 语义 | API §5.4/§5.10 |
| 10 断流告警 | M4.2、API §9 |
| 11 覆盖率 | M1.6、API §5.9 |
| 12 字节 | M2a.1/M2a.4、API §5.2 |
| 13 账号 P95 | M1.1、API §5.11 |
| 14 变量 SQL | M4.6、API §8.2 |
| 15 只读权限 | M4.5、API §7.1 |
| 16 迁移/usage 字段 | B1.4、M3.1/M3.4 |
| 17 索引验收 | M3.1、API §7.4 |
| 18 采样/清理 | M3.2/M3.5、API §7.3/§7.5 |
| 19 冷启动断言 | M5.2、API §11 |
| 20 告警表达式 | M4.2、API §9 |
| 21 AC 造数 | 本文 §6 |
| 22 基线/工期/内存 | 本文 §2/§4/§5.4 |
| 二轮-1 时间轴 | API §2.1 终止规则/§2.2 wait 截断、本文 M1.2 |
| 二轮-2 ctx/panic | API §3.2 写回 `c.Request`、FinalizeInput、safeObserve、Recovery 分支 |
| 二轮-3 attempt 归属 | API §1.3/§3.3（应用层创建、HTTP 层更新）、本文 M2a.3 |
| 二轮-4 断流告警 | API §9 规则 6（V1 流量 unless phase 增长，按实例） |
| 二轮-5 AC | 本文 §6 AC-1/8/9 |
| 三轮-1 已写响应 panic | API §3.2-5（Written 前设标记，保持响应） |
| 三轮-2 多实例缺失 | API §9 规则 6（unless 集合差） |
| 三轮-3 AC-1 30s | API §8 通用刷新、本文 §6 AC-1（02 看板 10s + 隔离窗口） |
| 三轮-4 内部重发时间戳 | API §3.3（UseResponse 冻结）、§5.11（Done 时 Observe 一次） |
