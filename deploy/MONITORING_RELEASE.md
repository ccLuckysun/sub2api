# Monitoring V2 W6a release checks

本卡仅提供本地检查和回滚 dry-run 骨架。不构建镜像、不执行部署或 push、不连接生产。W4a2c1 的 build/server race/wire 最终验证已通过；W6a 不修改 backend 或监控规则/看板。

## Deployment prerequisites (2026-11-15)

- `compose-deploy.sh --monitoring` 默认启用 V1 metrics、V2 ops metrics 与 PG samples，并自动生成 `GRAFANA_PG_PASSWORD`；服务就绪后自动执行 `monitoring/sql/grafana-ro.sql`。`remote-deploy.sh` 在应用启动后也执行同一 SQL。
- 当前 V2 看板为六张：`01-overview.json`、`02-requests.json`、`03-nodes.json`、`04-users.json`、`05-host.json`、`06-request.json`，UID 分别为 `sub2api-v2-overview`、`sub2api-v2-requests`、`sub2api-v2-nodes`、`sub2api-v2-users`、`sub2api-v2-host`、`sub2api-v2-request`，面板数 14/7/8/7/23/10。旧的 `02-chain.json` 已由 `02-requests.json` 取代（历史记录见 `MONITORING_LOCAL.md`）。Compose 逐文件只读挂载到 `/var/lib/grafana/dashboards/v2/`，provider 不整目录挂载。
- 手工 `docker compose` / `--no-start` 部署需自行执行 `monitoring/sql/grafana-ro.sql`，否则 Grafana PG 数据源无法登录。
- 部署前建议先跑 `python3 deploy/tests/verify-migrations.py` 与 `python3 deploy/tests/verify-instrumentation.py /path/to/main`；前者校验 49 组历史编号冲突 ack 基线，后者仅为二进制辅助检查。
- 正式发布仍需构建并 push `sun/sub2api:0.0.5-v2mon`，按 `deploy-us1.sh`/`remote-deploy.sh` 流程部署；本仓库未执行 push、生产部署或外部 SMTP。


## Dashboard deployment safety

当前 **只保留六张 canonical 看板**。生成器和 2026-11-15 重设计明确用 `02-requests.json` 取代旧 `02-chain.json`；不能补一个不存在的 chain bind mount，也不能把历史链路面板当作第七张看板。旧链路指标/规则仍可供现有看板查询，不代表应恢复旧 JSON。

- `compose-deploy.sh --monitoring`（包括 `--no-start`）、生成的 `sub2api-compose up/start/restart` 与 `deploy-us1.sh` 在启动/构建/上传前运行 `monitoring/grafana/dashboard-preflight.sh --source`：递归检查所有 `.json`，任何零字节文件都打印完整路径并非零退出，缺少六个必需文件或存在旧 chain 也失败。同时用 `--metadata` 检查 provisioning。两种模式都递归拒绝任意大小的 `._*` / `.DS_Store` 文件或目录。此检查只读，不自动忽略或清理文件。
- Grafana 的 entrypoint 在执行 `/run.sh` **之前**先检查挂载的 `/etc/grafana/provisioning` 元数据，再对真实 `/var/lib/grafana/dashboards/v2` 执行 `--volume` 检查，因此即使绕过部署脚本直接 `docker compose up`，provisioning 元数据、持久卷内非空 AppleDouble JSON 或历史空文件也会阻断 Grafana 进程启动并明确报错。全部通过后原样透传启动参数给 `/run.sh`。其他服务可能已经启动；这不是全栈事务。六个 JSON 使用 `create_host_path: false`，缺少源文件不会被 Docker 自动创建成目录。
- 普通 bind mount 中被当前非空源文件遮蔽的同名卷文件不会被 provider 读取；检查针对 provider 的实际可见目录。未挂载的历史 `02-chain.json`（空或非空）一定报错。非空历史内容须人工检查/迁移，不自动删除。
- 不使用 `sed -i`、`mv` 或“临时文件 + rename”直接修改已挂载 dashboard JSON；这些操作会替换 inode，30s provider 扫描仍可能读取旧文件。常规修改应改生成器并重新生成、运行 `--check` 与部署测试，再 `./sub2api-compose up -d --no-deps --force-recreate grafana`。生成器当前对已有文件是原 inode 写入，正常更新最多 30s 重载；写入期间不要重启 Grafana。部署脚本会显式重建 Grafana 以兼容安装器/打包工具替换 inode，保留持久卷，绝不 `down -v`。
- 所有监控发布包、配置备份与 snapshot tar 使用 `COPYFILE_DISABLE=1`，并排除 `._*` / `.DS_Store`；远端解包也排除旧包内这类元数据。不要通过原地 JSON 文本替换修复环境差异。

### SQL、空值语义与静态检查

请求明细的模型变量用查询返回的普通 `全部 / __all__` 选项（`includeAll: false`），而非 Grafana 的自定义 All 值；五个 SQL 面板统一保留 `${model:sqlstring}`。这样哨兵和具体模型都经过 SQL 字符串转义，不依赖自定义 All 绕过格式化的行为，也不依赖不同 PostgreSQL 插件默认插值是否额外包引号。模型名含单引号也必须按普通字符串匹配，不能拼接成 SQL 片段。用户、API 密钥的数字 `-1` 与 search textbox 的 `:sqlstring` 保持不变。

空数据按面板含义显示 `无流量 / 无失败 / 无异常请求 / 无请求`；数据完整性继续使用 `未采集`，coverage 的 `-1 → 无样本` 映射保持不变。noValue 不是查询错误的判定依据，仍须检查 Grafana 面板错误提示和 PG 日志。入向/出向带宽、上游放大比和 P95 TTFT 使用当前 `$__range`；账号切换保留现有 `increase(...[5m])` 的五分钟计数语义，与标题“账号切换 / 5m”一致。

```sh
python3 deploy/monitoring/grafana/lint-dashboards.py
python3 -B -m unittest discover -s deploy/monitoring/grafana -p 'test_lint_dashboards.py'
python3 deploy/monitoring/grafana/build-dashboards.py --check
python3 deploy/tests/monitoring-v2-dashboard-test.py --static-only
python3 -I deploy/tests/monitoring-deploy-test.py MonitoringDeploymentSafety
```

上述检查已接入 `.github/workflows/grafana-lint.yml`；`--static-only` 明确跳过需要 Docker 的 PromQL 执行，不代表实机验收通过。运行态还须在每张看板默认“全部”和具体用户/API 密钥/模型（包括含单引号的模型）下刷新，检查所有面板无查询错误，Grafana provisioning/PG 日志无相关报错，并通过 `/api/search?type=dash-db` 核对六张 canonical UID。不要为验收向生产灌入模拟样本。

### 历史空文件的人工清理（不是部署脚本的自动行为）

本地先用 `sh deploy/monitoring/grafana/dashboard-preflight.sh --source deploy/monitoring/grafana/dashboards` 检查源码。卷侧错误从 `./sub2api-compose logs grafana` 获取完整路径；确认实际项目的 `grafana_data` 卷名，停止 Grafana 后，以只读 helper 挂载该卷查看 `/var/lib/grafana/dashboards/v2`。**禁止删除整个 dashboards 目录或卷。**

仅在确认 `dashboards/v2/02-chain.json` 是历史零字节普通文件、不是 symlink，且不是当前挂载源时，操作人员可通过单独 helper 将同一卷挂至 `/volume`，运行下述严格限路径检查/清理。`GRAFANA_VOLUME` 必须是人工核实的实际卷名，helper 镜像须已在本机存在（`--pull=never`）；不对非空文件、其他文件或卷执行删除。此命令会修改卷，需明确授权后才能执行。

```sh
# 只读检查；先停止当前项目 Grafana，避免 provider 与清理并发。
docker run --rm --pull=never --network none --read-only \
  --mount "type=volume,src=${GRAFANA_VOLUME:?set verified volume name},dst=/volume,readonly" \
  --entrypoint sh grafana/grafana:13.3.1 -ec \
  'find /volume/dashboards/v2 -type f -name "*.json" -size 0c -print'
# 仅清理明确授权的那个历史空文件；其他错误一律停止并人工处理。
docker run --rm --pull=never --network none --read-only --user 0 \
  --mount "type=volume,src=${GRAFANA_VOLUME:?set verified volume name},dst=/volume" \
  --entrypoint sh grafana/grafana:13.3.1 -ec \
  'p=/volume/dashboards/v2/02-chain.json; test ! -L "$p"; test -f "$p"; test ! -s "$p"; rm -- "$p"'
./sub2api-compose up -d --no-deps --force-recreate grafana
```

本文只给出处理策略；本次代码修改/本地 fixture 测试不执行上述 Docker 清理、不运行远端部署。

## Binary check (auxiliary only)

```sh
python3 deploy/tests/verify-instrumentation.py /path/to/main
```

脚本只读取二进制，要求三个字符串：`sub2api_request_phase_seconds`、`sub2api_gateway_requests_total`、`sub2api_upstream_ttft_seconds`。缺任一字符串非零退出。日志明确“仅辅助、不证明运行态”；它不能证明功能开关已开启、请求路径已埋点或样本已落库。受控请求与运行态烟测属于后续任务。

实际用本机 `cc` 编译了含三个 V2 字符串和仅含 legacy 字符串的两个 C 二进制；未构建应用/生产镜像：

```text
含 V2：仅辅助、不证明运行态…… PASS: contains all 3 V2 metric strings
EXIT=0
不含 V2：FAIL: missing V2 metric string: sub2api_request_phase_seconds
         FAIL: missing V2 metric string: sub2api_gateway_requests_total
         FAIL: missing V2 metric string: sub2api_upstream_ttft_seconds
EXIT=1
```

## Migration inventory and baseline

```sh
# 默认只校验，不写文件、不连数据库。
python3 deploy/tests/verify-migrations.py
# 新建或显式更新基线：仓库存在历史冲突，必须带 ack。
python3 deploy/tests/verify-migrations.py --write --acknowledge-existing-number-conflicts
# 临时目录/发布包也可独立检查：
python3 deploy/tests/verify-migrations.py --migrations /path/to/migrations --manifest /path/to/manifest.json
```

`deploy/migrations.manifest.json` 记录完整文件名、版本前缀、SHA256 及历史冲突的完整文件组。算法与迁移器一致：对 UTF-8 SQL 去除首尾空白后求 SHA256；按完整文件名排序，保留历史 `006b` 等字母后缀。

默认校验拒绝同名异内容、缺失/新增文件、编号冲突组新增/改变，以及 manifest 重复文件名或冲突记录不自洽。历史冲突仅在显式 ack 后成为基线，后续仍逐组打印 WARN。`--write` 没有 ack 时不能写入带冲突的基线；ack 不能在默认校验中临时豁免新冲突。基线更新会原子替换 manifest，应同时检查其 diff；不会改写任何 SQL。

实际仓库输出：

```text
WROTE: deploy/migrations.manifest.json
WARN: acknowledged number conflict 249: <完整两个文件名见下表>
PASS: 304 migrations match; maximum numeric version=252; acknowledged conflict groups=49
```

对仓库 SQL 的临时副本实际追加注释、校验、恢复（backend 原文件未改）：

```text
FAIL: CHANGED (same name, different content): 251_ops_v2_samples.sql baseline=e85ba9ce2b8043a2cc24aaaf107b1ce54a0ae413ecae22f824d92cc485ad8b23 actual=<变化后的 SHA256>
EXIT=1
恢复后：PASS: 304 migrations match; maximum numeric version=252; acknowledged conflict groups=49
EXIT=0
```

当前已 ack 的 49 组历史编号冲突如下。这些完整文件组也逐个写入 manifest；新冲突不会自动加入。

| 版本前缀 | 完整文件组 |
| --- | --- |
| 006 | `006_add_users_allowed_groups_compat.sql`、`006_fix_invalid_subscription_expires_at.sql` |
| 028 | `028_add_account_notes.sql`、`028_add_usage_logs_user_agent.sql`、`028_group_image_pricing.sql` |
| 029 | `029_add_group_claude_code_restriction.sql`、`029_usage_log_image_fields.sql` |
| 033 | `033_add_promo_codes.sql`、`033_ops_monitoring_vnext.sql` |
| 034 | `034_ops_upstream_error_events.sql`、`034_usage_dashboard_aggregation_tables.sql` |
| 036 | `036_ops_error_logs_add_is_count_tokens.sql`、`036_scheduler_outbox.sql` |
| 037 | `037_add_account_rate_multiplier.sql`、`037_ops_alert_silences.sql` |
| 045 | `045_add_accounts_extra_index.sql`、`045_add_announcements.sql`、`045_add_api_key_quota.sql` |
| 046 | `046_add_sora_accounts.sql`、`046_add_usage_log_reasoning_effort.sql` |
| 047 | `047_add_sora_pricing_and_media_type.sql`、`047_add_user_group_rate_multipliers.sql` |
| 052 | `052_add_group_sort_order.sql`、`052_migrate_upstream_to_apikey.sql` |
| 053 | `053_add_security_secrets.sql`、`053_add_skip_monitoring_to_error_passthrough.sql` |
| 054 | `054_drop_legacy_cache_columns.sql`、`054_ops_system_logs.sql` |
| 060 | `060_add_gemini31_flash_image_to_model_mapping.sql`、`060_add_usage_log_openai_ws_mode.sql` |
| 070 | `070_add_scheduled_test_auto_recover.sql`、`070_add_usage_log_service_tier.sql` |
| 071 | `071_add_gemini25_flash_image_to_model_mapping.sql`、`071_add_usage_billing_dedup.sql` |
| 075 | `075_add_usage_log_upstream_model.sql`、`075_map_haiku45_to_sonnet46.sql` |
| 081 | `081_add_group_account_filter.sql`、`081_create_channels.sql` |
| 095 | `095_channel_features.sql`、`095_subscription_plans.sql` |
| 101 | `101_add_account_stats_pricing.sql`、`101_add_balance_notify_fields.sql`、`101_add_channel_features_config.sql`、`101_add_payment_mode.sql` |
| 102 | `102_add_balance_notify_threshold_type.sql`、`102_add_out_trade_no_to_payment_orders.sql` |
| 125 | `125_add_channel_monitors.sql`、`125_add_group_rpm_limit.sql` |
| 126 | `126_add_channel_monitor_aggregation.sql`、`126_add_user_rpm_limit.sql` |
| 127 | `127_add_user_group_rpm_override.sql`、`127_drop_channel_monitor_deleted_at.sql` |
| 134 | `134_affiliate_ledger_audit_snapshots.sql`、`134_image_generation_group_controls.sql` |
| 135 | `135_allow_email_oauth_provider_types.sql`、`135_content_moderation.sql` |
| 136 | `136_add_dingtalk_provider_type.sql`、`136_remove_ops_retry_replay.sql`、`136_usage_log_image_size_metadata.sql` |
| 145 | `145_deleted_api_key_audit.sql`、`145_ops_metrics_ttft_sample_count.sql` |
| 151 | `151_account_autopause_expiry_index_notx.sql`、`151_channel_monitor_jitter.sql` |
| 154 | `154_account_spark_shadow.sql`、`154_add_ops_system_logs_api_key_id.sql` |
| 158 | `158_add_group_peak_rate_multiplier.sql`、`158_enable_grok_media_generation_groups.sql` |
| 160 | `160_add_user_frozen_balance.sql`、`160_batch_image_provider_refs.sql` |
| 172 | `172_composite_model_routes.sql`、`172_video_per_second_billing_metadata.sql` |
| 174 | `174_add_usage_log_long_context_billing.sql`、`174_add_usage_logs_api_key_latest_ip_index_notx.sql`、`174_group_web_search_price_per_call.sql` |
| 175 | `175_add_ops_system_logs_host.sql`、`175_default_openai_long_context_billing.sql` |
| 181 | `181_group_duplicate_operation_id.sql`、`181_prompt_audit.sql` |
| 186 | `186_alipay_mobile_precreate_deep_link.sql`、`186_group_auth_cache_image_generation.sql` |
| 194 | `194_add_usage_log_upstream_response_model.sql`、`194_channel_monitor_v2.sql` |
| 195 | `195_add_usage_log_upstream_model_mismatch_index_notx.sql`、`195_channel_monitor_mode.sql` |
| 225 | `225_backfill_codex_fingerprint_seed.sql`、`225_channel_model_time_pricing.sql` |
| 226 | `226_add_usage_log_effective_model_indexes_notx.sql`、`226_channel_monitor_quota_mode.sql` |
| 231 | `231_add_usage_log_native_compaction_v2.sql`、`231_add_usage_log_requested_reasoning_effort.sql`、`231_user_restrict_public_groups.sql` |
| 232 | `232_add_usage_log_upstream_request_id.sql`、`232_channel_cache_write_1h_pricing.sql`、`232_group_force_openai_fast.sql`、`232_group_reasoning_effort_over_limit.sql` |
| 233 | `233_add_usage_log_upstream_request_id_index_notx.sql`、`233_group_free_openai_fast.sql` |
| 234 | `234_channel_max_reasoning_effort_multiplier.sql`、`234_group_codex_models_manifest_config.sql` |
| 238 | `238_opencode_go_platform.sql`、`238_purge_unlimited_user_platform_quotas.sql` |
| 241 | `241_add_payment_order_bonus_amount.sql`、`241_add_typesafe_platform.sql` |
| 242 | `242_drop_platform_check_constraints.sql`、`242_group_i18n.sql` |
| 249 | `249_add_usage_logs_api_key_cost_index_notx.sql`、`249_mcp_oauth.sql` |

## Rollback skeleton (dry-run only)

在更新前将当前本地镜像的身份写入一个新记录文件：

```sh
python3 deploy/rollback.py --image sun/sub2api:0.0.4 \
  --record /tmp/sub2api-before-release.json --dry-run \
  --compose-file deploy/docker-compose.yml \
  --compose-file deploy/docker-compose.monitoring.yml
```

按实际项目附加 `--project-name`、重复的 `--env-file` 与 `--compose-file`，保持原部署的文件顺序。记录路径必须尚不存在；脚本拒绝覆盖历史记录。此脚本只有 dry-run 模式。

脚本只执行 `docker image inspect`，记录输入 tag、镜像 ID、可用的 registry digest、已有 tags，并生成 `rollback-<UTC timestamp>` tag 名。若本地镜像没有 registry digest，会明确输出 unavailable，不把 image ID 当成 registry digest。实际 tag 尚未创建，JSON 中 `tag_created=false`。

实际本地 dry-run 输出：

```text
DRY-RUN ONLY: no tag/push/deploy commands executed
CURRENT REFERENCE: sun/sub2api:0.0.4
LOCAL IMAGE ID: sha256:764d49b5928e79b6eae3955c7bcf165dd264a8d8bd944d4e6aebc358c829f0bb
REGISTRY DIGESTS: sun/sub2api@sha256:764d49b5928e79b6eae3955c7bcf165dd264a8d8bd944d4e6aebc358c829f0bb
PLANNED ROLLBACK TAG: sun/sub2api:rollback-20261115T033002114824Z (not created)
PLAN: docker image tag <recorded image ID> <rollback tag>
PLAN: docker compose <original files> -f <planned override> config --quiet
PLAN: docker compose <original files> -f <planned override> up -d --no-deps sub2api
```

计划的 override 内容保存在记录 JSON 的 `override_content`，文件本身不会写出。它选择 rollback tag，开启 V1 metrics，关闭 `MONITORING_OPS_METRICS_ENABLED` / `MONITORING_OPS_SAMPLES_ENABLED`。真正执行恢复时还需保存旧镜像，写出 override 并独立执行计划命令；dry-run 不代表已经创建可用 rollback tag。脚本不回滚数据库、不删除数据卷、不执行 push。

后续实际回滚的检查项（本卡未执行）：V1 `/metrics` 可抓取；回滚时间后样本停止新增（允许排空在途队列后核对）；V2 面板显示“未采集/埋点未启用”，不得将缺数据显示为健康。旧 V1 看板回滚步骤见 MONITORING_LOCAL.md。既有清理任务是否继续按保留策略执行应另行核对，不依赖回滚骨架证明。

## Reproducible local tests and limits

```sh
python3 deploy/tests/monitoring-v2-release-contract-test.py
```

实际输出 `Ran 4 tests ... OK`，覆盖多个分支：历史 WARN、无 ack 禁止写入、新增/变化冲突 FAIL、同名改内容 FAIL、缺失/新增 FAIL、恢复 PASS、显式重建基线 PASS+WARN、重复 manifest 文件名 FAIL；真实编译二进制正反例；rollback 记录身份且仅调用 inspect，拒绝覆盖记录，未执行 tag/config/up，也未写出 override。

已验证仓库基线与临时发布包副本；未验证迁移在真实部署库中的执行状态或 schema 兼容性。未执行镜像构建、push、应用部署、实际回滚、运行态 V2 烟测。迁移 251/252 的原 checksum、go.mod/go.sum 未改变。
