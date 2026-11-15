# Dashboard V2：W2.0 路由与事件接入清单

基线：`95368134a3926882ba88886a35aee7cc4042666e`，核对日期 2026-11-14。本文只描述现行链路和后续接入位置，不表示业务链路已经完成 V2 埋点。下列业务源码引用均与该 HEAD 的内容、行号逐字一致。工作树已有 M1 已审核改动；M1 入口、Recorder、响应句柄和 coverage 生命周期按任务卡 §3、§4.1、§5 使用，不把这些未提交改动冒称为 HEAD 内容。

范围：逐条对应 API §3.5；P0 全量，P1 按指定的全量或 phase/字节，P2 仅保证请求级 M-10 和实际 body 的 M-3。排除项不进入 M-10/M-9 分母。每个源码位置后括号给出其所属函数；调用点与函数定义分开引用。

## 1. 调度、ctx 与 attempt 边界

- 路由平台注册表判定为 `backend/internal/domain/platforms.go:128`（`UsesOpenAIGateway`）；路由闭包在 `backend/internal/server/routes/gateway.go:49`（`RegisterGatewayRoutes`） 调用它，不应把“OpenAI 兼容协议”直接等同于 Claude 转换服务。Composite 的实际平台在 `backend/internal/server/routes/gateway.go:536`（`getGroupPlatform`） 读取解析结果。
- 每次应用层真实转发尝试，在账号/利润准入/slot 已成功、确实准备发送 HTTP 时创建一次 attempt；仅校验、策略拒绝、利润否决、选号或本地拦截成功不能虚构上游 attempt。下表的 handler 调用边界是组合与传递句柄的最小切口；service 内发生本地短路的分支必须先过滤，或延迟至第一次实际 HTTP 调用前才创建。
- attempt 覆盖同账号一次应用转发中的签名修复、协议 fallback、物理重发；应用 failover 循环下一次调用才创建新 attempt。HTTP 层只更新已存在句柄，不创建逻辑 attempt。所有错误返回和带部分 usage 的错误返回都需结算一次，不能把 `result != nil` 当成成功。
- 在 M1 已审入口覆盖的路径中，后续接入可从 `c.Request.Context()` 取得 Recorder；下面列出的现行 `requestCtx` 派生仍保留 context values。`backend/internal/service/gateway_usage_billing.go:592`（`detachUpstreamContext`） → `backend/internal/pkg/httputil/upstream_drain.go:21`（`DetachUpstreamContext`） 使用 `WithoutCancel` 保留 values；`backend/internal/pkg/httputil/upstream_drain.go:39`（`NewUpstreamAttemptContext`） 再派生取消/drain 生命周期。不可为了埋点替换为全新的 Background context。
- **usage worker ctx 不保留整个 Recorder**：`backend/internal/handler/openai_gateway_handler.go:248`（`usageRecordContext`） 只复制两种 request ID；`backend/internal/handler/openai_gateway_handler.go:267`（`wrapUsageRecordTaskContext`） 还接管余额预留引用。finish 必须在请求 goroutine 中、提交函数返回时记录；不在 worker 内找 Recorder、不把异步 DB 失败改成请求 `gateway_db`。

## 2. P0 路由 → handler → service → usage 矩阵

所有 P0 行均需 body、wait、发送/响应、实际字节、切换、usage 提交的全量观测；不同入口共享的事件位置见 §4。以下每行的 ctx“可用”仅表示传递通道现存，V2 attempt/物理响应句柄仍需接入。

| ID / 路由、方法 / 平台条件 | handler 与 service 及 attempt 创建切口 | 响应消费 / usage 提交 / ctx |
| --- | --- | --- |
| P0-A：POST `/v1/messages`；非 OpenAI 网关平台，常规 Claude/Anthropic，非 passthrough、非 Bedrock | 路由 `backend/internal/server/routes/gateway.go:192`（`RegisterGatewayRoutes`）; handler `backend/internal/handler/gateway_handler.go:125`（`GatewayHandler.Messages`）；在 `backend/internal/handler/gateway_handler.go:923`（`GatewayHandler.Messages`） 的真实转发分支传递一个 attempt；service `backend/internal/service/gateway_forward.go:91`（`GatewayService.Forward`），过滤其 web-search 本地短路。 | SSE `backend/internal/service/gateway_upstream_response.go:709`（`GatewayService.handleStreamingResponse`）；非流式 `backend/internal/service/gateway_upstream_response.go:1407`（`GatewayService.handleNonStreamingResponse`）。usage `backend/internal/handler/gateway_handler.go:967`（`GatewayHandler.Messages`） → Gateway RecordUsage；ctx：`requestCtx` 可用。 |
| P0-B：POST `/v1/messages`；`IsAnthropicAPIKeyPassthroughEnabled()`，是 A 的互斥分叉 | 仍由 Messages → GatewayService.Forward 分派：`backend/internal/service/gateway_forward.go:140`（`GatewayService.Forward`）；具体 `backend/internal/service/gateway_anthropic_passthrough.go:56`（`GatewayService.forwardAnthropicAPIKeyPassthroughWithInput`）。与 A 共用一个 attempt，不能在下级入口重复创建。 | SSE `backend/internal/service/gateway_anthropic_passthrough.go:373`（`GatewayService.handleStreamingResponseAnthropicAPIKeyPassthrough`）；JSON `backend/internal/service/gateway_anthropic_passthrough.go:857`（`GatewayService.handleNonStreamingResponseAnthropicAPIKeyPassthrough`）。返回 A 的 `submitForwardUsage` 闭包；ctx 已显式传递，构造请求 `backend/internal/service/gateway_anthropic_passthrough.go:332`（`GatewayService.buildUpstreamRequestAnthropicAPIKeyPassthrough`）。 |
| P0-C：POST `/v1/chat/completions`；非 OpenAI 网关平台的 Claude/兼容转换 | 路由 `backend/internal/server/routes/gateway.go:233`（`RegisterGatewayRoutes`）; handler `backend/internal/handler/gateway_handler_chat_completions.go:23`（`GatewayHandler.ChatCompletions`）；真实调用点 `backend/internal/handler/gateway_handler_chat_completions.go:317`（`GatewayHandler.ChatCompletions`）；service `backend/internal/service/gateway_forward_as_chat_completions.go:29`（`GatewayService.ForwardAsChatCompletions`）。在每次循环的实际调用分支创建一个 attempt。 | `backend/internal/service/gateway_forward_as_chat_completions.go:383`（`GatewayService.handleCCStreamingFromAnthropic`） / `backend/internal/service/gateway_forward_as_chat_completions.go:231`（`GatewayService.handleCCBufferedFromAnthropic`）；usage `backend/internal/handler/gateway_handler_chat_completions.go:337`（`GatewayHandler.ChatCompletions`）。ctx：`c.Request.Context()` 可用；Gemini / Antigravity 分叉见 §3。 |
| P0-D：POST `/v1/responses`、`/v1/responses/*subpath`；非 OpenAI 网关平台的兼容转换 | 路由 `backend/internal/server/routes/gateway.go:214`（`RegisterGatewayRoutes`）、`backend/internal/server/routes/gateway.go:221`（`RegisterGatewayRoutes`）; handler `backend/internal/handler/gateway_handler_responses.go:23`（`GatewayHandler.Responses`）；调用 `backend/internal/handler/gateway_handler_responses.go:299`（`GatewayHandler.Responses`）；service `backend/internal/service/gateway_forward_as_responses.go:31`（`GatewayService.ForwardAsResponses`）。每次应用循环真实调用一 attempt，受 guard 限制的子路径不可另造一次。 | `backend/internal/service/gateway_forward_as_responses.go:548`（`GatewayService.handleResponsesStreamingResponse`） / `backend/internal/service/gateway_forward_as_responses.go:379`（`GatewayService.handleResponsesBufferedStreamingResponse`）；usage `backend/internal/handler/gateway_handler_responses.go:319`（`GatewayHandler.Responses`）；ctx：派生 `requestCtx` 可用。 |
| P0-E：POST `/v1/responses`、可转发子路径；OpenAI/Grok/平台注册表指定的多协议供应商 | 同一路由的 OpenAI 分支；handler `backend/internal/handler/openai_gateway_handler.go:392`（`OpenAIGatewayHandler.Responses`），在 `backend/internal/handler/openai_gateway_handler.go:826`（`OpenAIGatewayHandler.Responses`） 创建/传入当前逻辑 attempt；这里字段 `gatewayService` 的类型是 **OpenAIGatewayService**，service `backend/internal/service/openai_gateway_forward.go:22`（`OpenAIGatewayService.Forward`）。 | 常规 `backend/internal/service/openai_gateway_response_handling.go:51`（`OpenAIGatewayService.handleStreamingResponseWithReasoning`） / `backend/internal/service/openai_gateway_response_handling.go:1652`（`OpenAIGatewayService.handleNonStreamingResponse`）；passthrough 消费分叉见 §5。usage `backend/internal/handler/openai_gateway_handler.go:862`（`OpenAIGatewayHandler.Responses`）；ctx：`c.Request.Context()` 可用。 |
| P0-F：POST `/v1/chat/completions`、`/chat/completions`；OpenAI 网关平台，原生 OpenAI 独立链 | v1 路由见 C；无 v1 别名 `backend/internal/server/routes/gateway.go:401`（`RegisterGatewayRoutes`）; handler `backend/internal/handler/openai_chat_completions.go:23`（`OpenAIGatewayHandler.ChatCompletions`）；实际调用 `backend/internal/handler/openai_chat_completions.go:262`（`OpenAIGatewayHandler.ChatCompletions`） → **OpenAIGatewayService** `backend/internal/service/openai_gateway_chat_completions.go:56`（`OpenAIGatewayService.ForwardAsChatCompletions`），不是 C 的同名方法，也不保证经过 E。 | Responses 桥接主分支消费于 `backend/internal/service/openai_gateway_chat_completions.go:827`（`OpenAIGatewayService.handleChatStreamingResponse`） / `backend/internal/service/openai_gateway_chat_completions.go:668`（`OpenAIGatewayService.handleChatBufferedStreamingResponse`）；raw/Grok/native Anthropic 分叉见 §5。usage `backend/internal/handler/openai_chat_completions.go:295`（`OpenAIGatewayHandler.ChatCompletions`）；ctx：显式 `c.Request.Context()` 可用。 |
| P0-G：POST `/v1/messages`；OpenAI 网关平台的 Anthropic 入站桥接（A 的另一条互斥路由） | 路由与 A 相同但进入 `backend/internal/handler/openai_gateway_handler.go:1185`（`OpenAIGatewayHandler.Messages`）；调用 `backend/internal/handler/openai_gateway_handler.go:1423`（`OpenAIGatewayHandler.Messages`） → `backend/internal/service/openai_gateway_messages.go:28`（`OpenAIGatewayService.ForwardAsAnthropic`）。在同一次 handler 循环的实际转发处建立 attempt，不能遗漏此 `/messages` 平台分叉。 | 该方法按上游协议桥接；Responses 桥接分支消费 `backend/internal/service/openai_gateway_messages.go:1009`（`OpenAIGatewayService.handleAnthropicStreamingResponse`） / `backend/internal/service/openai_gateway_messages.go:604`（`OpenAIGatewayService.handleAnthropicBufferedStreamingResponse`）；其 native Anthropic / raw Chat 分叉需各自锁定真实消费响应；usage `backend/internal/handler/openai_gateway_handler.go:1457`（`OpenAIGatewayHandler.Messages`）。ctx：显式传递。 |

## 3. P1、P2 与排除项逐项清单

| ID / 范围与路由 | 调用链、消费与 usage | 接入边界 / ctx |
| --- | --- | --- |
| P1-A：POST `/antigravity/v1/messages`；强制 Antigravity（全量） | `backend/internal/server/routes/gateway.go:504`（`RegisterGatewayRoutes`） → GatewayHandler.Messages → `backend/internal/handler/gateway_handler.go:921`（`GatewayHandler.Messages`） → `backend/internal/service/antigravity_gateway_claude.go:30`（`AntigravityGatewayService.Forward`）；消费 `backend/internal/service/antigravity_gateway_streaming.go:988`（`AntigravityGatewayService.handleClaudeStreamingResponse`） / `backend/internal/service/antigravity_gateway_streaming.go:959`（`AntigravityGatewayService.handleClaudeStreamToNonStreaming`）；usage 同 P0-A。 | 在应用 Forward 分支创建；内部 retry loop 不加逻辑次数。ctx 已有；API-key 账号可能转入 GatewayService，按实际账号分支处理。 |
| P1-B：POST `/v1beta/models/*modelAction`；Gemini 原生（phase + 字节） | `backend/internal/server/routes/gateway.go:351`（`RegisterGatewayRoutes`） → `backend/internal/handler/gemini_v1beta_handler.go:323`（`GatewayHandler.GeminiV1BetaModels`） → `backend/internal/handler/gemini_v1beta_handler.go:775`（`GatewayHandler.GeminiV1BetaModels`） → `backend/internal/service/gemini_messages_compat_service.go:1095`（`GeminiMessagesCompatService.ForwardNative`）；消费 `backend/internal/service/gemini_messages_compat_service.go:2878`（`GeminiMessagesCompatService.handleNativeStreamingResponse`） / `backend/internal/service/gemini_messages_compat_service.go:2830`（`GeminiMessagesCompatService.handleNativeNonStreamingResponse`）；usage `backend/internal/handler/gemini_v1beta_handler.go:834`（`GatewayHandler.GeminiV1BetaModels`）。 | 账号平台 Antigravity 时调用 ForwardGemini；`/antigravity/v1beta/models/*modelAction` 同 handler。创建边界在两者实际调用前，ctx requestCtx 已有；models GET 不属于本行。 |
| P1-C：Bedrock，无单独公共 URL；来自 `/v1/messages` 的账号类型分叉（phase + 字节） | `backend/internal/service/gateway_forward.go:151`（`GatewayService.Forward`） → `backend/internal/service/gateway_bedrock.go:59`（`GatewayService.forwardBedrock`）；HTTP `backend/internal/service/gateway_bedrock.go:205`（`GatewayService.executeBedrockUpstream`）；消费 `backend/internal/service/bedrock_stream.go:25`（`GatewayService.handleBedrockStreamingResponse`） / `backend/internal/service/gateway_bedrock.go:387`（`GatewayService.handleBedrockNonStreamingResponse`）；usage 回到 P0-A。 | 继承调用方 attempt，不在 Bedrock 内重复创建；ctx 保留，EventStream 帧解码不同于 SSE。 |
| P1-D：P0 URL 内 Gemini / Antigravity 协议转换分叉 | Messages 的 Gemini 调用 `backend/internal/handler/gateway_handler.go:514`（`GatewayHandler.Messages`） → `backend/internal/service/gemini_messages_compat_service.go:533`（`GeminiMessagesCompatService.Forward`）；Chat 的 Gemini 调用 `backend/internal/handler/gateway_handler_chat_completions.go:305`（`GatewayHandler.ChatCompletions`） → `backend/internal/service/gemini_chat_completions_compat_service.go:24`（`GeminiMessagesCompatService.ForwardAsChatCompletions`）。Antigravity Chat/Responses `backend/internal/service/antigravity_gateway_compat.go:57`（`AntigravityGatewayService.ForwardAsChatCompletions`） / `backend/internal/service/antigravity_gateway_compat.go:104`（`AntigravityGatewayService.ForwardAsResponses`）。 | usage 在原 handler：Gemini Messages `backend/internal/handler/gateway_handler.go:603`（`GatewayHandler.Messages`），Chat/Responses 回 P0-C/D；ctx 均可用。不能因为 URL 是 P0 就假设 service 一定是 GatewayService。 |
| P2-A：POST `/v1/images/generations`、`/edits`；OpenAI/Grok；请求结果 + 实际 body M-3 | 路由 `backend/internal/server/routes/gateway.go:253`（`RegisterGatewayRoutes`）、`backend/internal/server/routes/gateway.go:254`（`RegisterGatewayRoutes`）；OpenAI `backend/internal/handler/openai_images.go:23`（`OpenAIGatewayHandler.Images`） → `backend/internal/service/openai_images.go:682`（`OpenAIGatewayService.ForwardImages`）；Grok 经 `backend/internal/handler/grok_media.go:21`（`OpenAIGatewayHandler.GrokImages`） → `backend/internal/service/grok_media.go:648`（`OpenAIGatewayService.ForwardGrokMedia`），按结果调用 `backend/internal/handler/grok_media.go:718`（`recordGrokMediaUsage`），其中提交 `backend/internal/handler/grok_media.go:761`（`recordGrokMediaUsage`）。OpenAI 图片 usage 提交 `backend/internal/handler/openai_images.go:413`（`OpenAIGatewayHandler.Images`）。 | 本波不铺全量 attempt/TTFT；HTTP ctx 已有，但不把多图/下载 HTTP 当作文本 attempt。M-3 用 images 类及实际已读 bytes，不能凭声明值。 |
| P2-B：POST `/v1/images/generations/async`、`/edits/async`，GET `/v1/images/tasks/:task_id` | `backend/internal/server/routes/gateway.go:255`（`RegisterGatewayRoutes`） → `backend/internal/handler/image_task_handler.go:51`（`AsyncImageHandler.Submit`） → ImageTaskService.Create；`backend/internal/handler/image_task_handler.go:132`（`AsyncImageHandler.Submit`） 在 202 返回后执行 → `backend/internal/handler/image_task_handler.go:218`（`AsyncImageHandler.executeWithGateway`） → OpenAI Images / GrokImages，usage 在工作任务的图片链。 | M-10 只表示入口提交/查询请求；不延长入口 finish 到生成完成。后台 synthetic Gin context 不等于尚未 Finalize 的原 Recorder；不能借它回写入口指标。 |
| P2-C：POST/GET `/v1/images/batches`，GET items/content/download，POST cancel、DELETE record/outputs | 路由 `backend/internal/server/routes/gateway.go:258`（`RegisterGatewayRoutes`） 起；`backend/internal/handler/batch_image_handler.go:31`（`BatchImageHandler.Submit`） → `backend/internal/service/batch_image_public.go:200`（`BatchImagePublicService.Submit`）；后台 `backend/internal/service/batch_image_processor.go:59`（`BatchImageProviderProcessor.Process`）、`indexAndSettle`；计费 `backend/internal/service/batch_image_settlement.go:76`（`BatchImageSettlementService.Settle`），独立 usage 写入 `backend/internal/service/batch_image_settlement.go:252`（`BatchImageSettlementService.recordUsageLog`）。 | 请求级观测，不把异步 job 生命周期算作入口 attempt。入站 ctx 存在，worker ctx 独立；批量 models 端点仍排除。JSON binding 也需观测真实读取，不改绑定行为。 |
| P2-D：GET `/v1/responses`（WS），以及别名/直连 WS | `backend/internal/server/routes/gateway.go:229`（`RegisterGatewayRoutes`） → `backend/internal/handler/openai_gateway_handler.go:2466`（`OpenAIGatewayHandler.ResponsesWebSocket`） → `backend/internal/handler/openai_gateway_handler.go:3277`（`OpenAIGatewayHandler.ResponsesWebSocket`） → `backend/internal/service/openai_ws_forwarder_ingress.go:65`（`OpenAIGatewayService.ProxyResponsesWebSocketFromClient`）；per-turn usage `backend/internal/handler/openai_gateway_handler.go:3227`（`OpenAIGatewayHandler.ResponsesWebSocket`）。 | 只保证连接请求 M-10；不把 WS frame/per-turn usage 强行当 HTTP attempt/TTFT。HTTP ctx 可传但长连接/turn 已有独立定价与取消边界。 |
| 排除 E1：POST count_tokens，含 `/v1/messages/count_tokens`、`/messages/count_tokens`、Antigravity 同类 | `backend/internal/server/routes/gateway.go:203`（`RegisterGatewayRoutes`）、`backend/internal/server/routes/gateway.go:386`（`RegisterGatewayRoutes`）、`backend/internal/server/routes/gateway.go:505`（`RegisterGatewayRoutes`）；GatewayHandler.CountTokens 或 OpenAI CountTokens / Grok 本地估算。 | 按裁定不是业务生成请求；即使发生 HTTP 调用，也不建立网关 Recorder、不进入 M-10/M-9，不在其 service 内无条件创建 attempt。 |
| 排除 E2：GET models 与单模型、manifest、batch models、Gemini list/get | `backend/internal/server/routes/gateway.go:207`（`RegisterGatewayRoutes`）、`backend/internal/server/routes/gateway.go:209`（`RegisterGatewayRoutes`）、`backend/internal/server/routes/gateway.go:348`（`RegisterGatewayRoutes`）、`backend/internal/server/routes/gateway.go:349`（`RegisterGatewayRoutes`）、`backend/internal/server/routes/gateway.go:260`（`RegisterGatewayRoutes`）；GatewayHandler.Models / CodexModels / GeminiV1BetaListModels / GetModel 等。 | 发现/配置类请求不进入生成请求分母。Gemini POST `/models/*modelAction` 的生成动作与 GET 列表必须区分，不能按 models 字符串全部排除。 |
| 排除 E3：余额 / usage；GET `/v1/usage`、`/v1/sub2api/billing` 及管理余额查询 | usage `backend/internal/server/routes/gateway.go:210`（`RegisterGatewayRoutes`） → `backend/internal/handler/gateway_handler.go:1592`（`GatewayHandler.Usage`）；余额 `backend/internal/server/routes/gateway.go:186`（`RegisterGatewayRoutes`） → `backend/internal/handler/gateway_key_billing.go:35`（`GatewayHandler.KeyBillingInfo`），仅解析倍率并返回配置/额度摘要。 | 用量/余额查询与生成结果不是同一请求集合；无生成 attempt / usage 提交，不进入 M-10/M-9；现有业务查询逻辑保持不动。 |
| 排除 E4：账号/代理探测；POST `/api/v1/admin/accounts/:id/test`、`/api/v1/admin/proxies/:id/test` | 账号路由 `backend/internal/server/routes/admin.go:398`（`registerAccountRoutes`） → `backend/internal/handler/admin/account_handler.go:1295`（`AccountHandler.Test`） → `backend/internal/service/account_test_service.go:361`（`AccountTestService.TestAccountConnection`）；测试共享出口 `backend/internal/service/openai_plugin_transport.go:23`（`AccountTestService.doOpenAIAccountTestUpstream`）；代理路由 `backend/internal/server/routes/admin.go:535`（`registerProxyRoutes`）。 | 无业务 usage 提交；即使复用 HTTP/插件也不创建网关 Recorder / attempt。只能更新既有探测统计，不污染 M-10/M-9。 |
| 排除 E5：健康检查、管理与管理余额接口 | 健康 `backend/internal/server/routes/common.go:12`（`RegisterCommonRoutes`）；管理组 `backend/internal/server/routes/admin.go:26`（`RegisterAdminRoutes`）；余额 `backend/internal/server/routes/admin.go:519`（`registerCNProviderRoutes`） → `backend/internal/handler/admin/cn_provider_handler.go:53`（`CNProviderHandler.QueryBalance`）。 | 分别为存活响应/后台管理/额度查询，不代表网关生成请求，无此波次的 usage 提交；不建立 Recorder，不进入 M-10/M-9。 |

## 4. 事件接入点与消费边界

下表是未来补点位置，不是已埋点声明。所有包装只累计实际返回的 n，原样返回 `(n, err)` 并保留 Close/Flush/Hijack 语义。

| 事件 | 现行源码证据（位置与所属函数） | 接入动作与约束 |
| --- | --- | --- |
| body：实际首次读取、错误与预读 | 公共入口 `backend/internal/pkg/httputil/body.go:69`（`ReadRequestBodyWithPrealloc`），实际循环 `backend/internal/pkg/httputil/body.go:169`（`readRequestBodyChunksWithBudget`）；lenient `backend/internal/handler/request_body_limit.go:41`（`readLenientJSONRequestBodyWithPrealloc`）；失败 `backend/internal/handler/request_body_limit.go:20`（`handleRequestBodyReadError`）；前置预读 `backend/internal/server/middleware/group_model_allowlist.go:113`（`groupModelAllowlistModelsFromBody`）、`backend/internal/server/routes/gateway.go:564`（`compositeTargetPlatformMiddleware`）。 | 在原始 Read 累计 in_wire；错误也保留已读部分，首次 body 完成/失败只 MarkBodyRead 一次。预读后的 PrereadBody / reset 不重复计数；解码前 wire 与解码后 JSON 分开，Content-Length 仅 declared。 |
| body：P0/P1 handler 入口 | Messages `backend/internal/handler/gateway_handler.go:148`（`GatewayHandler.Messages`）；兼容 Chat `backend/internal/handler/gateway_handler_chat_completions.go:48`（`GatewayHandler.ChatCompletions`）；兼容 Responses `backend/internal/handler/gateway_handler_responses.go:48`（`GatewayHandler.Responses`）；OpenAI Responses `backend/internal/handler/openai_gateway_handler.go:426`（`OpenAIGatewayHandler.Responses`）、Messages `backend/internal/handler/openai_gateway_handler.go:1221`（`OpenAIGatewayHandler.Messages`）；独立 Chat `backend/internal/handler/openai_chat_completions.go:52`（`OpenAIGatewayHandler.ChatCompletions`）；Gemini `backend/internal/handler/gemini_v1beta_handler.go:375`（`GatewayHandler.GeminiV1BetaModels`）。 | 所有成功、空 body、解析拒绝、读失败/客户端取消分支都保留终止证据；读超时不能仅因为 ctx 被取消就当作 client_abort。 |
| slot wait | Messages 用户 `backend/internal/handler/gateway_handler.go:253`（`GatewayHandler.Messages`）、账号 `backend/internal/handler/gateway_handler.go:448`（`GatewayHandler.Messages`） / `backend/internal/handler/gateway_handler.go:791`（`GatewayHandler.Messages`）；兼容 Chat `backend/internal/handler/gateway_handler_chat_completions.go:135`（`GatewayHandler.ChatCompletions`） / `backend/internal/handler/gateway_handler_chat_completions.go:243`（`GatewayHandler.ChatCompletions`）；兼容 Responses `backend/internal/handler/gateway_handler_responses.go:139`（`GatewayHandler.Responses`） / `backend/internal/handler/gateway_handler_responses.go:240`（`GatewayHandler.Responses`）；OpenAI 共用 `backend/internal/handler/openai_gateway_handler.go:2128`（`OpenAIGatewayHandler.acquireResponsesUserSlot`） / `backend/internal/handler/openai_gateway_handler.go:2310`（`OpenAIGatewayHandler.acquireResponsesAccountSlot`）；Gemini `backend/internal/handler/gemini_v1beta_handler.go:428`（`GatewayHandler.GeminiV1BetaModels`） / `backend/internal/handler/gemini_v1beta_handler.go:704`（`GatewayHandler.GeminiV1BetaModels`）。 | 仅首次发送前的 slot 等待 AddWait；失败等待也记录实际值。调度、利润终检、重试退避、串行消息队列不得全部打包成 wait；不重复嵌套计时，超界截断由 Recorder 处理。 |
| 发送与响应头（公共出口） | `backend/internal/repository/http_upstream.go:204`（`httpUpstreamService.Do`） / `backend/internal/repository/http_upstream.go:247`（`httpUpstreamService.DoWithTLS`） → `backend/internal/repository/http_upstream.go:309`（`doUpstreamRequest`），实际调用 `backend/internal/repository/http_upstream.go:315`（`doUpstreamRequest`）；解压 `backend/internal/repository/http_upstream.go:1543`（`decompressResponseBody`）；取消由 cancelOnCloseBody 保持。 | 在真实发送前 Sent 获取物理响应句柄；拿到 headers 后 NoteHeadersFor。这里返回仅代表 headers，不能 Done。重定向/fallback 每次物理请求都须归同一个逻辑 attempt，body Write/Read 实际 n 要在原有 I/O 边界记录。 |
| 发送：服务/插件分叉 | Claude `backend/internal/service/gateway_forward.go:402`（`GatewayService.Forward`）；passthrough `backend/internal/service/gateway_anthropic_passthrough.go:112`（`GatewayService.forwardAnthropicAPIKeyPassthroughWithInput`）；CC `backend/internal/service/gateway_forward_as_chat_completions.go:138`（`GatewayService.ForwardAsChatCompletions`）；Responses `backend/internal/service/gateway_forward_as_responses.go:149`（`GatewayService.ForwardAsResponses`）；OpenAI `backend/internal/service/openai_gateway_forward.go:1091`（`OpenAIGatewayService.Forward`） / `backend/internal/service/openai_gateway_chat_completions.go:364`（`OpenAIGatewayService.forwardAsChatCompletions`） → `backend/internal/service/openai_plugin_transport.go:11`（`OpenAIGatewayService.doOpenAIUpstream`）。 | OpenAI OAuth plugin handled=true 可直接返回而不经过 repository，必须在插件/公共出口互斥路径补等价发送、headers 和 body 观测，不在两处双计。 |
| 首字节：原始 Read，不是首 token | Claude parser `backend/internal/service/gateway_upstream_response.go:775`（`GatewayService.handleStreamingResponse`），passthrough `backend/internal/service/gateway_anthropic_passthrough.go:451`（`GatewayService.handleStreamingResponseAnthropicAPIKeyPassthrough`）；OpenAI parser `backend/internal/service/openai_gateway_response_handling.go:165`（`OpenAIGatewayService.handleStreamingResponseWithReasoning`）；CC buffered `backend/internal/service/gateway_forward_as_chat_completions.go:253`（`GatewayService.handleCCBufferedFromAnthropic`），Responses buffered `backend/internal/service/gateway_forward_as_responses.go:403`（`GatewayService.handleResponsesBufferedStreamingResponse`）；共同非流式读取 `backend/internal/service/upstream_response_limit.go:49`（`ReadUpstreamResponseBody`） → `backend/internal/service/upstream_response_limit.go:34`（`readUpstreamResponseBodyLimited`）。 | 在解压后的真实响应体第一次 Read 返回 n>0 时 NoteFirstByteFor；parser/Scanner 首次完整事件和现有 FirstTokenMs 都可能晚于首字节。最终选定的响应调用 UseResponseFor；空 body/header-only 错误不虚构 TTFT。 |
| 响应体结束 / attempt Done | Claude `backend/internal/service/gateway_upstream_response.go:709`（`GatewayService.handleStreamingResponse`） / `backend/internal/service/gateway_upstream_response.go:1407`（`GatewayService.handleNonStreamingResponse`）；转换流共享 `backend/internal/service/gateway_forward_compat_stream.go:82`（`anthropicCompatStream.stop`）；独立 Chat `backend/internal/service/openai_gateway_chat_completions.go:827`（`OpenAIGatewayService.handleChatStreamingResponse`）；OpenAI `backend/internal/service/openai_gateway_response_handling.go:51`（`OpenAIGatewayService.handleStreamingResponseWithReasoning`）。 | 在真实消费 body 到 EOF/读失败/终止的边界 BodyDoneFor；应用层完成转发/决定重试时显式 Done 一次。下游断开后仍可能为 usage drain，需区分业务响应终止与实际 drain；不额外读/缓存/提前 Close，也不在 Do 返回时结算。 |
| 下游写入 | Claude `backend/internal/service/gateway_upstream_response.go:1105`（`GatewayService.handleStreamingResponse`）；passthrough `backend/internal/service/gateway_anthropic_passthrough.go:565`（`GatewayService.handleStreamingResponseAnthropicAPIKeyPassthrough`）；Responses 转换 `backend/internal/service/gateway_forward_as_responses.go:643`（`GatewayService.handleResponsesStreamingResponse`）；OpenAI buffer `backend/internal/service/openai_gateway_response_handling.go:138`（`OpenAIGatewayService.handleStreamingResponseWithReasoning`），非流式 `backend/internal/service/openai_gateway_response_handling.go:1738`（`OpenAIGatewayService.handleNonStreamingResponse`）；passthrough JSON `backend/internal/service/gateway_anthropic_passthrough.go:898`（`GatewayService.handleNonStreamingResponseAnthropicAPIKeyPassthrough`）。 | 只累计最终客户端 Writer.Write 成功返回的 n；不能按 Fprintf 输入字符串长度或尚未 flush 的 bufferedWriter 数量计真实下游字节。SSE ping/error/协议改写均有输出；包装必须保留 flush/hijack/unwrap。 |
| 账号切换 | 共享 FailoverState `backend/internal/handler/failover_loop.go:250`（`FailoverState.HandleFailoverError`）；OpenAI Responses `backend/internal/handler/openai_gateway_handler.go:975`（`OpenAIGatewayHandler.Responses`）、Messages `backend/internal/handler/openai_gateway_handler.go:1544`（`OpenAIGatewayHandler.Messages`）、独立 Chat `backend/internal/handler/openai_chat_completions.go:381`（`OpenAIGatewayHandler.ChatCompletions`）；WS 独立 `backend/internal/handler/openai_gateway_handler.go:2820`（`OpenAIGatewayHandler.ResponsesWebSocket`）。 | 在确定跨账号切换、与现有统计同一事件处 MarkSwitch；同账号重试、利润否决和物理 fallback 不能增加 M-7。共享 FailoverState 当前无统一 Recorder 参数，要传请求 ctx/句柄，不能用进程全局当前请求。 |
| usage 提交 / finish | Gateway `backend/internal/handler/gateway_handler.go:2566`（`GatewayHandler.submitUsageRecordTask`） / mandatory `backend/internal/handler/gateway_handler.go:2599`（`GatewayHandler.submitMandatoryUsageRecordTask`）；OpenAI `backend/internal/handler/openai_gateway_handler.go:3529`（`OpenAIGatewayHandler.submitOpenAIUsageRecordTask`） → regular `backend/internal/handler/openai_gateway_handler.go:3497`（`OpenAIGatewayHandler.submitUsageRecordTask`） / mandatory `backend/internal/handler/openai_gateway_handler.go:3539`（`OpenAIGatewayHandler.submitMandatoryUsageRecordTask`）；worker `backend/internal/service/usage_record_worker_pool.go:155`（`UsageRecordWorkerPool.Submit`）。 | 请求线程在完整提交函数返回时 MarkFinish，涵盖 Enqueued/Sync/Dropped 与 stopped 同步回退；不能只在一个 Submit 分支打点。同步 fallback 允许实际提交调用耗时包含任务执行；异步 DB 落库时间不属于 finish，无 usage 时保持 finish=0。 |

## 5. 共享与分叉、最小接入切口

1. **不能用同名 ForwardAs 判断覆盖。** GatewayService.ForwardAsChatCompletions 明确走 Chat → Responses → Anthropic、强制上游 streaming；非流式客户端也由 buffered streaming 消费并转换。GatewayService.ForwardAsResponses 是 Responses → Anthropic，同样分 SSE/聚合返回；均没有通过 GatewayService.Forward 的常规入口，因此仅改 Forward 会漏掉 P0-C/D。Antigravity 的同名适配器和 Gemini Chat 又是独立服务。
2. **OpenAI HTTP 公共方法也不是唯一出口。** P0-F 首先由 `backend/internal/service/openai_gateway_chat_completions.go:67`（`OpenAIGatewayService.forwardAsChatCompletions`） 分派。raw 分支 `backend/internal/service/openai_gateway_chat_completions_raw.go:57`（`OpenAIGatewayService.forwardAsRawChatCompletions`）；native Anthropic `backend/internal/service/openai_gateway_chat_completions_anthropic_native.go:37`（`OpenAIGatewayService.forwardChatCompletionsViaNativeAnthropic`）；Grok bridge `backend/internal/service/openai_gateway_grok_chat_bridge.go:516`（`OpenAIGatewayService.forwardGrokChatCompletionsViaResponses`）。同一次调用里的 recursive recovery / raw fallback 继承逻辑 attempt，不在每个子函数无条件 StartAttempt。
3. **OpenAI Responses 的 passthrough 另有消费函数。** `backend/internal/service/openai_gateway_passthrough.go:1864`（`OpenAIGatewayService.handleStreamingResponsePassthrough`） / `backend/internal/service/openai_gateway_passthrough.go:2314`（`OpenAIGatewayService.handleNonStreamingResponsePassthrough`），不能只包装常规 handleStreamingResponseWithReasoning。HTTP 插件见 §4，插件 body 也必须用自身物理响应句柄。
4. **签名纠错/Grok fallback 是物理重发。** Claude 常规发送后的修复调用位于 `backend/internal/service/gateway_forward.go:465`（`GatewayService.Forward`）、`backend/internal/service/gateway_forward.go:508`（`GatewayService.Forward`）。Grok repository transport `backend/internal/repository/http_upstream.go:407`（`grokAccessDeniedFallbackTransport.RoundTrip`） 两次 base.RoundTrip 的实际 fallback 在 `backend/internal/repository/http_upstream.go:422`（`grokAccessDeniedFallbackTransport.RoundTrip`）。只在 doUpstreamRequest 发 Sent 不能完整表达它内部的两次物理发送，需要在 transport 的真实发送处更新物理句柄、累计所有 body n；逻辑 M-4/M-5 不增加。错误体预读 `backend/internal/repository/http_upstream.go:501`（`bufferSmallResponseBody`） 后回放不能二次计字节。
5. **P1 不共用一个消费 parser。** Antigravity `backend/internal/service/antigravity_gateway_retry.go:462`（`AntigravityGatewayService.antigravityRetryLoop`） 内还有 smart retry / URL 重试；Gemini ForwardNative 使用原生解析器；Bedrock 是二进制 EventStream。共享“物理 I/O 观测 + 逻辑 attempt 句柄传递”，不为方便埋点把各协议改造成同一个响应管线。
6. **最小改动顺序。** 先在 §2 的 handler 实际转发分支建立/传递 attempt（保留本地拦截的 0-attempt），再在 HTTP/插件/物理 fallback 边界生成 OpsResponse 并包已有 I/O，随后在最终响应选择/消费路径使用 For 方法，最后接共享 slot、切换和 usage 提交函数。当前 HEAD 尚无统一业务 attempt factory；不能用 HTTPUpstream.Do 代替应用层创建点。
7. **异步媒体/WS 不拉长 HTTP 请求。** 图片 async 已在 202 后 go run，batch 的结算是独立 settlement service，WS 计费是 per-turn hooks；本波不将后台生命周期并入入口 Recorder。P2 只有请求级保证，M1 入口可能已有基础 phase 样本，但不能据此宣称 P2 上游 phase、字节、账号 TTFT 全覆盖。

8. **Gemini `/v1beta/*` 不全是生成动作。** 除 POST models 动作外，还有 `backend/internal/server/routes/gateway.go:353`（`RegisterGatewayRoutes`） 等缓存 CRUD，handler `backend/internal/handler/gemini_cached_contents_handler.go:98`（`GatewayHandler.GeminiCachedContentsCreate`）；缓存创建/延长的 usage 走 `backend/internal/handler/gemini_cached_contents_handler.go:660`（`GatewayHandler.submitGeminiCachedContentUsage`），不是原生生成行的 submit 分支。当前入口前缀会覆盖缓存路由；实施时逐项核对实际动作与已有缓存计费，不把 GET 列表/本地缓存返回强行当生成 attempt，也不擅自更改其计费或入口范围。

9. **Responses 子路径 guard 不是单纯透传。** `backend/internal/server/routes/gateway.go:171`（`RegisterGatewayRoutes`） 可直接进入 `backend/internal/handler/openai_gateway_count_tokens.go:21`（`OpenAIGatewayHandler.ResponsesInputTokens`），不会经过 P0-D/E 的 Forward 循环；与不合法子路径 404、compact/其他可转发子路径分别核对。token 计算动作和 models/count_tokens 的排除口径需以任务卡裁定为准，不能因为共用 `/responses/*subpath` 模板就声称已做生成 attempt 的全量接入，也不能在本卡擅改入口。

## 6. 风险与验证清单（供 W2a/W2b 审核逐项勾选）

| 风险 | 证据/最小措施 | 验证方法 |
| --- | --- | --- |
| 漏掉同名服务或 /messages 平台分叉 | §2 P0-A～G 必须逐行用真实账号类型路由；不是只改 gateway_forward.go。 | 每行各测 JSON/SSE 成功、上游错误与请求取消；核对 M-4/M-5 等于逻辑尝试数，M-10 每入口一次。 |
| 创建 attempt 太早，策略拒绝/本地拦截也计为上游 | Gateway Forward 有 web-search 短路、协议校验；OpenAI ForwardAs* 有策略拒绝。创建推迟到确实准备转发的边界，但仍由应用层负责。 | 认证/限流、利润否决、模型拒绝和缓存/本地成功均断言 M-4=0；0-attempt 成功 M-6=first_success。 |
| 预读、解压、JSON 标准化或 body 回放被重复计数 | §4 公共 body Read、group_model_allowlist/composite 预读与 PrereadBody；wire/decoded 分开。 | gzip/zstd、chunked、截断 body、未知 Content-Length、同一请求先白名单再 handler；与实际 socket/reader 返回 n 对比，重复消费不翻倍。 |
| 只改 repository 漏掉 OpenAI OAuth plugin | §4 doOpenAIUpstream 的 handled 分支直接返回。 | 同一受控请求分别走 plugin handled=true 与 false；发送/header/首字节/结束均存在，V2 序列和字节不双计。 |
| 内部纠错、代理 fallback、redirect 破坏响应身份 | §5 的 signature/Grok/Antigravity retry；必须使用 Sent 返回的 OpsResponse。 | 锁定 A 后再发送 B，迟到 A 的 FirstByte/BodyDone 仍生效，B 不覆盖；内部 400 不污染最终 TTFT；M-4 仍为 1，M-2 累计多次物理发送。 |
| Scanner 首事件被误作首字节；空响应伪造 0 | §4 明确在底层解压后 Read 第一次 n>0 观测。 | 分片把一行 SSE 拆成多次 Read、头先到正文迟到、空 body；TTFT 用实际首 Read 时刻，无首字节无 M-11；Finalize 兜底不 Observe M-11。 |
| SSE buffering/flush/drain 改变 failover 或计费 | 原代码用 Writer.Size、compact keepalive adjusted size、partial usage 控制换号；不替换其业务判据。 | 比对改前后响应 bytes、flush 时机、状态码、重试次数；200 后读超时/客户端断开保持部分 usage 入账，不能因埋点重新拼接第二条流。 |
| 后台 worker 找不到 Recorder，或异步 DB 错误覆盖请求 | §1 usageRecordContext 只保留指定 IDs；§4 在同步提交返回处 MarkFinish。 | Enqueued/Sync/Dropped/Stopped fallback 各测一次；检查 finish 闭合，异步 DB fail 不改变 M-10；任务余额预留引用仍释放。 |
| 新别名在入口白名单外，深层接入后仍没有 Recorder | 路由还注册 `/responses` `backend/internal/server/routes/gateway.go:378`（`RegisterGatewayRoutes`）、`/backend-api/codex/responses` `backend/internal/server/routes/gateway.go:392`（`RegisterGatewayRoutes`）、根 `/images/*` 等；当前 M1 白名单按裁定显式覆盖，未承诺这些额外别名。 | 逐条 httptest 记录 FullPath 和 Recorder 存在性；由主 agent 明确是否扩入口范围，不能默默把深层无 Recorder 当作已覆盖；P0 的 /chat/completions 别名必须已有 Recorder。 |
| P2 请求结果与后台生成结果混用 | §3 P2-B/C/D，202、查询成功与媒体生成成功是不同结果。 | 202/查询/取消每个入口一次 M-10；不因后台图片/多 turn completion 再结算同一入口；不创建文本 HTTP TTFT。 |
| 非业务探测污染分母；Gemini models 模板过度排除 | §3 E1～E3；Gemini GET models 与 POST models/*modelAction 区分。 | count_tokens、所有 models GET、余额/账号探测和管理请求断言无 Recorder/M-10/M-9 entry；Gemini 生成 POST 断言有入口。动态 action 的本地 token 计算若共用模板，须另核对真实动作，不能仅看模板名称。 |
| 跨分钟请求完成造成 coverage 虚高或污染 | 按已裁定 Entry token / current+previous / Complete 规则。 | 未 Finalize 请求留在入口分母；迟到只修正入口分钟，超窗丢弃；并发 Flush/Complete 及空闲 -1；所有 ratio 在允许范围。 |
| 响应 header/body 状态不足以区分代理与源站失败 | 错误分支只填写已有 FailureEvidence，不新增错误体读取；采用任务卡错误来源裁定。 | 代理 connect 502 与经代理源站 headers 502 分开；无来源证据 unknown；优先级冲突及 panic 强制覆盖单独断言。 |

## 7. 自检与交接

- API §3.5 的六个 P0 必选分支已逐条列出（P0-E/F 将 API 的原生 OpenAI 行拆开），额外补充 OpenAI Messages 平台分叉 P0-G。
- P1 Antigravity、Gemini 原生、Bedrock 各有独立行；P2 图片、async、batch、WS 与排除项分别说明其调用、消费和计费边界。
- 事件表覆盖九项要求；代码中既有的 FirstTokenMs / V1 OpsLatency 仅为定位线索，不冒充 V2 已接入。
- 本文所有业务 `file:line` 由源码查找生成，并在交付前用 `rg -n -F` 逐条复核行号和最近所属函数；同时与 `git show HEAD:<file>` 对比内容与行号。
- 本卡未修改任何代码、未运行会修改代码的生成器、未 commit；后续执行以矩阵和风险清单为验收项，不以阅读记录代替接入证据。

## 8. W7a 运维阅读入口与五张看板

更新日期：2026-11-15。§1–§7 保留为 W2.0 的历史路径核对清单，所引 HEAD 行号及“后续接入”措辞属于该阶段记录，不表示当前工作树的逐行实现状态。以下章节说明看板的使用口径和最终验收边界；组件检查与 AC 端到端结论分开记录，详见 [V2 验收记录](MONITORING_V2_VALIDATION.md)。

默认时间范围 6h、浏览器时区；看板不可在 UI 中持久编辑。02 看板刷新 10s，01/03/04/05 均为 30s。Prometheus 默认每 15s 抓取，抓取超时 10s。PG 面板的 `interval=1m` 是查询步长/min interval，**不是独立每分钟刷新**：混合看板上的 PG 查询仍可能随 10s/30s 的看板刷新触发。

| 看板 / UID / 面板数 | 用途与关键面板 | 口径与使用边界 |
| --- | --- | --- |
| 01 · 总览 / `sub2api-v2-overview` / 16 | 先看“采集目标健康”“成功率（1h）”“P95 TTFT”，再看“链路流量漏斗”“分层错误瀑布”“数据完整性” | V1 HTTP 成功率包含管理等 HTTP 请求；M-10 的业务失败可能出现在已经写出 200 的 SSE 中，两者不直接对账。目标可抓取不代表业务成功。 |
| 02 · 链路归因 / `sub2api-v2-chain` / 19 | “段耗时 P95 堆叠（6 互斥段）”“段均值占比”“TTFT 分位（P50 / P95 / P99）”“失败根因速率（请求视角）”；从“慢样本 Top 40（PG 明细）”进入“单请求分段分解（选中样本）” | 段 P95 只用于找嫌疑段；单请求闭合看 PG 明细。Top 40 是窗口内排序截取，不能代表总体分布。大请求体面板使用已有桶 **>256 KiB / 262144 byte**，没有 100 KiB 桶。 |
| 03 · 上游节点 / `sub2api-v2-nodes` / 9 | “账号健康矩阵”“账号 QPS”“账号失败率”“账号 P95 TTFT”“重试与降权信号” | QPS/失败率/M-11 TTFT 为 attempt 口径。账号矩阵和带宽使用 PG 请求样本，与 attempt 不能直接对账。“在途与并发利用率”原位置目前是“未采集”文字说明；没有对应指标，不可推算实时在途。 |
| 04 · 用户/密钥 / `sub2api-v2-users` / 6 | “用户请求量”“今日 Token / 成本”“API Key 流量”“慢请求明细（>30s）”“用户延迟分位”“用户错误构成” | 全为 PG。usage 成本/延迟来自落库日志，错误构成使用请求样本；两种数据集覆盖范围不同。只在有 user_id/api_key_id 的面板应用变量；All 为数值 -1，不把变量施加到 Prom 或系统快照。 |
| 05 · 宿主与应用 / `sub2api-v2-host` / 15 | “宿主 CPU 使用率”“宿主内存”“根分区使用率”“宿主网卡（排除虚拟设备）”“load5 / 核数”，结合“进程 RSS”“DB 连接”“Redis 状态”“并发队列深度” | node 是宿主口径，Go/process 是进程口径，ops_system_metrics 为应用窗口快照。系统快照时间列为 created_at。Docker Desktop 的 node 数据属于 Linux VM，不能当物理 macOS/Windows 主机验收。 |

### 8.1 阈值、颜色与告警持续时间

“数据完整性”已配置 0~1 ratio 的百分比显示：0.96→96% 绿，0.85→85% 黄，0→0% 黄，-1→灰色“无样本”。缺序列标为“未采集”，与真实 0、无样本 -1 分开。缺序列兜底仅在原查询完全为空时出现；不以补 1 模拟健康。

其他面板当前 JSON 多数只有默认绿色，**不能据绿色判断阈值未超标**。以下是 API 告警口径和 mockup 的人工判读线；未逐项配置到 JSON 的颜色不冒称已生效。Alertmanager 的 warning/critical 与 Grafana 显示色分开判断。

| 项目 | 数值 / 持续时间 | 判读与颜色含义 |
| --- | --- | --- |
| 目标健康 | sub2api `up=0` 持续 2m | critical。不能把缺序列或 node profile 未启用当目标正常。 |
| HTTP 5xx 比例 | 5m 速率比例 >1%，持续 10m | warning；总览“成功率（1h）”采用不同窗口。mockup 的 <99% 黄/<95% 红是人工判读参考。 |
| TTFT P95 | 10m 窗口 >3s，持续 10m | warning；mockup >8s 红是人工升级线，不是另一条已实现的 critical 告警。 |
| 账号失败率 | 5m 非 success attempt / 全部 attempt >20%，且该账号 5m increase >20；持续 5m | warning。低样本 100% 不能直接触发同样结论。 |
| 数据完整性 | 面板 <90% 黄、≥90% 绿；-1 灰 | 告警只对 account ratio ≥0 且 <80% 持续 30m。request/bytes/user 颜色不等于已有同名告警。 |
| 客户端中断比例 | 10m 中断速率 / 全部网关请求速率 >5%，持续 10m | warning；正常 EOF、服务端 shutdown 和普通超时不能冒充客户端断开。 |
| 下发字节差额 | `(resp_upstream-resp_downstream)/resp_upstream` >20%，持续 15m | warning；差额本身不能定性截断。 |
| unknown 根因 | 30m unknown / **全部网关请求** >10%，且 30m 网关 increase >100；持续 30m | warning；不是 unknown / 失败请求，也不是低流量一个 unknown 就告警。 |
| 宿主资源 | 根分区可用 <15% 持续 10m；内存可用 <8% 持续 10m；load5/核数 >2 持续 15m | warning。CPU >80% 黄/>95% 红为 mockup 人工参考，无新增 CPU 告警。load 查询已按裁定去掉分母的 cpu 和 mode 标签。 |

## 9. 一分钟初判：总览 → 链路 → 节点 → 用户 → 宿主

这一分钟用于确定下一步取证方向，不承诺一分钟修复故障，也不代替 AC-1 的隔离造数计时。

| 时间预算 | 看板与问题 | 分支与下一步 |
| --- | --- | --- |
| 0–10s | 01：目标可抓取吗？失败涨了，还是流量掉了？覆盖率是否有效？ | 目标 down/缺序列先查采集和开关；-1 先确认该分钟有无请求。区分 HTTP 5xx 与 M-10 业务失败，再进入 02。 |
| 10–25s | 02：主导段、TTFT、错误根因、字节差在哪？ | body_read 大：查上传体积/客户端上传；wait 大：查槽位排队；gateway 大：查首次发送前本地工作；upstream 大：查出口、上游及重试；response 大：查流式生成/客户端慢读；finish 大：查 usage 提交过程。用 PG 单请求确认，不能把段 P95 相加。 |
| 25–35s | 03：异常集中在一个账号吗？ | 联看账号 QPS、失败率、TTFT 和样本量。unknown 增加先核对启动快照/新增改名；失败不集中时按平台或网络共因取证。摘除、降权只作为后续人工决策，不由看板自动执行。 |
| 35–45s | 04：是否集中在某个 user/api_key、模型或大体积请求？ | 切数值型变量，看慢请求与错误构成；先核对落库覆盖率及数据集口径。不要把 PG 账单请求量强行对齐 Prom attempt 数。 |
| 45–60s | 05：机器、进程还是应用池在排队？ | 查 CPU/内存/磁盘/网卡/load，再看 RSS、goroutines、DB/Redis、并发队列。同一时窗定位相关性；共享宿主、快照/PG WAL 可造成资源峰值，不能单凭相关性定因果。 |

若投诉是“失败”，02 优先选 `failure_reason`；若是“慢”，先看段均值占比和 TTFT；若是“返回不完整”，先核对 result、abort stage 和四方向 status/source，再比较单请求字节。任一分支若“未采集”，先补采集证据，不用默认色或零值作健康结论。

### 9.1 六个互斥段与 headers_end

| 段 | 含义 | 不可混淆的边界 |
| --- | --- | --- |
| body_read | 入口到读完客户端 body；失败取已读到的终止时间 | 未读即拒绝为 0；不以 Content-Length 填实际字节。 |
| wait | 首次发送前的槽位等待累计 | 只扣首次发送前，重试间隔不重复扣入该段。 |
| gateway | body 完成到首次发送之间，扣除 wait 的残差本地时间 | 与 wait 互斥，不是所有本地 CPU 时间。 |
| upstream | 首次发送到最终响应头 | 包含失败 attempt、重试间隔、纠错/fallback。 |
| response | 最终响应头到响应体结束/终止 | 包含等首字节；长 SSE 和客户端慢读均可能拉长它。 |
| finish | 响应结束到 usage 任务提交返回 | 包含同步 fallback 的执行；异步模式不等待 DB commit。 |
| headers_end（辅助） | 最终 attempt 的发送到首字节 | 与 upstream/response 时间轴重叠；无首字节不 Observe，不能加入堆叠、占比、总时长。 |

六互斥段之和才是该请求的 total。各段 P95 来自不同请求，P95 之和不是总 P95；段均值占比可用于找主导段，单请求闭合以 PG 样本为准。

### 9.2 必须保留的误读边界

- **字节差异不等于截断。** gzip、解压后响应、协议转换、SSE 重写、内部物理重发的失败体、drain 和客户端 abort 都可能造成差异。M-2 按实际读写观测累计，样本中的 status/source 说明观测来源；req_upstream 当前 source 为 request_body_read，不能当 socket wire 发送量证明。
- **覆盖率不等于成功率。** M-9 分母是入口网关请求，含未完成/未覆盖路径；request/account/bytes/user 各自检查不同维度。-1 是无样本，0 是确有分母且该维度未覆盖。四方向完整性不足时，带宽和差额可能低估。
- **账号是启动快照。** 一次读取非软删 accounts.name，最多 10 个归一化真实名加 unknown；冲突只保留首名，新增/改名在重启前仍 unknown。不把原始账号名、ID、模型或 URL 加成新 Prom 标签。M-11 无 model_class，不能从它直接推导模型档位 TTFT。
- **请求与 attempt 不同。** M-10 每个入口一次；M-4/M-5 每次真实上游 attempt，内部物理重发不新增逻辑 attempt；M-6 每请求一次 outcome。0-attempt 失败不应制造 M-4；已输出 200 的 SSE 仍可能业务失败。
- **P2 只保底请求级。** 图片/async/batch/WS 等的入口请求结果及实际 body 分布不能等同后台生成完成或 per-turn 计费结果。没有完整上游 phase/字节/账号 TTFT 保证；不要把它们混入 P0 全量覆盖的结论。P1 的实际接入完成度也须独立核验，路径清单不是完成证明。
- **usage 延迟是提交前下界。** W4a2c2 按裁定写 `gateway + wait + max(0, snapshot_now-t_done)`，快照在入队前完成；不含快照后的入队/同步 fallback 时间。M-1 和请求 sample 的 finish 仍取真实 MarkFinish。body 未观测写 NULL，实测 0 写 0；无 attempt/未知 t_done 的 usage gateway_latency_ms 写 NULL。NULL 不当作 0 参与“完整率”结论。

## 10. failure_reason 字典与处置方向

API §4.4 的失败枚举**共 13 项，已包含 unknown**，即 12 个有明确证据的类别加 unknown；不是 13 项之外再新增一类。success 的 reason 为 `""`，不属于失败字典。按契约优先级分类，panic 按 Finalize 的强制规则归 gateway_panic。

共同入口：02“失败根因速率（请求视角）”使用 M-10，01“失败根因 Top”做概览；需要账号定位再查 03“账号失败率/账号 P95 TTFT”。PG 慢样本中的 failure_reason、result、account_id、时间戳只对该样本负责，Top 40 不覆盖所有错误。下表给出取证与人工处置方向，不承诺自动修复。

| failure_reason | 已有证据与查看路径 | 人工处置方向 |
| --- | --- | --- |
| upstream_rate_limit | 上游 429/529；02 根因→03 账号请求量/失败率→对应请求日志 | 核对上游配额、并发、Retry-After 和受影响范围；按已有策略评估节流或换号，不盲目增加重试。 |
| upstream_auth | 上游 401/403；02→03 账号→鉴权错误上下文 | 核对凭据有效期、权限和来源；按凭据流程处理，不把密钥或错误体内容放进指标标签。 |
| upstream_model_or_request | 400/404 且已有解析 code/type 证据；02→PG/日志中的请求模型和平台 | 核对模型映射、端点与参数兼容性；无已解析证据时保留 unknown，不为分类额外读 body。 |
| upstream_server_error | 来源明确的上游 500/502/503/504；02→03 多账号是否同步→平台日志 | 区分源站与出口代理；对照上游状态及时间窗，按既有 failover 策略评估影响。 |
| upstream_connect_timeout | connect 阶段 timeout；02 upstream 段→03 是否跨账号→05 网卡/load | 核对连接建立、出口链路及连接池，保留时间线；不把所有慢首字节都称连接超时。 |
| upstream_read_timeout | headers/body 阶段 timeout；02 TTFT/response 与根因→03→日志 | 确认卡在等头、首字节还是流中途；结合已有 timeout/drain 证据，不立即放大超时掩盖问题。 |
| proxy_unreachable | 明确使用代理且 connect refused/DNS/TLS，或明确代理返回 502/503/504；02→代理来源日志→05 | 检查代理可达性、DNS、证书和出口配置。不能只因用了代理就把经代理的源站 5xx 全归本类。 |
| client_abort_during_body_read | body_read 阶段有客户端取消证据；02 客户端中断/body 分布→样本 in_wire status | 查上传中断、客户端网络和请求体大小；普通 unexpected EOF 不自动归客户端 abort。 |
| client_abort_during_response | response 阶段已确认断开/写错误；02 客户端中断/字节差额→样本→客户端关联日志 | 核对客户端 deadline、慢读和断开时点；保留上游 drain/部分 usage 口径，不假定网关截断。 |
| gateway_panic | Recovery 的非 broken-pipe panic 标记；02→应用错误栈→05 | 记录 request_id、版本、栈和已写响应状态，交由缺陷处理；broken pipe 不归 panic。 |
| gateway_db | 请求路径同步鉴权/路由/计费 DB 失败；02→05 DB 连接/队列→数据库错误日志 | 核对池等待、锁、连接及 SQL 错误。异步 usage DB 失败不覆写请求结果为本类，应查异步写入日志。 |
| gateway_ratelimit | 网关限流拒绝；02→04 用户/密钥流量→现有限流日志 | 区分本地限额与上游限流，核对用户/密钥/组配置及突发量；不要把拒绝计作真实上游 attempt。 |
| unknown | 证据不足、尚未分类或来源不明；02 unknown 趋势→PG/应用日志 | 先核对最小样本和覆盖率，再保留已有错误证据补分类。告警按 unknown/全部网关请求计算；不能将 unknown 静默视为“其他成功”。 |

## 11. AC-1～AC-10 当前状态与记录入口

截至2026-11-15，以下状态按实际执行范围记录。最终本地镜像为`sub2api-v2smoke:worktree-final2-1794725105`，digest **`sha256:9bd114778a3373b008f6a04f1cf166228461f9e238f9fc323989ec4e43dad949`**（linux/amd64，未push/部署）。W6b、wait、AC-5、AC-8 Drill1、AC-9、AC-10已在此digest复跑；其他本地子项沿用各次记录的旧镜像，不能视为最终镜像全部重验。

| AC | 当前已验证状态 | 实际值与未执行边界 |
| --- | --- | --- |
| AC-1 | **PASS（四类API归因子项）** | upstream/body_read/response Prom10.190/20.158/10.243s；最终镜像wait5303ms、gateway8ms，Prom10.229s/query.223s、PG10.292s单行，sum5316/total5319取整差3ms。浏览器首见/联动未执行。 |
| AC-2 | **PASS（四类镜像字节/API子项）** | abort/转换/失败体重试/真实截断的resp_up/down为1037/746、749/265、313/223、1025/90B；abort根因可区分，截断unknown不单独定性。浏览器联动未执行。 |
| AC-3 | **PASS（本地159例fixture）** | ≥3类明确失败，unknown10/159=6.289308%；真实30m窗口未执行，保留旧149例及失败记录。 |
| AC-4 | **PASS（PG18 SQL/只读权限/过滤）** | 54/54面板SQL，复核62次调用max38.315ms；Grafana插值/浏览器切换<15s未执行。 |
| AC-5 | **PASS（最终镜像P0/API子项）** | 10×20=200，180成功/20受控400，失败率10%，M-11 account=10；Prom13.329779QPS（15.004s窗口），client16.148534QPS。浏览器三图同屏未执行。 |
| AC-6 | **PASS（本地Docker VM资源曲线）** | 14项instant/30s range有值，CPU10.80%、内存17.35%、load .79/.50/.37；生产宿主/rootfs/NIC与浏览器曲线未执行。 |
| AC-7 | **PASS（本地SMTP firing/resolved）** | 真实SMTP收件05:18:58.713704Z firing→05:19:00.714021Z resolved；唯一合成告警路由。外部SMTP/TLS/auth、生产告警规则链未执行。 |
| AC-8 | **Drill1 PASS；Drill2 未执行；完整 AC-8 PARTIAL** | 最终镜像 Drill1：94.801s firing、46.869s resolved；[report](/tmp/sub2api-v2smoke-ec416fbc-evidence/report.json)、[timeline](/tmp/sub2api-v2smoke-ec416fbc-evidence/ac8-timeline.json)。Drill2 无稳定 phase-only freeze 注入，未执行。 |
| AC-9 | **PASS（最终镜像协议/持久计费子项）** | 克隆DB初态相同；JSON/SSE/abort协议/帧/chunks/attempt/usage/cost一致，余额各999.99958600。Date/生成UUID/耗时和观测字段差异单列；镜像异常注入、精确Close-Flush计数未执行，W2c1只作组件证据。 |
| AC-10 | **PASS（本地四目标拓扑/负载）** | 全量TYPE合并后最大族128（Alertmanager，sub2api最大112），head2905→2999<8000；最终digest本地预算通过；生产代表负载未执行。 |

最终镜像证据入口：

- W6b：[报告](/tmp/sub2api-v2smoke-5eeaa986-evidence/report.json)，具体metrics/PG精确1行/M-9 request=1，in_wire=decoded=143B。
- AC-1 wait：[Prom原文](/tmp/sub2api-v2smoke-92194eee-evidence/phase-002-wait-phase-p95.json)、[PG单行](/tmp/sub2api-v2smoke-92194eee-evidence/phase-wait-pg.json)、[报告](/tmp/sub2api-v2smoke-92194eee-evidence/report.json)。
- AC-5：[报告与各账号统计](/tmp/sub2api-v2smoke-b356b485-evidence/report.json)、[200条请求记录](/tmp/sub2api-v2smoke-b356b485-evidence/ac5-request-records.json)。
- AC-9：[完整对照及差异](/tmp/sub2api-v2smoke-e75f9f16-evidence/ac9-comparison.json)、[报告](/tmp/sub2api-v2smoke-e75f9f16-evidence/report.json)。
- AC-10：[final2预算报告](/tmp/sub2api-v2smoke-0b84ad4b-evidence/report.json)，head2905→2999、622→624族、最大128；四目标各4次成功scrape，15s间隔。
- [同digest四轮验证及独立清理](/tmp/v2-final2-verification-report.json)：四项目容器/卷/网络/临时配置目录均为0。镜像与本机`/tmp`证据保留，不代表持久归档。

完整方法、旧/新镜像区分和全部原始响应见[验证记录§5](MONITORING_V2_VALIDATION.md#5-最终本地镜像与运行态-ac-补证2026-11-15)。真实30m、浏览器联动、生产宿主、外部SMTP、镜像异常注入/精确Close-Flush继续标未执行；不以子项PASS替代这些边界。

## 12. 序列预算与待测记录

硬预算：**整机 Prometheus head <8000，单个指标族 <2000**。单实例 V2 理论上界按 API §3.6 为 1406；账号最多 10 个真实名加 unknown。1406 不包含 V1 HTTP、Go/process、node、Prometheus、Alertmanager 自身序列，也不能替代多实例实际 head 测量。CounterVec 曾出现的历史标签和残留序列同样可能占 head。

按 API §3.6 的理论族预算如下，包含直方图的全部桶、sum、count；这是单实例容量计算，不是已测值。M-4/M-10 的 reason 上界按 13 个失败值加成功空串共14个计算。

| 指标族 | 单实例理论上界 |
| --- | --- |
| M-1 request_phase_seconds | 112 |
| M-2 transfer_bytes_total | 5 |
| M-3 request_body_bytes | 65 |
| M-4 upstream_requests_total | 924 |
| M-5 model_requests_total | 30 |
| M-6 attempts_total | 3 |
| M-7 account_switches_total | 1 |
| M-8 client_abort_total | 2 |
| M-9 metric_coverage_ratio | 4 |
| M-10 gateway_requests_total | 84 |
| M-11 upstream_ttft_seconds | 176 |
| 合计 | 1406 |

| 项目 | 当前值 | 结论 |
| --- | --- | --- |
| 四目标本地隔离栈 head series | 2905→2999（final2） | final2本地PASS；生产代表负载未执行 |
| 最大全量指标族 | 128 / alertmanager_notification_latency_seconds | metadata TYPE合并全部四目标后<2000 |
| sub2api最大合并族 | M-1=112 | 全族合并实测；原始分布见验证记录§5.7 |
| 样本负载/实例与采集窗口 | 单账号/单app、四job15s、各4次成功scrape后测量 | 仅本地成功请求负载，不外推生产容量 |

下面是手工复测命令模板，W7a当时未运行；随后W6c已用隔离脚本取得上述本地实测，原始响应见验证记录§5.7。使用已配置的本地 Compose wrapper 访问容器内 Prometheus，无需发布 9090：

```sh
cd deploy
# 精确记录该 Prometheus 实例的 headStats/各 metric name 计数。
./sub2api-compose exec -T prometheus wget -qO- \
  'http://localhost:9090/api/v1/status/tsdb?limit=100000'
# 同时记录自监控指标，核对整机预算。
./sub2api-compose exec -T prometheus wget -qO- \
  'http://localhost:9090/api/v1/query?query=prometheus_tsdb_head_series'
# 运行态 metric-name 序列分布（不是完整 histogram family 的最终结论）。
./sub2api-compose exec -T prometheus wget -qO- \
  'http://localhost:9090/api/v1/query?query=topk%2820%2Ccount%20by%20%28__name__%29%28%7B__name__%3D~%22.%2B%22%7D%29%29'
./sub2api-compose exec -T prometheus wget -qO- \
  'http://localhost:9090/api/v1/metadata'
```

将 `headStats.numSeries`、`seriesCountByMetricName`、metadata 与时间戳一起保存。按 metadata 的 histogram/summary 类型把同族 `_bucket`、`_sum`、`_count`（以及出现的其他同族子序列）合并后再比较 <2000；不能只看 topk 的一个子名称就宣布整族达标。`limit=100000` 若仍截断，须扩大/分批取得完整计数后再结论。instant 查询只反映当前可查询序列，TSDB head 还含残留状态，应以 TSDB 证据核对并记录差异。

若超预算，按契约顺序评估 `model_class`→other、`endpoint_class`→other、M-1 桶裁剪；任何调整需后续代码评审与重新测量，不能在值班时临时去掉 phase、failure_reason 或 account。W7a 不执行降维。

## 13. 部署、回滚与证据保留

部署前的二进制辅助检查、迁移 checksum 基线、历史编号冲突 WARN 与回滚 dry-run 使用 [MONITORING_RELEASE.md](../deploy/MONITORING_RELEASE.md)；本地监控栈、PG 只读角色、profile 与旧看板回滚见 [MONITORING_LOCAL.md](../deploy/MONITORING_LOCAL.md)。本手册不另实现部署/回滚命令，也不把 dry-run 当作实际回滚。

每次 AC 留存版本/镜像 digest、迁移 manifest、开关、采集参数、fixture/请求 ID、开始结束时间、Prom/PG/Grafana/SMTP 原始输出及判定。二进制字符串存在仅辅助，不证明运行态；旧样本行非空不能证明本次部署已写入。受控请求与新 request_id 的样本关联、运行态覆盖率和故障恢复仍需单独执行。
