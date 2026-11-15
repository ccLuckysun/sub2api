# Sub2API 观测看板 V2 · W5（M4 监控栈 as code）任务卡

| 项 | 内容 |
| --- | --- |
| 执行方 | herdr `t14`（执行2） |
| 契约 | `docs/MONITORING_V2_API.md` §6–§9、附录 A |
| 计划 | `docs/MONITORING_V2_DEV_PLAN.md` §4 M4（4.1–4.7） |
| 布局 | `docs/dashboard-v2-mockup.html` |
| 边界 | 只做 as code 与本地验证；**不连接生产、不执行线上部署、不写真实 SMTP 密码** |

## 1. 目标

把 Prometheus / Alertmanager / Grafana / PostgreSQL 只读数据源全部落成可复现的代码与脚本，供后续 M5 部署契约直接使用。V2 指标已由 M1 注册（`sub2api_request_phase_seconds` 等 11 族），本卡不修改后端代码。

## 2. 文件范围（只允许）

```
deploy/monitoring/prometheus.yml.tmpl
deploy/monitoring/render-prometheus-config.sh
deploy/monitoring/prometheus/rules/slo.yml
deploy/monitoring/prometheus/rules/chain.yml
deploy/monitoring/prometheus/rules/infra.yml
deploy/monitoring/prometheus/rules/*_test.yml          # promtool 单元测试
deploy/monitoring/alertmanager/alertmanager.yml.tmpl
deploy/monitoring/alertmanager/templates/*.tmpl
deploy/monitoring/sql/grafana-ro.sql
deploy/monitoring/grafana/provisioning/datasources/postgres.yml
deploy/monitoring/grafana/provisioning/dashboards/dashboards.yml
deploy/monitoring/grafana/build-dashboards.py
deploy/monitoring/grafana/dashboards/0[1-5]-*.json
deploy/monitoring/grafana/dashboards/backup/          # 旧 V1 看板备份
deploy/docker-compose.monitoring.yml
deploy/monitoring.env.example
deploy/MONITORING_LOCAL.md
deploy/README.md（仅监控相关章节）
deploy/tests/monitoring-*.py / *.sh（只增测试）
```

不得修改 `backend/**`、`docs/MONITORING_V2_API.md`、`docs/MONITORING_V2_DEV_PLAN.md`、`docs/dashboard-v2-mockup.html`、`docs/MONITORING_V2_TASKS.md`。

## 3. 契约要求

### 3.1 Prometheus（4.1）

- `scrape_interval: 15s`、`scrape_timeout: 10s`；jobs：`sub2api`、`prometheus`、`node`、`alertmanager`；`rule_files: /etc/prometheus/rules/*.yml`；alertmanager target `alertmanager:9093`。
- retention 默认 90d / 8GB，可用 `PROM_RETENTION_TIME` / `PROM_RETENTION_SIZE` 覆盖；node-exporter 由 profile 启用。
- 模板渲染脚本必须可重复执行、缺变量 fail-fast、输出可被 `promtool check config` 通过。

### 3.2 告警（4.2）

- 18 条规则逐条按 API §9 的名称、表达式、`for`、`severity` 落地；`Sub2apiInstrumentationStalled` 必须使用 API §9 的按实例集合差写法（V1 流量 `unless` phase 增长），不能退化为全局计数。
- `promtool check rules` + `promtool test rules`：每条规则至少覆盖触发/恢复，第 6 条额外覆盖全缺失、单实例缺失、单实例停写、空闲、正常慢流式、多实例混合；第 18 条覆盖 unknown 比例门槛。
- 规则注释保留 route 白名单维护提示。

### 3.3 Alertmanager（4.3）

- 邮件 SMTP 全部来自 `monitoring.env`（`ALERT_SMTP_*`），仓库中不得出现真实密码；收件人默认 `zsun40226@gmail.com`；`send_resolved: true`；critical 1h、warning 4h 重复。
- 模板可被 `amtool check-config` 或等价方式验证；本地 SMTP 验证可选，外发必须不做。

### 3.4 Compose / 资源（4.4）

- alertmanager 容器、规则挂载、PG 环境变量（`GRAFANA_PG_PASSWORD`）、node profile、备份 profile 与现有栈兼容；`docker compose config` 通过。
- 内存账：alertmanager 64m；监控栈稳定 RSS ≤640MB、容器 limit 合计 ≤896m 的配置依据写入文档；不得改动业务容器资源。
- 不破坏现有 `compose-deploy.sh` 的 `--monitoring` / `--host-metrics` / `--backup` 行为；如需新增变量，必须给默认值。

### 3.5 Grafana 只读 SQL（4.5）

- `grafana_ro` 角色、`default_transaction_read_only=on`、`statement_timeout=15s`、最小表/列授权按 API §7.1；不得修改应用账号。
- 数据源 `sub2api-pg` / `Sub2API Postgres`，url `postgres:5432`，database `sub2api`，user `grafana_ro`，password `${GRAFANA_PG_PASSWORD}`，jsonData 按 API §7.1。
- 提供可执行的本地验收脚本或文档步骤：只读查询成功；关掉 read-only 后 DML/DDL 仍因权限失败；`pg_sleep(20)` 15s 超时。

### 3.6 看板生成器（4.6）

- `build-dashboards.py` 一行查询一项，生成 5 张 JSON；固定 uid：`sub2api-v2-overview`、`sub2api-v2-chain`、`sub2api-v2-nodes`、`sub2api-v2-users`、`sub2api-v2-host`。
- 面板数：16 / 19 / 9 / 6 / 15；02 刷新 10s，其余 Prom 面板 30s，PG 面板 1m；默认 6h、浏览器时区；`editable:false`、`allowUiUpdates:false`。
- 每个面板必须有 description（口径 + 误读风险）；缺序列用 `or vector(0)` 并显示“未采集”，**禁止** `or vector(1)`。
- 布局与面板顺序以 `docs/dashboard-v2-mockup.html` 为准；生成器支持 `--check`（生成结果与仓库 JSON 一致，不允许手改 JSON）。
- 变量 SQL 按 API §8.2；Prom 堆叠/占比必须 `phase!="headers_end"`；Top 面板统一 `LIMIT 40` 并写“Top 40”。

### 3.7 旧看板下线（4.7）

- 先将现有 `sub2api-overview.json` 备份到 `deploy/monitoring/grafana/dashboards/backup/`，再从 provisioning 目录移除；最终只加载 5 张 V2 看板。
- 文档写明回滚方式（恢复旧 JSON 并重启 Grafana）。

## 4. 验收命令（必须实际执行并贴输出）

```sh
promtool check config deploy/monitoring/prometheus.yml   # 渲染后的配置
promtool check rules deploy/monitoring/prometheus/rules/*.yml
promtool test rules deploy/monitoring/prometheus/rules/*_test.yml
docker compose -f deploy/docker-compose.monitoring.yml config
python3 deploy/monitoring/grafana/build-dashboards.py --check
python3 - <<'PY'  # 校验 5 个 uid、面板数、刷新、description、无 or vector(1)
...
PY
```

如本机缺少 promtool/amtool，可用 `docker run --rm -v "$PWD/deploy/monitoring:/m" prom/prometheus:v3.16.0 ...` / `prom/alertmanager` 容器执行；不得因此跳过验证。

## 5. 完成定义

- 上述命令全部通过；报告文件清单、规则测试矩阵、面板数/刷新/description 检查、compose config 结果、内存账、已知未验证项。
- 不 commit、不 push、不连接生产；所有敏感值用占位符或 `monitoring.env`。
