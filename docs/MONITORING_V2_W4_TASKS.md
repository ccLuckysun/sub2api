# Sub2API 观测看板 V2 · W4（M3 迁移）任务卡

| 项 | 内容 |
| --- | --- |
| 执行方 | herdr `t14`（执行2） |
| 契约 | `docs/MONITORING_V2_API.md` §7.2 / §7.3 / §7.4 |
| 计划 | `docs/MONITORING_V2_DEV_PLAN.md` §4 M3（3.1、3.4） |
| 边界 | 本卡只做 SQL 迁移与本地验证；**不改 ent/code、不改运行中的 dev 库、不 commit** |

## W4a1 任务卡（迁移）

### 目标

新增两个纯增量迁移，为后续 sample repo / usage 字段写入做准备。

### 文件范围（只允许新增）

- `backend/migrations/<下一个可用编号>_ops_v2_samples.sql`
- `backend/migrations/<下一个可用编号+1>_add_usage_logs_v2_fields.sql`

不得修改既有迁移、`backend/ent/**`、`backend/internal/**`、`docs/**`。

### 契约

1. `ops_request_transfer_samples` 按 API §7.3：`CREATE TABLE IF NOT EXISTS`，`observed_at TIMESTAMPTZ NOT NULL`、`request_id TEXT NOT NULL`（均无虚构默认，由 writer 提供）、`created_at TIMESTAMPTZ NOT NULL DEFAULT now()`，其余列可空；主键 `BIGSERIAL`。`request_id` 的非空串由应用层保证（API §7.3/计划 M3.3）；可加 `CHECK (request_id <> '')` 作为加固，但必须在报告注明。
2. 索引至少：`(observed_at DESC)`、`(user_id, observed_at)`、`(api_key_id, observed_at)`；如已有查询证据不足，保留这三个并在报告说明。
3. `usage_logs` 仅追加 `body_bytes BIGINT NULL`、`gateway_latency_ms BIGINT NULL`（或 INT，需与 API 一致），使用 `ADD COLUMN IF NOT EXISTS`；不改既有列/约束/索引。
4. 迁移必须可重复执行（幂等）；不得使用 `DROP`、`TRUNCATE`、`DELETE` 或改写既有数据。
5. 编号遵循 `backend/migrations/README.md` 的规则，检查当前最大编号与同名异内容风险；不覆盖既有文件。

### 验收

- 用一次性 PostgreSQL 18 容器（不要用 `sub2api-postgres-dev`）创建临时库；
- 按仓库迁移器顺序应用这两个文件，**再执行一次**验证幂等；
- 查询 `information_schema.columns` 与 `pg_indexes`，贴出列/索引清单；
- 展示 `checksum`（如仓库脚本提供）与文件编号；
- 报告：文件、编号、列/索引、幂等结果、未验证项。

## W4a2a 任务卡（usage_logs ent 字段与读写映射）

### 目标

在 W4a1 的迁移之上，把 `usage_logs.body_bytes` / `gateway_latency_ms` 接入 ent schema、生成代码、插入映射与查询 DTO；本卡不接 Recorder 取值、不做 sample writer。

### 文件范围

- `backend/ent/schema/usage_log.go`
- `backend/ent/**`（仅 `go generate ./ent` 产生的文件）
- `backend/internal/repository/usage_log_repo_insert.go`
- `backend/internal/repository/usage_log_create_result.go`（或等价 DTO）
- 相关 usage 查询/DTO 与 `_test.go`
- 不得改迁移 251/252、`gateway_forward.go`、`http_upstream.go`、`monitoring/**`

### 契约

1. 两字段均为 optional/nillable：`body_bytes` 为 int64，`gateway_latency_ms` 为 int64（与迁移 BIGINT 一致）。
2. 插入路径能接收并写入这两个字段；未提供时写 NULL，不写 0 冒充缺失。
3. 查询/DTO 能读取并返回 NULL/值；历史行保持可读。
4. 不改既有 usage 计费/去重语义；不改变现有插入字段顺序或事务边界。
5. ent 生成代码由 `go generate ./ent` 实际生成，不手改生成文件。

### 验收

- `go generate ./ent`（Docker）后 `go build ./...`/定向 `go test ./internal/repository/... ./internal/service/...` 通过。
- 测试：插入带两字段/不带两字段；查询 NULL 与值；历史行兼容；迁移后新列存在。
- 报告改动文件、生成命令、插入/查询断言；不 commit。

## W4a2b 任务卡（sample 异步 writer + cleanup）

### 目标

在迁移 251 之上实现 `ops_request_transfer_samples` 的 100% 异步写入与 30d 清理；本卡不接 Recorder 发射（W4a2c）。

### 文件范围（允许）

- 新增 `backend/internal/monitoring/ops_sample.go`（writer 输入结构，不含 Recorder 发射）
- 新增 `backend/internal/repository/ops_sample_repo.go`（批量插入）
- 新增 `backend/internal/service/ops_sample_writer.go`（队列/批量/生命周期）
- `backend/internal/service/ops_cleanup*.go` 或等价 cleanup targets（新增 sample 目标）
- `backend/internal/monitoring/wire.go` / `backend/cmd/server/wire.go` / `wire_gen.go`（writer 生命周期）
- 对应 `_test.go`
- 不得改 `gateway_forward.go`、`http_upstream.go`、W2a2c 三个兼容 service、迁移 251/252

### 契约

1. 队列上限 8192；批量 500 行或 1s 先到者；队列满丢弃并日志告警，**绝不阻塞请求**。
2. 写入字段与迁移 251 的 40 列一致；`observed_at`/`request_id` 由输入提供，request_id 空串拒绝并日志，不落空行。
3. 100% 采样（不做随机丢弃）；异步提交失败/DB 错误不得影响请求路径；writer 自身错误可观测。
4. 保留 30d：使用 `OPS_SAMPLE_RETENTION_DAYS`（`ops.sample_retention_days`，默认 30）；接入现有 cleanup targets，按 `observed_at < now()-N days` 分批 DELETE；0 为显式 TRUNCATE 语义，与现有 cleanup 一致。
5. 生命周期 Start/Stop 幂等；Stop 刷完队列或明确报告丢弃；随应用 Cleanup 停止。
6. 不改计费/usage 写入；不新增 Prometheus 族（预算已满）。

### 验收

- `go test -race ./internal/repository/... ./internal/service/... ./internal/monitoring/...`
- 测试：500 QPS 压测不阻塞请求；批 500/1s；满队列丢弃日志；空 request_id 拒绝；DB 失败不影响调用方；31d 数据清理后剩 30d；0 天 TRUNCATE；Start/Stop 反复调用。
- 用一次性 PG18 容器验证真实插入与 cleanup；不碰 dev/生产库。
- 报告：队列/批量参数、丢弃路径、cleanup 结果、测试输出；不 commit。

## W4a2c1 任务卡（Recorder → sample 发射）

### 目标

让 `OpsRecorder.Finalize` 为每个网关请求产生一条 `OpsSample` 并提交给 W4a2b 的异步 writer；生成内部 request_id。**不接 usage_logs 两字段取值（W4a2c2）。**

### 文件范围（允许）

- `backend/internal/monitoring/ops_sample.go`（OpsSample 字段/sink 接口，若 W4a2b 已定义则扩展）
- `backend/internal/monitoring/ops_recorder.go`（Finalize 发射、request_id 生成）
- `backend/internal/monitoring/ops_runtime.go` / `wire.go`（sink 注入）
- `backend/internal/server/middleware/ops.go`（依赖传递）
- `backend/cmd/server/wire.go` / `wire_gen.go`（如需要）
- 对应 `_test.go`
- 不得改 W4a2b writer 语义、W2/W2b 文件、迁移、usage service

### 契约

1. 100% 网关请求一条样本：成功、失败、0-attempt、panic、abort、bytes 不完整都发；不采样丢弃。
2. `observed_at` 用入口时间；`request_id` 取 `OpsRequestMeta.RequestID`，为空时生成稳定内部 ID `internal-<unixnano>-<seq>`，禁止空串。
3. 字段映射：meta（user/api_key/account/group/platform/model/endpoint/endpoint_class）、result/http_status/failure_reason、attempt_count/failed_attempts、7 段 ms（含 headers_end/total）、五方向 bytes/declared/status/source、node（hostname）。
4. 提交必须非阻塞；sink 为 nil/禁用时 no-op；writer 丢弃不得反向影响 Finalize/请求。
5. Finalize 仍只结算一次；样本发射在指标 Observe 之后、不得改变任何指标值。
6. 不新增 Prometheus 族；不改 M-1~M-11 语义。

### 验收

- `go test -race ./internal/monitoring/... ./internal/server/...`
- 测试：200、0-attempt 拒绝、panic、abort、bytes partial、空 request_id 生成、字段逐个映射、sink 满/延迟不阻塞、Finalize 重入不重复发射。
- 报告字段映射表与测试输出；不 commit。

## W4a2c2 任务卡（usage_logs 两字段取值接线）

### 目标

把 `usage_logs.body_bytes` / `gateway_latency_ms` 从 Recorder 请求级观测接到 usage 写入；迁移 252 与 ent/insert 映射（W4a1/W4a2a）已就绪。

### 文件范围（允许）

- `backend/internal/monitoring/ops_recorder.go`（只读 getter：body bytes、gateway latency ms）
- `backend/internal/service/usage_service.go` / usage 创建 DTO（若需要）
- `backend/internal/service/gateway_usage_billing.go`（仅 `buildRecordUsageLog` 与 DTO 两字段透传/赋值，不改计费）
- `backend/internal/service/openai_gateway_usage.go`（同上）
- `backend/internal/handler/gateway_handler.go`、`openai_gateway_handler.go`、`openai_chat_completions.go`（仅在 usage 提交前填充两字段；不改计费/重试）
- 对应 `_test.go`
- 不得改 `gateway_forward*.go`、`http_upstream.go`、W2b1/W2b2 生产文件、迁移 251/252

### 契约

1. `body_bytes` = M-2 `in_wire` 实际读取值；未观测到（非网关/禁用/未读）写 NULL，不写 0 冒充。
2. `gateway_latency_ms` = `gateway + wait + finish_at_snapshot`（毫秒）；`finish_at_snapshot = max(0, snapshot_now - t_done)`，是提交前下界，**不改变** MarkFinish/M1/sample 的最终 finish。usage 行在入队前冻结该快照；报告必须写明“usage 字段为下界，样本表/ M-1 用真实 MarkFinish”。未观测到 t_done 写 NULL。
3. 在请求 goroutine 内、usage 入队前填充；异步 worker 只消费已填充值，不依赖 ctx 里的 Recorder。
4. 不改计费/去重/usage 语义；历史行保持 NULL；新请求非空率目标 ≥95%（可在测试中统计）。
5. 字段写入复用 W4a2a 的 insert 映射；不新增列/索引。

### 验收

- `go test -race ./internal/monitoring/... ./internal/service/... ./internal/handler/...` 定向。
- 测试：正常 200/SSE 有值；0-attempt 拒绝/禁用为 NULL；显式 0 字节为 0（区别于 NULL）；异步 enqueue/sync fallback 均带值；历史行可读。
- 报告字段来源、NULL/0 语义、测试输出；不 commit。
