# Sub2API 观测看板 V2 · 执行编排

| 项 | 内容 |
| --- | --- |
| 状态 | **完成（2026-11-15，本地验收；未 commit / 未部署生产）** |
| 契约 | `docs/MONITORING_V2_API.md` v1.0（命名/口径唯一来源） |
| 计划 | `docs/MONITORING_V2_DEV_PLAN.md` v1.0 |
| 布局 | `docs/dashboard-v2-mockup.html` |
| 角色 | 主 agent：拆解、派单、最终验收；执行1 codex `t10`；执行2 codex `t14`；审核 codex `t11` |
| 纪律 | 执行方不 commit / 不 push / 不改本文件；审核方只读、不改代码；每波次由主 agent 判定 PASS 后才进入下一波 |


## 0. 暂停交接（2026-11-15）

**原因**：Codex 周额度降至 3%，用户要求本轮结束后暂停，下次继续。未 commit、未部署生产、未外发邮件。

### 0.1 已 PASS（可信任的检查点）

| 波次 | 内容 | 审核 |
| --- | --- | --- |
| M1 | W1a 指标/标签/覆盖率/配置 + W1b1 Recorder/Attempt + W1b2 入口/Recovery/registry | t11 PASS |
| W2.0 | 路由→handler→service→usage 清单（203 锚点） | t11 PASS |
| W2a1 | 请求级 body/下游字节/finish/switch | t11 PASS |
| W2a2a | 上游 HTTP 观测原语（attempt ctx/body 包装/字节完成/响应句柄） | t11 PASS |
| W2a2b | Claude Messages 常规 Forward attempt 生命周期 | t11 PASS（3 项修复后） |
| W2a2d-account | 启动账号白名单快照 | t11 PASS |
| W5 | 监控栈 as code：18 规则/30 组规则测试/5 看板/alertmanager/只读 PG/内存与 compose | t11 PASS（5 项看板修复后） |
| W4a1 | 迁移 251（sample 表+3 索引）与 252（usage 两字段） | t11 PASS（幂等/PG18 验证） |
| W4a2a | usage_logs ent 字段与读写映射 | t11 PASS（NULL/0/值、4 插入路径、生成可复现） |

### 0.2 本轮未完成（下次第一件事）

- **W2a2c**：**审核 FAIL（1 项阻塞，尚未修复）**。passthrough 400/404 固定传空 `UpstreamCode`；Chat/Responses 只提取 `error.code`、未回退 `error.type`；`signature_400` 测试把 unknown 写成预期。下次派 t10：复用已读错误体提取 code/type，补三条链路的 code-only / type-only / 无代码用例，修正 `signature_400` 预期，不新增 body 读取、不改业务 failover 返回。
- **W4a2a**：**审核 PASS**。ent `Optional().Nillable()` 两字段与迁移 252 一致；4 条 INSERT 路径区分 NULL/0/值；查询/DTO 返回 null/数值；去重/计费未改；ent 生成可复现；真实源码 `go build ./...` 与 PG18 16 组通过。

### 0.3 剩余工作（按依赖顺序）

1. W2b：原生 OpenAI `OpenAIGatewayService.Forward` 与独立 `openai_chat_completions.go` 两条链；复用 W2a2b/W2a2c 的懒 attempt/UseResponse/Done 模式。
2. W2c：P0 集成测试与行为零变更（AC-1/2/3/9，假上游/录制回放）。
3. W4a2b：ops_request_transfer_samples 异步 writer（队列 8192、批 500/1s、request_id、保留 30d、cleanup）。
4. W4a2c：Recorder→sample 发射（Finalize 产生样本，携带 meta/phase/bytes/account/result）与 usage_logs 两字段取值接线。
5. W3（P1，可选）：Antigravity/Gemini v1beta/Bedrock；裁剪需签字并限定 AC 范围。
6. W6：构建自检、受控烟测、回滚 tag、迁移 checksum/版本校验。
7. W7：`docs/dashboard-v2.md` 阈值/判定树、AC-1~AC-10 记录、序列预算实测、部署文档。
8. W0 补充：线上只读 B1.4 需用户授权；本次未连接生产。

### 0.4 工作树与检查点

- 无 commit。当前工作树 = HEAD `95368134a` + 本轮全部未提交改动。
- 完整可恢复检查点：`/tmp/sub2api-checkpoints/pause-20261115-final/`
  - `tracked.patch`：所有已跟踪文件的二进制安全 diff（含删除的旧 V1 看板）
  - `untracked.tgz`：所有新增/未跟踪文件
  - `STATUS.txt`：HEAD、分支、`git status` 快照
- 恢复方式：`git apply tracked.patch` + 解压 `untracked.tgz`；或直接使用当前工作树（若未丢）。
- 早期中间检查点 `/tmp/sub2api-checkpoints/m1-w2a2a-2308.tgz` 只含 M1+W2a2a，不要用于最终恢复。

### 0.5 本地基线事实（B1 部分）

- HEAD `95368134a3926882ba88886a35aee7cc4042666e`；分支 `jk-dev`。
- 本地 dev 镜像 `deploy-sub2api` digest `sha256:02f64e22ffc6fbfab7084ca12a07fa88fb3918a2b6b39bf72a4c35f9be23887a`；V1 `/metrics` 有 `sub2api_http_requests_total`。
- 本地 `usage_logs` 无 `body_bytes`/`gateway_latency_ms`；`ops_request_transfer_samples` 不存在（与 API §7.2/§7.3 一致）。
- 线上只读核对、镜像构建/上传、AC-7 外发均未执行。

### 0.6 契约裁定索引

本轮新增裁定见 §3 第 1–9 条（代理 5xx、Stage 并集、入口白名单、注册错误、14 reason、NodeLoadHigh、Grafana 刷新语义、promtool 测试文件、compose overlay 验证口径）。下次继续时先读 §3，不要重新发明口径。

### 0.7 恢复动作

1. 确认 Codex 额度恢复；`hc ls` 确认 t10/t11/t14 状态。
2. 先让 t11 完成 §0.2 两项审核；FAIL 则派回对应执行方。
3. 两项 PASS 后，更新 §1 状态表，再派 W2b（t10）与 W4a2b（t14）；保持文件范围互斥。
4. 审核仍由 t11 串行执行；不 commit、不部署生产。

## 1. 波次与状态

| 波次 | 内容 | 依赖 | 状态 |
| --- | --- | --- | --- |
| W0 | B1 基线对齐（commit/digest/迁移/冷启动） | — | 待开始 |
| W1a | M1 纯函数与指标定义（1.1/1.5/1.6/1.7） | — | **PASS（2026-11-14）** |
| W1b1 | M1.2 Recorder/Attempt 状态机 | W1a | **PASS（2026-11-14）** |
| W1b2 | M1.3/M1.4/M1.8 入口接入与注册 | W1b1 | **PASS（2026-11-14）** |
| **M1** | **埋点基础设施完成** | W1a–W1b2 | **PASS（2026-11-14）** |
| W2.0 | M2a.0 路由→handler→service→usage 清单 | M1 | **PASS（2026-11-14）** |
| W2a1 | 请求级观测：body/下游字节/finish/switch | W2.0 | **PASS（2026-11-14）** |
| W2a2a | 上游 HTTP 观测原语（attempt ctx/body 包装/字节完成） | W2a1 | **PASS（2026-11-14）** |
| W2a2b | Claude Messages/Forward attempt 生命周期 | W2a2a | **PASS（2026-11-15）** |
| W2a2c | 兼容转换/passthrough 接入 | W2a2b | **PASS（2026-11-15）** |
| W2a2d-account | 启动账号白名单快照接入 | W1b2 | **PASS（2026-11-14）** |
| W2b1 | 原生 OpenAI Responses/plugin | W2a2c | **PASS（2026-11-15）** |
| W2b2 | 独立 Chat + OpenAI Messages 桥接 | W2b1 | **PASS（2026-11-15）** |
| W2c | P0 集成测试与行为零变更验证 | W2a2c/W2b | 待开始 |
| W3 | M2b P1 三条链路（2b.1–2b.4，可裁剪需签字） | W2 | 待开始 |
| W4a1 | M3 迁移（sample 表 + usage 字段） | W1b | **PASS（2026-11-15）** |
| W4a2a | usage_logs ent 字段与读写映射 | W4a1 | **PASS（2026-11-15）** |
| W4a2b | sample 异步 writer + 30d cleanup | W4a2a | **PASS（2026-11-15）** |
| W4a2c1 | Recorder → sample 发射 | W4a2b | **PASS（2026-11-15）** |
| W4a2c2 | usage_logs 两字段取值 | W4a2c1 | **PASS（2026-11-15）** |
| W4 | M3 PG 样本与 usage 字段（3.1–3.6） | W4a2c2 | 待开始 |
| W5 | M4 监控栈 as code（4.1–4.7） | W1a | **PASS（2026-11-14）** |
| W6a | 构建自检 + 迁移 checksum + 回滚骨架 | W5/W4a1 | **PASS（2026-11-15）** |
| W6b | 受控烟测（request_id 三处命中） | W6a/W2/W4 | **PASS（2026-11-15）** |
| W2a2d-meta | AttachMeta / request_id 接线 | W2a2d | **PASS（2026-11-15）** |
| W6 | M5 部署契约与回滚（5.1–5.4） | W6a/W6b/W2/W4 | 待开始 |
| W7a | 看板口径/判定树/AC 骨架 | W5/W2a | **PASS（2026-11-15）** |
| W7b | AC-1/2/3/9 证据记录 | W2c1/W7a | **AC-3 PASS（159/159，unknown 6.29%）；真实 30m 未执行** |
| W7c | AC-4 变量 SQL 验证 | W5 | **PASS（SQL/变量/权限子项）；浏览器未执行** |
| W8-AC5 | 10 账号 ×20 请求 | W6b/W6c | **PASS（2026-11-15）** |
| W7 | M6 文档与 AC（6.1–6.4） | W7a/W7b/全部 | 待开始 |

> 本次交付边界按计划：本地完成 AC-1~AC-10（AC-7 外发需授权，未授权记“未执行”），不执行线上部署。

## 2. W1a 任务卡（执行 codex）

### 目标

实现 M1 中不依赖 gin/请求生命周期的部分，供 W1b 直接组合。

### 文件范围（只允许新增/修改这些）

- `backend/internal/monitoring/ops_metrics.go`
- `backend/internal/monitoring/ops_labels.go`
- `backend/internal/monitoring/ops_coverage.go`
- `backend/internal/monitoring/ops_metrics_test.go`
- `backend/internal/monitoring/ops_labels_test.go`
- `backend/internal/monitoring/ops_coverage_test.go`
- `backend/internal/config/config.go`（只加 V2 配置字段、默认值、校验）
- `backend/internal/config/monitoring_test.go`（补 V2 用例）

### 契约要点

1. 指标 M-1~M-11 名称、类型、label、桶**逐字**按 API §5、附录 A；桶必须显式声明（M-1/M-11 13 桶，M-3 10 桶）。
2. 所有指标注册到**调用方传入的** `prometheus.Registerer`（生产上会传现有私有 registry），不得使用全局默认 registry；重复注册必须返回错误而非 panic。
3. 账号白名单：启动快照，最多 10 个真实名 + `unknown`；归一化小写/trim/`[a-z0-9_-]`/≤32，冲突保留首名。API §4.1。
4. `endpoint_class`、`model_class`、`result`、13 类 `failure_reason` 及优先级按 API §4.2–§4.4；`success` 的 reason 必须为空串；分类函数必须是纯函数。
5. `FailureEvidence` 字段按 API §3.3；代理 5xx 与源站 5xx 的区分只依据证据，无法判定时 `unknown`。
6. M-9 覆盖率：一把 `sync.Mutex` 保护 `{entry, request, account, bytes, user}`，按分钟整体 Swap 后 Set gauge；无样本 Set `-1`；时钟可注入以便测试。API §5.9。
7. 配置：`monitoring.ops_metrics_enabled` 默认 false，要求 `metrics_enabled=true`；`monitoring.ops_samples_enabled` 默认 false，要求 ops metrics 开启；`ops.sample_retention_days` 默认 30，拒绝负数（0 为显式 TRUNCATE 语义）。环境变量分别为 `MONITORING_OPS_METRICS_ENABLED`、`MONITORING_OPS_SAMPLES_ENABLED`、`OPS_SAMPLE_RETENTION_DAYS`。非法组合 fail-fast，错误信息含完整配置键。
8. 依赖方向：`monitoring` 不得 import `service`/`handler`/`gin`。

### 验收命令

```sh
cd backend
gofmt -l internal/monitoring internal/config
go vet ./internal/monitoring/... ./internal/config/...
go test -race ./internal/monitoring/...
go test -tags=unit ./internal/config/...
```

本机无 Go 时用 Docker（已预热模块缓存卷 `sub2api-go-mod`）：

```sh
docker run --rm -v "$PWD/backend:/src" -w /src -v sub2api-go-mod:/go/pkg/mod \
  -e GOTOOLCHAIN=auto golang:1.27.2-alpine \
  sh -c 'go test -race ./internal/monitoring/... && go test -tags=unit ./internal/config/...'
```

### 完成定义

- 上述命令全部通过，输出贴回对话。
- 报告：改动文件、新增公开 API 清单、11 个指标的名称/label/桶自检结果、测试覆盖点。
- 不做 W1b 内容（Recorder/Attempt/中间件/Recovery/http.go/wire）。

## 3. 契约裁定（主 agent，执行/审核共同遵循）

API 文本中以下两处存在自相矛盾，按“上位规则优先”裁定：

1. **代理 5xx 归属**（2026-11-14 修订，取代初审版）：§4.4 第 4 条若先命中，代理返回的 502/503/504 会被判成 `upstream_server_error`，使第 7 条的代理分支不可达。裁定按“错误来源”而非仅 `ProxyUsed` 区分：
   - `upstream_server_error`：源站来源的 500/502/503/504；`ProxyUsed=true` 且 `Stage="headers"|"body"` 视为源站经代理返回，归此类。
   - `proxy_unreachable`：代理来源的 5xx/连接失败；`ProxyUsed=true` 且 `Stage="connect"`，状态 502/503/504，或 TransportKind 为 refused/dns/tls。
   - `unknown`：`ProxyUsed=true` 但 Stage 为空或其他无法判定来源的组合。
   依据：§4.4 第 7 条原文是“**代理返回** 502/503/504”，§4.4 末尾要求“由 ProxyUsed + 错误来源判定；无法确定时落 unknown”；上一版“只要 ProxyUsed=true 就归 proxy_unreachable”会误伤经代理到达的源站 5xx，作废。
2. **Stage 枚举**：§3.3 的注释枚举缺 `body_read`/`response`，但 §4.4 第 8/9 条按 `Stage=body_read`/`Stage=response` 判定。裁定：`FailureEvidence.Stage` 接受并集 `connect|headers|body|body_read|response|downstream|db|ratelimit|panic`；`body` 仅用于上游读超时。

另明确两点实现约束：

3. **网关入口白名单口径**：API §3.2 的列表与 §3.5/计划 §3.3 的“排除 count_tokens、models”冲突。裁定以 §3.5/计划为准：入口中间件只覆盖实际转发业务端点（`/v1/messages`、`/v1/chat/completions`、`/chat/completions`、`/v1/responses` 及其可转发子路径、`/v1/embeddings`、`/v1/images/*`、`/v1beta/*`、`/antigravity/*`）；`/v1/messages/count_tokens`、`/v1/models`、余额/探测/管理接口不建 Recorder、不计 M-10/M-9 分母。
4. 指标注册不得用 `promauto`/`MustRegister` 造成重复注册 panic；用显式 `Register` 聚合错误返回。
5. `M-4`/`M-10` 的 `failure_reason` 合法值共 14 个（13 类 + 成功空串）；非 success 不得为空串。
6. **NodeLoadHigh 表达式（W5 裁定，2026-11-14）**：API §9 第 13 条 `count without(cpu)(node_cpu_seconds_total{mode="idle"})` 会保留 `mode="idle"`，与无 `mode` 的 `node_load5` 向量匹配失败。授权在 deploy 规则中改写为 `count without(cpu, mode)(node_cpu_seconds_total{mode="idle"})` 或 `count by (instance, job)(...)`；必须补 promtool 单测，证明原式不触发、修正式触发并可恢复。规则注释写明该偏差，不回改 API 文档。
7. **Grafana 刷新语义（W5 裁定）**：Grafana 只有看板级 refresh；PG 面板的 `interval: "1m"` 是查询步长/min interval，不是独立刷新。02 看板 refresh=10s、其余 30s；文档不得把 PG 面板写成“独立 1m 刷新”，差异列入 W5 已知偏差。
8. **Promtool 测试文件（W5 裁定）**：运行时 `rule_files` 不得匹配 `*_test.yml`；测试文件统一放 `deploy/monitoring/prometheus/rules/tests/`（或等价独立目录），compose 只挂载 3 个生产规则文件；`promtool check rules` 显式列生产文件，`promtool test rules` 指向测试目录。
9. **Compose 验证口径（W5 裁定，2026-11-14）**：`deploy/docker-compose.monitoring.yml` 是 overlay，本身不含 `sub2api` 的 image/build，单独 `docker compose -f` 失败属预期；不得为满足单文件命令给 overlay 添加业务 image。验收以 `docker compose -f deploy/docker-compose.dev.yml -f deploy/docker-compose.monitoring.yml --profile host-metrics --profile backup config` 的合并结果为准，并在报告中同时贴出单文件命令的失败输出与原因。



## 4. W1b1 任务卡（执行 codex）

### 目标

实现请求级 `OpsRecorder` 与应用级 `OpsAttempt` 状态机，严格按 API §2（时间轴/phase 闭合）、§3.3（契约表）、§5.1/§5.4–§5.11 结算指标。本卡只做内存状态机与单测，不接 gin、不改 `http.go`/`recovery.go`、不做 DB 快照。

### 文件范围（只允许）

- `backend/internal/monitoring/ops_recorder.go`
- `backend/internal/monitoring/ops_recorder_test.go`

### 必须实现的公开 API

```go
type OpsRequestMeta struct { /* RequestID, UserID, APIKeyID, AccountID, GroupID, Platform, Model, Endpoint/EndpointClass 等 */ }
type FinalizeInput struct { Status int; Panic bool }

func BeginGatewayRequest(ctx context.Context, path string, m *OpsMetrics) (context.Context, *OpsRecorder)
func RecorderFromContext(ctx context.Context) *OpsRecorder
func (r *OpsRecorder) AttachMeta(m OpsRequestMeta)
func (r *OpsRecorder) MarkBodyRead(n, declared int64, status string)
func (r *OpsRecorder) AddWait(d time.Duration)
func (r *OpsRecorder) StartAttempt(account string) *OpsAttempt
func (r *OpsRecorder) AddBytes(direction string, n int64, status string)
func (r *OpsRecorder) MarkClientAbort(stage string)
func (r *OpsRecorder) MarkSwitch()
func (r *OpsRecorder) MarkFinish()              // usage 任务提交完成；未调用则 t_finish=t_done
func (r *OpsRecorder) Finalize(in FinalizeInput)
```

`OpsAttempt` 方法按 API §3.3：`Sent/NoteHeaders/NoteFirstByte/UseResponse/BodyDone/Done`。允许为测试增加非导出的时钟注入（如 `beginGatewayRequestAt`），但生产入口保持 `BeginGatewayRequest`。

### 结算规则

1. 六互斥段严格按 API §2.1 终止规则与 §2.2 公式；`Finalize` 一次性 Observe M-1 的 6 段（未到达段 0），`headers_end` 仅在最终 attempt 有首字节时 Observe 且不参与闭合。
2. 闭合断言：同一请求 `sum(6 段) == t_finish - t_entry ±1ms`；`wait` 截断到 `[t_body, t_send1]`，保证 `gateway >= 0`。
3. 每个 attempt 在 `Done` 时：M-4（account/result/reason）+1、M-5（model_class/result）+1；M-11 仅在已 `UseResponse()` 锁定且锁定响应首字节已到时 Observe 一次。
4. `Finalize` 时：M-10（result/reason）+1；M-6 outcome = `first_success` / `retried_success` / `all_failed`（0 attempt 的拒绝也算 all_failed）；未 Done 的 attempt 按 `upstream_error/unknown` 结算但不得重复计数已 Done 的 attempt。
5. `panic=true` 强制 `result=gateway_error, reason=gateway_panic`，但已结算 attempt 的 M-4 不回溯改写。
6. `MarkSwitch` 加 M-7；`MarkClientAbort(stage)` 加 M-8；`MarkBodyRead` 加 M-3（读失败按已读部分）与 M-2 `in_wire`；`AddBytes` 按 direction 加 M-2。
7. 单次/累计语义按 API §3.3：`MarkBodyRead`/`Finalize`/`Done` 只生效一次，重复忽略；`AddWait`/`AddBytes`/`MarkSwitch` 累加。
8. 内部重发：`Sent` 只记首次；`NoteHeaders/NoteFirstByte` 保存最近候选；`UseResponse` 后其他响应的首字节事件忽略；TTFT 用锁定响应的首字节 - 首次发送。
9. 并发：`sync.Mutex`；async finish 与 Finalize 跨 goroutine 安全；所有方法 nil receiver 安全；`-race` 通过。
10. `Finalize` 不得 recover 业务 panic，也不得重新执行 handler；埋点自身不得 panic。

### 验收命令

```sh
cd backend
gofmt -l internal/monitoring
go vet ./internal/monitoring/...
go test -race ./internal/monitoring/...
```

Docker 命令同 W1a（Alpine 下 `-race` 需先 `apk add gcc musl-dev` 并 `CGO_ENABLED=1`）。

### 完成定义

- 单测覆盖：正常 200、200 SSE、429、500、502、超时（connect/read）、连接拒绝、客户端读 body 中断、200 后读超时、客户端响应中断、签名纠错重发、Grok fallback、0 attempt 拒绝、panic、无 usage、跨 goroutine finish、重复 Finalize/Done、未 Done attempt。
- 每个用例断言：M-1 六段闭合、headers_end 不参与、M-2 方向字节、M-4/M-5/M-6/M-10/M-11 计数、reason 优先级。
- 报告指标结算矩阵与测试输出；不改 W1b2 文件。

### 4.1 W1b1 审核修复单（主 agent 裁定）

审核 t11 判定 FAIL，四项均成立，按以下口径修复并补测。本次修复允许同时修改 `ops_coverage.go` / `ops_coverage_test.go`（M-9 入口/完成拆分所需），其余范围不变：

1. **M-9 入口分母**：`BeginGatewayRequest` 调用 `OpsCoverage.Entry()` 并保存返回的分钟 token；`Finalize` 调用 `OpsCoverage.Complete(token, sample)`。分子归属**入口分钟**，不归属完成分钟。`OpsCoverage` 保留 current/previous 两个分钟桶、一把锁；分钟滚动时发布 previous；previous 尚未被更晚分钟取代前允许迟到完成更新该分钟 ratio 并重设 gauge，超过一个分钟窗口的完成记 debug 并丢弃。ratio 始终 clamp 到 0~1；entry=0 且分子>0 时记告警并按 1 处理；无样本仍为 -1。补测：Begin 后不 Finalize、跨分钟完成、迟到完成、连续空闲分钟、并发入口/完成/Flush。
2. **响应身份**：`Sent()` 返回 `*OpsResponse`；新增 `NoteHeadersFor(resp, status)`、`NoteFirstByteFor(resp)`、`BodyDoneFor(resp, err)`、`UseResponseFor(resp)`；无参方法保留为“当前响应”便捷入口。`UseResponseFor` 锁定后，即使又发生新的 `Sent()`，锁定句柄后续到达的首字节/结束事件仍必须被接受；其他句柄事件忽略。TTFT 用锁定句柄首字节 - 该 attempt 首次发送。补测：锁定 A 后再 Sent B，A 的首字节/结束仍生效，B 的事件被忽略。
3. **M-11 只在 Done**：`settle` 增加 `observeTTFT` 参数；显式 `Done` 传 true，Finalize 对未 Done attempt 的兜底结算传 false。补测未 Done attempt 不增加 M-11。
4. **0-attempt 成功**：明确 `first_success` 定义为“成功且 ≤1 次逻辑 attempt”，覆盖 0-attempt 本地成功（拦截/缓存等）；M-6 label 集合不变。补测 0-attempt 200 为 `first_success`，0-attempt 拒绝仍为 `all_failed`。

> 第 4 项是对 API §5.6 括号注释的明确化：`first_success (1 attempt 且成功)` 扩展为 `≤1 attempt 且成功`；不新增 outcome，避免突破 M-6 序列预算。

## 5. W1b2 任务卡（待 W1b1 验收后派发）

### 目标

把 W1b1 状态机接到 Gin 入口，注册到现有私有 registry，并提供分钟 Flush 生命周期。账号白名单 DB 快照**不在本卡**，由 W2 在账号选择链路接入；W1b2 传 nil 快照，所有账号暂记 `unknown`。

### 文件范围（允许）

- 新增 `backend/internal/server/middleware/ops.go`、`ops_test.go`
- 修改 `backend/internal/server/middleware/recovery.go`（及测试）
- 修改 `backend/internal/server/http.go`（及必要测试）
- 修改 `backend/internal/monitoring/metrics.go`、`wire.go`，新增 coverage 生命周期文件（如 `ops_runtime.go`）及测试
- 修改 `backend/cmd/server/wire.go`、`wire_gen.go`（如无法运行 wire，可手工同步）
- 新增 `monitoring/ops_keys.go`（`OpsPanicKey`）

### 契约要点

1. **注册顺序**：`r.Use(metrics) → r.Use(Ops) → r.Use(Recovery)`。`OpsMiddleware` 用 `c.FullPath()` 白名单判定；非网关路径直接 `c.Next()`，不建 Recorder。
2. **ctx 写回**：`ctx, rec := BeginGatewayRequest(...)`; `c.Request = c.Request.WithContext(ctx)`；`defer rec.Finalize(...)` 必须在 Recovery 之后执行，读到 `c.Writer.Status()` 与 panic 标记。
3. **异常隔离**：`safeObserve` 只包埋点调用（Begin/Finalize/coverage Flush），**不得**包 `c.Next()`，不得 recover 业务 panic，不得重跑 handler。
4. **panic 标记**：`recovery.go` 除 broken pipe 外，在 `c.Writer.Written()` 判断**之前** `c.Set(monitoring.OpsPanicKey, true)`；保持原响应状态码/已写内容不变。broken pipe 分支不设标记。
5. **禁用零变更**：`cfg.Monitoring.OpsMetricsEnabled=false` 时 `OpsMiddleware` 为 no-op，不创建 Recorder，不注册 V2 指标；启动/响应行为与现状一致。
6. **注册**：V2 指标注册到 `/metrics` 同一个私有 registry；注册失败必须通过 wire 返回错误使启动失败，不得静默降级或使用全局 registry。
7. **coverage 生命周期**：提供可 Start/Stop 的分钟 Flush（ticker 或等价），随应用退出清理；测试不得泄漏 goroutine。无流量空闲分钟发布 -1。
8. `OpsPanicKey` 值稳定；Recovery 与 OpsMiddleware 使用同一常量。

### 验收

- `go test -race ./internal/server/... ./internal/monitoring/...`
- 集成测试用 `httptest`/gin：非网关旁路、认证拒绝、限流拒绝、body 读失败、panic 未写响应、panic 已写响应、broken pipe、重复 Finalize、禁用模式零指标。
- 断言：panic 落 `gateway_error/gateway_panic` 而非 success；已写响应内容不变；M-10/M-9 分母正确；`/metrics` 有且仅有一套 V2 HELP。
- 报告改动文件、启动顺序、coverage 生命周期、测试输出。

## 6. W2.0 任务卡（执行 codex）

### 目标

产出 M2a.0 路径清单，作为 W2a/W2b 的接入依据。**只写文档，不改代码。**

### 文件范围

- 新增 `docs/dashboard-v2.md`（仅此文件）

### 内容要求

1. **路由矩阵**：逐条列出 API §3.5 的 P0/P1/P2/排除项，包含：路由与方法、平台判定、handler 函数、service/forward 函数、attempt 应在何处创建、响应在何处被消费、usage 在何处提交、当前是否已有可用的 ctx 传递。
2. **P0 必须逐条覆盖**：`/v1/messages`（`GatewayService.Forward`）、Anthropic API-key passthrough、`/v1/chat/completions` 兼容转换、`/v1/responses` 兼容转换、原生 OpenAI `OpenAIGatewayService.Forward`（`openai_gateway_handler.go`）及独立链（`openai_chat_completions.go`）。
3. **事件接入点表**：body 读取（读循环/失败分支）、slot wait、首个上游发送、响应头、首字节、响应体结束、下游写入、账号切换、usage 提交；每项给 `file:line` 和现有函数名，行号以当前 HEAD 为准。
4. **共享与分叉**：明确 `ForwardAs*` 覆盖哪些协议、原生 OpenAI 独立链与公共出口的差异、哪些路径没有统一的 attempt 创建点、需要重构的最小切口。
5. **排除项**：count_tokens、models、余额/探测/管理接口、P2 只做请求级结果的端点，逐项说明不接入原因。
6. **风险清单**：可能漏接、会被内部重发影响、会改变 SSE/计费行为的位置；每条给验证方法。

### 验收

- `rg` 核对所有 `file:line` 存在且函数名一致。
- 与 API §3.5 对照，P0 无遗漏、无“TBD”。
- 审核方按本文件打勾，不以“已阅读”代替逐条核对。


## 7. W2a1 任务卡（执行 codex，待 W2.0 审核通过后派发）

### 目标

先完成不依赖逻辑 attempt 的请求级观测：真实请求体字节、下游字节、finish、账号切换。**不创建 attempt、不改 service/response 解析。**

### 文件范围（允许）

- `backend/internal/server/middleware/ops.go` / `ops_test.go`
- 新增 `backend/internal/monitoring/ops_body.go` / `ops_body_test.go`（计数 body 包装器）
- `backend/internal/monitoring/ops_recorder.go`（仅新增 `ClientAborted()` 查询；不改结算逻辑）
- `backend/internal/handler/gateway_handler.go`（仅 `submitUsageRecordTask` / `submitMandatoryUsageRecordTask`）
- `backend/internal/handler/openai_gateway_handler.go`（仅两个同名 submit 函数）
- `backend/internal/handler/failover_loop.go`（仅 `SwitchCount++` 处）
- 对应 `_test.go`

### 契约要点

1. **in_wire 计数**：OpsMiddleware 在 `BeginGatewayRequest` 后、`c.Next()` 前用计数 ReadCloser 包 `c.Request.Body`；每次 Read 原样返回 `(n, err)`，累计实际 n，不预读、不缓存、不提前 Close。EOF -> `MarkBodyRead(total, declared, "complete")`；读错误 -> `"partial"`；handler 未读完但请求结束 -> Finalize 前 `Finish()` 以 partial 补记；从未读 -> 不调用 `MarkBodyRead`（body_read=0）。
2. **预读兼容**：group_model_allowlist / composite 的 `ReadRequestBodyWithPrealloc` 必须计入；其后的 `ResetRequestBody` 内存重读不得重复计数。测试覆盖预读后 handler 再读。
3. **下游字节**：Finalize 前 `rec.AddBytes("resp_downstream", int64(c.Writer.Size()), status)`；`c.Writer.Size()` 是 body 已写字节。status：panic 或客户端中断为 partial，否则 complete。不得包装/替换 Writer。
4. **finish**：在 Gateway/OpenAI 的 submit 与 mandatory submit 函数中，`task == nil` 判断之后 `defer monitoring.RecorderFromContext(parent).MarkFinish()`；异步 enqueue、Dropped、同步 fallback 都在返回时记录；异步 DB 失败不额外调用。
5. **切换**：在 `FailoverState.HandleFailoverError` 的 `s.SwitchCount++` 同点调用 `MarkSwitch()`；同账号重试路径不得调用。
6. **中断查询**：给 Recorder 增加只读 `ClientAborted() bool` 供下游状态判定；不改既有一致性/并发语义。
7. 埋点异常隔离：body 包装器的 Read/Close 不得 panic 传播；埋点回调用 safeObserve/等价隔离。

### 验收

- `go test -race ./internal/server/... ./internal/monitoring/... ./internal/handler/...`
- 新测试：正常 body、空 body、读错误、客户端中断、未读即拒绝、预读+重读、gzip/chunked 原始字节、下游 SSE 字节、panic 已写/未写、finish 四种提交模式、同账号重试不计 switch。
- 断言：M-2 五方向实际 n；M-3 只 Observe 一次且为实际读取；body_read/gateway 闭合；M-7 只在跨账号；M-10 每入口一次。


## 8. W2a2a 任务卡（执行 codex）

### 目标

只做 monitoring 层的上游 HTTP 观测原语，不接 service/repository。为 W2a2b 提供可组合、可单测的 attempt ctx、请求体/响应体包装、响应句柄与字节完成升级。

### 文件范围（允许）

- 新增 `backend/internal/monitoring/ops_http.go` / `ops_http_test.go`
- `backend/internal/monitoring/ops_recorder.go`（仅新增 attempt ctx 辅助与 `MarkBytesComplete`，不改既有结算）

### 必须提供

```go
func WithAttempt(ctx context.Context, a *OpsAttempt) context.Context
func AttemptFromContext(ctx context.Context) *OpsAttempt

// 请求体：累计 req_upstream 实际 Read 的 n；EOF 标记 complete，错误/提前 Close 为 partial。
func ObserveRequestBody(rec *OpsRecorder, body io.ReadCloser) io.ReadCloser

// 响应体：首个 n>0 的 Read 调用 NoteFirstByteFor；累计 resp_upstream（解压后）；
// EOF 调用 BodyDoneFor(nil) 并标记 complete；错误/提前 Close 为 partial。
func ObserveResponseBody(rec *OpsRecorder, attempt *OpsAttempt, handle *OpsResponse, body io.ReadCloser) io.ReadCloser

// 从已被 ObserveResponseBody 包装的 resp.Body 取回句柄；未包装返回 nil。
func ResponseHandle(resp *http.Response) *OpsResponse

// 把某方向从未完成升级为 complete；不得降级；无既有观测时建立 n=0 complete。
func (r *OpsRecorder) MarkBytesComplete(direction string)
```

### 契约要点

1. 包装器原样返回 `(n, err)`，保留 Close 语义；不预读、不缓存、不提前 Close；埋点回调 panic 不得传播。
2. `resp_upstream` 必须是**解压后**字节；`req_upstream` 覆盖 `req.Body` 与其 `GetBody()` 返回的副本，重试时累计所有物理发送。
3. `ObserveResponseBody` 包装层必须可被 `ResponseHandle` 识别（例如实现接口），且重复包装不得双计。
4. `MarkBytesComplete` 允许 partial -> complete，不能把 complete 降回 partial；Finalize 后调用忽略。
5. nil rec/attempt/handle/body 全部安全；并发 Read 与 Close 不产生数据竞争。
6. 不修改 `addBytes` 既有五方向累计语义；`MarkBytesComplete` 只改状态位。

### 验收

- `go test -race ./internal/monitoring/...`
- 测试：分块响应首字节、EOF 完成、读错误、提前 Close、空 body、`req.Body` + `GetBody` 两次物理读取累计、ResponseHandle 取回、重复包装、partial->complete 升级、complete 不降级、nil 安全。
- 断言：M-2 实际 n、M-11 仅在锁定响应首字节后 Done、M-9 bytes 状态；不改变调用方看到的数据与错误。


## 9. W2a2b 任务卡（执行 codex）

### 目标

把 W2a2a 原语接到 Claude Messages 常规 Forward 路径（P0-A）：真实 attempt 生命周期、上游请求/响应字节、最终响应锁定、Done 结算。**不含 passthrough/ForwardAs*/原生 OpenAI/账号快照/元数据/等待，后续卡处理。**

### 文件范围（允许）

- `backend/internal/repository/http_upstream.go`（仅 `doUpstreamRequest` 与 Grok fallback transport 的观测接入）
- `backend/internal/service/gateway_forward.go`（仅 P0-A 常规 Forward 的 attempt 创建/结算/UseResponse）
- 新增 `backend/internal/repository/http_upstream_ops_test.go`
- 新增 `backend/internal/service/gateway_forward_ops_test.go`
- 必要的 `backend/internal/service/*_test.go` 辅助

### 契约要点

1. **HTTP 层只更新句柄**：`doUpstreamRequest` 在真实发送前 `Sent()` 取 `OpsResponse`，并 `ObserveHTTPRequest(rec, req)` 包装 Body/GetBody；成功后 `NoteHeadersFor(handle, status)`；在 `decompressResponseBody` 和 `cancelOnCloseBody` 之后安装 `ObserveResponseBody`（最外层），使 `ResponseHandle(resp)` 可取出锁定句柄。错误时 `BodyDoneFor(handle, err)`。
2. **Grok fallback**：transport 的第二次物理发送必须重新 `Sent()` 并包装其请求/响应；`doUpstreamRequest` 若发现 `ResponseHandle(resp)` 已存在则不得重复包装。第一次被丢弃响应的字节仍计入 `resp_upstream`。
3. **attempt 创建**：P0-A 在重试循环前、所有本地短路（web-search、beta policy 拒绝等）之后创建一次；`ctx = WithAttempt(ctx, attempt)` 必须在 `detachStreamUpstreamContext` 之前，确保派生 ctx 保留。签名纠错/同一 Forward 内重发不新增 attempt。
4. **UseResponse**：最终成功响应在消费前 `UseResponseFor(ResponseHandle(resp))`；错误体读取/被丢弃响应不锁定。
5. **Done**：在每个确定的结束点显式 `Done` 一次：
   - `DoWithTLS` 传输错误：按错误来源填 `FailureEvidence{Stage:"connect", TransportKind:...}`，timeout -> `timeout`，否则 `upstream_error`。
   - HTTP 状态错误（最终放弃/交给 failover 前）：`{StatusCode, Stage:"headers", ProxyUsed}`。
   - 流/非流式消费错误：按读错误/客户端取消/超时填 evidence；客户端取消才 `client_abort`，正常 EOF 不算。
   - 消费成功：`Done("success","",{})`。
   - 已有 `Done` 后重复调用必须无效（Recorder 幂等）。
6. **行为零变更**：包装只观察实际 n，不改变请求体/头、响应体/状态、SSE 分帧、Flush、Close、重试次数与时机、usage/计费。
7. **M-4/M-5/M-11**：M-4/M-5 在 Done；M-11 仅在锁定响应首字节存在时 Observe 一次；Finalize 兜底不补 M-11。

### 审核重点（主 agent 预检）

- `buildUpstreamRequest` 等**本地构建失败**不应产生 M-4/M-5 attempt 计数；若当前实现先在循环前 `StartAttempt`，必须在审核中判阻塞并改为“构建成功、真正准备发送前懒创建”，或证明该分支不存在。
- `UseResponseFor` 只能锁定最终被消费的响应；被 failover/重试丢弃的 400/5xx 响应不得锁定。
- 客户端取消与读超时/服务端 drain 必须区分：正常 EOF、上游超时不得记 `client_abort`。
- `opsDoneError` 对传输错误/HTTP 错误/SSE 错误的 evidence 必须能区分代理与源站、headers/body/connect，不得全部落 headers。

### 验收

- `go test -race ./internal/repository/... ./internal/service/... ./internal/monitoring/...`
- 假上游/httptest 覆盖：200 JSON、200 SSE、429、500、502、connect timeout、read timeout、连接拒绝、客户端提前断开、签名纠错重发、Grok fallback。
- 每例断言：M-1 六段闭合、M-2 五方向实际 n、M-3、M-4/M-5/M-6/M-10/M-11；请求逐字节与头不变；响应字节/状态/SSE 帧不变；usage/cost 不变。
- 报告逐场景指标矩阵与测试输出。


## 10. W2a2d-account 任务卡（执行2 t14）

### 目标

把启动时 `accounts.name` 快照接到 OpsMiddleware/Recorder，使 M-4/M-11 的 account label 与 M-9 account 覆盖率有真实数据。**只做账号快照，不做 AttachMeta/wait/其他 handler 改动。**

### 文件范围（允许）

- `backend/internal/monitoring/ops_runtime.go`（`Accounts *AccountLabels` 字段，nil 安全）
- `backend/internal/monitoring/wire.go`（`ProvideOpsRuntime(instruments, accounts)`）
- `backend/cmd/server/wire.go`、`wire_gen.go`（新增 `provideAccountLabels`，可运行 wire 生成）
- `backend/internal/server/middleware/ops.go`（传入 `runtime.Accounts`）
- 对应 `_test.go`

### 契约

1. 启动时只读一次 `accounts.name`；使用 5s 超时；失败时 log warning 并返回空快照（不得阻止启动）。
2. `NewAccountLabels` 已有规则：最多 10 个真实名 + `unknown`，归一化/冲突规则不变；不得改为运行期刷新。
3. V2 disabled 时 `runtime` 为 nil、不查询 DB、不改变启动行为；错误路径不泄露账号名到日志之外。
4. 不得修改 `gateway_forward.go`、`http_upstream.go`、handler、repository、ent schema。

### 验收

- `go test -race ./internal/monitoring/... ./internal/server/...`；`go test -run '^$' ./cmd/server`；`wire check ./cmd/server`。
- 测试：10+ 账号截断、冲突/未知、空/DB 错误、V2 disabled、middleware 使用 runtime.Accounts。
- 报告：查询 SQL、快照数量、测试输出、wire 生成结果。

## 11. 审核协议（审核 codex）

只读审核，不修改代码。输入：本文任务卡 + API 对应条款 + 执行方 diff。输出格式：

```
VERDICT: PASS | FAIL
FINDINGS:
- [阻塞|建议] file:line — 问题 — 对应契约条款 — 最小修复建议
EVIDENCE: 实际运行的命令与关键输出（若无法运行需说明）
```

判定规则：任一契约偏差（名称/label/桶/优先级/一次性结算/依赖方向）为阻塞；测试不能证明验收点视为建议或阻塞由主 agent 裁定。

## 12. 派单记录

| 时间 | 波次 | 对象 | 内容 | 结果 |
| --- | --- | --- | --- | --- |
| 2026-11-14 | W1a | t10 | 本文 §2 | 完成，自测通过 |
| 2026-11-14 | W1a 审核 | t11 | 本文 §2/§3/§6 | FAIL：代理 5xx 归类与 §3.1 旧文本不一致 |
| 2026-11-14 | W1a 复审 | t11 | 修订 §3.1 后定向复核 | PASS |
| 2026-11-14 | W1b1 | t10 | 本文 §4 | 完成，30 场景自测通过 |
| 2026-11-14 | W1b1 审核 | t11 | §4/§3 | FAIL：M-9 分母、响应冻结、M-11 兜底、0-attempt 成功 4 项 |
| 2026-11-14 | W1b1 修复 | t10 | §4.1 | 4 项修复完成，补充定向测试 |
| 2026-11-14 | W1b1 复审 | t11 | §4.1 定向 | PASS，无剩余阻塞 |
| 2026-11-14 | W1b2 | t10 | 本文 §5 | 完成，自测通过 |
| 2026-11-14 | W1b2 审核 | t11 | 本文 §5/§3.3 | PASS，无阻塞 |
| 2026-11-14 | M1 | — | W1a–W1b2 | **里程碑 PASS** |
| 2026-11-14 | W2.0 | t10 | 本文 §6 | 完成，203 锚点自检通过 |
| 2026-11-14 | W2.0 审核 | t11 | 本文 §6/API §3.5 | PASS，无阻塞 |
| 2026-11-14 | W2a1 | t10 | 本文 §7 | 完成，自测通过 |
| 2026-11-14 | W2a1 审核 | t11 | 本文 §7/API §2.2/§5.2 | PASS，无阻塞 |
| 2026-11-14 | W2a2a | t10 | 本文 §8 | 完成，自测通过 |
| 2026-11-14 | W2a2a 审核 | t11 | §8 | FAIL：装饰器外层重复安装会双计 |
| 2026-11-14 | W2a2a 修复 | t10 | §8 | 改为可转发观测身份接口 + 组合测试 |
| 2026-11-14 | W2a2a 复审 | t11 | §8 定向 | PASS，finding 闭合 |
| 2026-11-14 | W2a2b | t10 | 本文 §9 | 完成：定向 -race 通过；全量 -race 3 类基线竞态已复现 |
| 2026-11-14 | W2a2b 预检 | — | §9 审核重点 | 发现“请求构建失败显式结算”可能误计 M-4，交 t11 判定 |
| 2026-11-14 | W2a2b 审核 | t11 | §9/API §5.4–§5.5 | FAIL：构建失败误计 attempt、Do 错误全标 connect、SSE 合成状态污染证据 |
| 2026-11-14 | W2a2b 修复 | t10 | t11 findings | 已派发 |
| 2026-11-14 | W5 | t14（执行2） | `MONITORING_V2_W5_TASKS.md` | 进行中：18 规则 + 29 组 promtool 单测 |
| 2026-11-14 | W5 裁定 | — | §3 第6–9条 | 规则表达式/刷新语义/测试文件/compose 验证口径 |
| 2026-11-14 | W5 | t14（执行2） | `MONITORING_V2_W5_TASKS.md` | 完成 |
| 2026-11-14 | W5 审核 | t11 | W5 任务卡/API §6–§9 | FAIL：宿主负载旧表达式、M-3 不存在桶、未采集兜底、覆盖率单位、04 刷新 5 项 |
| 2026-11-14 | W5 修复 | t14（执行2） | t11 findings | 已派发 |
| 2026-11-14 | W2a2d-account | t14（执行2） | 本文 §10 | 完成：16 名字→10+unknown，-race/wire 通过 |
| 2026-11-14 | W5 修复 | t14（执行2） | t11 findings | 5 项已修复，新增 dashboard 回归测试 |
| 2026-11-14 | W5 复审 | t11 | 5 项定向 | PASS，5 项全部闭合 |
| 2026-11-14 | W2a2d-account 审核 | t11 | §10 | PASS，无阻塞 |
| 2026-11-14 | W4a1 | t14（执行2） | `MONITORING_V2_W4_TASKS.md` | 完成：251/252 幂等验证通过 |
| 2026-11-15 | W4a1 审核 | t11 | W4 任务卡/API §7.2–§7.4 | PASS |
| 2026-11-15 | W2a2b 复审 | t11 | 3 项定向 | PASS，无剩余阻塞 |
| 2026-11-15 | W2a2c | t10 | 兼容/passthrough | 完成：定向/unit 兼容回归 -race 通过 |
| 2026-11-15 | W2a2c 审核 | t11 | §9/API §1.3/§3.3/§3.5 | FAIL：400/404 code/type 证据不完整 |
| 2026-11-15 | W4a2a 审核 | t11 | W4a2a 任务卡 | PASS |
| 2026-11-15 | 暂停 | — | §0 | 记录进展，停止派单 |
| 2026-11-15 | 恢复 | — | §0 | 派 W2a2c 修复（t10）与 W4a2b（t14） |
| 2026-11-15 | W2a2c 修复 | t10 | 400/404 code/type | 完成，定向 -race 通过 |
| 2026-11-15 | W2a2c 复审 | t11 | 定向 | PASS，27+5+2 子用例 |
| 2026-11-15 | W4a2b | t14（执行2） | 定向 race/PG18 | 定向 PASS：500QPS 0 drop、Submit 295µs、shutdown 8692=written+dropped、PG18 501/5001/30/29/当前/0 |
| 2026-11-15 | W2b1 | t10 | OpenAI Responses/plugin | 完成：37 场景矩阵，定向 -race 通过 |
| 2026-11-15 | W2b1 审核 | t11 | W2b1 卡 | FAIL：非流式写失败仍 success、首语义输出超时丢 timeout 证据 |
| 2026-11-15 | W4a2b 审核 | t11 | W4a2b 卡 | PASS，无阻塞 |
| 2026-11-15 | W2b1 修复 | t10 | t11 findings | 完成：非流式写失败→client_abort；首语义超时→timeout/read_timeout |
| 2026-11-15 | W2b1 复审 | t11 | 2 项定向 | PASS |
| 2026-11-15 | W2b2 | t10 | 独立 Chat/Messages | 完成：矩阵/重试/400 code-type，定向 -race 通过 |
| 2026-11-15 | W2b2 审核 | t11 | W2b2 卡 | FAIL：raw Chat SSE 读失败 nil error 时误记 success |
| 2026-11-15 | W4a2c1 最终 build | t14 | server build/race | PASS：build、monitoring/server/middleware/routes/cmd、wire check |
| 2026-11-15 | W4a2c1 审核 | t11 | code/monitoring | 进行中（server build 待 W2b 收敛） |
| 2026-11-15 | W6a | t14（执行2） | `MONITORING_V2_W6_TASKS.md` | 完成：304 SQL/49 组 ack、构建自检、rollback dry-run、4 组测试 |
| 2026-11-15 | W6a 审核 | t11 | W6 任务卡 | FAIL：checksum 归一化 CRLF 导致漏检 |
| 2026-11-15 | W4a2c1 审核 | t11 | code/build | PASS |
| 2026-11-15 | W2b2 修复 | t10 | raw SSE cause | 完成：read_timeout/server_cancel 流式两用例 |
| 2026-11-15 | W2b2 复审 | t11 | 定向 | PASS，121 组合矩阵 |
| 2026-11-15 | W2c1 | t10 | 149 例矩阵 | 88 PASS/61 FAIL，全为 M-11=0；trackedBody 未转发 OpsObservation |
| 2026-11-15 | W2c1 修复 | t10 | trackedBody 身份 | 完成：149 PASS/0 FAIL，public Do 8 例、Close/keep-alive 通过 |
| 2026-11-15 | W2c1 审核 | t11 | 149 矩阵/trackedBody | PASS：149/149、身份转发/Close/M-11/行为零变更 |
| 2026-11-15 | W6a 修复 | t14 | 字节级 checksum | 完成：LF→CRLF FAIL、恢复 PASS、4 tests OK |
| 2026-11-15 | W6a 复审 | t11 | 定向 | PASS，CRLF finding 闭合 |
| 2026-11-15 | W4a2c2 初轮 | t14 | getter/映射/handler | PASS：16 场景 fallback、NULL/0/值、100 正常 fixture |
| 2026-11-15 | W4a2c2 收尾 | t14 | billing/finish 回归 | PASS：build、四包 -race、unit RecordUsage、16 场景、100/100 非空 |
| 2026-11-15 | W4a2c2 审核 | t11 | W4a2c2 卡 | PASS：下界口径/NULL-0/6 处冻结/worker 无 Recorder |
| 2026-11-15 | W7a | t14（执行2） | `MONITORING_V2_W7_TASKS.md` | 完成：看板/判定树/13根因/AC/预算/回滚引用，AC 全标未执行 |
| 2026-11-15 | W7a 审核 | t11 | W7 任务卡 | PASS；VALIDATION 文档已放行 |
| 2026-11-15 | W6b 预检 | t14 | 发现缺 AttachMeta/x-request-id | 暂停，先补元数据 |
| 2026-11-15 | W2a2d-meta | t14（执行2） | AttachMeta 接线 | 完成：6 初始+7 选号更新，ID/13 字段，-race/build PASS |
| 2026-11-15 | W2a2d-meta 审核 | t11 | W2a2d-meta 卡 | PASS：ID 来源/6+7 调用点/字段/failover |
| 2026-11-15 | W7b | t10 | AC-1/2/3/9 证据 | 完成记录：AC-1 后端 PASS/AC-2 前三类/AC-9 本地 PASS；AC-3 unknown 20/149=13.42% FAIL |
| 2026-11-15 | AC-3 修复 | t10 | unknown 归因 | 进行中 |
| 2026-11-15 | W6b 首次 PASS | t14 | 三处命中 | image 8794fde…、request v2smoke-…、PG 1 行、7 phase、M4/M5/M9/M11、usage 11/7+143B、已清理 |
| 2026-11-15 | W6b 幂等复跑 | t14 | 重复执行/清理 | PASS：二跑 exit0，4 组项目 assets=0 |
| 2026-11-15 | W6b 审核 | t11 | 烟测脚本/证据 | PASS：两次 PASS 证据、负例、清理全过 |
| 2026-11-15 | W6c AC-10 | t14 | head/族预算 | PASS：head=2904、最大族 request_phase_seconds=112；4 目标 ≥3 scrape |
| 2026-11-15 | W6c AC-1 子项 | t14 | 3s delay/10s/≤2s/PG | PASS：Prom 20.165s 指出 upstream（P95 4.875s，查询 0.163s）、PG 20.241s/upstream_ms=3010/六段 3021 闭合；完整四类/浏览器首见未执行 |
| 2026-11-15 | W6c 收尾 | t14 | 120s quiet/head 复测/清理 | PASS：head 2904→2975、13 族最大 112、清理 0 |
| 2026-11-15 | W6c 审核 | t11 | series-budget | FAIL：单族仅 sub2api、六段取整表述 |
| 2026-11-15 | W6c 修复 | t14 | 全族预算/取整 | 重跑 PASS：719→622 族、全局 128、sub2api 112、head 2904→2998、取整 2ms≤5ms |
| 2026-11-15 | W6c 复审 | t11 | 2 项定向 | PASS |
| 2026-11-15 | AC-3 审核 | t11 | 分类定向 | FAIL：TLS 指纹 CONNECT 502 未传状态、passthrough SSE code/type 被丢弃 |
| 2026-11-15 | AC-3 修复 | t10 | 两条遗漏 | 完成：159/159，unknown 6.29%，TLS/passthrough 回归 |
| 2026-11-15 | M-7 修复复审 | t11 | 耗尽/WS | PASS |
| 2026-11-15 | final2 | t14 | W6b/AC1/AC5/AC9 | 全部 PASS，digest 9bd11477… |
| 2026-11-15 | AC-4 补全 | t10 | PG18 54 查询 | SQL/变量/角色/过滤 54/54 PASS；浏览器未执行 |
| 2026-11-15 | AC-3 修复 | t10 | unknown 归因 | 完成：149/149，unknown 10/149=6.71%；真实 30m 未执行 |
| 2026-11-15 | W7c AC-4 | t10 | 变量 SQL | PARTIAL：4 变量查询，36/54 面板查询；18 失败疑似 dev 库缺 251/252 |
| 2026-11-15 | W8-AC5 | t14（执行2） | 10×20 请求 | 完成：200 请求/180 成功/20 受控 400，M11 account=10 各 18，QPS 13.33，P95 有数 |
| 2026-11-15 | W8-AC5 审核 | t11 | AC-5 卡 | 进行中 |
| W8-AC6/7 | t14（执行2） | 宿主指标 + 本地 SMTP | 完成：14 项 node 指标、local SMTP firing/resolved |
| W8-AC6/7 审核 | t11 | AC-6/7 | PASS（本地子项） |
| W8-AC2 | t14（执行2） | 字节差异四类 | **完成待审（四类 M2/PG/Prom 一致）** |
| W8-AC9 | t14（执行2） | 行为零变更 | 镜像协议/计费 PASS；异常/Close/Flush 未执行 |
| W8-AC9 审核 | t11 | AC-9 | PASS（镜像协议/计费子项） |
| W8-AC1 | t14（执行2） | 四类延迟注入 | **四类全 PASS（最终镜像 wait=3818ms）** |
| W2a2d-wait | t10（执行1） | 槽位 AddWait 接线 | **完成待审（12 槽位用例，second_wait 100ms）** |
| W2a2d-decoded | t10（执行1） | req_decoded 接线 | **完成待审（五方向 20 例）** |
| W2a2d-switch | t10（执行1） | OpenAI M-7 接线 | **PASS（2026-11-15）** |
| W8-AC8 | t10（执行1） | 断流演练 | 最终镜像 Drill1 PASS |
| W8-AC8 审核 | t11 | AC-8 | 进行中 |
| W8-FinalImage | t14（执行2） | 最终镜像 + wait/W6b 复测 | **final2 全部 PASS**：W6b/AC-1 wait/AC-5/AC-9，digest 9bd11477… |
| W8-Final2-AC8 | t10（执行1） | AC-8 on final2 | 进行中 |
| W8-Final2-AC10 | t14（执行2） | AC-10 on final2 | 进行中 |
| W7c | 最终 AC 表更新 | t14（执行2） | **PASS（除 AC-8 两行待同步）** |
| W8-AC8 | t10（执行1） | 断流演练 | **Drill1 PASS；Drill2 未执行；完整 PARTIAL** |
| 2026-11-15 | W2b2 | t10 | 独立 Chat/Messages | 进行中 |
| 2026-11-15 | W4a2b 最终验证 | t14 | 无 overlay build/test | **PASS**：wire check、精确定向 -race、build；service 全包仅基线 race |
| 2026-11-15 | W4a2b 补充证据 | t14 | PG18 -race | PASS：500+nullable/501 异步/5001 过期/30d/29d/0 |
| 2026-11-15 | W4a2b 审核 | t11 | W4a2b 卡 | 排队（W2b1 后） |
| 2026-11-15 | W4a2c1 | t14（执行2） | Recorder→sample | 接线+wire 完成；server build 等 W2b2 helper 收敛 |
| 2026-11-15 | W4a2c1 source | t14 | sample source 映射 | in_wire/body_read、req_upstream/request_body_read、resp_upstream/response_body_read、resp_downstream/writer_size |
| 2026-11-15 | W4a2c1 monitoring | t14 | 定向/全包 -race | PASS：200/0attempt/panic/abort/partial/retry、16 并发、重入一次、sink 隔离、非阻塞 |
| 2026-11-15 | W4a2b | t14（执行2） | W4a2b 任务卡 | PASS |
| 2026-11-15 | W4a2a | t14（执行2） | usage ent/读写映射 | 完成：真实源码 build/PG18 16 组/定向 -race 通过 |

## 13. 最终状态（2026-11-15）

- 最终本地镜像：`sub2api-v2smoke:worktree-final2-1794725105`，digest `sha256:9bd114778a3373b008f6a04f1cf166228461f9e238f9fc323989ec4e43dad949`（linux/amd64，未 push/未部署）。
- final2 复跑：W6b 三处命中、AC-1 wait（wait 5303ms）、AC-5 10×20、AC-9 协议/计费、AC-8 Drill1、AC-10 head/族预算全部 PASS。
- 代码缺口：AddWait、req_decoded、原生 OpenAI M-7 均已修复并由 t11 复审 PASS。
- 验收表与证据：`docs/MONITORING_V2_VALIDATION.md`、`docs/dashboard-v2.md` §11（t11 最终只读确认 PASS，97 链接有效）。
- 明确未执行：真实 30m unknown 窗口、Grafana 浏览器联动、生产宿主指标、外部 SMTP、AC-8 Drill2、镜像异常注入/精确 Close-Flush、AC-10 生产代表负载；W3 P1 未实现，AC-1/AC-5 结论限定 P0。
- 工作树未 commit；最终检查点 `/tmp/sub2api-checkpoints/final-20261115/`（tracked.patch + untracked.tgz + STATUS）。
- 生产部署、镜像 push、发布 tag 均未执行，需用户授权后按 `deploy/MONITORING_RELEASE.md` 执行。
