# Sub2API 监控栈本地部署

V2 默认监控部署包含六个独立的常驻容器：`sub2api`、`postgres`（PostgreSQL）、`redis`、`grafana`、`prometheus`、`alertmanager`。数据库、缓存、应用、可视化与指标存储均独立运行。Grafana 自动配置纯 Prometheus 数据源（`sub2api-prometheus`，无 PostgreSQL 数据源）与三张 V2 看板（挂载到 `/etc/grafana/dashboards/v2`）；实时请求流与单请求详情位于 Sub2API 管理后台 `/admin/ops#realtime-requests`（由 Redis 环形缓冲驱动）。

> **纯 Prometheus 架构更新**：当前生成器输出 3 张纯 Prometheus 看板（`01-overview.json` / `02-nodes-chain.json` / `03-host.json`，面板数 14/11/23，全部挂在 `/etc/grafana/dashboards/v2`，独立于 `grafana_data` 持久卷），不再配置 Grafana PG 数据源或 `grafana-ro.sql`。下文 2026-11-15 及更早的六看板/PG 记录保留作为历史参考。

需要 Docker Compose ≥2.35。启用快照扩展时还需 Docker Engine ≥26（API ≥1.45）。部署脚本需要 Bash；Prometheus 配置渲染脚本兼容 POSIX `/bin/sh`，不再依赖 envsubst 或临时联网安装 gettext。

## 从本地构建并更新 us1

在仓库根目录执行：

```bash
./deploy-us1.sh 0.0.4
```

版本号按本次发布调整。脚本继续完成 linux/amd64 构建、压缩、上传、数据库 pg_dump、运行中旧镜像备份、应用更新与健康检查，并默认部署 Prometheus、Grafana、Alertmanager。脚本从现有应用的 Compose labels 读取**项目名称、全部配置文件及环境文件**，保留 PostgreSQL、Redis、Caddy 及现有数据和业务 `.env`，不会重建它们。

若只更新应用：

```bash
./deploy-us1.sh 0.0.4 --app-only
```

已有监控的项目会继续保留监控配置；未启用监控的项目不需要 `monitoring.env`。脚本结束后输出数据库备份位置和可直接执行的应用回滚脚本。应用回滚不自动回退数据库 schema；数据库备份用于必要时人工恢复。

服务器上的 Grafana 仅绑定 `127.0.0.1:3000`。在本机执行 `ssh -L 3000:127.0.0.1:3000 us1`，再打开 <http://127.0.0.1:3000>。用户名 `admin`，密码保存在**服务器 Compose 目录**的 `monitoring.env`（0600），首次自动生成，重复部署不重置。已有 Grafana 数据卷的管理员密码以卷内已保存的密码为准。

## 新服务器或独立目录部署

在目标部署目录运行仓库中的准备脚本：

```bash
/path/to/sub2api/deploy/docker-deploy.sh --monitoring --image sun/sub2api:0.0.4
```

`--image` 应指定与本次监控配置同版的应用镜像。未指定时沿用原 Compose 的默认镜像，若它未暴露新版指标，启动检查会报错而不会宣告部署成功。首次安装会生成 PostgreSQL/JWT/TOTP 等业务凭据、Grafana 密码及持久化目录；已有 `.env` 和 Compose 配置会保留。新服务器使用私有镜像时须先登录镜像仓库。

不传参数仍保留原脚本的三容器配置准备功能。加 `--start` 可启动三容器基础栈。`--monitoring --no-start` 可以只准备并校验六容器配置。

## 当前源码开发环境

已有本地业务 `.env` 时：

```bash
cd deploy
./compose-deploy.sh --monitoring --compose-file docker-compose.dev.yml --project-name deploy
```

之后日常启动/停止不需要再记 Compose 参数，直接用 `./dev.sh`（首次准备仍用上面的命令）：

```bash
./dev.sh up        # 启动全部（源码构建 + Grafana/Prometheus），结束打印访问地址
./dev.sh status    # 容器、健康状态、端口
./dev.sh stop      # 停止容器，保留数据
```

其余命令（`restart` / `rebuild` / `logs` / `shell` / `down` / `reset --yes`）见 `README.md` 的 “Local Docker Development”。

没有 `.env` 时先从 `.env.example` 复制并设置 PostgreSQL 密码。不要覆盖已有私有 `monitoring.env`。旧版本曾把本地数据库密码放在 `monitoring.env`，脚本仍支持该文件；新的部署优先把业务变量保存在 `.env`，监控变量保存在 `monitoring.env`，Compose 按此顺序读取两者。

脚本生成 `sub2api-compose`，保存完整项目名、配置文件列表、扩展 profiles 及环境文件顺序。后续操作统一使用它：

```bash
./sub2api-compose ps
./sub2api-compose logs -f sub2api
./sub2api-compose up -d
./sub2api-compose stop grafana prometheus
```

更改采样间隔或规则后，重新执行原来的 `compose-deploy.sh` 命令。Prometheus 配置和规则会先由 `promtool` 校验；成功部署时重建 Prometheus 容器，使更新的文件挂载生效，保留 TSDB 卷。

## 可选扩展

六容器基础栈采集应用 HTTP、Go/process 和 Prometheus 自身指标。没有开启宿主机监控时，看板的宿主机面板显示无数据，Prometheus 不创建一个必定失败的 node 抓取任务。

```bash
# 增加独立 Node Exporter，采集宿主机 CPU/内存/磁盘/网络
./compose-deploy.sh --monitoring --host-metrics

# 增加独立快照 cron 容器及一次性卷初始化容器
./compose-deploy.sh --monitoring --backup

# 两种扩展一起启用；开发环境仍需带原 --compose-file / --project-name 参数
./compose-deploy.sh --monitoring --host-metrics --backup
```

后续升级会保留已经启用的扩展。Node Exporter 使用 Linux host 网络命名空间，解决 bridge 模式采到容器网络的问题。脚本从 Docker 默认 bridge 获取 RFC1918 私有网关地址，Exporter 只在该地址的 9100 监听，Prometheus 使用相同地址采集。它不监听公网地址，不依赖 host 网络下不存在的服务 DNS。默认 bridge 被禁用或使用非 RFC1918 地址的主机不支持此自动配置，脚本会报错。

Docker Desktop 下宿主指标属于 **Linux 虚拟机**。它们不是 macOS/Windows 物理主机的数据。Docker Desktop 的 host networking 能力受版本和设置影响；生产 Linux 仍需复核实际网卡、挂载点和可用资源。

快照扩展继续使用同一 TSDB 卷的 `snapshots` 子路径，避免跨卷硬链接失败；备份容器看不到活跃 TSDB。定时任务默认 UTC 04:00（北京时间 12:00），备份包默认保留 3 天，输出目录 `monitoring/backup-data`。

```bash
./sub2api-compose exec backup /usr/local/bin/snapshot.sh
```

## 验证与停用

```bash
./sub2api-compose exec prometheus wget -qO- 'http://localhost:9090/api/v1/query?query=up'
```

基础栈的 `sub2api`、`prometheus`、`alertmanager` 三个目标应为 1；开启宿主扩展时另有 `node=1`。`up=1` 表示可抓取，仍需验证指标口径。邮件默认不发送；仅在 `monitoring.env` 显式配置受控收件人 `ALERT_SMTP_TO` 和 SMTP 后启用，沿用旧配置时请先核对收件人。Grafana PostgreSQL 数据源使用 `POSTGRES_DB`（默认 `sub2api`）；`grafana_ro` 只能查询看板所需列和 API Key 脱敏视图。

从旧版七容器环境迁移到默认五容器时，先用原 Compose 命令停止 `node-exporter` 和 `backup`，再执行新脚本；未选中的旧容器不会自动删除。不要使用 `down -v`，它会删除命名数据卷。

Grafana 和 Prometheus 分别使用独立持久卷。Grafana 只绑定宿主回环；Prometheus 9090、应用指标 9091 不发布宿主端口。2GB 生产机器须按业务和监控的实测用量校准内存限额与 retention。

部署回归测试：`python3 deploy/tests/monitoring-deploy-test.py`（在仓库根目录执行）。测试不连接 us1，服务更新和 pg_dump 故障由本地桩模拟，配置合并和 promtool 校验使用真实 Docker。

完整隔离栈验收（需要一个已经构建、包含监控埋点的应用镜像；会创建并清理独立项目及其测试卷）：

```bash
python3 deploy/tests/monitoring-stack-smoke.py --image sun/sub2api:0.0.4 --extensions
```

这条测试通过真正的 `docker-deploy.sh --monitoring` 启动六容器，验证抓取目标、六张看板、自定义数据库连接和最小权限，再在本机演练纯应用及带监控升级；`--extensions` 还会验证宿主网络计数与快照压缩。测试不会连接远程服务器。

## V2 W5 本地验收（2026-11-14）

本节保留 W5 历史输出；当前完整栈验收见上面的 smoke 测试。`compose-deploy.sh --monitoring` 会自动生成 `GRAFANA_ADMIN_PASSWORD` 与 `GRAFANA_PG_PASSWORD`，并在服务就绪后执行 `monitoring/sql/grafana-ro.sql`；如手工启动栈，服务就绪后可用已生成的 wrapper 和 `./compose-deploy.sh --setup-grafana-db` 补齐授权。监控叠加默认启用 `MONITORING_METRICS_ENABLED`、`MONITORING_OPS_METRICS_ENABLED`、`MONITORING_OPS_SAMPLES_ENABLED`，可用 `monitoring.env` 覆盖。

```sh
# 在仓库根目录；既有 monitoring.env 若仍写 30s，须显式改为 15s。
PROM_SCRAPE_INTERVAL=15s sh deploy/monitoring/render-prometheus-config.sh
# 本机无 promtool，实际使用 v3.16.0 容器等价执行：
docker run --rm --network none \
  -v "$PWD/deploy/monitoring:/m:ro" \
  -v "$PWD/deploy/monitoring/prometheus/rules:/etc/prometheus/rules:ro" \
  --entrypoint promtool prom/prometheus:v3.16.0 check config /m/prometheus.yml
docker run --rm --network none -v "$PWD/deploy/monitoring:/m:ro" \
  --entrypoint promtool prom/prometheus:v3.16.0 check rules \
  /m/prometheus/rules/slo.yml /m/prometheus/rules/chain.yml /m/prometheus/rules/infra.yml
docker run --rm --network none -v "$PWD/deploy:/deploy:ro" \
  --entrypoint promtool prom/prometheus:v3.16.0 test rules /deploy/tests/monitoring-rules-test.yml
python3 deploy/monitoring/grafana/build-dashboards.py --check
python3 deploy/tests/monitoring-v2-dashboard-test.py
python3 deploy/tests/monitoring-v2-pg-test.py
python3 deploy/tests/monitoring-v2-stack-test.py
# 真实部署配置从 .env / monitoring.env 注入；不要把密码写进命令历史。
docker compose --env-file deploy/.env --env-file deploy/monitoring.env \
  -f deploy/docker-compose.dev.yml -f deploy/docker-compose.monitoring.yml \
  --profile host-metrics --profile backup config --quiet
```

实际工具输出（精简重复文件前缀）：

```text
check config /m/prometheus.yml
  SUCCESS: 3 rule files found
  SUCCESS: /m/prometheus.yml is valid prometheus config file syntax
check rules slo.yml chain.yml infra.yml
  SUCCESS: 2 rules found / 8 rules found / 8 rules found
Processing test file: /m/prometheus/rules/tests/v2_test.yml
  SUCCESS
amtool check-config /alertmanager/alertmanager.yml
  SUCCESS; 1 receivers; 1 templates; SUCCESS
PASS: five V2 dashboards match generator
sub2api-v2-overview 16 30s description/datasource/PG interval/no-vector(1): PASS
sub2api-v2-chain 19 10s description/datasource/PG interval/no-vector(1): PASS
sub2api-v2-nodes 9 30s description/datasource/PG interval/no-vector(1): PASS
sub2api-v2-users 6 30s description/datasource/PG interval/no-vector(1): PASS
sub2api-v2-host 15 30s description/datasource/PG interval/no-vector(1): PASS
check rules /queries.yml  # 从五张 JSON 的 expr 提取，替换 Grafana 时间宏
  SUCCESS: 54 rules found
PASS: TCP/password SELECT and idempotent provisioning; application role unchanged
PASS: read-only OFF still rejects UPDATE / CREATE TABLE; users secret column denied
PASS: pg_sleep(20) canceled by 15s statement timeout (15.04s)
PASS: native Grafana provisioned exactly five V2 dashboards (16/19/9/6/15); V1 absent
PASS: PG datasource provisioned with secure password and grafana_ro
```

邮件检查通过无网络容器运行 `render-prometheus-config.sh --alertmanager`，设置 `ALERTMANAGER_RENDER_ONLY=true` 与 example 中的本地 SMTP 占位值，实际调用 amtool 并解析 email.tmpl；不启动投递。SMTP 各标量通过环境 allowlist 替换并作 YAML 单引号转义；拒绝换行值。容器启动时使用同一渲染路径，渲染/校验失败立即退出。

规则测试矩阵共 30 组：

| 组 | 规则/场景 | 验证 |
| --- | --- | --- |
| 18 | API §9 的全部 18 条告警 | 每条 60m 触发、110m 恢复；包含标签与注解断言 |
| 7 | InstrumentationStalled：全缺失、单实例缺失、单实例停写、空闲、慢流式、混合实例、管理路由排除 | 10m 实例集合逐项匹配；65m 恢复/保持不告警 |
| 4 | UnknownFailureHigh：高流量低比例、低流量高比例、恰好 10%、零流量 | 不误触发；18 条主测试另验证高比例且高流量触发/恢复 |
| 1 | NodeLoadHigh 标签匹配回归 | 原表达式在真实标签下无结果，去掉 mode 后高负载触发、低负载恢复 |

上表是历史矩阵。当前纳入版本控制和 CI 的 `deploy/tests/monitoring-rules-test.yml` 验证可选 Node Exporter 和按实例埋点停写的回归。第 6 条保留 `unless on(instance)`，用应用侧的 `sub2api_ops_eligible_requests_total`，不再复制一份路由名单。

### 只读 SQL 初始化与实测边界

在本地完成 M3 表/字段迁移后，以管理员连接执行以下命令（`psql` 的 password 变量取自同一 `GRAFANA_PG_PASSWORD`，通过本地安全方式提供；不要把真实值写入仓库）：

```sh
psql -h 127.0.0.1 -U postgres -d sub2api -v password="$GRAFANA_PG_PASSWORD" \
  -f deploy/monitoring/sql/grafana-ro.sql
PGPASSWORD="$GRAFANA_PG_PASSWORD" psql -h 127.0.0.1 -U grafana_ro -d sub2api -c 'SELECT id,email FROM users LIMIT 1'
PGPASSWORD="$GRAFANA_PG_PASSWORD" psql -h 127.0.0.1 -U grafana_ro -d sub2api -c "SET default_transaction_read_only=off; UPDATE users SET email='x'"
PGPASSWORD="$GRAFANA_PG_PASSWORD" psql -h 127.0.0.1 -U grafana_ro -d sub2api -c 'SET default_transaction_read_only=off; CREATE TABLE public.x(i int)'
PGPASSWORD="$GRAFANA_PG_PASSWORD" psql -h 127.0.0.1 -U grafana_ro -d sub2api -c 'SELECT pg_sleep(20)'
```

后面三条必须分别为权限失败、权限失败、15s statement timeout。脚本原子执行且可重复，只重置 `grafana_ro` 直接权限/角色参数，不改应用角色；若旧 PG 的 PUBLIC 默认 CREATE 使其继承建表能力，会回滚并明确报错，要求管理员先审计 PUBLIC 权限。本次自动验收在隔离 PG18 fixture 上通过，不等于 M3 实际表结构、数据或查询计划验收。

### 内存账与已知偏差

| 容器 | limit | 配置依据 |
| --- | --- | --- |
| Prometheus | 288m | V2 序列预算约 1406/实例，初始低 QPS；保留 head/WAL 余量，仍须实测高峰 |
| Grafana | 384m | 现有 13.3.1 冷启动逼近 256m；保留 GOMEMLIMIT=160MiB 与插件禁预装 |
| Alertmanager | 64m | 单收件人、有限分组，无外置存储 |
| node-exporter | 64m | 可选 host-metrics profile |
| backup | 64m | 可选快照压缩任务 |
| snapshots-init | 32m | 一次性目录初始化 |
| 默认全部合计 | **896m** | 常驻含可选 profile 为 864m，含一次性 init 为 896m；业务资源未改 |

稳定 RSS ≤640MB 是运行验收目标，不能由 limit 相加证明。本次隔离三容器启动后仅做 40s 采样，首个 cgroup memory 样本为 Prometheus 58.91MiB、Grafana 303.9MiB、Alertmanager 15.8MiB（合计 378.61MiB）；末次样本分别为 60.58/305.9/18.77MiB（合计 385.25MiB）。cgroup charge 含缓存，不等于 RSS。未加载业务流量、node/backup，不能签署长期稳定 RSS 或生产容量结论。Retention 默认 90d/8GB，先到先删；块限制不含 WAL/head/快照，90d 不构成磁盘/保留时长保证。

- **授权契约偏差**：NodeLoadHigh 使用 `count without(cpu, mode)(node_cpu_seconds_total{mode="idle"})`，API 原式仅去 cpu 会因 mode 标签不匹配失效。规则注释与单测均记录；不回改契约文档。
- **刷新偏差**：Grafana 只有看板级 refresh。02 为 10s，01/03/05 为 30s，04 为 1m，06 为 30s；混合看板 PG 面板 `interval=1m` 是查询步长/min interval，实际仍随看板刷新，**不宣称 PG 独立每分钟刷新**。
- **Compose overlay**：实际执行单文件 `docker compose -f deploy/docker-compose.monitoring.yml config`，无环境变量先报告管理员密码缺失；注入本地占位密码后输出 `service "sub2api" has neither an image nor a build context specified: invalid compose project`。该文件是叠加文件，不应添加业务 image。实际验收 `dev + monitoring + host-metrics + backup config --quiet` 退出码 0、无输出。
- **布局数据边界**：当前为 14/7/8/7/23/10（含每页导航 text 面板）与 06 请求下钻；M-2 无 account，账号带宽改 PG 请求样本/分钟；无账号在途指标，原位置明确“未采集”；账号矩阵展示已有请求样本口径，无法凭空提供实时在途；M-11 无 model_class，不生成模型 TTFT。用户 result 改查请求样本表，系统快照使用实际 `created_at`。单请求分解从 Top 40 数据链接写入 request 变量，排除 headers_end。
- **未验证**：生产 Linux 的 node 指标语义、backup 真正压缩/恢复、真实 SMTP 投递、M3 真实迁移后的全部 SQL 执行/Top 40 ≤2s 与覆盖率、长期 RSS/峰值容量、业务栈完整端到端烟测。当前隔离测试只验证 provisioning；不将缺少 PG 服务的 datasource 配置成功视为查询成功。

### V1 看板回滚

旧文件原样保存在 `monitoring/grafana/dashboards/backup/sub2api-overview.json`，provider 只扫描单独的 `/var/lib/grafana/dashboards/v2`，备份不会被递归加载。回滚时把备份复制到宿主机 dashboards 目录，将 Compose 六张 V2 JSON（5 主 + 1 下钻）挂载改回该目录挂载，并把 provider path 改回 `/var/lib/grafana/dashboards`（备份应移至该目录外避免重复）；重建/重启 Grafana。保留 Grafana/Prometheus 持久卷，不执行 `down -v`。


### W5 审核修复验收（2026-11-14）

已修复五项生成器阻塞并重新生成全部 JSON：

1. h6 与告警一致，分母改为 `count without(cpu, mode)(node_cpu_seconds_total{mode="idle"})`。新增真实 node-exporter 标签回归，原式为空、修正式在 load=6/2 cores 时返回 3。
2. c14 使用 M-3 已有 `le="262144"` 桶，标题/description 明确 **>256 KiB（262144 byte）**；分子用总计数减桶计数，避免零流量误显示 100%，未新增指标桶。
3. 缺序列兜底改为 `(原查询) or (label_replace(vector(0), "collection", "未采集", "", "") unless on() (原查询))`，有任意真实序列时抑制兜底。定向测试覆盖多条正常序列、带标签真实 0、无标签真实 0、完全缺失。
4. o15/c12 使用 `percentunit`（0~1 ratio），-1 映射灰色“无样本”，0~<0.9 黄色、≥0.9 绿色。断言 0.96→96% 绿、0.85→85% 黄、0→0% 黄、-1→无样本灰；PromQL 测试另验证原始值完整保留。
5. 04 看板改为 **30s refresh**，PG `interval=1m` 保留为查询步长；并未实现独立 PG 刷新。

实际执行：

```text
python3 deploy/monitoring/grafana/build-dashboards.py
PASS: five V2 dashboards generated
python3 deploy/monitoring/grafana/build-dashboards.py --check
PASS: five V2 dashboards match generator
python3 deploy/tests/monitoring-v2-dashboard-test.py
Checking /m/queries.yml
  SUCCESS: 54 rules found
Processing test file: /m/regression_test.yml
  SUCCESS
PASS: sub2api-v2-overview panels=16 refresh=30s description/datasource/interval
PASS: sub2api-v2-chain panels=19 refresh=10s description/datasource/interval
PASS: sub2api-v2-nodes panels=9 refresh=30s description/datasource/interval
PASS: sub2api-v2-users panels=6 refresh=30s description/datasource/interval
PASS: sub2api-v2-host panels=15 refresh=30s description/datasource/interval
PASS: o15 coverage 0.96=96% green / 0.85=85% yellow / 0=0% yellow / -1=无样本 gray
PASS: c12 coverage 0.96=96% green / 0.85=85% yellow / 0=0% yellow / -1=无样本 gray
PASS: 54 generated PromQL parsed; 11 PromQL regression cases passed
```

11 组查询回归与前述 30 组告警测试分别计数。百分比浮点计算按 1e-6 精度断言；实际 JSON 查询不加 round。不重启业务或已运行的监控栈，生产、SMTP、长期 RSS 和 M3 SQL 的未验证边界维持如前。


## Dashboard V2 redesign（2026-11-15）

本次按 `docs/dashboard-v2-mockup.html` 重做 5 个主模块，不保留折线图；新增请求详情下钻页。

| 看板 | UID | 文件 | 面板数 |
| --- | --- | --- | --- |
| 01 · 总览 | `sub2api-v2-overview` | `01-overview.json` | 14 |
| 02 · 请求明细 | `sub2api-v2-requests` | `02-requests.json` | 7 |
| 03 · 上游节点 | `sub2api-v2-nodes` | `03-nodes.json` | 8 |
| 04 · 用户与密钥 | `sub2api-v2-users` | `04-users.json` | 7 |
| 05 · 宿主与应用 | `sub2api-v2-host` | `05-host.json` | 23 |
| 请求详情（下钻） | `sub2api-v2-request` | `06-request.json` | 10 |

面板数包含导航 text 面板。02 的请求表以 PG samples 为主表，LEFT JOIN usage_logs/users/api_keys/accounts/groups，最新在前 `LIMIT 200`；6 段耗时列和请求体大小在同表展示，详情列进入 06。01 的链路占比使用 PG 宽表 + barchart percent 堆叠，等价于 mockup 的单条堆叠条。

当前 Compose 将上述六个 JSON **逐文件**只读挂载到 `/var/lib/grafana/dashboards/v2/`，provider 的 `path` 保持 `/var/lib/grafana/dashboards/v2` 不变；不整目录挂载，避免 `backup/` 下的旧 V1 JSON 被 provider 扫描加载。旧的 `02-chain.json` 已由 `02-requests.json` 取代，历史输出段落保留仅作记录。provider 的 `updateIntervalSeconds: 30` 只保证对**同一 inode 原地写入**的内容最多 30s 重载；安装器复制、`sed -i` 或临时文件 rename 等替换 inode 后，已有文件 bind mount 仍可能指向旧文件，必须 `./sub2api-compose up -d --no-deps --force-recreate grafana` 重新建立挂载（不能仅等待 provider 扫描）。不要用 `sed -i` 修改 dashboard JSON，应修改生成器后重新生成并校验；部署脚本会重建 Grafana，保留持久卷。源码/卷侧零字节 JSON 与历史 `02-chain.json` 的只读 preflight 和显式人工清理策略见 `MONITORING_RELEASE.md` 的 Dashboard deployment safety。

本地验收命令：

```bash
python3 deploy/monitoring/grafana/build-dashboards.py
python3 deploy/monitoring/grafana/build-dashboards.py --check
python3 deploy/tests/monitoring-v2-dashboard-test.py
python3 deploy/tests/monitoring-v2-stack-test.py
```

已实际通过：
- 生成器 `--check` 通过；
- 隔离 Grafana 13.3.1 成功 provision 5 主 + 1 下钻（14/7/8/7/23/10），V1 不加载；
- PG 数据源以 `grafana_ro` 安全密码 provision；
- 结构测试检查无 timeseries/graph/heatmap、无 `or vector(1)`、每面板 description、PG interval=1m、02 五表 JOIN/LIMIT 200/详情链接、06 六段分解。

原生限制（不是配置遗漏）：Grafana 表格无自动滚动/跟随/单面板暂停，02 以“最新在前 + 10s 刷新”近似；Grafana 无抽屉，使用 06 下钻；顶部 Tab 用 dashboard links + HTML text 导航（不可 sticky）。
