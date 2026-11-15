# Sub2API 观测看板 V2 - 接口与指标契约

| 项 | 内容 |
| --- | --- |
| 文档状态 | **契约定稿 v1.0**（Codex 审核无阻塞，2026-11-14 定案） |
| 编写日期 | 2026-11-14 |
| 输入 | `Sub2API-观测看板-V2-需求文档.md` v0.3 §13；`/tmp/v2-codex-review.md` |
| 配套文档 | `docs/MONITORING_V2_DEV_PLAN.md`、`docs/dashboard-v2-mockup.html`（布局评审稿）、`docs/MONITORING_API.md`（V1） |
| 交付形态 | 源码内增量埋点 + Prometheus/Grafana as code；本次不含线上部署 |

本文件是 V2 的唯一命名与口径契约。与需求文档 §13 冲突时按本文执行，所有差异集中在 §12「对需求的修订请求」。**v2 相对 v1 的实质变更**：修正 phase 时间轴、Recorder 提前到网关入口中间件、新增请求级结果指标、账号 TTFT 直方图、字节按实际读写计数、修正断流告警与只读账号权限、明确异步 finish 边界。

---

## 1. 总则

### 1.1 命名与单位

- 指标前缀统一 `sub2api_`；耗时一律**秒**，字节一律 **byte**。
- Counter 以 `_total` 结尾；Histogram 以 `_seconds` / `_bytes` 结尾；Gauge 用裸名。
- Label 值小写 snake_case；未知值统一落 `unknown`，禁止透传 URL/模型/用户名/IP。
- 新增指标族必须先过 §3.6 序列预算；不得先加后算。

### 1.2 三条硬约束

1. **行为零变更**：埋点只读观测值、累加内存计数；不得改变请求体、响应体、状态码、计费、重试次数与时机；不得额外读取/缓存/提前关闭响应体。
2. **标签有界**：所有 label 来自有限枚举，归一化函数负责兜底；绝不接受任意字符串。
3. **抓取不查 DB**：`/metrics` 期间不访问数据库；覆盖率在进程内按分钟滚动。

### 1.3 两个请求集合（必须区分）

| 集合 | 定义 | 指标 |
| --- | --- | --- |
| 网关请求（gateway request） | 进入网关路由组、由入口中间件建立 Recorder 的请求 | M-10、M-9 分母、M-6 |
| 上游尝试（upstream attempt） | 应用层一次转发尝试；一次尝试内部可能因签名纠错/Grok fallback 发生多次物理 HTTP 发送 | M-4、M-5、M-11 |

**attempt 的边界**：从应用层发出上游请求到响应体读完/失败结束。`Do`/`DoWithTLS` 返回只代表拿到响应头，**不能**在返回处结算 attempt。attempt 句柄由**应用转发层**（`Forward`、`ForwardAs*`、`OpenAIGatewayService.Forward` 等）在每次逻辑尝试开始时创建，HTTP 层（`doUpstreamRequest`）只更新该句柄的发送/响应头/首字节/结束事件，**不得**自行创建 attempt。一次 attempt 内部由 `httpUpstream` 触发的额外物理发送（签名纠错重试、Grok fallback、redirect）不单独计入 M-4，但其请求/响应字节计入 M-2；这是有意的取舍，避免 M-4 与 M-6 的 attempt_count 不一致。

---

## 2. 时间轴与 phase 口径（修正 v1 错误）

### 2.1 时间点

```
t_entry   入口中间件建立 Recorder
t_body    读完客户端请求体（或读失败/未读即拒绝）
t_waitS/t_waitE  首次发送前的槽位等待（0..n 次）
t_send1   首个上游请求发出（应用层第一个 attempt）
t_sendN   最终 attempt 的请求发出
t_headers 最终 attempt 的响应头（transport 全失败时 = t_terminal）
t_first   最终 attempt 的响应体首字节
t_done    响应体读完 / 流结束 / 客户端断开（transport 全失败时 = t_headers）
t_finish  usage 任务提交完成；无 usage 任务时 = t_done
t_terminal 终止时刻（失败已确定、不再重试）
```

正常路径满足 `t_entry ≤ t_body ≤ t_send1 ≤ t_sendN ≤ t_headers ≤ t_first ≤ t_done ≤ t_finish`。

**终止规则（必须实现，不能只靠“残差吸收”）**：

| 场景 | 规则 |
| --- | --- |
| 未读 body 即被拒绝（认证/限流/参数） | `t_body = t_entry`（body_read=0），`t_send1=t_sendN=t_terminal`，wait/upstream/response/finish=0，`gateway = t_terminal - t_entry` |
| 读 body 失败/客户端断开 | `t_body = t_terminal`，body_read 覆盖全程，其余互斥段 0 |
| body 读完但发送前失败（路由/参数/计费） | `t_send1=t_sendN=t_terminal`，`upstream=response=0`，`gateway = (t_terminal - t_body) - wait` |
| transport 全失败（无响应头） | `t_headers=t_done=t_terminal`，`upstream = t_terminal - t_send1`，`response=0` |
| 响应头后失败/中断 | `t_done=t_terminal`，response 覆盖到头到终止，upstream 正常 |
| 无 usage 任务（拒绝/未进入计费） | `t_finish=t_done`，finish=0 |

### 2.2 互斥段（用于堆叠与总时长闭合）

| phase | 计算 | 说明 |
| --- | --- | --- |
| `body_read` | `t_body - t_entry` | 读失败取终止时刻；未读即拒绝为 0 |
| `wait` | `Σ(t_waitE - t_waitS)`，**仅首次发送前** | 只扣首次发送前的槽位等待；重试等待不属于本段 |
| `gateway` | `(t_send1 - t_body) - wait` | **残差本地耗时**；发送前失败时 `t_send1` 换成 `t_terminal` |
| `upstream` | `t_headers - t_send1` | 首次发送→最终响应头；**所有失败 attempt、重试间隔、纠错/fallback 都计入此段** |
| `response` | `t_done - t_headers` | 响应头→读完响应体，**包含等待首字节的时间**；无响应头时为 0 |
| `finish` | `t_finish - t_done` | 见 §2.4 |

六个互斥段满足：

```
body_read + wait + gateway + upstream + response + finish = t_finish - t_entry
```

`wait` 必须在 `[t_body, t_send1]` 内测量；多个 wait 区间不得重叠，累加后若超过 `t_send1 - t_body`（时钟/实现异常），按 `wait = t_send1 - t_body` 截断并记录内部告警，保证 gateway ≥0。

### 2.3 辅助重叠段 `headers_end`（TTFT）

- `headers_end = t_first - t_sendN`，**与 `upstream` 重叠**，不是 `upstream` 的子区间，也不参与堆叠/占比/总时长。
- 重试场景取最终 attempt 的 `t_first - t_sendN`。
- 无响应体首字节（空响应、纯 header 错误、transport 全失败）时 **不 Observe** `headers_end`，不得虚构 0 或 TTFT。
- 看板：`phase="headers_end"` 只用于 TTFT 分位与告警；所有堆叠/占比查询必须 `phase!="headers_end"`。

> 需求 §13.1.1 写“`headers_end` 是 `upstream` 的子区间”，方向相反；已列为修订 §12-R1。

### 2.4 finish 与异步 usage

finish 定义为“响应结束 → usage 任务提交完成”，不是 DB 提交完成：

- 同步路径：任务函数返回即结束。
- 异步路径：`UsageRecordWorkerPool.Submit` 返回（Enqueued/Sync/Dropped）即结束；Dropped 也 Observe 实际时长，不置空。
- 异步 usage 的 DB 失败**不得**覆写请求结果，也不计入 `gateway_db`；`gateway_db` 只用于请求路径内的同步 DB 失败（计费、鉴权、路由查询）。
- 6 个互斥段 + headers_end 的 Observation 在 `Finalize()` 内一次性完成；`finish` 永远可观测，不存在“drop 不 Observe”的分支。

---

## 3. Go 侧埋点接口

### 3.1 包与依赖

```
backend/internal/monitoring/
  metrics.go          # 已有 V1
  ops_metrics.go      # V2 指标定义（M-1..M-11）
  ops_recorder.go     # Recorder / Attempt 状态机
  ops_labels.go       # 归一化与 failure 分类
  ops_coverage.go     # M-9 分钟桶
backend/internal/server/middleware/
  ops.go              # gin 入口中间件（可 import gin + monitoring）
  ops_test.go
```

依赖方向：`monitoring` 不 import `service`/`handler`/`gin`；gin 入口中间件放 `server/middleware`，它已 import `monitoring`，避免循环依赖。

### 3.2 入口中间件（关键修正）

Recorder 必须在**认证/限流之前**建立，否则拿不到 body_read 起点，也覆盖不了中间件拒绝：

```go
// server/http.go
r.Use(gin.HandlerFunc(metricsMiddleware))   // V1，最外层
r.Use(middleware2.OpsMiddleware(cfg))       // V2：只对网关路由生效，在 Recovery 之前
r.Use(middleware2.Recovery())
```

`OpsMiddleware` 行为：

1. 用 `c.FullPath()` 判断是否网关路由；非网关直接 `c.Next()`。
2. 网关路由：
   ```go
   ctx, rec := monitoring.BeginGatewayRequest(c.Request.Context(), c.FullPath())
   c.Request = c.Request.WithContext(ctx) // 关键：让 handler/service/HTTP 层都能取到
   defer func() {
       rec.Finalize(monitoring.FinalizeInput{
           Status: c.Writer.Status(),
           Panic:  c.GetBool(monitoring.OpsPanicKey),
       })
   }()
   c.Next()
   ```
3. Finalize 在 Recovery 之后执行：Recovery 已写入最终状态码，并在真正处理 panic 的分支 `c.Set(monitoring.OpsPanicKey, true)`。Finalize 读到 panic 标记后强制 `result=gateway_error, failure_reason=gateway_panic`。
4. **埋点异常隔离**：只对埋点调用包 `safeObserve(func())`（内部 `recover` + 日志），**不得**包裹 `c.Next()`，不得重新执行 handler，不得吞业务 panic。
5. **保留 Recovery 现有语义**：broken pipe 分支**不**设置 panic 标记（归 `client_abort_during_response`）；**除 broken pipe 外，在检查 `c.Writer.Written()` 之前**设置 panic 标记，即使响应已写出（如 SSE 中途 panic）也要让 Finalize 归 `gateway_panic`，同时保持原响应状态码与已写内容不变。不得把 broken pipe 误记 gateway_panic。

网关路由判定用**显式前缀白名单**，与 `routes/gateway.go` 同步维护：

```
/v1/messages, /v1/messages/count_tokens,
/v1/chat/completions, /chat/completions,
/v1/responses, /v1/embeddings, /v1/images/,
/v1beta/, /antigravity/, /v1/models (仅 POST 转发)
```

不匹配的路径（管理接口、健康检查、静态资源）不建立 Recorder，也不计入 M-9 分母。

### 3.3 Recorder / Attempt 契约

```go
type OpsRecorder struct{ /* 未导出；内部加锁 */ }

// 返回的 ctx 必须写回 c.Request / 传给 service；HTTP 层从这个 ctx 取 attempt 句柄。
func BeginGatewayRequest(ctx context.Context, path string) (context.Context, *OpsRecorder)
func (r *OpsRecorder) AttachMeta(m OpsRequestMeta) // 认证/解析后补充；可多次，后写覆盖
func (r *OpsRecorder) MarkBodyRead(n int64, declared int64, status string)
func (r *OpsRecorder) AddWait(d time.Duration)     // 仅首次发送前
func (r *OpsRecorder) StartAttempt(account string) *OpsAttempt // 仅应用转发层调用
func (r *OpsRecorder) AddBytes(direction string, n int64, status string)
func (r *OpsRecorder) MarkClientAbort(stage string)
func (r *OpsRecorder) MarkSwitch()
func (r *OpsRecorder) Finalize(in FinalizeInput)   // 仅入口中间件 defer 调一次

type FinalizeInput struct {
    Status int  // c.Writer.Status()
    Panic  bool // Recovery 设置
}

type OpsAttempt struct{ /* 未导出 */ }
// 以下由 HTTP 层/响应处理层更新，不创建 attempt：
func (a *OpsAttempt) Sent()                        // 仅首次物理发送记录 sentAt；后续重发只更新 lastSentAt
func (a *OpsAttempt) NoteHeaders(status int)       // 记录最近一次物理响应的响应头候选
func (a *OpsAttempt) NoteFirstByte()               // 记录最近一次物理响应的首字节候选
func (a *OpsAttempt) UseResponse()                 // 锁定“最终消费的响应身份”，不冻结后续首字节
func (a *OpsAttempt) BodyDone(err error)           // 响应体结束/失败
func (a *OpsAttempt) Done(result, reason string, ev FailureEvidence) // 应用层最终结算
```

契约：

| 项 | 规则 |
| --- | --- |
| 请求级单次 | `MarkBodyRead`/`Finalize` 只生效一次；重复调用忽略并打 debug |
| 累计事件 | `AddWait`/`AddBytes`/`MarkSwitch` 可多次，累加 |
| attempt 创建 | **只有应用转发层** `StartAttempt`；HTTP 层 `doUpstreamRequest` 只能 `Sent/Headers/FirstByte/BodyDone` |
| attempt 结算 | 应用层 `Done` 只生效一次/句柄；未 Done 的 attempt 在 Finalize 时按 unknown 结算 |
| 内部重发 | 同一 attempt 内 `Sent` 只记录**首次**物理发送时间；`NoteHeaders/NoteFirstByte` 保存“最近一次物理响应”的候选；`UseResponse()` 锁定最终响应身份后，仅接受该响应的首字节事件，其他内部重发的响应事件忽略 |
| TTFT | M-11 只在 `Done` 时 Observe **一次**：最终响应已锁定且其首字节已到（可能在 `UseResponse` 之后才到），用该首字节时间 - 该 attempt 首次发送时间；未锁定或未收到首字节不 Observe |
| 请求级 headers_end | 取最终 attempt 的冻结响应首字节 - 最终 attempt 首次发送时间；内部重发的 400 首字节不得污染 |
| nil 安全 | 所有方法 nil receiver 安全；未启用埋点时入口中间件不创建 Recorder |
| 并发 | Recorder 加 `sync.Mutex`；async finish 与 Finalize 可能跨 goroutine |
| panic | Finalize 不 recover 业务 panic；只读 Recovery 设置的标记；埋点调用自身用 `safeObserve` 隔离 |

`FailureEvidence`（纯数据，由错误分支填充，不额外读 body）：

```go
type FailureEvidence struct {
    StatusCode     int    // 上游 HTTP 状态码，0 表示 transport 错误
    Stage          string // connect|headers|body|downstream|db|ratelimit|panic
    TransportKind  string // timeout|refused|dns|tls|reset|eof|other
    ProxyUsed      bool
    UpstreamCode   string // 已解析的 error.code/type，可为空
    ClientCanceled bool
    DBError        bool
    RateLimited    bool
}
```

### 3.4 接入点（已核对代码）

| 事件 | 接入位置 |
| --- | --- |
| 建立 Recorder | `server/http.go` 的 `OpsMiddleware`（新增） |
| 请求体读完/失败 | `handler/gateway_handler.go:148` 等各 handler 的 body 读取处；失败分支统一包一层 `ops.BodyReadError(err)` |
| 等待 | `gateway_handler.go:253`、`:448` 槽位获取前后 |
| 上游发送/响应头 | 应用转发层 `StartAttempt`；`repository/http_upstream.go:doUpstreamRequest` 从 `req.Context()` 取句柄并 `Sent()`，返回后 `Headers()` |
| 首字节 | `service/gateway_forward.go` 流式/非流式响应读取首字节处 `FirstByte()` |
| 响应结束 | `handleStreamingResponse` / `handleNonStreamingResponse` 的 defer；写客户端处累计 `resp_downstream` |
| attempt 结束 | 响应体读完/失败后 `attempt.Done(...)`；**不在 Do 返回处** |
| 账号切换 | `handler/failover_loop.go:245-260`，`SwitchCount++` 同点 |
| 限流拒绝 | 限流中间件拒绝分支填 `RateLimited=true`；入口中间件已建立 Recorder |
| panic | `middleware/recovery.go` recover 分支设置 `OpsPanicKey` |
| finish | `handler/gateway_handler.go:2566 submitUsageRecordTask` 返回处 |
| 原生 OpenAI | `openai_gateway_handler.go:826 OpenAIGatewayService.Forward`、`openai_chat_completions.go:262` 等，见 §5 路径矩阵 |

### 3.5 路径覆盖矩阵（修正 P0 遗漏）

| 优先级 | 路由/入口 | 转发函数 | 埋点内容 |
| --- | --- | --- | --- |
| P0 | `/v1/messages` | `GatewayService.Forward` | 全量 |
| P0 | `/v1/messages` API-key passthrough | `forwardAnthropicAPIKeyPassthroughWithInput` | 全量 |
| P0 | `/v1/chat/completions` → Claude/兼容 | `ForwardAsChatCompletions` | 全量 |
| P0 | `/v1/responses` → Claude/兼容 | `ForwardAsResponses` | 全量 |
| P0 | 原生 OpenAI `/v1/chat/completions`、`/chat/completions`、`/v1/responses` | `OpenAIGatewayService.Forward`（`openai_gateway_handler.go:826`）及独立链（`openai_chat_completions.go:262`） | 全量 |
| P1 | `/antigravity/v1/messages` | `AntigravityGatewayService.Forward` | 全量 |
| P1 | `/v1beta/*` Gemini | Gemini v1beta 上游链 | phase + 字节 |
| P1 | Bedrock | `forwardBedrock` | phase + 字节 |
| P2 | `/v1/images/*`、批量、WS | 各自上游链 | 仅请求级结果 M-10；M-3 归 `images`/`other` |
| 排除 | count_tokens、models、余额/探测、管理接口 | — | 不进入 M-10/M-9 分母 |

> M-1/M-11 没有 `endpoint_class` 标签，P2“phase 记 endpoint_class=other”不成立；P2 只保证请求级计数与 M-3 的 `endpoint_class=images`。

### 3.6 序列上界（修正账号上限）

账号白名单**启动时快照，最多 10 个真实名 + `unknown`**，运行期不刷新；新账号/改名一律 `unknown` 直到重启。这样避免 CounterVec 子序列只增不减。

| 指标 | 计算 | 上界 |
| --- | --- | --- |
| M-1 | 7 phase × (13 桶 + Inf + sum + count) | 112 |
| M-2 | 5 direction | 5 |
| M-3 | 5 endpoint_class × (10 桶 + Inf + sum + count) | 65 |
| M-4 | 11 account × 6 result × **14 reason**（13 类 + 成功空串） | 924 |
| M-5 | 5 model_class × 6 result | 30 |
| M-6 | 3 outcome | 3 |
| M-7 | 无标签 | 1 |
| M-8 | 2 stage | 2 |
| M-9 | 4 kind | 4 |
| M-10 | 6 result × **14 reason** | 84 |
| M-11 | 11 account × 16 | 176 |
| **合计** | | **1406** |

单族 <2000、总 <8000 均满足；**不再用“result/reason 相关性”证明上限**。CI 单测：随机 1000 个恶意账号名，输出集合必须 ≤11。

---

## 4. 标签枚举与归一化

### 4.1 `account`

- 启动时从 DB 读一次 `accounts.name`，归一化：小写、trim、`[a-z0-9_-]`、长度 ≤32；**归一化后冲突**（如 `a.b` 与 `a b`）保留首个，其余 `unknown`。
- 白名单之外一律 `unknown`；改名/新增在重启前不可见，属预期。面板 description 写明。
- 禁止 `account_id` 作为 label。

### 4.2 `endpoint_class`

`responses` / `chat` / `embeddings` / `images` / `other`。

| 路径 | class |
| --- | --- |
| `/v1/messages`、`/antigravity/v1/messages`、`/v1/responses` | `responses` |
| `/v1/chat/completions`、`/chat/completions` | `chat` |
| `/v1/embeddings` | `embeddings` |
| `/v1/images/*`、图片生成端点 | `images` |
| count_tokens、models、其他 | `other` |

### 4.3 `model_class`

`gpt` / `codex` / `claude` / `gemini` / `other`；匹配顺序：`codex` → `claude` → `gemini` → `gpt` → `other`，大小写不敏感，使用**请求原始模型**。

### 4.4 `result` 与 `failure_reason`

`result`：`success` / `upstream_error` / `timeout` / `client_abort` / `gateway_error` / `rejected`。

`failure_reason`（按优先级，先命中先返回）：

| # | 值 | 判定（FailureEvidence） |
| --- | --- | --- |
| 1 | `upstream_rate_limit` | status 429/529 |
| 2 | `upstream_auth` | status 401/403 |
| 3 | `upstream_model_or_request` | status 400/404 且 `UpstreamCode` 非空 |
| 4 | `upstream_server_error` | status 500/502/503/504（**新增，见 §12-R2**） |
| 5 | `upstream_connect_timeout` | Stage=connect 且 TransportKind=timeout |
| 6 | `upstream_read_timeout` | Stage=headers/body 且 TransportKind=timeout |
| 7 | `proxy_unreachable` | Stage=connect 且 (refused/dns/tls) 且 ProxyUsed；或代理返回 502/503/504 |
| 8 | `client_abort_during_body_read` | Stage=body_read 且 ClientCanceled |
| 9 | `client_abort_during_response` | Stage=response 且 ClientCanceled |
| 10 | `gateway_panic` | Panic 标记 |
| 11 | `gateway_db` | 请求路径同步 DB 错误 |
| 12 | `gateway_ratelimit` | RateLimited |
| 13 | `unknown` | 兜底 |

规则：

- `success` 的 reason 必须是空串 `""`。
- 分类函数纯函数，输入 `FailureEvidence`，单测覆盖 13 类 + 优先级冲突 + success 空串。
- 代理 5xx 与源站 5xx 必须可区分：由 `ProxyUsed` + 错误来源判定；无法确定时落 `unknown`。

---

## 5. 指标字典（M-1 ~ M-11）

### 5.1 M-1 `sub2api_request_phase_seconds`

- Histogram，label `phase`（7 值），桶 `0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5,10,30,60,+Inf`。
- 六互斥段 + headers_end 在 `Finalize` 一次性 Observe；未到达的互斥段 Observe 0；`headers_end` 未到达不 Observe。
- 占比/堆叠必须 `phase!="headers_end"`；总时长闭合单测：`sum(6 段) == total ± 1ms`（用同一请求的样本，不用 P95 相加）。

### 5.2 M-2 `sub2api_transfer_bytes_total`

- Counter，label `direction`：`in_wire` / `req_decoded` / `req_upstream` / `resp_upstream` / `resp_downstream`。

| direction | 实际观测 | 状态 |
| --- | --- | --- |
| `in_wire` | 请求体读循环实际返回的 `n` 之和 | complete/partial/unknown |
| `req_decoded` | 解码后 JSON 长度 | complete/unknown |
| `req_upstream` | 写上游请求体的 `Write` 返回 `n` 之和（所有物理发送） | complete/partial/unknown |
| `resp_upstream` | 读上游响应体实际返回的 `n` 之和（**解压后**） | complete/partial |
| `resp_downstream` | 写客户端 `Write` 成功返回 `n` 之和 | complete/partial |

- **禁止**用 `Content-Length` 冒充实际字节；声明值只作为 M-3 的辅助观测。
- 计数包装器原样返回 `(n, err)`、保留 `Close` 语义，不预读、不缓存、不提前关闭。
- `resp_upstream` 与 `resp_downstream` 不守恒是正常的（协议转换、SSE 重写、重试失败体、drain）；面板称“字节差异/放大”，**不得**单独据此判定 abort/截断，必须结合 `failure_reason` 与 PG 样本。
- 覆盖状态不进入 label；只用于 M-9。

### 5.3 M-3 `sub2api_request_body_bytes`

- Histogram，label `endpoint_class`，桶 `1024,4096,16384,65536,262144,1048576,4194304,16777216,67108864,268435456`。
- Observe **实际读取字节数**；读失败时 Observe 已读部分（partial），不以声明值补全。
- 大请求占比只看分布，不做带宽求和。

### 5.4 M-4 `sub2api_upstream_requests_total`

- Counter，label `account`、`result`、`failure_reason`。
- **只统计真实上游 attempt**（应用层一次尝试），在响应体结束/失败时 +1；gateway 0-attempt 失败不计入本族。
- 看板“真实上游请求”不再需要 `account!="unknown"` 过滤；`unknown` 只代表账号名不可知。

### 5.5 M-5 `sub2api_model_requests_total`

- Counter，label `model_class`、`result`。
- **每次 attempt 结束 +1**，与需求 §13.1“同上”一致；重试会重复计数。
- 用途：模型档位对比，不用于计费/token 核算。

### 5.6 M-6 `sub2api_attempts_total`

- Counter，label `outcome`：`first_success`（1 attempt 且成功）/ `retried_success`（≥2 attempt 且成功）/ `all_failed`（最终失败，含 0 attempt 的网关拒绝）。
- 每网关请求一次，在 Finalize 时计数。

### 5.7 M-7 `sub2api_account_switches_total`

- Counter，无 label；跨账号切换时 +1；同账号重试不算。

### 5.8 M-8 `sub2api_client_abort_total`

- Counter，label `stage`：`body_read` / `response`。
- 判定：客户端连接断开且请求 context 已取消/写返回 `broken pipe`/`connection reset`；超时与服务端 shutdown 不算。
- 正常 EOF 不是 abort；body 读失败但客户端未取消（如 `unexpected EOF`）归 `upstream`? 不，归 `unknown` 并在 M-10 reason=unknown；不要伪造 abort。

### 5.9 M-9 `sub2api_metric_coverage_ratio`

- Gauge，label `kind`：`request` / `account` / `bytes` / `user`；值 0~1，`-1` 表示该分钟无样本。
- 分母 = 入口中间件记录的网关请求数（**包含未接入路径**，因此能发现漏接入）；分子按 kind：
  - `request`：6 段 + finish 已 Observe 的 Recorder 数（检测部分接入）。
  - `account`：最终账号名非 `unknown` 的请求数。
  - `bytes`：四个方向都有 `complete` 观测的请求数。
  - `user`：UserID 与 APIKeyID 均非 0 的请求数。
- 分钟桶用一把 `sync.Mutex` 保护 `{entry, request, account, bytes, user}`，每分钟整体 Swap 后 Set gauge；不使用多个独立 atomic，避免跨分钟错配。
- 无样本时 Set -1，面板显示“无样本”，禁止 `or vector(1)` 伪装健康。

### 5.10 M-10 `sub2api_gateway_requests_total`（新增）

- Counter，label `result`、`failure_reason`；每网关请求 Finalize 时 +1，成功 reason 为空串。
- 解决三件事：0-attempt 失败的根因归属、断流告警的流量分母、M-4 语义纯净。
- 理论 6×13，实际合法组合约 60；上界 78。
- 看板：失败根因优先用 M-10（请求视角）；上游尝试视角用 M-4。

### 5.11 M-11 `sub2api_upstream_ttft_seconds`（新增）

- Histogram，label `account`；桶同 M-1（TTFT 需要 30s/60s 长尾）。
- 每次 attempt 在 `Done` 时 Observe **一次**：必须已 `UseResponse()` 锁定最终响应身份，且该响应首字节已到（首字节通常晚于锁定时刻，仍应接受）；内部重发产生的 400 首字节不 Observe。
- 作用：账号 P95 TTFT（AC-5）。口径是“每 attempt 的 TTFT”，不是每请求；面板注明。
- 上界 11×16=176。

---

## 6. Prometheus 契约

### 6.1 抓取

| 项 | V2 |
| --- | --- |
| `scrape_interval` | 15s |
| `scrape_timeout` | 10s |
| jobs | `sub2api`、`prometheus`、`node`（V2 强制启用）、`alertmanager` |
| `rule_files` | `/etc/prometheus/rules/*.yml` |
| alertmanager | `alertmanager:9093` |
| retention | 90d / 8GB |

### 6.2 预算与降维

- 上限：head series <8000、单族 <2000。
- 降维顺序：M-5 `model_class` 合并为 `other` → M-3 `endpoint_class` 合并为 `other` → M-1 桶裁剪。**禁止**去掉 phase、failure_reason、account。
- 监控栈自身指标（`prometheus_*`、`node_*`、`go_*`、`process_*`、V1 HTTP）也占 head；验收必须看整机 `prometheus_tsdb_head_series`，不能只看 V2 的 1406。

---

## 7. PostgreSQL 契约

### 7.1 Grafana 只读角色（修正权限范围）

角色 `grafana_ro` 是**唯一**配置对象；不得修改应用账号 `sub2api` 的任何参数。

```sql
\set ON_ERROR_STOP on
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'grafana_ro') THEN
    CREATE ROLE grafana_ro LOGIN;
  END IF;
END
$$;
ALTER ROLE grafana_ro WITH PASSWORD :'password';
ALTER ROLE grafana_ro SET default_transaction_read_only = on;
ALTER ROLE grafana_ro SET statement_timeout = '15s';

GRANT CONNECT ON DATABASE sub2api TO grafana_ro;
GRANT USAGE ON SCHEMA public TO grafana_ro;

-- 最小权限：只授面板需要的表/列
GRANT SELECT ON usage_logs, ops_request_transfer_samples,
                accounts, groups, api_keys, ops_system_metrics,
                ops_metrics_hourly, ops_metrics_daily
  TO grafana_ro;
GRANT SELECT (id, email) ON users TO grafana_ro;
GRANT SELECT (id, name) ON api_keys TO grafana_ro;

-- 仅当迁移 owner 确定后执行；owner 用 psql 变量传入
-- ALTER DEFAULT PRIVILEGES FOR ROLE :owner IN SCHEMA public
--   GRANT SELECT ON TABLES TO grafana_ro;
```

验收（必须用 Grafana 相同的 TCP + 密码连接）：

```sh
PGPASSWORD=... psql -h 127.0.0.1 -U grafana_ro -d sub2api -c 'select 1'
PGPASSWORD=... psql ... -c "set default_transaction_read_only=off; update users set email='x'"  # 必须因权限失败
PGPASSWORD=... psql ... -c "set default_transaction_read_only=off; create table x(i int)"       # 必须因权限失败
PGPASSWORD=... psql ... -c 'select pg_sleep(20)'                                                # 15s 超时
```

> `default_transaction_read_only` 只是默认值，不是权限边界；必须验证即使关掉它，DML/DDL 仍因授权失败。

Grafana 数据源：

| 项 | 值 |
| --- | --- |
| uid / name | `sub2api-pg` / `Sub2API Postgres` |
| url | `postgres:5432` |
| database/user | `sub2api` / `grafana_ro` |
| password | `${GRAFANA_PG_PASSWORD}` |
| jsonData | `sslmode: disable`、`maxOpenConns: 3`、`maxIdleConns: 1`、`connMaxLifetime: 14400`、`postgresVersion: 1500` |

### 7.2 `usage_logs` 字段（源码缺失，需新增）

当前仓库的 `usage_logs` **没有** `body_bytes` 与 `gateway_latency_ms`（ent schema 与迁移均无）。V2 必须补：

| 动作 | 文件 |
| --- | --- |
| 迁移加列（可空 INT/BIGINT） | `backend/migrations/xxx_add_usage_logs_v2_fields.sql` |
| ent schema 字段 | `backend/ent/schema/usage_log.go` |
| 写入映射 | `backend/internal/repository/usage_log_repo_insert.go`、`service/usage_service.go` |
| 查询/DTO | 相关 usage 查询与 `usage_log_create_result.go` |

口径：`body_bytes` = M-2 `in_wire` 实际读取值；`gateway_latency_ms` = `gateway + wait + finish`（毫秒）。覆盖率按小时展示，<90% 黄色。

### 7.3 `ops_request_transfer_samples`

```sql
CREATE TABLE IF NOT EXISTS ops_request_transfer_samples (
  id BIGSERIAL PRIMARY KEY,
  observed_at TIMESTAMPTZ NOT NULL,
  request_id TEXT NOT NULL,
  user_id BIGINT, api_key_id BIGINT, account_id BIGINT, group_id BIGINT,
  platform TEXT, model TEXT, endpoint_class TEXT, endpoint TEXT,
  result TEXT, http_status INT, failure_reason TEXT,
  attempt_count INT, failed_attempts INT, node TEXT,
  body_read_ms INT, gateway_ms INT, wait_ms INT, upstream_ms INT,
  headers_end_ms INT, response_ms INT, finish_ms INT, total_ms INT,
  bytes_in_declared BIGINT, bytes_in_wire BIGINT, bytes_in_decoded BIGINT,
  bytes_up_req_total BIGINT, bytes_resp_upstream BIGINT, bytes_resp_downstream BIGINT,
  bytes_in_wire_status TEXT, bytes_up_req_total_status TEXT,
  bytes_resp_upstream_status TEXT, bytes_resp_downstream_status TEXT,
  bytes_in_wire_source TEXT, bytes_up_req_total_source TEXT,
  bytes_resp_upstream_source TEXT, bytes_resp_downstream_source TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

- 迁移必须**纯增量**：`CREATE TABLE IF NOT EXISTS` + `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`；部署前只读核对线上列类型/非空约束/索引，产出映射表；不兼容时**阻止发布**，不得“只写交集”静默丢字段。
- 写入 100% 网关请求（当前实测 ~0.15 QPS ≈ 1.3 万行/日，30d ≈ 40 万行，可接受）；队列上限 8192，批量 500 行/1s；满则丢弃并日志告警，绝不阻塞请求。
- `request_id` 必填：入站无 ID 时由入口中间件生成稳定内部 ID（`internal-<unixnano>-<seq>`），禁止空串。
- 丢弃/失败必须可观测：日志 + `ops_system_logs`（或等价现有通道）；若新增 Prometheus 计数，需另算预算。
- 保留 30d，加入现有 cleanup targets；`retention=0` 的 TRUNCATE 语义只用于显式配置，默认 30，配置校验拒绝 0 以外的负数。
- 索引按查询实测确定（见 §7.4），不要假设复合索引能免排序。
- 样例来源：本地 3257 行历史数据回放前必须脱敏（去掉 request_id、user_id、IP）。

### 7.4 Top-N 查询与索引验收

`WHERE observed_at > $from ORDER BY total_ms DESC LIMIT 40` 在 `(observed_at, total_ms)` 上**不能**免排序。方案：

- 默认保留 `(observed_at DESC)` 索引，6h 窗口内行数有限，排序成本可接受。
- 若 30d/6h 实测超过 2s，再加 `(total_ms DESC)`、`(body_read_ms DESC)`、`(wait_ms DESC)` 专用索引，或用窗口预聚合。
- 用户/密钥过滤先修改变量类型（§8），保留 `(user_id, observed_at)`、`(api_key_id, observed_at)` 索引。
- 验收标准是 **实际执行 ≤2s / 扫描页数可解释**，不是“绝不 Seq Scan”；本地小表 Seq Scan 可能最优。
- Top 面板统一 `LIMIT 40`，UI 文案写“Top 40”，不要写 Top 20（需求两处不一致，见 §12-R6）。

### 7.5 清理任务

`ops_request_transfer_samples` 纳入现有 ops cleanup，逐项：
- 按 `observed_at < now() - N days` 分批 DELETE（每批 5000，带 `statement_timeout`）。
- 统计行数/最旧时间写入现有日志；失败不阻塞其他清理目标。
- 测试：造 31d 数据 → 清理后只剩 30d；`retention=0` 行为与现有语义一致。

---

## 8. 看板契约（5 张，uid 固定）

| uid | 标题 | 面板数 | 数据源 |
| --- | --- | --- | --- |
| `sub2api-v2-overview` | 01 · 总览 | 16 | Prom |
| `sub2api-v2-chain` | 02 · 链路归因 | 19 | Prom + PG |
| `sub2api-v2-nodes` | 03 · 上游节点 | 9 | Prom + PG |
| `sub2api-v2-users` | 04 · 用户/密钥 | 6 | PG |
| `sub2api-v2-host` | 05 · 宿主与应用 | 15 | Prom(node) + PG |

通用：`editable:false`、`allowUiUpdates:false`、默认 6h、浏览器时区；01/03/04/05 Prom 面板 30s 刷新，**02 看板 10s 刷新**（AC-1 的 30s 预算：15s 抓取 + 10s 刷新 + ≤2s 查询 ≈ 27s 最坏）；PG 面板 1m；每面板必须有 description（口径 + 误读风险）；缺序列用 `or vector(0)` 并显示“未采集”，**不得**用 `or vector(1)`。

### 8.1 关键面板

| 看板 | 面板 | 查询 |
| --- | --- | --- |
| 02 | 段耗时堆叠（互斥段） | `histogram_quantile(0.95, sum by (le,phase)(rate(sub2api_request_phase_seconds_bucket{phase!="headers_end"}[$__rate_interval])))`；说明：各段 P95 非同一请求分解，仅用于定位主导段；同一请求分解看 PG 样本 |
| 02 | 段均值占比 | `sum by (phase)(rate(..._sum{phase!="headers_end"}[$__rate_interval])) / clamp_min(sum(rate(..._sum{phase!="headers_end"}[$__rate_interval])),1e-9)*100`；互斥段均值可堆叠 |
| 02 | TTFT P95 | `histogram_quantile(0.95, sum by (le)(rate(sub2api_request_phase_seconds_bucket{phase="headers_end"}[$__rate_interval])))` |
| 02 | 每方向字节速率 | `sum by (direction)(rate(sub2api_transfer_bytes_total[$__rate_interval]))` |
| 02 | 字节差异 | `(sum(rate(...{direction="resp_upstream"}[10m])) - sum(rate(...{direction="resp_downstream"}[10m]))) / clamp_min(sum(rate(...{direction="resp_upstream"}[10m])),1)`；描述：差异 ≠ 截断，需结合失败根因 |
| 02 | 失败根因 | `sum by (failure_reason)(rate(sub2api_gateway_requests_total{result!="success"}[$__rate_interval]))` |
| 02 | 客户端中断 | `sum by (stage)(rate(sub2api_client_abort_total[$__rate_interval]))` |
| 02 | 慢样本表 | `SELECT observed_at,total_ms,body_read_ms,wait_ms,upstream_ms,headers_end_ms,response_ms,result,failure_reason,account_id,model FROM ops_request_transfer_samples WHERE $__timeFilter(observed_at) ORDER BY total_ms DESC LIMIT 40` |
| 03 | 账号失败率 | `sum by (account)(rate(...{result!="success"}[5m])) / clamp_min(sum by (account)(rate(...[5m])),1e-9)` |
| 03 | 账号 TTFT P95 | `histogram_quantile(0.95, sum by (le,account)(rate(sub2api_upstream_ttft_seconds_bucket[$__rate_interval])))` |
| 03 | 账号 QPS | `sum by (account)(rate(sub2api_upstream_requests_total[$__rate_interval]))` |
| 04 | 用户请求量 | `SELECT $__timeGroupAlias(created_at,'5m'), count(*) FROM usage_logs WHERE $__timeFilter(created_at) AND ($user = -1 OR user_id = $user) GROUP BY 1 ORDER BY 1` |
| 04 | 慢请求明细 | `usage_logs WHERE duration_ms > 30000 ... LIMIT 20`，不展示 IP |
| 05 | 宿主资源 | `node_*` 标准表达式；应用参数 `ops_system_metrics` 1m 聚合 |

### 8.2 变量（修正列名与类型）

```yaml
user:
  type: query
  query: "SELECT id AS __value, COALESCE(email, id::text) AS __text FROM users ORDER BY 2 LIMIT 1000"
  includeAll: true
  allValue: "-1"
  multi: false
api_key:
  type: query
  query: "SELECT id AS __value, name AS __text FROM api_keys WHERE ($user = -1 OR user_id = $user) ORDER BY 2 LIMIT 1000"
  includeAll: true
  allValue: "-1"
  multi: false
```

- `users` 主键是 `id`，不是 `user_id`。
- 过滤用 `($user = -1 OR user_id = $user)` / `($api_key = -1 OR api_key_id = $api_key)`，保持 bigint 索引可用；变量本身是数字。
- 仅对含 `user_id`/`api_key_id` 列的面板应用过滤；`ops_system_metrics`、`node_*`、Prom 面板不应用。
- 变量变化后依赖面板自动刷新；生成器需为每个面板声明 `datasource` 与可用变量。

---

## 9. 告警契约（18 条）

`deploy/monitoring/prometheus/rules/{slo,chain,infra}.yml`；`promtool check rules` + 规则单测必须通过。相对需求 17 条新增第 18 条（unknown 根因）。

| # | 规则 | 表达式 | for | 级别 |
| --- | --- | --- | --- | --- |
| 1 | `Sub2apiTargetDown` | `up{job="sub2api"} == 0` | 2m | critical |
| 2 | `Sub2apiErrorRateHigh` | `sum(rate(sub2api_http_requests_total{status_code=~"5.."}[5m])) / clamp_min(sum(rate(sub2api_http_requests_total[5m])),1e-9) > 0.01` | 10m | warning |
| 3 | `Sub2apiSlowFirstByteP95` | `histogram_quantile(0.95, sum by (le)(rate(sub2api_request_phase_seconds_bucket{phase="headers_end"}[10m]))) > 3` | 10m | warning |
| 4 | `Sub2apiUpstreamAccountFailing` | `sum by (account)(rate(sub2api_upstream_requests_total{result!="success"}[5m])) / clamp_min(sum by (account)(rate(sub2api_upstream_requests_total[5m])),1e-9) > 0.2 and sum by (account)(increase(sub2api_upstream_requests_total[5m])) > 20` | 5m | warning |
| 5 | `Sub2apiGatewaySelfError` | `sum(rate(sub2api_gateway_requests_total{result="gateway_error"}[5m])) > 0.05` | 5m | critical |
| 6 | `Sub2apiInstrumentationStalled` | 见下方两分支 | 1m | critical |
| 7 | `Sub2apiMetricCoverageLow` | `sub2api_metric_coverage_ratio{kind="account"} >= 0 and sub2api_metric_coverage_ratio{kind="account"} < 0.8` | 30m | warning |
| 8 | `Sub2apiClientAbortSpike` | `sum(rate(sub2api_client_abort_total[10m])) / clamp_min(sum(rate(sub2api_gateway_requests_total[10m])),1e-9) > 0.05` | 10m | warning |
| 9 | `Sub2apiDownstreamShortfall` | `(sum(rate(sub2api_transfer_bytes_total{direction="resp_upstream"}[10m])) - sum(rate(sub2api_transfer_bytes_total{direction="resp_downstream"}[10m]))) / clamp_min(sum(rate(sub2api_transfer_bytes_total{direction="resp_upstream"}[10m])),1) > 0.2` | 15m | warning |
| 10 | `NodeExporterDown` | `absent(up{job="node"}) or up{job="node"} == 0` | 5m | warning |
| 11 | `NodeDiskLow` | `node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"} < 0.15` | 10m | warning |
| 12 | `NodeMemoryPressure` | `node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes < 0.08` | 10m | warning |
| 13 | `NodeLoadHigh` | `node_load5 / count without(cpu)(node_cpu_seconds_total{mode="idle"}) > 2` | 15m | warning |
| 14 | `PrometheusRuleEvalFailures` | `rate(prometheus_rule_evaluation_failures_total[10m]) > 0` | 10m | warning |
| 15 | `PrometheusTsdbCompactionFailed` | `rate(prometheus_tsdb_compactions_failed_total[15m]) > 0` | 15m | warning |
| 16 | `PrometheusTargetMissing` | `absent(up{job="sub2api"}) or absent(up{job="prometheus"}) or absent(up{job="alertmanager"}) or up == 0` | 10m | warning |
| 17 | `AlertmanagerDown` | `up{job="alertmanager"} == 0` | 5m | critical |
| 18 | `Sub2apiUnknownFailureHigh` | `sum(rate(sub2api_gateway_requests_total{failure_reason="unknown"}[30m])) / clamp_min(sum(rate(sub2api_gateway_requests_total[30m])),1e-9) > 0.1 and sum(increase(sub2api_gateway_requests_total[30m])) > 100` | 30m | warning |

第 6 条（5 分钟内可发现，窗口 3m + for 1m + 抓取/求值 ≤1m；以独立 V1 网关流量为基准，按实例用集合差检测“缺失或停写”）：

```yaml
- alert: Sub2apiInstrumentationStalled
  expr: |
    (
      sum by (instance) (increase(sub2api_http_requests_total{
        route=~"/v1/messages|/v1/chat/completions|/chat/completions|/v1/responses|/antigravity/.*|/v1beta/.*"
      }[3m])) > 0
    )
    unless on (instance)
    (
      sum by (instance) (increase(sub2api_request_phase_seconds_count[3m])) > 0
    )
  for: 1m
```

- 左边 = 有 V1 网关流量的实例；右边 = phase 有增长的实例。差集 = 有流量但 phase 缺失**或**停写的实例，同时覆盖无埋点镜像与埋点停写，且不会因其他健康实例而掩盖坏实例。
- 若 phase 序列从未出现，右边为空，左边全部保留 → fire；若某实例 phase 存在但不增长，该实例不出现在右边 → fire。
- 健康空闲（V1 流量=0）时左边为空 → 不 fire；健康慢流式 phase 有增长 → 不 fire。
- `promtool test rules` 覆盖：全部缺失、单实例缺失（其余健康）、单实例停写、空闲、正常慢流式、多实例混合。
- route 白名单需随 `routes/gateway.go` 维护，写进规则注释。

投递：`alertmanager.yml` 邮件 SMTP 由 `monitoring.env` 注入，收件人 `zsun40226@gmail.com`，`send_resolved: true`；critical 1h、warning 4h 重复。

---

## 10. 配置契约

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `MONITORING_METRICS_ENABLED` | `true`（overlay） | V1 端点，V2 前置条件 |
| `MONITORING_METRICS_BIND` | `0.0.0.0:9091` | 不变 |
| `MONITORING_OPS_METRICS_ENABLED` | `false` | V2 埋点开关；要求 `METRICS_ENABLED=true`，否则启动失败 |
| `MONITORING_OPS_SAMPLES_ENABLED` | `false` | PG 样本写；要求 ops 指标开启 |
| `OPS_SAMPLE_RETENTION_DAYS` | `30` | 样本保留；拒绝负数，0 为显式 TRUNCATE 语义 |
| `GRAFANA_PG_PASSWORD` | 无 | 只读角色密码 |
| `PROM_RETENTION_TIME` / `SIZE` | `90d` / `8GB` | V2 |
| `PROM_SCRAPE_INTERVAL` | `15s` | V2 |
| `ALERT_SMTP_*` | 无 | alertmanager |

“两个功能开关 + 一个保留天数配置”，依赖关系是单向的；非法组合 fail-fast，不允许静默不生效。

---

## 11. 部署与验收契约（修正冷启动断言）

### 11.1 构建

- tag `sun/sub2api:0.0.5-v2mon`；禁止复用 0.0.4。
- 二进制字符串检查只作为**辅助**（`grep -c 'sub2api_request_phase_seconds' ./main`），不能证明运行态。
- 序列断言改为**烟测**：部署后发一条带唯一 `request_id` 的受控请求（100% 采样保证落表），然后：
  1. `curl /metrics | grep` 对应指标有值（具体 phase/account，而非只数行数）；
  2. PG 按该 `request_id` 查到本次部署后的行；
  3. `sub2api_metric_coverage_ratio{kind="request"} > 0`。
- 无流量环境用显式烟测脚本，不以 `count(*) > 0` 判断（旧行会误判）。

### 11.2 部署后断言

```sh
# 受控请求
REQ_ID="v2smoke-$(date +%s)"
curl -s -H "x-request-id: $REQ_ID" ... /v1/messages ...
docker exec sub2api-postgres psql -U sub2api -d sub2api -tAc \
  "select count(*) from ops_request_transfer_samples where request_id='$REQ_ID' and observed_at > now() - interval '5 minutes'"
```

必须 =1；失败立即回滚。另查 M-9 四个 kind 非 -1、`kind="request" >= 0.9`。

### 11.3 回滚

自动打 `sun/sub2api:rollback-*`；回滚后 V1 端点正常、样本表停写、看板显示“埋点未启用”而非报错。

---

## 12. 对需求的修订请求（需签字）

| # | 需求原文 | 建议修订 | 理由 |
| --- | --- | --- | --- |
| R1 | `headers_end` 是 `upstream` 的子区间 | `headers_end` 与 `upstream` 重叠（TTFT 包含头延迟） | 时间轴方向写反，见 §2.3 |
| R2 | failure_reason 12 类 | 新增第 13 类 `upstream_server_error`（500/502/503/504） | 否则上游 5xx 全落 unknown，`unknown<10%` 无法保证 |
| R3 | 指标 M-1~M-9 | 新增 M-10 `gateway_requests_total`、M-11 `upstream_ttft_seconds` | 请求级根因/断流分母/账号 P95 没有数据源 |
| R4 | M-5 “同上（每次尝试）” | 保持每次 attempt，v1 的“每请求”是误改 | 对齐原文 |
| R5 | 序列上界 105/55/940 | 修正为 112/65/1406（含 Inf/sum/count、13 类 reason + 成功空串与新增族） | client_golang 实际口径 |
| R6 | Top 20 / Top 40 两处不一致 | 统一 Top 40 | §5.3 与 §13.2.1 冲突 |
| R7 | finish = `usage_log` 落库完成 | finish = usage 任务提交完成；DB 延迟另由 servertiming 观测 | 异步写入下无法在不延长请求生命周期的前提下测 DB 提交 |
| R8 | 告警 17 条 | 18 条（新增 unknown 根因） | 需求自身要求 unknown<10%，但没有对应告警 |
| R9 | 埋点覆盖“所有请求” | 明确 P0/P1/P2 路径矩阵与排除项 | 原生 OpenAI 等路径 v1 漏掉 |
| R10 | “约 940 序列” | 单实例 V2 1406；整机 head 另算 | 不能只按 V2 指标证明 <8000 |
| R11 | 全局刷新 30s | 02 看板 10s，其余 30s | 15s 抓取 + 30s 刷新最坏 45s，无法满足 AC-1 的 30s |

---

## 13. 待决策项（修订后）

| # | 决策 | 当前结论 | 仍需确认 |
| --- | --- | --- | --- |
| D1 | P1 路径是否首发 | 首发包含 Antigravity/Gemini/Bedrock（+2d） | 若裁掉，AC-1/AC-5 必须限定 P0 流量 |
| D2 | Recorder 生命周期 | 入口中间件建立，Finalize 在 Recovery 之后 | 无 |
| D3 | finish 口径 | 任务提交完成，非 DB 提交 | 需求方是否接受 R7 |
| D4 | 样本写入比例 | 100%（当前量级可承受） | 峰值 QPS 未知，需压测确认 |
| D5 | 上游 5xx | 新增 `upstream_server_error` | 需求方是否接受 R2 |
| D6 | headers_end 同族 | 保留同族，严格排除堆叠 | 无 |
| D7 | 开关 | 两个功能开关 + 保留天数 | 无 |

---

## 附录 A：指标一览

```
sub2api_request_phase_seconds{phase}                        histogram  s
sub2api_transfer_bytes_total{direction}                     counter    byte
sub2api_request_body_bytes{endpoint_class}                  histogram  byte
sub2api_upstream_requests_total{account,result,failure_reason} counter 次
sub2api_model_requests_total{model_class,result}            counter    次
sub2api_attempts_total{outcome}                             counter    次
sub2api_account_switches_total                              counter    次
sub2api_client_abort_total{stage}                           counter    次
sub2api_metric_coverage_ratio{kind}                         gauge      0~1 / -1
sub2api_gateway_requests_total{result,failure_reason}       counter    次
sub2api_upstream_ttft_seconds{account}                      histogram  s
```

## 附录 B：v1→v2 修订索引

| Codex 条目 | 修订位置 |
| --- | --- |
| 1 时间轴 | §2.1–2.3、§5.1 |
| 2 入口/body_read | §3.2–3.3 |
| 3 Recorder/panic | §3.3、§3.2-3 |
| 4 原生 OpenAI/P1 | §3.5 |
| 5 attempt 边界 | §1.3、§3.4 |
| 6 finish | §2.4、§5.4 |
| 7 failure 输入/5xx | §3.3 FailureEvidence、§4.4、§12-R2 |
| 8 序列预算/账号上限 | §3.6、§4.1 |
| 9 unknown 语义 | §5.4、§5.10 |
| 10 断流告警 | §9 第 6 条 |
| 11 覆盖率 | §5.9 |
| 12 字节语义 | §5.2 |
| 13 账号 P95 | §5.11、§8.1 |
| 14 变量 SQL | §8.2 |
| 15 只读权限 | §7.1 |
| 16 迁移/usage 字段 | §7.2–7.3 |
| 17 索引验收 | §7.4 |
| 18 采样/清理 | §7.3、§7.5 |
| 19 冷启动断言 | §11.1 |
| 20 告警表达式 | §9 |
| 21 AC 造数 | 开发计划 §6 |
| 22 基线/工期/内存 | 开发计划 §2、§4、§6 |
