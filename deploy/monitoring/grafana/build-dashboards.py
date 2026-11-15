#!/usr/bin/env python3
"""Authoritative V2 dashboard generator for the redesigned mockup.

The reference layout is docs/dashboard-v2-mockup.html. Grafana cannot render
the reference's custom drawer/tab chrome; this generator maps the same modules,
tables and phase data onto native Grafana 13 panels. Edit queries here, never
the generated JSON.

Hard rules kept from the V2 contract:
- no line charts (no timeseries/graph/heatmap panels)
- every panel carries a description (semantics + misread risk)
- PG panels use interval=1m; Prom panels use 15s
- no `or vector(1)`; coverage -1 is rendered as "无样本"
- generated JSON must match `--check`
"""
import argparse
import json
from pathlib import Path

PROM = {"type": "prometheus", "uid": "sub2api-prometheus"}
PG = {"type": "postgres", "uid": "sub2api-pg"}

GREEN = "#3ecf8e"
YELLOW = "#f5b942"
RED = "#ef6b6b"
GRAY = "#6c7782"
BLUE = "#3987e5"
AQUA = "#1aa57a"
ORANGE = "#e07a3f"
GOLD = "#d4a72c"
PINK = "#d9669a"
VIOLET = "#9085e9"

PHASE = [
    ("body_read", "读取请求体", BLUE),
    ("wait", "槽位等待", AQUA),
    ("gateway", "网关本地", ORANGE),
    ("upstream", "上游耗时", GOLD),
    ("response", "响应传输", PINK),
    ("finish", "收尾", VIOLET),
]
PHASE_NAMES = {key: name for key, name, _ in PHASE}
PHASE_COLORS = {key: color for key, _, color in PHASE}

REASONS = [
    ("upstream_rate_limit", "上游限流", ORANGE),
    ("upstream_server_error", "上游 5xx", GOLD),
    ("upstream_read_timeout", "上游超时", PINK),
    ("client_abort_during_response", "客户端中断", VIOLET),
    ("gateway_error", "网关错误", RED),
    ("rejected", "请求被拒", GRAY),
    ("unknown", "未知", GRAY),
]
REASON_NAMES = {key: name for key, name, _ in REASONS}
REASON_COLORS = {key: color for key, _, color in REASONS}


def with_no_data(query):
    """Only synthesize a labelled zero when the whole vector is empty."""
    return (
        f'({query}) or (label_replace(vector(0), "collection", "未采集", "", "") '
        f"unless on() ({query}))"
    )


def thresholds(*steps):
    return {
        "mode": "absolute",
        "steps": [{"color": color, "value": value} for color, value in steps],
    }


def fixed(color):
    return thresholds((color, None))


def value_map(options):
    return [{"type": "value", "options": options}]


def regex_map(pattern, text, color):
    return {
        "type": "regex",
        "options": {"pattern": pattern, "result": {"text": text, "color": color}},
    }


def link_to_request():
    return [
        {
            "title": "查看详情",
            "url": "/d/sub2api-v2-request?var-request=${__value.raw}&from=${__from}&to=${__to}",
            "targetBlank": False,
        }
    ]


MODULES = [
    ("01", "总览", "sub2api-v2-overview", "overview"),
    ("02", "请求明细", "sub2api-v2-requests", "requests"),
    ("03", "上游节点", "sub2api-v2-nodes", "nodes"),
    ("04", "用户与密钥", "sub2api-v2-users", "users"),
    ("05", "宿主与应用", "sub2api-v2-host", "host"),
]

def nav(current):
    parts = []
    if current == "request":
        parts.append('<a href="/d/sub2api-v2-requests" style="display:inline-block;padding:5px 10px;'
                     'margin-right:8px;border-radius:5px;font-size:12px;text-decoration:none;'
                     'color:#ffb35f;border:1px solid rgba(255,152,48,.35);background:rgba(255,152,48,.08)">'
                     '← 请求明细</a>')
    for num, title, uid, key in MODULES:
        active = key == current
        background = "#1c2229" if active else "transparent"
        color = "#f4f5f5" if active else "#828c97"
        border = "#323a43" if active else "transparent"
        weight = "600" if active else "500"
        parts.append(
            f'<a href="/d/{uid}" style="display:inline-block;padding:5px 12px;margin-right:5px;'
            f'border-radius:5px;font-size:12px;text-decoration:none;font-weight:{weight};'
            f'background:{background};color:{color};border:1px solid {border}">{num} {title}</a>')
    return '<div style="display:flex;align-items:center;flex-wrap:wrap">' + "".join(parts) + "</div>"


def phase_legend():
    parts = []
    for _, name, color in PHASE:
        parts.append(
            f'<span style="display:inline-block;width:10px;height:10px;background:{color};'
            f'border-radius:2px;margin-right:5px"></span>'
            f'<span style="color:#b9c0c8;margin-right:16px">{name}</span>')
    return ('<div style="display:flex;align-items:center;flex-wrap:wrap;font-size:11px">'
            + "".join(parts)
            + '<span style="color:#5f6872">6 段互斥，之和等于总耗时；TTFT（headers_end）与上游段重叠，不参与堆叠。</span></div>')


def P(pid, kind, title, span, height, ds="prom", **kw):
    panel = dict(id=pid, kind=kind, title=title, span=span, height=height, ds=ds, desc="")
    panel.update(kw)
    return panel


# ---------------------------------------------------------------------------
# Dashboard layout (order mirrors the reference mockup)
# ---------------------------------------------------------------------------
DASHBOARDS = [
    dict(
        id="overview", uid="sub2api-v2-overview", num="01",
        title="01 · 总览", short="总览", file="01-overview.json",
        refresh="30s", time_from="now-15m",
        panels=[
            P("ov_nav", "text", "", 24, 2, content=nav("overview"), mode="html", nav=True,
              desc="模块导航。Grafana 原生没有 Tab，使用文本链接等价实现；当前页加粗。"),
            P("ov_qps", "stat", "网关 QPS", 3, 4, unit="reqps", decimals=1,
              desc="网关请求速率（含 0-attempt 失败）；不是上游 attempt 速率。重试会让上游 attempt 高于此值。"),
            P("ov_success", "stat", "成功率（1h）", 3, 4, unit="percentunit", decimals=2,
              desc="1h 内 M-10 成功网关请求占比。业务失败可能发生在已写出 200 的 SSE 中，不能只看 HTTP 5xx。"),
            P("ov_p95_total", "stat", "P95 总耗时", 3, 4, ds="pg", unit="s", decimals=2,
              desc="PG 请求样本 total_ms 的 P95。样本受落库覆盖率影响；不是各段 P95 相加。"),
            P("ov_ttft", "stat", "P95 TTFT", 3, 4, unit="s", decimals=2,
              desc="最终 attempt 首次发送到尾响应首字节；与 upstream 重叠，不参与段堆叠。无首字节不观察。"),
            P("ov_in", "stat", "入向带宽", 3, 4, unit="Bps", decimals=2,
              desc="客户端请求体 in_wire 实际读取字节速率；gzip 按压缩后长度计，不使用 Content-Length。"),
            P("ov_out", "stat", "出向带宽", 3, 4, unit="Bps", decimals=2,
              desc="实际写给客户端的响应体速率。客户端慢读/断开会让该值低于上游返回量。"),
            P("ov_amp", "stat", "上游放大比", 3, 4, unit="x", decimals=2,
              desc="resp_upstream / resp_downstream。>1.2 可能是客户端 abort、协议转换或流式重写；不能单独定性截断。"),
            P("ov_cov", "stat", "数据完整性", 3, 4, unit="percentunit", decimals=1,
              desc="M-9 request 覆盖率。0~1 比例；-1 表示无样本，不是 0% 或成功率。"),
            P("ov_share", "barchart", "链路段耗时占比", 9, 10, ds="pg", unit="percentunit", decimals=1,
              desc="PG 请求样本的 6 个互斥段平均耗时，Grafana 按 100% 堆叠归一化；headers_end 与 upstream 重叠，不在此面板。各段 P95 不可相加。"),
            P("ov_fail", "bargauge", "失败根因 Top 6", 7, 10, unit="short", decimals=0,
              desc="近 $__range 内 M-10 失败请求数 Top 6；无失败时无数据是健康状态，不是未采集。unknown 长期偏高说明分类缺失。"),
            P("ov_coverage", "bargauge", "数据完整性", 8, 10, unit="percentunit", decimals=1,
              desc="4 类 M-9 覆盖率（request/account/bytes/user）。<90% 黄；-1 灰=无样本；缺序列不等于健康。"),
            P("ov_anom", "table", "最近异常请求", 14, 12, ds="pg",
              desc="PG 请求样本中最近的失败请求；点击请求 ID 打开单请求详情。样本量不代表总体分位。"),
            P("ov_acct", "table", "上游账号速览", 10, 12, ds="pg",
              desc="PG 请求样本按账号聚合的 QPS/TTFT/失败率/带宽；不是 attempt 口径，低样本百分比噪声大。"),
        ],
    ),
    dict(
        id="requests", uid="sub2api-v2-requests", num="02",
        title="02 · 请求明细", short="请求明细", file="02-requests.json",
        refresh="10s", time_from="now-15m",
        panels=[
            P("rq_nav", "text", "", 24, 2, content=nav("requests"), mode="html", nav=True,
              desc="模块导航。Grafana 原生没有 Tab，使用文本链接等价实现；当前页加粗。"),
            P("rq_count", "stat", "窗口内请求", 6, 3, ds="pg", unit="short", decimals=0,
              desc="当前时间窗口内 PG 请求样本数；不是客户端缓冲，当前没有在途指标。无样本显示未采集，表格最多展示最新 200 行。"),
            P("rq_rate", "stat", "成功率", 6, 3, ds="pg", unit="percent", decimals=1,
              desc="当前窗口样本中 result=success 的比例。与 M-10 网关请求口径不同，不可直接对账。"),
            P("rq_avg", "stat", "平均总耗时", 6, 3, ds="pg", unit="s", decimals=2,
              desc="当前窗口 total_ms 平均值。均值会被长流式请求拉高，排查看 P95/单请求。"),
            P("rq_slow", "stat", "慢请求 >3s", 6, 3, ds="pg", unit="short", decimals=0,
              desc="当前窗口 total_ms >= 3000 的样本数；无样本显示未采集，0 表示有样本但没有慢请求。慢不等于失败，需结合段耗时和 failure_reason。"),
            P("rq_legend", "text", "", 24, 2, content=phase_legend(), mode="html", nav=True,
              desc="链路分段颜色图例。6 段互斥，之和等于总耗时；TTFT（headers_end）与上游重叠，不参与堆叠。"),
            P("rq_table", "table", "实时请求", 24, 24, ds="pg", cell_height="md", footer_show=True,
              desc="按使用记录口径展示最近请求；最新在前，随 10s 刷新更新。Grafana 表格不支持原生自动滚动/抽屉，点击详情列打开单请求看板。"),
        ],
    ),
    dict(
        id="nodes", uid="sub2api-v2-nodes", num="03",
        title="03 · 上游节点", short="上游节点", file="03-nodes.json",
        refresh="30s", time_from="now-15m",
        panels=[
            P("nd_nav", "text", "", 24, 2, content=nav("nodes"), mode="html", nav=True,
              desc="模块导航。Grafana 原生没有 Tab，使用文本链接等价实现；当前页加粗。"),
            P("nd_online", "stat", "在线账号", 6, 3, ds="pg", unit="short", decimals=0,
              desc="accounts 表中 status=active 且未删除的账号数；这是配置状态，不代表上游此刻可用。"),
            P("nd_capacity", "stat", "账号并发上限", 6, 3, ds="pg", unit="short", decimals=0,
              desc="活动账号 concurrency 之和。当前没有 per-account 在途指标，不能把上限当实时占用。"),
            P("nd_retry", "stat", "重试后成功", 6, 3, unit="percentunit", decimals=1,
              desc="M-6 retried_success / 全部 attempt 结束事件，窗口内按 $__range。比例高说明账号仍可用但需要重试；无流量时也可能为 0）。"),
            P("nd_switch", "stat", "账号切换 / 5m", 6, 3, unit="short", decimals=0,
              desc="M-7 跨账号切换次数的 5m increase。同账号重试不算；突增通常意味着账号故障。"),
            P("nd_accounts", "table", "账号健康矩阵", 24, 10, ds="pg",
              desc="PG 请求样本按账号聚合。QPS/失败率/TTFT 是请求口径；低样本百分比噪声大。摘除/降权前确认样本量与持续时间。"),
            P("nd_models", "table", "模型档位对比", 14, 10, ds="pg",
              desc="PG 请求样本按 model 归一到 4+1 档。TTFT 为 request headers_end P95；重试会重复样本。"),
            P("nd_conc", "table", "账号并发上限", 10, 10, ds="pg",
              desc="活动账号的并发上限条。没有 per-account 在途指标；不要把它当实时利用率。"),
        ],
    ),
    dict(
        id="users", uid="sub2api-v2-users", num="04",
        title="04 · 用户与密钥", short="用户与密钥", file="04-users.json",
        refresh="1m", time_from="now-24h",
        panels=[
            P("us_nav", "text", "", 24, 2, content=nav("users"), mode="html", nav=True,
              desc="模块导航。Grafana 原生没有 Tab，使用文本链接等价实现；当前页加粗。"),
            P("us_active", "stat", "活跃用户", 6, 3, ds="pg", unit="short", decimals=0,
              desc="今日 usage_logs 中去重 user_id 数。不是账号系统在线人数。"),
            P("us_req", "stat", "今日请求", 6, 3, ds="pg", unit="short", decimals=0,
              desc="今日 usage_logs 行数；与 Prom 网关请求数口径不同，不可直接对账。"),
            P("us_tokens", "stat", "今日 Token", 6, 3, ds="pg", unit="short", decimals=2,
              desc="今日 input+output+cache creation+cache read token 合计。与账单最终金额可能有舍入差异。"),
            P("us_cost", "stat", "今日成本", 6, 3, ds="pg", unit="currencyUSD", decimals=2,
              desc="今日 usage_logs actual_cost（实际扣费）合计。请求时计费快照；不是账单系统的最终应收。"),
            P("us_users", "table", "用户用量", 24, 10, ds="pg",
              desc="usage_logs 按用户聚合，成本列为 actual_cost；失败率来自 PG 请求样本。两种数据集覆盖范围不同，variables 只作用于含 user_id/api_key_id 的查询。"),
            P("us_keys", "table", "API 密钥", 24, 10, ds="pg",
              desc="usage_logs 按 api_key_id 聚合，成本列为 actual_cost；前缀来自 api_keys.key。Key 名称是用户可改展示值，不进入 Prometheus 标签。"),
        ],
    ),
    dict(
        id="host", uid="sub2api-v2-host", num="05",
        title="05 · 宿主与应用", short="宿主与应用", file="05-host.json",
        refresh="30s", time_from="now-1h",
        panels=[
            P("hs_nav", "text", "", 24, 2, content=nav("host"), mode="html", nav=True,
              desc="模块导航。Grafana 原生没有 Tab，使用文本链接等价实现；当前页加粗。"),
            P("hs_hostname", "stat", "主机名", 6, 3, unit="short", text_mode="name",
              desc="node_uname_info 的 nodename。没有 node-exporter 时显示未采集。"),
            P("hs_uptime", "stat", "运行时长", 6, 3, unit="dtdurations", decimals=0,
              desc="time() - node_boot_time_seconds。宿主重启会归零，不代表应用重启。"),
            P("hs_go", "stat", "Go 版本", 6, 3, unit="short", text_mode="name",
              desc="go_info 的 version 标签。构建版本/commit 当前未暴露为指标，见构建部署记录。"),
            P("hs_targets", "stat", "采集目标", 6, 3, unit="short", decimals=0,
              desc="当前 up=1 的抓取目标数；不代表业务健康。可选 node-exporter 未启用时目标数会少一个。"),
            P("hs_cpu", "bargauge", "CPU 使用率", 3, 5, unit="percent", decimals=1, min=0, max=100,
              desc="node-exporter 整机 CPU。process_* 是应用进程口径，不能与整机百分比直接对比。"),
            P("hs_mem", "bargauge", "内存", 3, 5, unit="percent", decimals=1, min=0, max=100,
              desc="1 - MemAvailable/MemTotal。swap 压力会先体现在 load 与响应抖动，而不是 OOM。"),
            P("hs_disk", "bargauge", "根分区", 3, 5, unit="percent", decimals=1, min=0, max=100,
              desc="只代表 mountpoint=/ 的分区；Prometheus TSDB、备份和 PG 数据若分开挂载需另看。"),
            P("hs_load", "bargauge", "load5 / 核数", 3, 5, unit="short", decimals=2, min=0, max=4,
              desc="node_load5 / 核数（去掉分母 cpu 标签）。>2 说明明显排队；Docker Desktop 是 Linux VM 口径。"),
            P("hs_net_rx", "bargauge", "网卡接收", 3, 5, unit="Bps", decimals=2, min=0,
              desc="整机网卡接收速率，已排除 lo/veth/docker/br-*。与网关自报字节不可精确对账。"),
            P("hs_net_tx", "bargauge", "网卡发送", 3, 5, unit="Bps", decimals=2, min=0,
              desc="整机网卡发送速率，已排除虚拟设备。与 resp_downstream 口径不同。"),
            P("hs_disk_r", "bargauge", "磁盘读", 3, 5, unit="Bps", decimals=2, min=0,
              desc="node_disk_read_bytes_total 速率。Prometheus 查询/备份会造成读峰值。"),
            P("hs_disk_w", "bargauge", "磁盘写", 3, 5, unit="Bps", decimals=2, min=0,
              desc="node_disk_written_bytes_total 速率。Prometheus 压缩、PG WAL 会造成写峰值。"),
            P("hs_goroutines", "stat", "goroutines", 3, 5, unit="short", decimals=0,
              desc="Go runtime goroutine 数。持续上升可能泄漏；与队列深度一起看，区分真并发与卡住。"),
            P("hs_heap", "stat", "Go 堆内存", 3, 5, unit="bytes", decimals=1,
              desc="go_memstats_heap_inuse_bytes。Go heap 不等于 RSS；容器 limit 是 RSS 口径。"),
            P("hs_rss", "stat", "进程 RSS", 3, 5, unit="bytes", decimals=1,
              desc="process_resident_memory_bytes。接近容器 limit 时关注 GC 与 OOM。"),
            P("hs_gc", "stat", "GC Pause P95", 3, 5, unit="s", decimals=3,
              desc="go_gc_duration_seconds{quantile=0.95}。summary 分位，不是 histogram；与 STW 时长相关但不完全等同。"),
            P("hs_db", "stat", "DB 连接", 3, 5, ds="pg", unit="short", decimals=0,
              desc="ops_system_metrics 最新窗口的 active / idle。应用侧连接池口径；与 pg_stat_activity 可能有时差。"),
            P("hs_queue", "stat", "并发队列", 3, 5, ds="pg", unit="short", decimals=0,
              desc="ops_system_metrics 最新 concurrency_queue_depth。与 02 的 wait 段互相印证。"),
            P("hs_redis", "stat", "Redis", 3, 5, ds="pg", unit="short", decimals=0,
              desc="ops_system_metrics 最新 redis_ok。应用探活结果，不代表 Redis 内部延迟/内存。"),
            P("hs_qps", "stat", "应用 QPS / TPS", 3, 5, ds="pg", unit="reqps", decimals=2,
              desc="ops_system_metrics 最新 qps（TPS 见描述/另一序列）。应用窗口聚合口径，与 Prom HTTP QPS 不可对账。"),
            P("hs_services", "table", "服务与进程", 14, 10,
              desc="Prometheus 抓取目标 up/版本信息。PostgreSQL/Redis 为应用探活，Grafana/Alertmanager 未纳入抓取；不要伪造未观测服务状态。"),
            P("hs_appinfo", "table", "应用信息", 10, 10,
              desc="Go/process 指标；构建版本与 commit 当前未作为指标暴露。启动时间来自 process_start_time_seconds。"),
        ],
    ),
    dict(
        id="request", uid="sub2api-v2-request", num="06",
        title="请求详情", short="请求详情", file="06-request.json",
        refresh="30s", time_from="now-1h",
        panels=[
            P("dt_nav", "text", "", 24, 2, content=nav("request"), mode="html", nav=True,
              desc="返回请求明细。该看板是请求表格的下钻页，不是第 6 个主模块。"),
            P("dt_total", "stat", "总耗时", 6, 3, ds="pg", unit="ms", decimals=0,
              desc="该请求 total_ms。6 段互斥，之和应等于总耗时（±1ms）。"),
            P("dt_ttft", "stat", "TTFT", 6, 3, ds="pg", unit="ms", decimals=0,
              desc="该请求 headers_end_ms。无响应体首字节时显示——，不虚构 0。"),
            P("dt_body", "stat", "请求体", 6, 3, ds="pg", unit="bytes", decimals=1,
              desc="bytes_in_wire 实际读取字节；不是 Content-Length 声明值。"),
            P("dt_http", "stat", "HTTP 状态", 6, 3, ds="pg", unit="short", decimals=0,
              desc="该请求最终 HTTP 状态；SSE 已开始输出后失败不一定改状态码。"),
            P("dt_basic", "table", "基本信息", 24, 8, ds="pg",
              desc="请求身份、模型映射、端点、分组与客户端信息。用户/IP/UA 来自 usage_logs，可能受异步落库时差影响。"),
            P("dt_phase", "table", "转发链路分段", 10, 9, ds="pg", unit="ms", decimals=0,
              desc="6 个互斥段的真实耗时（同一请求），颜色与总览一致。TTFT 与上游重叠，未放入六段。"),
            P("dt_bytes", "table", "字节流转", 14, 9, ds="pg",
              desc="四方向实际读写字节与观测状态。上游/下发不守恒属正常（协议转换、SSE 重写、重试失败体、客户端中断）。"),
            P("dt_tokens", "table", "Token 与费用", 10, 8, ds="pg",
              desc="usage_logs 的 token 与 actual_cost（实际扣费）快照。费用与最终账单可能有舍入差异。"),
            P("dt_fail", "table", "失败信息", 14, 8, ds="pg",
              desc="result/failure_reason/HTTP/attempt 结算。成功请求的 failure_reason 为空是正常值，不要当缺失。"),
        ],
    ),
]
"""Queries, styles, columns and Grafana JSON builders (part 2)."""

# ---------------------------------------------------------------------------
# Shared filters
# ---------------------------------------------------------------------------
REQ_STAT_FILTER = """$__timeFilter(s.observed_at)
  AND ($user = -1 OR s.user_id = $user)
  AND ($api_key = -1 OR s.api_key_id = $api_key)
  AND (${model:sqlstring} = '__all__' OR s.model = ${model:sqlstring})
  AND ('$result' = 'all'
       OR ('$result' = 'success' AND s.result = 'success')
       OR ('$result' = 'failure' AND s.result <> 'success'))"""

REQ_TABLE_FILTER = REQ_STAT_FILTER + """
  AND (${search:sqlstring} = ''
       OR COALESCE(u.email, '') ILIKE '%' || ${search:sqlstring} || '%'
       OR COALESCE(k.name, '') ILIKE '%' || ${search:sqlstring} || '%'
       OR s.model ILIKE '%' || ${search:sqlstring} || '%'
       OR s.request_id ILIKE '%' || ${search:sqlstring} || '%')"""

# Grafana expands Grafana-time macros to quoted timestamps, so the difference is
# "unknown - unknown" unless both sides are cast. Never use $__from/$__to: the
# Postgres datasource does not interpolate those.
WINDOW_SECONDS = (
    "EXTRACT(EPOCH FROM ($__timeTo()::timestamptz - $__timeFrom()::timestamptz))"
)
TODAY_SH = "date_trunc('day', now() AT TIME ZONE 'Asia/Shanghai') AT TIME ZONE 'Asia/Shanghai'"

# samples.request_id is the inbound x-request-id while usage_logs.request_id is
# "client:<uuid>" (or "local:<request_id>" for historical rows). Migration 253
# adds ops_request_transfer_samples.client_request_id; the join is a LATERAL
# "newest matching usage row" so retries/billing duplicates can never multiply
# request-table rows.
CLIENT_REQUEST_ID_JOIN = True


def usage_logs_join():
    if CLIENT_REQUEST_ID_JOIN:
        return (
            "LEFT JOIN LATERAL (\n"
            "  SELECT ul.*\n"
            "  FROM usage_logs ul\n"
            "  WHERE ul.api_key_id = s.api_key_id\n"
            "    AND (ul.request_id = 'client:' || s.client_request_id\n"
            "         OR (NULLIF(s.client_request_id, '') IS NULL\n"
            "             AND ul.request_id = 'local:' || s.request_id))\n"
            "  ORDER BY (ul.request_id = 'client:' || s.client_request_id) DESC,\n"
            "           ul.created_at DESC\n"
            "  LIMIT 1\n"
            ") ul ON TRUE"
        )
    return ("LEFT JOIN usage_logs ul ON ul.request_id = s.request_id "
            "AND ul.api_key_id = s.api_key_id")

FAILURE_LABEL = """CASE s.failure_reason
  WHEN 'upstream_rate_limit' THEN '上游限流'
  WHEN 'upstream_server_error' THEN '上游 5xx'
  WHEN 'upstream_read_timeout' THEN '上游超时'
  WHEN 'client_abort_during_response' THEN '客户端中断'
  WHEN 'gateway_error' THEN '网关错误'
  WHEN 'rejected' THEN '请求被拒'
  WHEN 'unknown' THEN '未知'
  ELSE COALESCE(s.failure_reason, s.result, 'unknown')
END"""

REQ_TABLE_SQL = f"""
SELECT
  s.observed_at AS "时间",
  COALESCE(u.email, '#' || s.user_id) AS "用户",
  COALESCE(k.name, '—') ||
    CASE WHEN k.key IS NOT NULL
      THEN ' · ' || left(k.key, 8) || '…' || right(k.key, 4)
      ELSE '' END AS "API 密钥",
  COALESCE(a.name, '#' || s.account_id) ||
    CASE WHEN a.platform IS NOT NULL THEN ' · ' || a.platform ELSE '' END AS "账户",
  s.model ||
    CASE WHEN ul.upstream_model IS NOT NULL AND ul.upstream_model <> s.model
      THEN ' ↳ ' || ul.upstream_model ELSE '' END AS "模型",
  COALESCE(ul.inbound_endpoint, s.endpoint, '—') || ' / ' ||
    COALESCE(ul.upstream_endpoint, '—') AS "端点",
  COALESCE(g.name, '—') AS "分组",
  CASE WHEN ul.stream IS TRUE THEN '流式'
       WHEN ul.stream IS FALSE THEN '非流式'
       ELSE '—' END AS "类型",
  CASE ul.billing_mode
    WHEN 'token' THEN '按量'
    WHEN 'per_request' THEN '按次'
    WHEN 'image' THEN '图片'
    WHEN 'video' THEN '视频'
    ELSE COALESCE(ul.billing_mode, '—') END AS "计费模式",
  CASE WHEN ul.input_tokens IS NULL THEN '—' ELSE
    '↓ ' || to_char(ul.input_tokens, 'FM999,999,999') ||
    '  ↑ ' || to_char(ul.output_tokens, 'FM999,999,999') ||
    CASE WHEN COALESCE(ul.cache_read_tokens, 0) > 0
      THEN '  ·  缓存 ' || to_char(ul.cache_read_tokens, 'FM999,999,999')
      ELSE '' END
  END AS "TOKEN",
  ul.actual_cost AS "费用",
  s.bytes_in_wire AS "请求体",
  COALESCE(s.body_read_ms, 0) AS "读体",
  COALESCE(s.wait_ms, 0) AS "等待",
  COALESCE(s.gateway_ms, 0) AS "网关",
  COALESCE(s.upstream_ms, 0) AS "上游",
  COALESCE(s.response_ms, 0) AS "响应",
  COALESCE(s.finish_ms, 0) AS "收尾",
  s.total_ms AS "总耗时",
  s.headers_end_ms AS "TTFT",
  CASE WHEN s.result = 'success' THEN '成功'
       ELSE '失败 · ' || ({FAILURE_LABEL}) END AS "状态",
  s.request_id AS "详情"
FROM ops_request_transfer_samples s
{usage_logs_join()}
LEFT JOIN users u ON u.id = s.user_id
LEFT JOIN api_keys k ON k.id = s.api_key_id
LEFT JOIN accounts a ON a.id = s.account_id
LEFT JOIN groups g ON g.id = s.group_id
WHERE {REQ_TABLE_FILTER}
ORDER BY s.observed_at DESC
LIMIT 200
"""

# ---------------------------------------------------------------------------
# Queries. Each entry is a list of targets for one panel.
# ---------------------------------------------------------------------------
QUERIES = {
    # 01 overview
    "ov_qps": [dict(kind="prom", expr="sum(rate(sub2api_gateway_requests_total[$__rate_interval]))", legend="网关 QPS")],
    "ov_success": [dict(kind="prom", expr='(sum(rate(sub2api_gateway_requests_total{result="success"}[1h])) or sum(rate(sub2api_gateway_requests_total[1h])) * 0) / clamp_min(sum(rate(sub2api_gateway_requests_total[1h])), 1e-9)', legend="成功率")],
    "ov_p95_total": [dict(kind="pg", sql=f"""
SELECT percentile_cont(0.95) WITHIN GROUP (ORDER BY total_ms) / 1000.0 AS "P95 总耗时"
FROM ops_request_transfer_samples
WHERE $__timeFilter(observed_at)
""")],
    "ov_ttft": [dict(kind="prom", expr='histogram_quantile(0.95, sum by (le)(rate(sub2api_request_phase_seconds_bucket{phase="headers_end"}[10m])))', legend="P95 TTFT")],
    "ov_in": [dict(kind="prom", expr='sum(rate(sub2api_transfer_bytes_total{direction="in_wire"}[5m]))', legend="入向带宽")],
    "ov_out": [dict(kind="prom", expr='sum(rate(sub2api_transfer_bytes_total{direction="resp_downstream"}[5m]))', legend="出向带宽")],
    "ov_amp": [dict(kind="prom", expr='(sum(rate(sub2api_transfer_bytes_total{direction="resp_upstream"}[10m]))) / clamp_min(sum(rate(sub2api_transfer_bytes_total{direction="resp_downstream"}[10m])), 1)', legend="上游放大比")],
    "ov_cov": [dict(kind="prom", expr='min(sub2api_metric_coverage_ratio{kind="request"})', legend="数据完整性")],
    "ov_share": [dict(kind="pg", sql="""
SELECT '链路段耗时占比' AS "分类",
       avg(body_read_ms) AS "读取请求体",
       avg(wait_ms) AS "槽位等待",
       avg(gateway_ms) AS "网关本地",
       avg(upstream_ms) AS "上游耗时",
       avg(response_ms) AS "响应传输",
       avg(finish_ms) AS "收尾"
FROM ops_request_transfer_samples
WHERE $__timeFilter(observed_at)
""")],
    "ov_fail": [dict(kind="prom", expr='topk(6, sum by (failure_reason)(increase(sub2api_gateway_requests_total{result!="success"}[$__range])))', legend="{{failure_reason}}")],
    "ov_coverage": [dict(kind="prom", expr='sub2api_metric_coverage_ratio', legend="{{kind}}")],
    "ov_anom": [dict(kind="pg", sql=f"""
SELECT
  s.observed_at AS "时间",
  COALESCE(u.email, '#' || s.user_id) AS "用户",
  s.model AS "模型",
  s.total_ms AS "总耗时",
  ({FAILURE_LABEL}) AS "失败原因",
  s.request_id AS "详情"
FROM ops_request_transfer_samples s
LEFT JOIN users u ON u.id = s.user_id
WHERE $__timeFilter(s.observed_at)
  AND s.result <> 'success'
ORDER BY s.observed_at DESC
LIMIT 8
""")],
    "ov_acct": [dict(kind="pg", sql=f"""
WITH win AS (
  SELECT s.account_id,
         max(COALESCE(s.platform, a.platform)) AS platform,
         count(*) AS requests,
         count(*) FILTER (WHERE s.result <> 'success') * 100.0 / count(*) AS failure_pct,
         percentile_cont(0.95) WITHIN GROUP (ORDER BY s.headers_end_ms) / 1000.0 AS ttft_p95_s,
         sum(COALESCE(s.bytes_resp_downstream, 0)) / {WINDOW_SECONDS} AS downstream_bps,
         mode() WITHIN GROUP (ORDER BY COALESCE(g.name, '—')) AS group_name
  FROM ops_request_transfer_samples s
  LEFT JOIN accounts a ON a.id = s.account_id
  LEFT JOIN groups g ON g.id = s.group_id
  WHERE $__timeFilter(s.observed_at)
    AND s.account_id IS NOT NULL
  GROUP BY s.account_id
)
SELECT COALESCE(a.name, '#' || w.account_id) AS "账号",
       w.platform AS "平台",
       w.group_name AS "分组",
       round((w.requests / {WINDOW_SECONDS})::numeric, 2) AS "QPS",
       round(w.ttft_p95_s::numeric, 2) AS "P95 TTFT",
       round(w.failure_pct::numeric, 1) AS "失败率",
       w.downstream_bps AS "带宽",
       CASE WHEN a.id IS NULL THEN '未知'
            WHEN a.deleted_at IS NOT NULL OR a.status <> 'active' OR a.schedulable IS FALSE THEN '停用'
            WHEN a.overload_until > now() OR a.rate_limited_at > now() - interval '5 minutes' THEN '限流'
            WHEN w.failure_pct > 5 THEN '降权候选'
            WHEN w.failure_pct > 2 THEN '观察'
            ELSE '正常' END AS "状态"
FROM win w
LEFT JOIN accounts a ON a.id = w.account_id
ORDER BY w.requests DESC
LIMIT 5
""")],

    # 02 requests
    "rq_count": [dict(kind="pg", sql=f"""
SELECT NULLIF(count(*), 0) AS "窗口内请求"
FROM ops_request_transfer_samples s
WHERE {REQ_STAT_FILTER}
""")],
    "rq_rate": [dict(kind="pg", sql=f"""
SELECT count(*) FILTER (WHERE s.result = 'success') * 100.0 / NULLIF(count(*), 0) AS "成功率"
FROM ops_request_transfer_samples s
WHERE {REQ_STAT_FILTER}
""")],
    "rq_avg": [dict(kind="pg", sql=f"""
SELECT avg(s.total_ms) / 1000.0 AS "平均总耗时"
FROM ops_request_transfer_samples s
WHERE {REQ_STAT_FILTER}
""")],
    "rq_slow": [dict(kind="pg", sql=f"""
SELECT CASE WHEN count(*) = 0 THEN NULL ELSE count(*) FILTER (WHERE s.total_ms >= 3000) END AS "慢请求"
FROM ops_request_transfer_samples s
WHERE {REQ_STAT_FILTER}
""")],
    "rq_table": [dict(kind="pg", sql=REQ_TABLE_SQL)],

    # 03 nodes
    "nd_online": [dict(kind="pg", sql='SELECT count(*) AS "在线账号" FROM accounts WHERE status = \'active\' AND deleted_at IS NULL')],
    "nd_capacity": [dict(kind="pg", sql='SELECT COALESCE(sum(concurrency), 0) AS "账号并发上限" FROM accounts WHERE status = \'active\' AND deleted_at IS NULL')],
    "nd_retry": [dict(kind="prom", expr='sum(increase(sub2api_attempts_total{outcome="retried_success"}[$__range])) / clamp_min(sum(increase(sub2api_attempts_total[$__range])), 1)', legend="重试后成功")],
    "nd_switch": [dict(kind="prom", expr='sum(increase(sub2api_account_switches_total[5m]))', legend="账号切换")],
    "nd_accounts": [dict(kind="pg", sql=f"""
WITH win AS (
  SELECT s.account_id,
         max(COALESCE(s.platform, a.platform)) AS platform,
         count(*) AS requests,
         count(*) FILTER (WHERE s.result <> 'success') * 100.0 / count(*) AS failure_pct,
         percentile_cont(0.95) WITHIN GROUP (ORDER BY s.headers_end_ms) / 1000.0 AS ttft_p95_s,
         sum(COALESCE(s.bytes_resp_downstream, 0)) / {WINDOW_SECONDS} AS downstream_bps,
         mode() WITHIN GROUP (ORDER BY COALESCE(g.name, '—')) AS group_name
  FROM ops_request_transfer_samples s
  LEFT JOIN accounts a ON a.id = s.account_id
  LEFT JOIN groups g ON g.id = s.group_id
  WHERE $__timeFilter(s.observed_at)
    AND s.account_id IS NOT NULL
  GROUP BY s.account_id
), today AS (
  SELECT account_id, count(*) AS today_requests
  FROM usage_logs
  WHERE created_at >= {TODAY_SH}
  GROUP BY account_id
)
SELECT COALESCE(a.name, '#' || w.account_id) AS "账号",
       w.platform AS "平台",
       w.group_name AS "分组",
       round((w.requests / {WINDOW_SECONDS})::numeric, 2) AS "QPS",
       round(w.ttft_p95_s::numeric, 2) AS "P95 TTFT",
       round(w.failure_pct::numeric, 1) AS "失败率",
       w.downstream_bps AS "带宽",
       COALESCE(t.today_requests, 0) AS "今日请求",
       a.concurrency AS "并发上限",
       CASE WHEN a.id IS NULL THEN '未知'
            WHEN a.deleted_at IS NOT NULL OR a.status <> 'active' OR a.schedulable IS FALSE THEN '停用'
            WHEN a.overload_until > now() OR a.rate_limited_at > now() - interval '5 minutes' THEN '限流'
            WHEN w.failure_pct > 5 THEN '降权候选'
            WHEN w.failure_pct > 2 THEN '观察'
            ELSE '正常' END AS "状态"
FROM win w
LEFT JOIN accounts a ON a.id = w.account_id
LEFT JOIN today t ON t.account_id = w.account_id
ORDER BY w.requests DESC
LIMIT 40
""")],
    "nd_models": [dict(kind="pg", sql=f"""
WITH t AS (
  SELECT CASE
           WHEN s.model ILIKE '%codex%' THEN 'codex'
           WHEN s.model ILIKE '%claude%' THEN 'claude'
           WHEN s.model ILIKE '%gemini%' THEN 'gemini'
           WHEN s.model ILIKE '%gpt%' THEN 'gpt'
           ELSE 'other' END AS tier,
         count(*) AS requests,
         count(*) FILTER (WHERE s.result = 'success') * 100.0 / count(*) AS success_pct,
         percentile_cont(0.95) WITHIN GROUP (ORDER BY s.headers_end_ms) / 1000.0 AS ttft_p95_s,
         avg(s.bytes_resp_downstream) AS avg_resp_bytes,
         mode() WITHIN GROUP (ORDER BY CASE WHEN s.result <> 'success' THEN ({FAILURE_LABEL}) ELSE NULL END) AS top_failure
  FROM ops_request_transfer_samples s
  WHERE $__timeFilter(s.observed_at)
  GROUP BY 1
)
SELECT tier AS "档位",
       requests AS "请求",
       round(success_pct::numeric, 1) AS "成功率",
       round(ttft_p95_s::numeric, 2) AS "P95 TTFT",
       avg_resp_bytes AS "平均响应体",
       COALESCE(top_failure, '—') AS "主要失败",
       CASE WHEN success_pct < 98 THEN '观察' ELSE '正常' END AS "状态"
FROM t
ORDER BY requests DESC
""")],
    "nd_conc": [dict(kind="pg", sql="""
SELECT name AS "账号", concurrency AS "并发上限"
FROM accounts
WHERE status = 'active' AND deleted_at IS NULL AND COALESCE(schedulable, true)
ORDER BY concurrency DESC, name
LIMIT 20
""")],

    # 04 users
    "us_active": [dict(kind="pg", sql=f"""
SELECT count(DISTINCT user_id) AS "活跃用户"
FROM usage_logs
WHERE created_at >= {TODAY_SH}
  AND ($user = -1 OR user_id = $user)
  AND ($api_key = -1 OR api_key_id = $api_key)
""")],
    "us_req": [dict(kind="pg", sql=f"""
SELECT count(*) AS "今日请求"
FROM usage_logs
WHERE created_at >= {TODAY_SH}
  AND ($user = -1 OR user_id = $user)
  AND ($api_key = -1 OR api_key_id = $api_key)
""")],
    "us_tokens": [dict(kind="pg", sql=f"""
SELECT COALESCE(sum(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS "今日 Token"
FROM usage_logs
WHERE created_at >= {TODAY_SH}
  AND ($user = -1 OR user_id = $user)
  AND ($api_key = -1 OR api_key_id = $api_key)
""")],
    "us_cost": [dict(kind="pg", sql=f"""
SELECT COALESCE(sum(actual_cost), 0) AS "今日成本"
FROM usage_logs
WHERE created_at >= {TODAY_SH}
  AND ($user = -1 OR user_id = $user)
  AND ($api_key = -1 OR api_key_id = $api_key)
""")],
    "us_users": [dict(kind="pg", sql="""
WITH sample AS (
  SELECT user_id,
         count(*) FILTER (WHERE result <> 'success') * 100.0 / count(*) AS failure_pct
  FROM ops_request_transfer_samples
  WHERE $__timeFilter(observed_at)
  GROUP BY user_id
)
SELECT u.email || ' #' || ul.user_id AS "用户",
       count(*) AS "请求",
       sum(ul.input_tokens) AS "输入 Token",
       sum(ul.output_tokens) AS "输出 Token",
       sum(ul.cache_read_tokens + ul.cache_creation_tokens) AS "缓存 Token",
       sum(ul.actual_cost) AS "成本",
       round(COALESCE(max(s.failure_pct), 0)::numeric, 1) AS "失败率",
       percentile_cont(0.95) WITHIN GROUP (ORDER BY ul.duration_ms) / 1000.0 AS "P95 耗时",
       max(ul.created_at) AS "最近活跃",
       CASE WHEN COALESCE(max(s.failure_pct), 0) > 5 THEN '观察' ELSE '正常' END AS "状态"
FROM usage_logs ul
JOIN users u ON u.id = ul.user_id
LEFT JOIN sample s ON s.user_id = ul.user_id
WHERE $__timeFilter(ul.created_at)
  AND ($user = -1 OR ul.user_id = $user)
  AND ($api_key = -1 OR ul.api_key_id = $api_key)
GROUP BY ul.user_id, u.email
ORDER BY count(*) DESC
LIMIT 100
""")],
    "us_keys": [dict(kind="pg", sql="""
WITH sample AS (
  SELECT api_key_id,
         count(*) FILTER (WHERE result <> 'success') * 100.0 / count(*) AS failure_pct
  FROM ops_request_transfer_samples
  WHERE $__timeFilter(observed_at)
  GROUP BY api_key_id
)
SELECT k.name AS "API 密钥",
       u.email AS "所属用户",
       left(k.key, 8) || '…' || right(k.key, 4) AS "前缀",
       count(*) AS "请求",
       sum(ul.input_tokens + ul.output_tokens + ul.cache_read_tokens + ul.cache_creation_tokens) AS "Token",
       sum(ul.actual_cost) AS "成本",
       round(COALESCE(max(s.failure_pct), 0)::numeric, 1) AS "失败率",
       max(COALESCE(k.last_used_at, ul.created_at)) AS "最近使用",
       CASE WHEN k.status <> 'active' THEN '停用'
            WHEN COALESCE(max(s.failure_pct), 0) > 5 THEN '观察'
            ELSE '正常' END AS "状态"
FROM usage_logs ul
JOIN api_keys k ON k.id = ul.api_key_id
JOIN users u ON u.id = ul.user_id
LEFT JOIN sample s ON s.api_key_id = ul.api_key_id
WHERE $__timeFilter(ul.created_at)
  AND ($user = -1 OR ul.user_id = $user)
  AND ($api_key = -1 OR ul.api_key_id = $api_key)
GROUP BY k.id, k.name, k.key, k.status, u.email
ORDER BY count(*) DESC
LIMIT 100
""")],

    # 05 host & app
    "hs_hostname": [dict(kind="prom", expr="node_uname_info", legend="{{nodename}}")],
    "hs_uptime": [dict(kind="prom", expr="time() - node_boot_time_seconds", legend="运行时长")],
    "hs_go": [dict(kind="prom", expr='go_info{job="sub2api"}', legend="{{version}}")],
    "hs_targets": [dict(kind="prom", expr="count(up == 1)", legend="采集目标")],
    "hs_cpu": [dict(kind="prom", expr='100 - avg by(instance)(rate(node_cpu_seconds_total{mode="idle"}[$__rate_interval])) * 100', legend="CPU")],
    "hs_mem": [dict(kind="prom", expr="(1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes) * 100", legend="内存")],
    "hs_disk": [dict(kind="prom", expr='(1 - node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"}) * 100', legend="根分区")],
    "hs_load": [dict(kind="prom", expr="node_load5 / count without(cpu, mode)(node_cpu_seconds_total{mode=\"idle\"})", legend="load5 / 核数")],
    "hs_net_rx": [dict(kind="prom", expr='sum(rate(node_network_receive_bytes_total{device!~"lo|veth.*|docker.*|br-.*"}[$__rate_interval]))', legend="接收")],
    "hs_net_tx": [dict(kind="prom", expr='sum(rate(node_network_transmit_bytes_total{device!~"lo|veth.*|docker.*|br-.*"}[$__rate_interval]))', legend="发送")],
    "hs_disk_r": [dict(kind="prom", expr="sum(rate(node_disk_read_bytes_total[$__rate_interval]))", legend="磁盘读")],
    "hs_disk_w": [dict(kind="prom", expr="sum(rate(node_disk_written_bytes_total[$__rate_interval]))", legend="磁盘写")],
    "hs_goroutines": [dict(kind="prom", expr='max(go_goroutines{job="sub2api"})', legend="goroutines")],
    "hs_heap": [dict(kind="prom", expr='max(go_memstats_heap_inuse_bytes{job="sub2api"})', legend="Go 堆内存")],
    "hs_rss": [dict(kind="prom", expr='max(process_resident_memory_bytes{job="sub2api"})', legend="进程 RSS")],
    "hs_gc": [dict(kind="prom", expr='max(go_gc_duration_seconds{quantile="0.95",job="sub2api"})', legend="GC Pause P95")],
    "hs_db": [dict(kind="pg", sql="""
SELECT db_conn_active AS "active", db_conn_idle AS "idle"
FROM ops_system_metrics
WHERE $__timeFilter(created_at)
ORDER BY created_at DESC
LIMIT 1
""")],
    "hs_queue": [dict(kind="pg", sql="""
SELECT concurrency_queue_depth AS "队列"
FROM ops_system_metrics
WHERE $__timeFilter(created_at)
ORDER BY created_at DESC
LIMIT 1
""")],
    "hs_redis": [dict(kind="pg", sql="""
SELECT CASE WHEN redis_ok IS NULL THEN NULL WHEN redis_ok THEN 1 ELSE 0 END AS "Redis"
FROM ops_system_metrics
WHERE $__timeFilter(created_at)
ORDER BY created_at DESC
LIMIT 1
""")],
    "hs_qps": [dict(kind="pg", sql="""
SELECT qps AS "QPS", tps AS "TPS"
FROM ops_system_metrics
WHERE $__timeFilter(created_at)
ORDER BY created_at DESC
LIMIT 1
""")],
    "hs_services": [dict(kind="prom", expr='up{job=~"sub2api|prometheus|node"}', legend="", prom_format="table")],
    "hs_appinfo": [
        dict(kind="prom", expr='go_info{job="sub2api"}', legend="", prom_format="table"),
        dict(kind="prom", expr='process_start_time_seconds{job="sub2api"}', legend="", prom_format="table"),
        dict(kind="prom", expr='time() - process_start_time_seconds{job="sub2api"}', legend="", prom_format="table"),
    ],

    # 06 request detail
    "dt_total": [dict(kind="pg", sql="""
SELECT total_ms AS "总耗时"
FROM ops_request_transfer_samples
WHERE request_id = ${request:sqlstring} AND $__timeFilter(observed_at)
ORDER BY observed_at DESC LIMIT 1
""")],
    "dt_ttft": [dict(kind="pg", sql="""
SELECT headers_end_ms AS "TTFT"
FROM ops_request_transfer_samples
WHERE request_id = ${request:sqlstring} AND $__timeFilter(observed_at)
ORDER BY observed_at DESC LIMIT 1
""")],
    "dt_body": [dict(kind="pg", sql="""
SELECT bytes_in_wire AS "请求体"
FROM ops_request_transfer_samples
WHERE request_id = ${request:sqlstring} AND $__timeFilter(observed_at)
ORDER BY observed_at DESC LIMIT 1
""")],
    "dt_http": [dict(kind="pg", sql="""
SELECT http_status AS "HTTP 状态"
FROM ops_request_transfer_samples
WHERE request_id = ${request:sqlstring} AND $__timeFilter(observed_at)
ORDER BY observed_at DESC LIMIT 1
""")],
    "dt_basic": [dict(kind="pg", sql=f"""
SELECT COALESCE(u.email, '#' || s.user_id) AS "用户",
       COALESCE(k.name, '—') AS "API 密钥",
       COALESCE(a.name, '—') AS "账户",
       COALESCE(g.name, '—') AS "分组",
       s.model AS "模型",
       COALESCE(ul.upstream_model, '—') AS "上游模型",
       COALESCE(ul.inbound_endpoint, s.endpoint, '—') AS "入站端点",
       COALESCE(ul.upstream_endpoint, '—') AS "上游端点",
       CASE WHEN ul.stream IS TRUE THEN '流式'
            WHEN ul.stream IS FALSE THEN '非流式'
            ELSE '—' END AS "类型",
       COALESCE(ul.billing_mode, '—') AS "计费模式",
       s.observed_at AS "时间",
       s.node AS "节点",
       ul.ip_address AS "客户端 IP",
       ul.user_agent AS "User-Agent"
FROM ops_request_transfer_samples s
{usage_logs_join()}
LEFT JOIN users u ON u.id = s.user_id
LEFT JOIN api_keys k ON k.id = s.api_key_id
LEFT JOIN accounts a ON a.id = s.account_id
LEFT JOIN groups g ON g.id = s.group_id
WHERE s.request_id = ${{request:sqlstring}} AND $__timeFilter(s.observed_at)
ORDER BY s.observed_at DESC
LIMIT 1
""")],
    "dt_phase": [dict(kind="pg", sql="""
WITH r AS (
  SELECT *
  FROM ops_request_transfer_samples
  WHERE request_id = ${request:sqlstring} AND $__timeFilter(observed_at)
  ORDER BY observed_at DESC
  LIMIT 1
), parts AS (
  SELECT '读取请求体' AS phase, COALESCE(body_read_ms, 0) AS ms, total_ms FROM r
  UNION ALL SELECT '槽位等待', COALESCE(wait_ms, 0), total_ms FROM r
  UNION ALL SELECT '网关本地', COALESCE(gateway_ms, 0), total_ms FROM r
  UNION ALL SELECT '上游耗时', COALESCE(upstream_ms, 0), total_ms FROM r
  UNION ALL SELECT '响应传输', COALESCE(response_ms, 0), total_ms FROM r
  UNION ALL SELECT '收尾', COALESCE(finish_ms, 0), total_ms FROM r
)
SELECT phase AS "阶段",
       ms AS "耗时",
       round(ms * 100.0 / NULLIF(total_ms, 0), 1) AS "占比"
FROM parts
""")],
    "dt_bytes": [dict(kind="pg", sql="""
WITH r AS (
  SELECT *
  FROM ops_request_transfer_samples
  WHERE request_id = ${request:sqlstring} AND $__timeFilter(observed_at)
  ORDER BY observed_at DESC
  LIMIT 1
)
SELECT * FROM (
  SELECT '客户端请求体' AS "方向", bytes_in_wire AS "字节",
         COALESCE(bytes_in_wire_status, 'unknown') AS "状态" FROM r
  UNION ALL SELECT '解码后请求体', bytes_in_decoded, 'complete' FROM r
  UNION ALL SELECT '发送上游请求体', bytes_up_req_total,
         COALESCE(bytes_up_req_total_status, 'unknown') FROM r
  UNION ALL SELECT '上游响应体（解压后）', bytes_resp_upstream,
         COALESCE(bytes_resp_upstream_status, 'unknown') FROM r
  UNION ALL SELECT '下发客户端响应体', bytes_resp_downstream,
         COALESCE(bytes_resp_downstream_status, 'unknown') FROM r
) x
""")],
    "dt_tokens": [dict(kind="pg", sql=f"""
SELECT ul.input_tokens AS "输入",
       ul.output_tokens AS "输出",
       ul.cache_read_tokens AS "缓存读",
       ul.cache_creation_tokens AS "缓存写",
       ul.actual_cost AS "费用"
FROM ops_request_transfer_samples s
{usage_logs_join()}
WHERE s.request_id = ${{request:sqlstring}} AND $__timeFilter(s.observed_at)
ORDER BY s.observed_at DESC
LIMIT 1
""")],
    "dt_fail": [dict(kind="pg", sql=f"""
SELECT CASE WHEN s.result = 'success' THEN '成功' ELSE '失败' END AS "结果",
       CASE WHEN s.result = 'success' THEN '—' ELSE ({FAILURE_LABEL}) END AS "失败原因",
       s.http_status AS "HTTP 状态",
       s.attempt_count AS "尝试次数",
       s.failed_attempts AS "失败次数",
       s.node AS "节点"
FROM ops_request_transfer_samples s
WHERE s.request_id = ${{request:sqlstring}} AND $__timeFilter(s.observed_at)
ORDER BY s.observed_at DESC
LIMIT 1
""")],
}

# ---------------------------------------------------------------------------
# Variables
# ---------------------------------------------------------------------------
VARIABLES = {
    "user": dict(
        name="user", label="用户", type="query", datasource=PG,
        query="SELECT id AS __value, COALESCE(email, id::text) AS __text FROM users ORDER BY 2 LIMIT 1000",
        definition="SELECT id AS __value, COALESCE(email, id::text) AS __text FROM users ORDER BY 2 LIMIT 1000",
        refresh=2, includeAll=True, allValue="-1", multi=False,
        current={"text": "All", "value": "$__all"}, options=[],
    ),
    "api_key": dict(
        name="api_key", label="API 密钥", type="query", datasource=PG,
        query="SELECT id AS __value, name AS __text FROM api_keys WHERE ($user = -1 OR user_id = $user) ORDER BY 2 LIMIT 1000",
        definition="SELECT id AS __value, name AS __text FROM api_keys WHERE ($user = -1 OR user_id = $user) ORDER BY 2 LIMIT 1000",
        refresh=2, includeAll=True, allValue="-1", multi=False,
        current={"text": "All", "value": "$__all"}, options=[],
    ),
    "result": dict(
        name="result", label="结果", type="custom",
        query="全部 : all,成功 : success,失败 : failure",
        definition="全部 : all,成功 : success,失败 : failure",
        includeAll=False, multi=False,
        current={"text": "全部", "value": "all"}, options=[],
    ),
    "model": dict(
        name="model", label="模型", type="query", datasource=PG,
        query="SELECT DISTINCT model AS __value, model AS __text FROM ops_request_transfer_samples WHERE $__timeFilter(observed_at) ORDER BY 1 LIMIT 500",
        definition="SELECT DISTINCT model AS __value, model AS __text FROM ops_request_transfer_samples WHERE $__timeFilter(observed_at) ORDER BY 1 LIMIT 500",
        refresh=1, includeAll=True, allValue="__all__", multi=False,
        current={"text": "All", "value": "$__all"}, options=[],
    ),
    "search": dict(
        name="search", label="搜索", type="textbox",
        query="", definition="", includeAll=False, multi=False,
        current={"text": "", "value": ""}, options=[],
    ),
    "request": dict(
        name="request", label="请求 ID", type="query", datasource=PG,
        query="SELECT request_id AS __value, request_id AS __text FROM ops_request_transfer_samples WHERE $__timeFilter(observed_at) ORDER BY observed_at DESC LIMIT 1000",
        definition="SELECT request_id AS __value, request_id AS __text FROM ops_request_transfer_samples WHERE $__timeFilter(observed_at) ORDER BY observed_at DESC LIMIT 1000",
        refresh=1, includeAll=False, multi=False,
        current={}, options=[],
    ),
}

VAR_SETS = {
    "requests": ["user", "api_key", "result", "model", "search"],
    "users": ["user", "api_key"],
    "request": ["request"],
}

# ---------------------------------------------------------------------------
# Styles, series mappings and table columns
# ---------------------------------------------------------------------------
STATUS_MAP = [
    regex_map("^成功$", "成功", GREEN),
    regex_map("^失败", "失败", RED),
    regex_map("^正常$", "正常", GREEN),
    regex_map("^观察$", "观察", YELLOW),
    regex_map("^降权候选$", "降权候选", RED),
    regex_map("^停用$", "停用", GRAY),
    regex_map("^未知$", "未知", GRAY),
    regex_map("^限流$", "限流", ORANGE),
]

BYTE_STATUS_MAP = [
    regex_map("^complete$", "完整", GREEN),
    regex_map("^partial$", "部分", YELLOW),
    regex_map("^unknown$", "未知", GRAY),
]

FAILURE_STATUS_MAP = [
    regex_map("^成功$", "成功", GREEN),
    regex_map("^(?:失败 · )?上游限流$", "上游限流", ORANGE),
    regex_map("^(?:失败 · )?上游 5xx$", "上游 5xx", GOLD),
    regex_map("^(?:失败 · )?上游超时$", "上游超时", PINK),
    regex_map("^(?:失败 · )?客户端中断$", "客户端中断", VIOLET),
    regex_map("^(?:失败 · )?网关错误$", "网关错误", RED),
    regex_map("^(?:失败 · )?请求被拒$", "请求被拒", GRAY),
    regex_map("^(?:失败 · )?未知$", "未知", GRAY),
]

STYLE = {
    # First step (value None) is the no-data/base colour: gray so that a
    # missing series never masquerades as red "failure" or green "healthy".
    "ov_success": dict(thresholds=thresholds((GRAY, None), (RED, 0), (YELLOW, 0.95), (GREEN, 0.99)), color_mode="value"),
    "ov_p95_total": dict(thresholds=thresholds((GRAY, None), (GREEN, 0), (YELLOW, 3), (RED, 8)), color_mode="value"),
    "ov_ttft": dict(thresholds=thresholds((GRAY, None), (GREEN, 0), (YELLOW, 3), (RED, 8)), color_mode="value"),
    "ov_amp": dict(thresholds=thresholds((GRAY, None), (GREEN, 0), (YELLOW, 1.2), (RED, 2.0)), color_mode="value"),
    "ov_cov": dict(
        thresholds=thresholds((GRAY, None), (YELLOW, 0), (GREEN, 0.9)),
        mappings=value_map({"-1": {"text": "无样本", "color": "gray"}}),
        color_mode="value",
    ),
    "ov_coverage": dict(
        thresholds=thresholds((GRAY, None), (YELLOW, 0), (GREEN, 0.9)),
        mappings=value_map({"-1": {"text": "无样本", "color": "gray"}}),
        min=0, max=1,
    ),
    "ov_share": dict(unit="percentunit", decimals=1, min=0, max=1),
    "ov_fail": dict(unit="short", decimals=0),
    "rq_rate": dict(thresholds=thresholds((GRAY, None), (RED, 0), (YELLOW, 95), (GREEN, 99)), color_mode="value"),
    "nd_retry": dict(thresholds=thresholds((GRAY, None), (GREEN, 0), (YELLOW, 0.2), (RED, 0.4)), color_mode="value"),
    "hs_cpu": dict(thresholds=thresholds((GREEN, None), (YELLOW, 80), (RED, 95))),
    "hs_mem": dict(thresholds=thresholds((GREEN, None), (YELLOW, 85), (RED, 95))),
    "hs_disk": dict(thresholds=thresholds((GREEN, None), (YELLOW, 80), (RED, 90))),
    "hs_load": dict(thresholds=thresholds((GREEN, None), (YELLOW, 1.5), (RED, 2))),
    "hs_net_rx": dict(max=10485760),
    "hs_net_tx": dict(max=10485760),
    "hs_disk_r": dict(max=10485760),
    "hs_disk_w": dict(max=10485760),
    "hs_goroutines": dict(thresholds=thresholds((GREEN, None), (YELLOW, 1000), (RED, 2000))),
    "hs_heap": dict(thresholds=thresholds((GREEN, None), (YELLOW, 209715200), (RED, 251658240))),
    "hs_rss": dict(thresholds=thresholds((GREEN, None), (YELLOW, 419430400), (RED, 503316480))),
    "hs_gc": dict(thresholds=thresholds((GREEN, None), (YELLOW, 0.01), (RED, 0.05))),
    "hs_db": dict(text_mode="value_and_name"),
    "hs_qps": dict(text_mode="value_and_name"),
    "hs_redis": dict(
        thresholds=thresholds((RED, None), (GREEN, 1)),
        mappings=value_map({"1": {"text": "OK", "color": "green"}, "0": {"text": "DOWN", "color": "red"}}),
        color_mode="value",
    ),
    "dt_http": dict(thresholds=thresholds((GRAY, None), (GREEN, 200), (YELLOW, 400), (RED, 500)), color_mode="value"),
    "dt_ttft": dict(thresholds=thresholds((GRAY, None), (GREEN, 0), (YELLOW, 3000), (RED, 8000)), color_mode="value"),
}

SERIES = {
    "ov_share": [(name, name, color) for key, name, color in PHASE],
    "ov_fail": [(key, name, color) for key, name, color in REASONS],
    "ov_coverage": [
        ("request", "request（埋点完整）", None),
        ("account", "account（账号可知）", None),
        ("bytes", "bytes（四方向完整）", None),
        ("user", "user（用户 + 密钥）", None),
    ],
}


def col(name, width=None, unit=None, decimals=None, align=None, cell=None, color=None,
        mappings=None, links=None, minv=None, maxv=None, wrap=None):
    props = []
    if width is not None:
        props.append({"id": "custom.width", "value": width})
    if unit is not None:
        props.append({"id": "unit", "value": unit})
    if decimals is not None:
        props.append({"id": "decimals", "value": decimals})
    if align is not None:
        props.append({"id": "custom.align", "value": align})
    if wrap is not None:
        props.append({"id": "custom.wrapText", "value": wrap})
    if cell == "gauge":
        props.append({"id": "custom.cellOptions", "value": {"type": "gauge", "mode": "basic", "valueDisplayMode": "text"}})
    elif cell == "data-links":
        props.append({"id": "custom.cellOptions", "value": {"type": "data-links"}})
    elif cell is not None:
        props.append({"id": "custom.cellOptions", "value": {"type": cell, "mode": "basic"}})
    if minv is not None:
        props.append({"id": "min", "value": minv})
    if maxv is not None:
        props.append({"id": "max", "value": maxv})
    if color is not None:
        props.append({"id": "thresholds", "value": fixed(color)})
    if mappings is not None:
        props.append({"id": "mappings", "value": mappings})
    if links is not None:
        props.append({"id": "links", "value": links})
    return {"matcher": {"id": "byName", "options": name}, "properties": props}


PHASE_COLS = [
    col("读体", width=66, unit="ms", decimals=0, align="right", cell="gauge", color=BLUE, minv=0, maxv=5000),
    col("等待", width=66, unit="ms", decimals=0, align="right", cell="gauge", color=AQUA, minv=0, maxv=5000),
    col("网关", width=66, unit="ms", decimals=0, align="right", cell="gauge", color=ORANGE, minv=0, maxv=5000),
    col("上游", width=74, unit="ms", decimals=0, align="right", cell="gauge", color=GOLD, minv=0, maxv=5000),
    col("响应", width=74, unit="ms", decimals=0, align="right", cell="gauge", color=PINK, minv=0, maxv=5000),
    col("收尾", width=66, unit="ms", decimals=0, align="right", cell="gauge", color=VIOLET, minv=0, maxv=5000),
]

COLUMNS = {
    "ov_anom": [
        col("时间", width=150, unit="dateTimeAsIso"),
        col("用户", width=170),
        col("模型", width=180, wrap=True),
        col("总耗时", width=90, unit="ms", decimals=0, align="right"),
        col("失败原因", width=150, cell="color-text", mappings=FAILURE_STATUS_MAP),
        col("详情", width=100, cell="data-links", links=link_to_request()),
    ],
    "ov_acct": [
        col("账号", width=100),
        col("平台", width=100),
        col("分组", width=80),
        col("QPS", width=80, unit="reqps", decimals=2, align="right"),
        col("P95 TTFT", width=100, unit="s", decimals=2, align="right"),
        col("失败率", width=80, unit="percent", decimals=1, align="right"),
        col("带宽", width=100, unit="Bps", decimals=2, align="right"),
        col("状态", width=100, cell="color-text", mappings=STATUS_MAP),
    ],
    "rq_table": [
        col("时间", width=150, unit="dateTimeAsIso"),
        col("用户", width=170),
        col("API 密钥", width=150),
        col("账户", width=120),
        col("模型", width=180, wrap=True),
        col("端点", width=220, wrap=True),
        col("分组", width=80),
        col("类型", width=70),
        col("计费模式", width=80),
        col("TOKEN", width=160),
        col("费用", width=80, unit="currencyUSD", decimals=4, align="right"),
        col("请求体", width=90, unit="bytes", decimals=1, align="right"),
    ] + PHASE_COLS + [
        col("总耗时", width=90, unit="ms", decimals=0, align="right"),
        col("TTFT", width=80, unit="ms", decimals=0, align="right"),
        col("状态", width=130, cell="color-text", mappings=FAILURE_STATUS_MAP),
        col("详情", width=100, cell="data-links", links=link_to_request()),
    ],
    "nd_accounts": [
        col("账号", width=100),
        col("平台", width=100),
        col("分组", width=80),
        col("QPS", width=80, unit="reqps", decimals=2, align="right"),
        col("P95 TTFT", width=100, unit="s", decimals=2, align="right"),
        col("失败率", width=80, unit="percent", decimals=1, align="right"),
        col("带宽", width=100, unit="Bps", decimals=2, align="right"),
        col("今日请求", width=90, unit="short", decimals=0, align="right"),
        col("并发上限", width=90, unit="short", decimals=0, align="right"),
        col("状态", width=100, cell="color-text", mappings=STATUS_MAP),
    ],
    "nd_models": [
        col("档位", width=90),
        col("请求", width=90, unit="short", decimals=0, align="right"),
        col("成功率", width=90, unit="percent", decimals=1, align="right"),
        col("P95 TTFT", width=100, unit="s", decimals=2, align="right"),
        col("平均响应体", width=110, unit="bytes", decimals=1, align="right"),
        col("主要失败", width=180),
        col("状态", width=90, cell="color-text", mappings=STATUS_MAP),
    ],
    "nd_conc": [
        col("账号", width=120),
        col("并发上限", unit="short", decimals=0, align="right", cell="gauge", color=BLUE, minv=0, maxv=20),
    ],
    "us_users": [
        col("用户", width=200),
        col("请求", width=80, unit="short", decimals=0, align="right"),
        col("输入 Token", width=100, unit="short", decimals=0, align="right"),
        col("输出 Token", width=100, unit="short", decimals=0, align="right"),
        col("缓存 Token", width=100, unit="short", decimals=0, align="right"),
        col("成本", width=90, unit="currencyUSD", decimals=2, align="right"),
        col("失败率", width=80, unit="percent", decimals=1, align="right"),
        col("P95 耗时", width=100, unit="s", decimals=2, align="right"),
        col("最近活跃", width=110, unit="dateTimeFromNow"),
        col("状态", width=90, cell="color-text", mappings=STATUS_MAP),
    ],
    "us_keys": [
        col("API 密钥", width=120),
        col("所属用户", width=200),
        col("前缀", width=140),
        col("请求", width=80, unit="short", decimals=0, align="right"),
        col("Token", width=100, unit="short", decimals=0, align="right"),
        col("成本", width=90, unit="currencyUSD", decimals=2, align="right"),
        col("失败率", width=80, unit="percent", decimals=1, align="right"),
        col("最近使用", width=110, unit="dateTimeFromNow"),
        col("状态", width=90, cell="color-text", mappings=STATUS_MAP),
    ],
    "hs_services": [
        col("job", width=120),
        col("instance", width=200),
        col("Value", width=100, unit="short", decimals=0, align="right", cell="color-text",
            mappings=[regex_map("^1$", "up", GREEN), regex_map("^0$", "down", RED)]),
    ],
    "hs_appinfo": [
        col("instance", width=200),
        col("version", width=140),
        col("job", width=100),
        col("Value #A", width=70, unit="short", decimals=0, align="right"),
        col("Value #B", width=170, unit="dateTimeAsIso"),
        col("Value #C", width=110, unit="dtdurations"),
    ],
    "dt_basic": [
        col("用户", width=190),
        col("API 密钥", width=120),
        col("账户", width=100),
        col("分组", width=80),
        col("模型", width=170, wrap=True),
        col("上游模型", width=170, wrap=True),
        col("入站端点", width=140),
        col("上游端点", width=140),
        col("类型", width=70),
        col("计费模式", width=80),
        col("时间", width=170, unit="dateTimeAsIso"),
        col("节点", width=110),
        col("客户端 IP", width=120),
        col("User-Agent", width=160, wrap=True),
    ],
    "dt_phase": [
        col("阶段", width=120, cell="color-text", mappings=[
            regex_map("^读取请求体$", "读取请求体", BLUE),
            regex_map("^槽位等待$", "槽位等待", AQUA),
            regex_map("^网关本地$", "网关本地", ORANGE),
            regex_map("^上游耗时$", "上游耗时", GOLD),
            regex_map("^响应传输$", "响应传输", PINK),
            regex_map("^收尾$", "收尾", VIOLET),
        ]),
        col("耗时", width=100, unit="ms", decimals=0, align="right"),
        col("占比", width=90, unit="percent", decimals=1, align="right"),
    ],
    "dt_bytes": [
        col("方向", width=220),
        col("字节", width=110, unit="bytes", decimals=1, align="right"),
        col("状态", width=100, cell="color-text", mappings=BYTE_STATUS_MAP),
    ],
    "dt_tokens": [
        col("输入", width=100, unit="short", decimals=0, align="right"),
        col("输出", width=100, unit="short", decimals=0, align="right"),
        col("缓存读", width=100, unit="short", decimals=0, align="right"),
        col("缓存写", width=100, unit="short", decimals=0, align="right"),
        col("费用", width=100, unit="currencyUSD", decimals=4, align="right"),
    ],
    "dt_fail": [
        col("结果", width=100, cell="color-text", mappings=STATUS_MAP),
        col("失败原因", width=180),
        col("HTTP 状态", width=100, unit="short", decimals=0, align="right"),
        col("尝试次数", width=90, unit="short", decimals=0, align="right"),
        col("失败次数", width=90, unit="short", decimals=0, align="right"),
        col("节点", width=120),
    ],
}
def make_targets(p):
    targets = []
    for index, query in enumerate(QUERIES.get(p["id"], [])):
        target = {"refId": chr(65 + index)}
        if query["kind"] == "prom":
            target["datasource"] = PROM
            expr = query["expr"]
            if query.get("fallback"):
                expr = with_no_data(expr)
            target.update(expr=expr, legendFormat=query.get("legend", ""), range=False, instant=True)
            if query.get("prom_format"):
                target["format"] = query["prom_format"]
        else:
            target["datasource"] = PG
            target.update(
                rawSql=query["sql"],
                format=query.get("format", "table"),
                rawQuery=True,
                editorMode="code",
            )
        targets.append(target)
    return targets


def style_for(p):
    style = dict(unit=p.get("unit", "short"), decimals=p.get("decimals", 0))
    style.update(STYLE.get(p["id"], {}))
    return style


def series_overrides(pid):
    overrides = []
    for key, display, color in SERIES.get(pid, []):
        props = [{"id": "displayName", "value": display}]
        if color:
            props.append({"id": "color", "value": {"mode": "fixed", "fixedColor": color}})
        overrides.append({"matcher": {"id": "byName", "options": key}, "properties": props})
    return overrides


def no_data_override():
    return {
        "matcher": {"id": "byRegexp", "options": ".*未采集.*"},
        "properties": [{"id": "displayName", "value": "未采集"}],
    }


def make_panel(p, panel_id, x, y):
    kind = p["kind"]
    ds = PG if p["ds"] == "pg" else PROM
    panel = {
        "id": panel_id,
        "title": p["title"],
        "type": kind,
        "description": p["desc"],
        "datasource": ds,
        "gridPos": {"h": p["height"], "w": p["span"], "x": x, "y": y},
        "targets": [] if kind == "text" else make_targets(p),
        "interval": "1m" if ds is PG else "15s",
        "fieldConfig": {"defaults": {}, "overrides": []},
        "options": {},
    }

    if kind == "text":
        panel["options"] = {
            "mode": p.get("mode", "markdown"),
            "content": p.get("content", ""),
            "codeLanguage": "plaintext",
            "codeShowLineNumbers": False,
            "renderMode": "once",
        }
        panel["fieldConfig"]["defaults"] = {
            "unit": "short",
            "decimals": 0,
            "noValue": "未采集",
            "mappings": [],
            "thresholds": thresholds((GREEN, None)),
        }
        return panel

    style = style_for(p)
    defaults = {
        "unit": style.get("unit", "short"),
        "decimals": style.get("decimals", 0),
        "noValue": "未采集",
        "mappings": style.get("mappings", []),
        "thresholds": style.get("thresholds", thresholds((GREEN, None))),
    }
    if style.get("min") is not None:
        defaults["min"] = style["min"]
    if style.get("max") is not None:
        defaults["max"] = style["max"]
    panel["fieldConfig"]["defaults"] = defaults

    if kind == "stat":
        defaults["color"] = (
            {"mode": "thresholds"}
            if style.get("color_mode") == "value"
            else {"mode": "fixed", "fixedColor": "#e6e9ed"}
        )
        text_mode = p.get("text_mode", style.get("text_mode", "value"))
        if text_mode == "value_and_name":
            value_size, title_size = 16, 9
        elif p["id"].startswith("ov_"):
            value_size, title_size = 26, 10
        elif p["id"].startswith(("hs_", "dt_")):
            value_size, title_size = 20, 9
        else:
            value_size, title_size = 18, 9
        panel["options"] = {
            "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
            "colorMode": style.get("color_mode", "none"),
            "graphMode": "none",
            "justifyMode": "center",
            "orientation": "auto",
            "textMode": text_mode,
            "wideLayout": True,
            "showPercentChange": False,
            "text": {"valueSize": value_size, "titleSize": title_size},
        }
        panel["fieldConfig"]["overrides"] = [no_data_override()]
        return panel

    if kind == "bargauge":
        defaults["color"] = {"mode": "thresholds"}
        single = p["id"].startswith("hs_")
        panel["options"] = {
            "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
            "orientation": "horizontal",
            "displayMode": "basic",
            "valueMode": "text",
            "namePlacement": "hidden" if single else "left",
            "showUnfilled": True,
            "textMode": "value" if single else "value_and_name",
            "sizing": "manual",
            "minVizWidth": 8,
            "minVizHeight": 10 if single else 22,
            "maxVizHeight": 16 if single else 34,
            "text": {"valueSize": 18 if single else 13},
            "legend": {"showLegend": False},
        }
        panel["fieldConfig"]["overrides"] = series_overrides(p["id"]) + [no_data_override()]
        return panel

    if kind == "barchart":
        defaults["color"] = {"mode": "thresholds"}
        defaults["min"] = style.get("min", 0)
        if style.get("max") is not None:
            defaults["max"] = style["max"]
        defaults["custom"] = {
            "lineWidth": 0,
            "fillOpacity": 100,
            "gradientMode": "none",
            # The reference is a single share bar: no numeric axis or in-bar
            # labels, only the colour legend underneath.
            "axisPlacement": "hidden",
            "axisLabel": "",
        }
        panel["options"] = {
            "xField": "分类",
            "orientation": "horizontal",
            "stacking": "percent",
            "showValue": "never",
            "barWidth": 0.3,
            "groupWidth": 0.7,
            "barRadius": 0.05,
            "fullHighlight": False,
            "xTickLabelRotation": 0,
            "xTickLabelMaxLength": 0,
            "legend": {"showLegend": True, "displayMode": "list", "placement": "bottom", "calcs": []},
            "tooltip": {"mode": "multi", "sort": "none"},
            "text": {"valueSize": 12},
        }
        panel["fieldConfig"]["overrides"] = series_overrides(p["id"]) + [no_data_override()]
        return panel

    if kind == "table":
        defaults["custom"] = {
            "align": "auto",
            "cellOptions": {"type": "auto"},
            "filterable": False,
            "inspect": False,
            "wrapText": False,
        }
        footer = {
            "show": p.get("footer_show", False),
            "reducer": ["count"],
            "countRows": p.get("footer_show", False),
            "fields": "",
        }
        sort_by = p.get("sort_by", [])
        if p["id"] == "rq_table":
            sort_by = [{"displayName": "时间", "desc": True}]
        panel["options"] = {
            "frameIndex": 0,
            "showHeader": True,
            "showTypeIcons": False,
            "cellHeight": p.get("cell_height", "sm"),
            "maxRowHeight": p.get("max_row_height", 80),
            "enablePagination": False,
            "footer": footer,
            "sortBy": sort_by,
        }
        panel["fieldConfig"]["overrides"] = COLUMNS.get(p["id"], []) + [no_data_override()]
        if p.get("transformations"):
            panel["transformations"] = p["transformations"]
        if p["id"] == "hs_appinfo":
            panel["transformations"] = [
                {"id": "joinByField", "options": {"byField": "instance", "mode": "outer"}}
            ]
        return panel

    # Fallback for an unsupported type should fail the generator, not emit an
    # unrenderable panel.
    raise SystemExit(f"unsupported panel type: {kind} ({p['id']})")


BACK_LINK = {
    "title": "← 返回请求明细",
    "url": "/d/sub2api-v2-requests",
    "type": "link",
    "icon": "arrow-left",
    "targetBlank": False,
    "keepTime": True,
    "includeVars": True,
    "asDropdown": False,
    "tags": [],
}


def build(spec):
    # The in-panel text nav already mirrors the mockup tabs. Grafana dashboard
    # links would add a second, duplicate module row above it, so only the
    # drilldown back link is kept as a native dashboard link.
    links = [BACK_LINK] if spec["id"] == "request" else []
    tags = ["sub2api", "v2"]
    if spec["id"] == "request":
        tags.append("drilldown")
    dashboard = {
        "uid": spec["uid"],
        "title": spec["title"],
        "schemaVersion": 39,
        "version": 1,
        "editable": False,
        "allowUiUpdates": False,
        "timezone": "browser",
        "refresh": spec["refresh"],
        "time": {"from": spec["time_from"], "to": "now"},
        "tags": tags,
        "panels": [],
        "templating": {"list": []},
        "links": links,
    }
    for name in VAR_SETS.get(spec["id"], []):
        dashboard["templating"]["list"].append(VARIABLES[name])

    x = y = row_height = 0
    for index, p in enumerate(spec["panels"], 1):
        if x + p["span"] > 24:
            y += row_height
            x = row_height = 0
        dashboard["panels"].append(make_panel(p, index, x, y))
        x += p["span"]
        row_height = max(row_height, p["height"])
    return dashboard


LAYOUT = DASHBOARDS


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    directory = Path(__file__).parent / "dashboards"
    for spec in DASHBOARDS:
        path = directory / (spec["num"] + "-" + spec["id"] + ".json")
        result = json.dumps(build(spec), ensure_ascii=False, indent=2) + "\n"
        if args.check:
            if not path.exists() or path.read_text() != result:
                raise SystemExit(f"OUT OF DATE: {path}")
        else:
            path.write_text(result)
    print("PASS: 5 main dashboards + 1 request drilldown " + ("match generator" if args.check else "generated"))


if __name__ == "__main__":
    main()
