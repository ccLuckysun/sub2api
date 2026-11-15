# Monitoring V2 验收记录与 AC 骨架

记录日期：2026-11-15。范围为当前共享工作树的本地组件验证及后续 AC 记录入口，不是生产验收或发布授权。方法与阈值来自 [开发计划 §6](MONITORING_V2_DEV_PLAN.md#6-ac-造数与判定修正-v1)；看板口径见 [dashboard-v2.md §8 起](dashboard-v2.md#8-w7a-运维阅读入口与五张看板)。不修改 API、开发计划或任务卡。

## 1. 状态规则与当前 AC 表

PASS 必须注明已实际完成的范围并引用原始输出；API、组件或镜像子项不能替代浏览器、生产或真实时间窗口验收。以下为 2026-11-15 最终收尾状态，详细版本和证据见 §5。历史失败及旧镜像记录保留，不覆盖为通过。AC-8 最终状态为 Drill1 PASS、Drill2 未执行、完整 PARTIAL；不以过程输出推定完整完成。

| AC | 方法 / 完整判定 | 当前已验证状态 | 实际证据与未执行边界 |
| --- | --- | --- | --- |
| AC-1 | 四类延迟；15s scrape、10s轮询，Prom ≤30s 指主导段、查询≤2s；PG ≤90s 单请求确认 | **PASS（四类 API 子项）**；浏览器联动未执行 | upstream/body_read/response 的 Prom 首次识别为10.190/20.158/10.243s；最终镜像 wait=5303ms、gateway=8ms，Prom10.229s、PG10.292s。逐请求PG精确1行，六段按≤5ms取整容差检查。前三类为旧镜像，wait为最终镜像，不宣称最终镜像重跑全部四类。见§5.2。 |
| AC-2 | abort、Chat→Anthropic转换、重试失败体、真实网关限长；M-2/根因/PG对照 | **PASS（四类镜像/API 子项）**；浏览器联动未执行 | resp_upstream/resp_downstream为1037/746、749/265、313/223、1025/90；截断触发真实1024-byte cap、partial/502。abort reason=client_abort_during_response，截断=unknown，不以差额单独定性。见§5.3。 |
| AC-3 | ≥3类失败，unknown/全部网关请求<10%，达到样本门槛；真实30m另验 | **PASS（本地159例 fixture）**；真实30m未执行 | 159例：53成功、106失败，6类明确失败根因；unknown=10/159=6.289308%。[实际输出](/tmp/ac3-rereview-159.jsonl)。旧149例及修复前FAIL保留于§2.3.3；不把fixture时长当30m窗口。 |
| AC-4 | user/api_key All/单选/无匹配，适用面板跟随<15s | **PASS（PG18 SQL/权限/过滤子项）**；浏览器联动未执行 | 4变量查询、54/54面板查询通过；复核62次调用max38.315ms，包含docker/psql启动。All/单选/no-match及只读角色实测见[PG18复核](/tmp/ac4-review-pg18.log)。Grafana实时插值、浏览器切换<15s、生产规模未执行。 |
| AC-5 | 10账号×20，QPS/失败率/TTFT及M-11对照 | **PASS（最终镜像 P0/API 子项）**；浏览器三图同屏未执行 | 200=180成功+20受控400，每账号20/失败率10%，M-11 account=10、每账号18成功TTFT观测；Prom13.329779QPS（15.004s scrape窗口），client16.148534QPS。PG与请求/M-11 sum/count一致，见§5.4。 |
| AC-6 | CPU/内存/磁盘/网卡/load有效曲线与宿主语义 | **PASS（本地 Docker VM 查询/曲线）**；生产宿主未执行 | 14项instant+30s range，每曲线3点；10核CPU10.80%、内存17.35%、load .79/.50/.37。文件系统为VM /dev/vda1的/etc文件挂载，不能代表生产rootfs/NIC。见§5.5。 |
| AC-7 | 本地SMTP触发/恢复邮件；外部投递另验 | **PASS（本地SMTP firing/resolved）**；外部SMTP未执行 | 同一test_id真实SMTP DATA收件两封，05:18:58.713704Z firing、05:19:00.714021Z resolved，原始MIME保存。仅唯一合成API告警路由，非完整生产warning/critical规则链；外部SMTP/TLS/auth未执行。见§5.5。 |
| AC-8 | 缺埋点/停写持续流量、≤5min fire、恢复resolved、空闲/混合实例 | **Drill1 PASS；Drill2 未执行；完整 AC-8 PARTIAL** | 最终镜像 Drill1：94.801s firing、46.869s resolved；[report](/tmp/sub2api-v2smoke-ec416fbc-evidence/report.json)、[timeline](/tmp/sub2api-v2smoke-ec416fbc-evidence/ac8-timeline.json)。Drill2 无稳定 phase-only freeze 注入，未执行。 |
| AC-9 | 同DB初态，开/关埋点比较协议/Flush/attempt/usage/cost及异常隔离 | **PASS（最终镜像协议/持久计费子项）**；镜像异常注入/精确Close-Flush未执行 | 同digest双栈克隆PG初态相同。JSON223B、SSE749B/6帧6chunks、abort698B/5帧5chunks，输出一致；每例usage1、tokens11/7、cost .000138、余额均999.99958600。仅归一化Date/UUID并验证ID关联；差异单列，见§5.6。W2c1异常隔离只作组件证据。 |
| AC-10 | 全目标head<8000、全指标族合并后<2000 | **PASS（本地四目标隔离拓扑/负载）**；生产代表负载未执行 | 四目标15s、各≥3成功scrape。全量719→721指标名按metadata合并622→624族，head2905→2999，最大族alertmanager_notification_latency_seconds=128，sub2api最大112。最终digest容量重测通过，见§5.7。 |

未执行的边界继续明确记录：真实30m运行态窗口、浏览器联动与首次可见时间、生产Linux宿主、外部SMTP、镜像层埋点异常注入及精确Go Close/Flush调用计数。§2为历史组件证据，后续§5为本地运行态补充，二者不互相冒充。

## 2. 已实际执行的组件证据

以下 PASS 仅指该行描述的组件范围。历史命令使用 Docker 中的 Go/工具执行；W7a 只读取证据、追加文档，没有重跑构建、容器或部署。W7b 读取 W2c1 既有实际日志，并补跑 §2.3.5 的异常隔离/生命周期定向 race；仅修改本文件，未改代码、未部署。`/tmp` 链接为本机日志，非持久归档；后续正式 AC 需另存随版本的证据包。

| 范围 | 实际命令 / 方法 | 实际结论及边界 | 证据 |
| --- | --- | --- | --- |
| W5 规则/看板 | Docker promtool v3.16.0 check config / check rules / test rules；generator --check；面板断言；54 条 PromQL 解析 | PASS：18 条规则（2/8/8）、30 组告警测试、5 UID/16/19/9/6/15 面板、刷新 30/10/30/30/30；11 组查询回归。含真实零值/缺序列、coverage .96/.85/0/-1、已有 256 KiB 桶、load mode 匹配。不能代替 AC 时延或真实曲线。 | [本地监控验收](../deploy/MONITORING_LOCAL.md#v2-w5-本地验收2026-11-14) 与其“W5 审核修复验收”章节 |
| W5 Grafana / PG 只读 | 隔离启动三个监控容器，通过 Grafana API 查五张看板；隔离 PG18 TCP 密码连接验证权限 | PASS：只加载五张 V2，看板数正确，V1 未加载，PG 数据源 secure password/grafana_ro 已配置。独立 PG 查询成功，read-only 关闭后 UPDATE/CREATE 仍权限失败，pg_sleep(20) 在 15.04s 取消。未证明实际 M3 数据查询性能。 | 同上；`deploy/tests/monitoring-v2-stack-test.py`、`monitoring-v2-pg-test.py` 为已运行脚本 |
| W5 Alertmanager / Compose | amtool 模板检查；dev+monitoring+host-metrics+backup 合并 config | PASS：1 receiver/1 template，合并 config exit 0。纯 overlay 不是完整项目，单文件 config 的失败已记录。未发送邮件、未证明生产部署。 | 同上 |
| W2a2d 账号快照 | Docker Wire 生成/check；monitoring/server race；cmd/server 快照测试 | PASS：一次 SQL `SELECT name FROM accounts WHERE deleted_at IS NULL ORDER BY id`，5s timeout；16 名称 fixture 收敛为 10 真实标签+unknown；disabled 不查库；失败空快照。未读取实际部署账号清单。 | 本会话实际输出，未单独保存文件；关键输出见下方“账号快照记录” |
| W4a1 迁移 | 一次性 PG18.6，251→252 各单事务首次/重复；fresh 与 partial 两临时库 | PASS：样本 40 列、3 查询索引+主键；usage 两列 BIGINT NULL；重复 schema/index 一致，原数据保留。未连 dev/生产，未执行全历史链。 | [迁移实际输出](/tmp/w4a1-verify.log) |
| W4a2a usage 映射 | 实际 `go generate ./ent`；隔离 PG18；真实源码 build/定向 race | PASS：4 插入路径×4 NULL/0/int64/混合输入=16 组；历史行 NULL，SQL/ent 一致，重复请求不覆盖；DTO null/0 可区分。PG18 轮使用过临时编译 overlay，后续真实源码无 overlay build/race 已通过，须保留此区别。 | [PG18 输出](/tmp/w4a2a-pg.log)、[最终 build/race](/tmp/w4a2a-final-build-race.log) |
| W4a2b writer/cleanup | writer 定向 race；一次性 PG18 真实插入/清理再跑 race | PASS：队列8192、批500/1s、非阻塞/错误/停止计数；真实 writer 501 行入库；5001 条31d旧行清理，恰好30d/29d/当前3行保留，0清空。没有 Recorder→PG 受控请求端到端计时。 | [writer 定向 race](/tmp/w4a2b-final-wire-focused.log)、[PG18 race](/tmp/w4a2b-pg-race.log) |
| W4a2c1 Recorder→sink | `go build ./...`；`go test -race ./internal/monitoring/... ./internal/server/... ./cmd/server`；wire check | PASS：200/0-attempt/panic/abort/partial/retry 一次发射，完整字段映射，ID 稳定唯一，指标快照与 nil sink 相同，并发/重入只一次，满 sink 延迟消费不阻塞。该 sink 测试不等于浏览器看板 AC-1。 | [字段/指标定向日志](/tmp/w4a2c1-monitoring-race.log)、[真实源码最终验证](/tmp/w4a2c1-final-validation.log) |
| W4a2c2 usage 快照 | monitoring/service/handler/repository 定向 race；unit 下既有 RecordUsage 回归；build | PASS：NULL/0/未知 t_done、200/SSE、enqueue/无池/停止/溢出同步，worker ctx 不保留 Recorder；100 个正常 fixture 非空100/100。usage 延迟采用提交前下界，不冒充实际流量95%覆盖率。 | [首轮详细用例](/tmp/w4a2c2-race.log)、[最终 race/build](/tmp/w4a2c2-final-race.log) |
| W6a 部署契约脚本 | 两个 cc 编译二进制正反例；304 SQL manifest+49历史冲突ack；临时副本变更/恢复；本地镜像 rollback dry-run | PASS：缺 V2 字符串失败；同名变更、LF→CRLF、缺失/新增、冲突变化均失败，恢复通过；只 inspect 镜像、记录身份/计划命令，未 tag/push/deploy。二进制字符串仅辅助，未完成运行态烟测或回滚。 | [CRLF 回归](/tmp/w6-crlf-regression.log)、[发布契约手册](../deploy/MONITORING_RELEASE.md) |

### 2.1 账号快照记录

账号快照任务的实际记录来自本会话已执行输出，不借用别的任务日志：

```text
go test -race ./internal/monitoring/... ./internal/server/...：各包 PASS
go test -run '^$' ./cmd/server：PASS [no tests to run]
go test -race -v -run '^TestProvideAccountLabels' ./cmd/server：PASS
snapshot: 16 names read, 10 real labels + unknown; collisions/new names stay unknown
5s context timeout: 5.006319586s; empty snapshot returned
wire：wrote /src/cmd/server/wire_gen.go
wire check：exit 0，无输出
```

该轮未单独保存日志文件，故不提供虚假的日志路径；后续正式归档可重新运行对应测试。生产部署账号数仍未测；§5.4的隔离DB已实际种子并验证10账号。

### 2.2 最终通过的关键输出摘录

```text
W4a2c1（真实源码，无 overlay）：
BUILD_PASS
monitoring PASS（缓存）
server 1.061s / middleware 1.371s / routes 13.028s / cmd/server 6.061s
RACE_PASS
WIRE_CHECK_PASS

W4a2b（一次性 PG18，-race）：
repository 1.132s：batch 500 + nullable row；全部40列核对
service 1.154s：5001过期行清理，30d边界+29d+当前保留；显式0 truncate
real async writer：501 accepted/written；Stop 刷500+尾1
Tool cleanup: 0 / PG cleanup: 0 / Network cleanup: 0

W4a2c2（真实源码，无 overlay）：
monitoring 1.047s / service 1.565s / handler 1.161s / repository 1.107s
-tags unit service 1.597s：新增映射和既有 Gateway/OpenAI RecordUsage 回归
BUILD_AND_DIRECTED_RACE_PASS
```

上述耗时为组件命令时长，不可用于填写 AC-1、AC-4、AC-8 的运行态时延。

### 2.3 W7b：W2c1 后端实证（2026-11-15）

**历史阶段快照：**本节保留W7b当时的“尚缺/未执行”判断及149例证据；后续本地运行态补证、159例复核与最终镜像状态以§1/§5为准。这里的内存fixture仍不能被解释为持久DB或浏览器验收。

证据范围为本机 Docker Go 测试、httptest 回环假上游、错误回放及内存 gRPC plugin；没有真实 LLM、生产凭据、部署或生产流量。W2c1 成功日志始于 **2026-11-15 04:07:29 UTC**；W7b 读取时的代码基点为 `95368134a3926882ba88886a35aee7cc4042666e`，但实际测试对象包含共享工作树中未提交的 V2 改动，不能将这个 HEAD 当作完整候选版本或镜像 digest。

原始证据及定位：

- [W2c1 修复后 race 原始输出](/tmp/w2c1-tracked-body-fixed-race.log)：`TestP0MonitoringHTTPMatrix` **141/141**、`TestP0MonitoringPluginReplay` **8/8**，合计 **149/149 PASS**；service `22.245s`。每个叶子用例使用独立 registry，先 V2=false 再 V2=true，各请求顺序执行；不是一个真实 Prom 采集窗口。
- [public 出口及 Close 生命周期实际输出](/tmp/w2c1-public-identity-fixed-race.log)：无 `integration` 标签，repository `1.268s`；public `Do/DoWithTLS` JSON/SSE/gzip/空响应 **8/8**，未观测 body 及 Close/取消/keep-alive/HTTP2 隔离通过。
- [W7b 补充异常隔离与生命周期 race 输出](/tmp/w7b-backend-evidence-race.log)：middleware `1.160s`、monitoring `1.036s`、repository `1.327s`，均 PASS，无 `DATA RACE`；执行命令见 §2.3.5。
- [HTTP 矩阵与五方向/阶段断言](../backend/internal/service/p0_monitoring_integration_test.go)：`p0AssertMetrics:458`、`TestP0MonitoringHTTPMatrix:556`；[plugin 对照](../backend/internal/service/p0_plugin_monitoring_integration_test.go)：`TestP0MonitoringPluginReplay:25`。行号是本次读取工作树的定位，不替代版本化归档。

保留失败过程：[修复前集成日志](/tmp/w2c1-final-integration-race.log) 为 88 PASS / 61 FAIL，61 项均是 M-11 预期 1、实际 0；[新增 public 回归修复前日志](/tmp/w2c1-public-identity-before-race.log) 的 8 条身份用例全部复现句柄丢失。根因为外层 `trackedBody` 未转发 `OpsObservation`；转发身份后上述 149 条严格断言全部通过，没有删除或放宽 M-11 断言。本卡只补文档，不再次修改该修复。

#### 2.3.1 AC-1：仅后端六段闭合 PASS

149 条 enabled 请求均断言 `body_read/gateway/wait/upstream/response/finish` 六段非负、每段 histogram count=1；明确排除重叠的 `headers_end`。六段 histogram sum 转成毫秒后与同次 Finalize 的**内存** `OpsSample.TotalMs` 比较，原始断言容差为 **1.01ms**（TotalMs 整毫秒截断），每次仅提交一个样本。没有将各段 P95 相加。

这只证明后端时间分解与内存样本闭合：**逐段延迟造数、前后 2 分钟隔离窗口、Prom ≤30s 指出主导段、PG ≤90s 用新 request_id 确认，均未执行，待 W6b。** 没有本次实时 Prom 查询结果、PG 对应新行或浏览器首次可见时间；`22.245s` 是测试进程时长，不能充当实时验收时延。

#### 2.3.2 AC-2：四类字节差异与证据层级

以下 W2c1 日志中 `M2=a/b/c/d/e` 依次是 `in_wire / req_decoded / req_upstream / resp_upstream / resp_downstream`，单位为实际 body 字节；上游响应按解压后读取的 n 计数，不含 HTTP 头。数值只对应所列 fixture，不外推到真实请求。

| 类别 / fixture | 已执行的字节证据 | 结算 / 判读 | 结论与缺口 |
| --- | --- | --- | --- |
| 客户端 abort：`HTTPMatrix/claude/abort` | `95/95/95/650/0`；上游已读650、下游成功写0，差650 | `client_abort / client_abort_during_response`，M-8=1；客户端写 EPIPE 后继续 drain，不能把未成功写出的字节当成下游已收 | 后端 PASS；并非“差额650就等于客户端丢包”，根因来自写失败证据。 |
| 协议转换：`HTTPMatrix/compat_chat/200` | `96/96/96/650/245`；Anthropic SSE→Chat JSON，响应差405 | `success / 空`，M-8=0 | 后端 PASS；协议封装不同本来就不守恒。 |
| 协议转换扩张：`HTTPMatrix/compat_responses/SSE` | `48/48/97/650/2051`；下游比上游多1401字节 | `success / 空`，M-8=0 | 后端 PASS；也覆盖下游字节大于上游的合法差异。 |
| 重试失败体：`HTTPMatrix/claude/signature` | `331/331/563/272/182`；两次物理发送；上游272=被丢弃的400失败体90+最终成功体182 | 最终 `success / 空`；M-4/M-5=1、M-7=0；首次失败不冒充最终 M-10 根因 | 后端 PASS；失败体读取仍计入 M-2，不能只数最终响应。另有跨账号 `96/96/192/252/182`，M-4/M-5=2、M-7=1。 |
| 网关截断的主动提前 Close 模型：`TestOpsHTTPResponseLifecycle/early_close_after_positive_read` | 原语只读取 `ab`，`resp_upstream=2`；该测试显式将未写的 `resp_downstream` 标为 complete 零值，差2；Close 时上游状态断言为 `partial`，不补未知剩余字节 | 测试显式 `Done(upstream_error, unknown)`；没有客户端取消证据，不标 abort。这不是生产限长入口自动归因的证明 | **仅原语 PASS**（本卡补跑）；没有真实 writer，也未验证生产网关限长/截断分支与同次 M-10/PG 的联合结果，完整场景未执行。 |

四类证据不可混为同一层级：前三类为实际 Gin→service→假上游路径；第四类是观测原语模型。[原语测试](../backend/internal/monitoring/ops_http_test.go) `TestOpsHTTPResponseLifecycle:40` 明确断言实际 n、提前 Close 的 partial 及原样 Close 错误，不能冒充完整网关截断场景。W2c1 原始摘要只输出字节 n，没有输出所有方向的 status/source；本次未另取 PG status/source，不能从差额反推或补造它们。因此 **AC-2 完整状态仍为未执行**，尚需真实截断入口及四类联合看板/PG 取证。

#### 2.3.3 AC-3：证据修复后6类明确根因，fixture unknown占比达标

本节最新统计基于[证据修复后149例实际M-10日志](/tmp/w7b-ac3-final-149-race.log)。统计单位是其中149条PASS叶子用例中 **enabled请求的M-10**，每例已经断言 `gateway_requests_total=1` 及其 `result/failure_reason`；disabled 对照不产生 V2 计数。归并依据为`p0AssertMetrics`读取独立registry后的实际M-10标签日志及全部叶子PASS；不是部署中Prom的counter。每个 case 的独立 registry 只取一次；不把 298 次开关对照业务调用、M-4 attempt、内部 signature 重发或跨账号首个失败重复计入分母。

| M-10 failure_reason | 请求数 | 构成 |
| --- | ---: | --- |
| 空（result=success） | 53 | HTTP 的200/SSE/gzip/preread/cross_account共50，signature 1，plugin成功2 |
| upstream_rate_limit | 11 | HTTP 429共10，plugin 429共1 |
| upstream_server_error | 22 | HTTP 500/502共20，plugin 500/502共2 |
| upstream_connect_timeout | 10 | HTTP connect timeout回放10 |
| upstream_read_timeout | 21 | HTTP headers/read timeout各10，plugin timeout 1 |
| client_abort_during_response | 12 | HTTP abort 10，plugin SSE/JSON abort各1 |
| proxy_unreachable | 10 | HTTP代理CONNECT 502共10；真实CONNECT响应状态、ProxyUsed=true、Stage=connect；不误写成源站5xx |
| unknown | 10 | HTTP无代理refused共10；§4.4没有专属无代理refused类，保留unknown |
| **总计** | **149** | **53成功、96失败；6类明确失败根因，另有unknown** |

API §9 的 `Sub2apiUnknownFailureHigh` 用 **30m 内全部网关请求 increase>100** 作最小样本门槛，unknown/全部网关请求>10% 并持续30m才告警。本 fixture 的数量部分为149>100，但统计来自分离的内存 registry，**真实30m窗口、increase/rate、持续时间和告警 fire/resolved 均未执行**。

修复前本地数值为 `20/149=13.422819%`，判定 **FAIL**，原始输出保留在[W2c1通过日志](/tmp/w2c1-tracked-body-fixed-race.log)；当时通过的是旧分类期望，不代表unknown阈值通过。20个unknown逐一定位为10条`refused`和10条`proxy_502`，没有SSE用例。此次从`net/http.Transport.OnProxyConnectResponse`保留已经收到的CONNECT状态，经请求context传给结算，`ProxyUsed=true + Stage=connect + StatusCode=502`把后10条正确分类为`proxy_unreachable`；保持原错误返回、不读取body、不更改handler/failover。每次物理发送重置该证据，成功CONNECT 200不冒充源站状态，额外回归覆盖502/503/504及残留证据清除。

修复后 `unknown / all_gateway_requests = 10 / 149 = 6.711409% <10%`，fixture数量149>100，≥3类根因与比例子项均 **PASS**。成功53/失败96保持不变，全部149个fixture保留，没有将失败伪造成success、删除unknown用例、混入额外成功数或加权稀释。这里无代理refused虽然有ECONNREFUSED证据，但§4.4未给出相应独立原因类，不能冒充proxy_unreachable；仍为unknown。该测试分布不能直接推断生产unknown率，真实30m窗口仍未执行。

SSE/协议证据另做定向回归，不纳入149分母：兼容Chat/Responses在流式和缓冲消费中均保留实际HTTP状态、Stage=body、error.code优先/type回退；HTTP200体中的明确model/request、authentication、rate-limit语义码分别映射到已有model_or_request/auth/rate_limit类，不伪造400/403/529状态。普通`api_error`、无code/type、未知供应商码保留unknown；成功reason仍为空。此为本次用户要求的语义证据分类补充，不把任意非空code当作可判定根因；API/计划文档未改。定向用例包含code与type冲突时code优先、仅type、无code/type及原有HTTP400/404 code/type矩阵，并对开关前后业务返回/响应/SSE/usage/cost保持相等。

可复核统计的只读命令（直接统计日志中的实际M-10 result/failure_reason，不重新运行分类器、不按scenario预填标签）：

```bash
python3 - <<'PY'
from pathlib import Path
from collections import Counter
import re
text = Path('/tmp/w7b-ac3-final-149-race.log').read_text()
cases = re.findall(r'^    --- PASS: (TestP0Monitoring(?:HTTPMatrix|PluginReplay)/\S+) \(', text, re.M)
rows = re.findall(r' M10=1 [^\n]* result=(\S+) failure_reason=([^\s]*)', text)
assert len(cases) == len(set(cases)) == len(rows) == 149
counts = Counter()
for result, reason in rows:
    if result == 'success':
        assert reason == ''
        counts['success'] += 1
    else:
        assert reason
        counts[reason] += 1
print('leaf_cases=', len(cases))
for reason, n in sorted(counts.items()):
    print(reason, n)
print('unknown/all=', f'{counts["unknown"]}/{len(cases)}={counts["unknown"]/len(cases):.6%}')
print('failed=', len(cases) - counts['success'])
PY
```

实际输出：

```text
leaf_cases= 149
client_abort_during_response 12
proxy_unreachable 10
success 53
unknown 10
upstream_connect_timeout 10
upstream_rate_limit 11
upstream_read_timeout 21
upstream_server_error 22
unknown/all= 10/149=6.711409%
failed= 96
```

#### 2.3.4 AC-9：本地行为对照与异常隔离 PASS，完整 AC 未执行

W2c1 的基线/候选是**同一工作树、同一二进制**的 V2=false/true，不是跨版本镜像。149例均比较请求 body/headers、物理发送序列、响应 status/headers/body、SSE chunks 和 Flush 次数；仅归一化生成的时间戳及随机 ID，保留字段名、帧边界及 ID 对应关系。enabled 另断言逻辑 attempt、最终 outcome 和 M-10 每入口一次；不是为 disabled 组虚构 attempt 指标。

usage 按消费返回值比较；cost 是同步 `CalculateTokenCostForRequest` 返回的完整 `CostBreakdown`，计算返回后再 `MarkFinish`；两组对照相等。fixture 使用内存 sink、nil/测试 repository，没有真实 usage DB 写入或异步提交任务。**不能将同步计价完成称为等待真实异步计费入库，也没有“相同持久 DB 初态”的跨版本证据。**

本卡补跑的异常与生命周期断言：

| 要求 | 实际用例 / 已通过断言 | 边界 |
| --- | --- | --- |
| 埋点异常不重跑 handler | `TestOpsMiddlewareInstrumentationPanicIsolation`：Begin 时钟 panic、Finalize collector panic；两次各断言 handler calls=1，仍返回202和`business response` | 是受控埋点异常注入，不是所有任意异常的穷举。 |
| 不吞业务 panic | `TestOpsMiddlewareGatewaySettlement`：未写响应panic→500，已写SSE后panic→原200及`data: preserved\n\n`；两者panic标记为true、根因为gateway_panic，handler仅一次。`DisabledBehaviorIdentical`对照原状态/头/body | 业务panic仍由原Recovery处理；不声称Recovery要重新向外抛panic。源码`OpsMiddleware`的`c.Next()`也位于safeObserve之外。 |
| 观测回调异常不改I/O | `TestOpsHTTPObservationPanicsPreserveIO`：first/add/done/complete各注入panic，仍返回原n=2、原body错误及原Close错误，后续回调不被前一个panic跳过，原Closer执行一次 | 覆盖Read/Close回调隔离；不是新增body读取或catch业务handler。 |
| Close不变 | public身份8例；`ConcurrentEarlyCloseDoesNotPoisonNextResponse`、`CompletedBodyPreservesKeepAlive`、`CompressedEarlyCloseStopsReader`；重复Close不重复释放in-flight，提前Close能终止读、后续请求可用 | 使用httptest可信transport，未连接真实上游。 |
| Flush/取消不变 | 149例SSE chunks/Flush相等；已写SSE再panic保持body；`CancellationReleasesAttempt`、`HTTP2CloseDoesNotCancelOtherStream`、`TestOpsHTTPCloseUnblocksRead`通过 | 取消释放当前请求且不取消另一HTTP2流；不等同真实客户端/反向代理部署验收。 |

故上述本地子项为 PASS，**完整 AC-9 仍未执行**：尚缺相同持久 DB 初态、跨版本回放、等待真实 usage/计费任务完成及差异归档。已有全包基线失败仍保留在 §3，不用本次定向 PASS 覆盖它们。

#### 2.3.5 命令与实际输出

W2c1 当时实际执行的关键命令（Docker 内；完整日志见本节开头）：

```bash
go vet -p 1 ./internal/repository
go test -p 1 -race -parallel 1 -tags integration -v ./internal/service ./internal/repository \
  -run 'TestP0Monitoring|TestHTTPUpstreamOps|TestHTTPUpstreamConcurrentEarlyCloseDoesNotPoisonNextResponse|TestHTTPUpstream.*(Body|Cancel|Close)' -timeout 4m
go test -p 1 -race -parallel 1 -v ./internal/repository \
  -run 'TestHTTPUpstreamOps|TestHTTPUpstreamConcurrentEarlyCloseDoesNotPoisonNextResponse|TestHTTPUpstream.*(Body|Cancel|Close)' -timeout 4m
```

```text
--- PASS: TestP0MonitoringHTTPMatrix (20.81s)
--- PASS: TestP0MonitoringPluginReplay (0.05s)
ok  github.com/Wei-Shaw/sub2api/internal/service 22.245s
带 integration 的 repository：TestMain 因容器内无 Docker socket 跳过，不能当作已跑。
无 integration 的 repository：public 身份8/8及生命周期实际执行。
ok  github.com/Wei-Shaw/sub2api/internal/repository 1.268s
```

W7b 本卡实际补跑命令；仅执行已有测试，没有修改源码：

```bash
docker run --rm -v "$PWD/backend:/src" -w /src \
  -v sub2api-go-mod:/go/pkg/mod -v sub2api-go-build:/root/.cache/go-build \
  -e GOTOOLCHAIN=auto -e CGO_ENABLED=1 -e GOMAXPROCS=2 golang:1.27.2-alpine \
  sh -c 'apk add --no-cache gcc musl-dev >/dev/null && go test -p 1 -race -parallel 1 -v ./internal/server/middleware ./internal/monitoring ./internal/repository -run "TestOpsMiddleware(GatewaySettlement|DisabledBehaviorIdentical|InstrumentationPanicIsolation|BodyAndDownstreamBytes)|TestOpsHTTP(ResponseLifecycle|ObservationPanicsPreserveIO|CloseUnblocksRead)|TestHTTPUpstream(OpsPublicResponseIdentity|OpsTrackedBodyWithoutObservation|ConcurrentEarlyCloseDoesNotPoisonNextResponse|CompletedBodyPreservesKeepAlive|CancellationReleasesAttempt|CompressedEarlyCloseStopsReader|HTTP2CloseDoesNotCancelOtherStream)" -timeout 4m' \
  > /tmp/w7b-backend-evidence-race.log 2>&1
```

```text
TestOpsMiddlewareGatewaySettlement / DisabledBehaviorIdentical：PASS
TestOpsMiddlewareInstrumentationPanicIsolation / BodyAndDownstreamBytes：PASS
ok  github.com/Wei-Shaw/sub2api/internal/server/middleware 1.160s
TestOpsHTTPResponseLifecycle / ObservationPanicsPreserveIO / CloseUnblocksRead：PASS
ok  github.com/Wei-Shaw/sub2api/internal/monitoring 1.036s
public身份8/8、未观测body、提前Close/keep-alive/取消/压缩Close/HTTP2隔离：PASS
ok  github.com/Wei-Shaw/sub2api/internal/repository 1.327s
三包均无 DATA RACE；命令 exit 0。
```

W7b AC-3证据修复复测（同一Docker镜像/缓存卷、`CGO_ENABLED=1`、`GOMAXPROCS=2`；[分类与回归日志](/tmp/w7b-ac3-final-validation.log)、[149例实际M-10日志](/tmp/w7b-ac3-final-149-race.log)）：

```bash
go vet -p 1 ./internal/service ./internal/repository ./internal/monitoring
go test -p 1 -race -parallel 1 -v ./internal/service ./internal/monitoring ./internal/repository \
  -run 'TestHTTPUpstreamProxyConnectOpsEvidence|TestGatewayForwardOps|TestGatewayCompatForwardOps|TestOpenAIResponsesOps|TestOpenAIBridgeOps|TestFailureClassification|TestHTTPUpstreamOps' -timeout 4m
go test -p 1 -race -parallel 1 -tags integration -v ./internal/service \
  -run TestP0Monitoring -timeout 4m
```

```text
go vet：exit 0
SSE转换流式/缓冲×Chat/Responses×code优先/type回退/无代码：12/12 PASS
CONNECT 502/503/504、成功200不误归因、下一次发送清除旧状态：5/5 PASS
ok  github.com/Wei-Shaw/sub2api/internal/service 4.042s
ok  github.com/Wei-Shaw/sub2api/internal/monitoring (cached)
ok  github.com/Wei-Shaw/sub2api/internal/repository 1.180s
最终149例实际M-10输出轮：
--- PASS: TestP0MonitoringHTTPMatrix (20.87s)
--- PASS: TestP0MonitoringPluginReplay (0.05s)
ok  github.com/Wei-Shaw/sub2api/internal/service 22.001s
149/149 PASS；53成功/96失败；unknown=10/149=6.711409%；无DATA RACE。
```

## 3. 已知边界与失败记录

- **非本卡 · 基线 race**：宽匹配 usage cleanup 的 `TestUsageCleanupServiceExecuteTaskDashboardRecomputeError/Success` 检测到 `usage_cleanup_service_test.go` 的 recomputeCalls 测试桩读写竞争。W4a2b 精确定向 race 另行通过；不将整个宽匹配命令写成通过。日志：[最终宽匹配输出](/tmp/w4a2b-final-validation.log)。
- 前一轮全范围 service race 中，`TestRecordCyberPolicyEvent_RuntimeSnapshotRefreshFailureKeepsStaleScope` 发现 content moderation runtime snapshot 竞争；`TestOpenAIResponsesOrphanPrecheckLargeEscapedContent` 的大字符串零分配断言失败。均不在该轮 sample writer 修改范围内；后续是否修复未在 W7a 重跑确认。[该轮全包输出](/tmp/w4a2b-full-race.log)。
- 并行接线曾导致临时 helper 未定义；W4a2c1 最终真实源码 build/server race/wire 已通过。既不能继续把该次临时编译错误当当前阻塞，也不能据此抹掉其他未复核基线测试失败。
- 监控容器仅做过短时启动/cgroup memory 采样，末次三容器合计约385.25MiB；不是 RSS，未带完整业务/node/backup 代表负载，不能证明稳定 RSS≤640MB、生产容量或90d留存。
- 本地SMTP、10账号受控流量、四目标head、PG变量SQL计时和同镜像开关对照已补证于§5。实际回滚、生产Linux曲线、浏览器切换计时、生产规模Top40≤2s执行计划及跨版本镜像对照仍未执行。
- 组件测试不证明所有 P0/P1/P2 路径均已接入。尤其 P2 不包含后台完成结果或完整上游 phase/字节/账号 TTFT；不得将100个正常 fixture的非空率外推到全部流量。
- W7a 没有运行任何本节列出的应用/数据库/部署命令；新写的是取证方法与状态骨架，没有新 AC PASS。

## 4. 后续逐条 AC 的留证模板

每完成一条，应在本文件追加以下字段并引用原始输出；以下为空白模板，不替代 §2.3 已执行的后端记录。

| 字段 | 当前内容 |
| --- | --- |
| AC 编号 / 执行范围 | 未执行；按 §1 选择一个完整方法 |
| 执行时间 / 环境 / 实例数 | 未执行 / 待测 / 待测 |
| 基线与候选版本 / image digest / migration manifest | 待测 / 待测 / 待测 |
| V2 开关 / sample 开关 / retention / scrape 与看板刷新 | 待测；使用当次真实配置，不能仅复制默认值 |
| 假上游与输入 fixture / request_id / 账号或用户变量 | 待测 |
| 开始 / 请求完成 / 首次可见 / fire / resolved 时间 | 待测；依各 AC 保留所需时间点 |
| 实际命令 / 原始输出路径 | 未执行 |
| 实测值 / 阈值比较 / 异常与恢复 | 待测 |
| 结论 / 判定与复核 | 未执行 / 未执行 |

部署前检查及回滚骨架直接引用 [MONITORING_RELEASE.md](../deploy/MONITORING_RELEASE.md)。运行态受控请求必须用本次新 request_id 查到对应新行；旧样本存在、静态字符串存在或 provisioned 看板存在，均不足以填写端到端 PASS。


## 5. 最终本地镜像与运行态 AC 补证（2026-11-15）

本节为实际隔离测试记录，非生产验收/发布授权。未连接dev/生产，未push、未部署、未commit；本次仅补文档，未重新运行应用。`/tmp` 原始证据为本机留存，尚非持久发布归档。旧镜像测试与最终镜像复跑分别列出，不能把旧数值改标成新digest实测。

### 5.1 最终镜像、W6b 与清理

最终tag：`sub2api-v2smoke:worktree-final2-1794725105`。最终digest：`sha256:9bd114778a3373b008f6a04f1cf166228461f9e238f9fc323989ec4e43dad949`；linux/amd64，当前backend源码、CGO_ENABLED=0、本地0.0.4 runtime base，resources/entrypoint随源码复制，frontend未改。它是本地测试镜像，不是已发布生产镜像。

[构建输出](/tmp/v2-final2-build.log)、[镜像身份](/tmp/v2-final2-local-image.json)、[四轮报告与独立清理汇总](/tmp/v2-final2-verification-report.json)。以下四命令使用上述同一tag/digest，均真实exit 0：

```bash
python3 -B deploy/tests/monitoring-v2-deploy-smoke.py --image sub2api-v2smoke:worktree-final2-1794725105
python3 -B deploy/tests/monitoring-v2-phase-delay-smoke.py --cases wait --image sub2api-v2smoke:worktree-final2-1794725105
python3 -B deploy/tests/monitoring-v2-accounts-smoke.py --image sub2api-v2smoke:worktree-final2-1794725105
python3 -B deploy/tests/monitoring-v2-behavior-smoke.py --image sub2api-v2smoke:worktree-final2-1794725105
```

W6b项目`sub2api-v2smoke-5eeaa986`，唯一request_id=`v2smoke-1794725129708792000-7c95d4`：具体phase/account/result/model标签有值、PG精确1行、M-9 kind=request=1；bytes_in_wire=bytes_in_decoded=143、response=231B、tokens11/7。证明req_decoded接线已在最终镜像内，未把NULL当零。

- [最终W6b报告](/tmp/sub2api-v2smoke-5eeaa986-evidence/report.json)、[metrics原文](/tmp/sub2api-v2smoke-5eeaa986-evidence/metrics.txt)、[运行输出](/tmp/v2-final2-w6b.log)。
- W6b / wait / AC-5 / AC-9四项目分别为`5eeaa986 / 92194eee / b356b485 / e75f9f16`（均带`sub2api-v2smoke-`前缀）。自动清理及独立Docker查询确认容器、卷、网络、临时配置目录均为0；本地镜像与证据保留。

### 5.2 AC-1 四类 API 归因矩阵

每目标唯一ID、Prom15s抓取/10s API轮询、查询≤2s，PG按完整ID/本次observed_at精确1行，前后120s安静窗口。wait显式占槽辅助实例共享账号/DB/Redis、V2关闭且不抓取，避免占槽请求的upstream段污染目标Prom分位。

| 类别 | 镜像/唯一request_id | Prom首次指认 / 查询耗时 | PG确认 / 主导段 | 原始证据 |
| --- | --- | --- | --- | --- |
| upstream | 旧镜像；`v2smoke-delay-3000-1794718422350370000-8adb83` | 10.190s / .183s | 10.282s；upstream3005ms | [Prom](/tmp/sub2api-v2smoke-fe0f7e27-evidence/008-ac1-phase-p95.json)、[PG](/tmp/sub2api-v2smoke-fe0f7e27-evidence/ac1-pg-row.json) |
| body_read | 旧镜像；`ac1-body-1794721803369218000-2870cf` | 20.158s / .156s | 20.227s；body_read3013ms | [Prom](/tmp/sub2api-v2smoke-3eceee5b-evidence/phase-003-body-phase-p95.json)、[PG](/tmp/sub2api-v2smoke-3eceee5b-evidence/phase-body-pg.json) |
| response | 旧镜像；`ac1-response-1794722205675987000-12cc62` | 10.243s / .237s | 10.306s；response3022ms | [Prom](/tmp/sub2api-v2smoke-2f8c56a2-evidence/phase-002-response-phase-p95.json)、[PG](/tmp/sub2api-v2smoke-2f8c56a2-evidence/phase-response-pg.json) |
| wait | **最终镜像**；`ac1-wait-1794725321898359000-a9adf5` | **10.229s / .223s** | **10.292s；wait5303ms、gateway8ms** | [Prom](/tmp/sub2api-v2smoke-92194eee-evidence/phase-002-wait-phase-p95.json)、[PG](/tmp/sub2api-v2smoke-92194eee-evidence/phase-wait-pg.json)、[最终报告](/tmp/sub2api-v2smoke-92194eee-evidence/report.json)、[输出](/tmp/v2-final2-wait.log) |

前三类旧镜像digest为`sha256:8794fde5080d079221edadf68d422f1d1d6b5e328ae070b35ce730c6a444fd0a`，只wait已在最终镜像重跑。最终wait六段为2+8+5303+3+0+0=5316ms，total5319ms，误差3ms；各次PG记录采用`abs(sum-total)<=5ms`容差，属于逐段毫秒取整误差，不称精确闭合。旧镜像wait=0/gateway4980ms的未通过证据仍在[旧PG行](/tmp/sub2api-v2smoke-3eceee5b-evidence/phase-wait-pg.json)。Prom P95是桶估计，不是某请求的精确耗时；不相加P95。浏览器首次可见时间未执行。

### 5.3 AC-2 四类真实字节差异

旧镜像`8794fde5…`项目`sub2api-v2smoke-e215822c`实测，未在最终镜像重跑。每类PG exact-ID单行、M-2 live delta与PG一致；下一次抓取后Prom累计计数与live一致。[矩阵](/tmp/sub2api-v2smoke-e215822c-evidence/bytes-comparison.json)、[报告](/tmp/sub2api-v2smoke-e215822c-evidence/report.json)、[输出](/tmp/ac2-final.log)。

| 类别 | in_wire / req_upstream / resp_upstream / resp_downstream（B） | 结果 |
| --- | --- | --- |
| abort | 144 / 144 / 1037 / 746 | client_abort/client_abort_during_response；客户端实际收到698B，下游partial |
| Chat→Anthropic | 150 / 150 / 749 / 265 | success/空reason；请求内容已改写但长度偶合相等，响应封装差484B |
| 重试失败体 | 358 / 621 / 313 / 223 | 400→200两物理发送，失败体90+成功223=313B，logical attempt1、最终success |
| 网关截断 | 148 / 148 / 1025 / 90 | 真实read cap1024，mock完整8409B，网关读取limit+1后Close、上游partial，502/upstream_error/unknown；不是mock EOF |

[abort Prom](/tmp/sub2api-v2smoke-e215822c-evidence/bytes-004-abort-m2.json)、[转换Prom](/tmp/sub2api-v2smoke-e215822c-evidence/bytes-013-conversion-m2.json)、[重试Prom](/tmp/sub2api-v2smoke-e215822c-evidence/bytes-022-retry-m2.json)、[截断Prom](/tmp/sub2api-v2smoke-e215822c-evidence/bytes-031-truncate-m2.json)。目录内保留每类请求/响应bin、上游planned/raw bytes、PG status/source及M-10原始响应。旧镜像req_decoded未观测为NULL，不能以默认0冒充采集。四类能结合reason区分abort与其他；unknown不精确识别截断，差额不单独定性。浏览器看板联动未执行。

### 5.4 AC-5 最终镜像 10×20 回归

项目`sub2api-v2smoke-b356b485`：10个启动账号，各独立group/key固定选号；测量200条=180成功+20受控400，每账号20条/18成功/2失败；另20条预热不计入200。mock总220、PG完整request_id集合200行、account/key/group/status及单attempt对照通过。

client发压12.385025s、16.148534QPS；Prom抓取端点span15.004s，总QPS13.329779=200/span。两者分母不同，不能要求数值相等。每账号失败率10%，M-11 account标签恰好10、无unknown；每账号成功TTFT观测增18。P95由既有hist桶插值、与PG经验分位不同；M-11 sum与同ID PG headers_end合计在逐样本<1ms取整容差内。

[最终报告（含各账号QPS/失败率/P95与Prom原文索引）](/tmp/sub2api-v2smoke-b356b485-evidence/report.json)、[请求记录](/tmp/sub2api-v2smoke-b356b485-evidence/ac5-request-records.json)、[PG行](/tmp/sub2api-v2smoke-b356b485-evidence/ac5-pg-rows.json)、[输出](/tmp/v2-final2-ac5.log)。P0/API子项PASS，P1/P2与浏览器三图同屏未执行。

### 5.5 AC-6 / AC-7 本地资源与 SMTP

旧镜像项目`sub2api-v2smoke-c5cc9028`。AC-6四目标栈node≥3成功scrape；14项instant与30s/15s-step曲线均非空，每曲线3点，finite/非负、百分比≤100、available≤capacity。CPU10核/10.80%，内存total8319504384/available6876393472B、used17.35%；FS485421555712/410790400000B；load1/5/15=.79/.50/.37。文件系统为Docker VM `/dev/vda1`的`/etc/hostname`、`/etc/hosts`、`/etc/resolv.conf`挂载，非生产根分区证据。

AC-7通过Alertmanager v2 API注入`ac7-1794719868917624000-85594a`，真实本地SMTP DATA收件：05:18:58.713704Z firing、05:19:00.714021Z resolved。信封`alerts@example.invalid → inbox@example.invalid`；SMTP sink无relay，未配置真实密码。仅唯一合成告警route发本地邮件，其他receiver黑洞。

[报告与资源原文索引](/tmp/sub2api-v2smoke-c5cc9028-evidence/report.json)、[查询时间/URL清单](/tmp/sub2api-v2smoke-c5cc9028-evidence/host-mail-requests.jsonl)、[firing MIME](/tmp/sub2api-v2smoke-c5cc9028-evidence/ac7-firing.eml)、[resolved MIME](/tmp/sub2api-v2smoke-c5cc9028-evidence/ac7-resolved.eml)、[恢复后Alertmanager响应](/tmp/sub2api-v2smoke-c5cc9028-evidence/host-mail-038-after-resolve.json)、[输出](/tmp/ac6-ac7-first.log)。生产宿主、浏览器曲线、真实Prom规则触发至收件链、外部SMTP/TLS/auth均未执行。

### 5.6 AC-9 最终镜像协议与持久计费

项目`sub2api-v2smoke-e75f9f16`，同一最终digest，V2 metrics/samples=false/true，PG全库在首条流量前克隆、独立Redis/data。启动后六张业务表初态相同。JSON/SSE/abort各两组一次；上游headers/body、响应status/body、SSE帧、HTTP chunks一致，每组物理attempt1、usage1。

| 场景 | 响应 / SSE帧 / HTTP chunks | tokens / actual cost（两组一致） |
| --- | --- | --- |
| JSON | 223B / 0 / 0 | 11/7 / .000138 |
| SSE | 749B / 6 / 6，约.2s逐帧可见 | 11/7 / .000138 |
| abort | 已读698B / 5 / 5，message_delta后RST | 11/7 / .000138 |

最终余额均999.99958600。headers仅归一化Date墙钟和生成x-client-request-id UUID，保留字段/重复次数并验证UUID到usage client:UUID关系。created_at、duration_ms、first_token_ms抖动单列；观测新增字段off NULL，on body_bytes117/115/117、gateway_latency_ms27/2/3。off无V2指标/sample0，on sample3，abort样本为client_abort_during_response。

[最终逐项对照/差异](/tmp/sub2api-v2smoke-e75f9f16-evidence/ac9-comparison.json)、[最终报告](/tmp/sub2api-v2smoke-e75f9f16-evidence/report.json)、[SSE off原始响应](/tmp/sub2api-v2smoke-e75f9f16-evidence/ac9-sse-off-response.json)、[SSE on原始响应](/tmp/sub2api-v2smoke-e75f9f16-evidence/ac9-sse-on-response.json)、[on PG样本](/tmp/sub2api-v2smoke-e75f9f16-evidence/ac9-on-samples.json)、[输出](/tmp/v2-final2-ac9.log)。目录内每例两组完整upstream/response/body.bin/usage均保留。

客户端RST后两组均继续drain上游并写usage；不将其解释为“取消立即终止上游”。镜像层故障collector/sink/时钟panic注入、业务panic注入、Go精确Close/Flush计数、HTTP2/context取消隔离未执行。§2.3.4/2.3.5的W2c1异常/生命周期race为既有组件证据，不能冒充本镜像重跑或完整AC-9异常验收。

### 5.7 AC-10 全量指标族预算

最终镜像项目`sub2api-v2smoke-0b84ad4b`，单实例/单账号/成功负载，app/Prometheus/node/Alertmanager四目标15s抓取、测前各4次成功。2026-11-15 06:53:50Z head gauge/current均2905，06:57:54Z均2999。`count by(__name__)({__name__=~".+"})`覆盖719→721名字，用`/api/v1/metadata` TYPE合并hist bucket/sum/count及summary quantile父名/sum/count为622→624族；全局最大`alertmanager_notification_latency_seconds=128<2000`，sub2api最大`request_phase_seconds=112`。无TYPE的up/scrape自带指标及ALERTS/ALERTS_FOR_STATE独立保留，TYPE/名称冲突fail-fast；不只统计sub2api。

[报告](/tmp/sub2api-v2smoke-0b84ad4b-evidence/report.json)、[全量名字计数](/tmp/sub2api-v2smoke-0b84ad4b-evidence/010-after-probe-all-names.json)、[metadata](/tmp/sub2api-v2smoke-0b84ad4b-evidence/011-after-probe-metadata.json)、[head](/tmp/sub2api-v2smoke-0b84ad4b-evidence/009-after-probe-head.json)、[当前TSDB](/tmp/sub2api-v2smoke-0b84ad4b-evidence/012-after-probe-tsdb.json)、[输出](/tmp/v2-final2-ac10.log)。旧LF/CRLF等release回归与本次cardinality是不同验收；最终digest本地预算通过，不证明生产代表负载或多实例容量上界。

### 5.8 AC-8 最终镜像 Drill1

项目`sub2api-v2smoke-ec416fbc`，与§5.1相同final2 digest。Drill1 PASS：停用V2但保留V1请求计数，持续流量后94.801s进入firing（<5min），恢复V2后46.869s resolved；恢复后空闲>3min通过，cleanup=true。15s scrape/evaluation；未配置Alertmanager通知，不代表邮件验收。Drill2缺少稳定phase-only freeze注入，未执行；完整AC-8为PARTIAL。

[最终报告](/tmp/sub2api-v2smoke-ec416fbc-evidence/report.json)、[时间线](/tmp/sub2api-v2smoke-ec416fbc-evidence/ac8-timeline.json)。
