#!/usr/bin/env python3
"""Authoritative V2 dashboard generator (pure Prometheus, 3 canonical dashboards).

Grafana renders 3 pure-Prometheus dashboards (zero PostgreSQL datasource
queries). Realtime per-request streams and single-request details live in
Sub2API Admin Ops (/admin/ops#realtime-requests), backed by Redis.
Edit queries here, never the generated JSON.

Hard rules kept from the V2 contract:
- pure Prometheus only (zero PG queries / zero sub2api-pg references)
- no line charts (no timeseries/graph/heatmap panels)
- every panel carries a description (semantics + misread risk)
- Prom panels use interval=15s
- no `or vector(1)`; coverage -1 is rendered as "无样本"
- generated JSON must match `--check`
"""
import argparse
import json
from pathlib import Path

PROM = {"type": "prometheus", "uid": "sub2api-prometheus"}

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

DIRECTIONS = [
    ("in_wire", "客户端请求体 (in_wire)", BLUE),
    ("req_decoded", "解码后请求体 (req_decoded)", AQUA),
    ("req_upstream", "发送上游请求体 (req_upstream)", ORANGE),
    ("resp_upstream", "上游响应体 (resp_upstream)", GOLD),
    ("resp_downstream", "下发客户端响应体 (resp_downstream)", PINK),
]

REASONS = [
    ("upstream_rate_limit", "上游限流", ORANGE),
    ("upstream_server_error", "上游 5xx", GOLD),
    ("upstream_read_timeout", "上游超时", PINK),
    ("client_abort_during_response", "客户端中断", VIOLET),
    ("gateway_error", "网关错误", RED),
    ("rejected", "请求被拒", GRAY),
    ("unknown", "未知", GRAY),
]

ABORT_RETRY_SERIES = [
    ("尝试 · first_success", "首次尝试成功", GREEN),
    ("尝试 · retried_success", "重试后成功", YELLOW),
    ("尝试 · all_failed", "全部尝试失败", RED),
    ("中断 · body_read", "中断 · 读取请求体", BLUE),
    ("中断 · response", "中断 · 响应传输中", VIOLET),
]

ABORT_STAGES = [
    ("body_read", "读取请求体中断", BLUE),
    ("response", "响应传输中断", VIOLET),
]


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


MODULES = [
    ("01", "总览", "sub2api-v2-overview", "overview"),
    ("02", "上游节点与链路", "sub2api-v2-nodes-chain", "nodes-chain"),
    ("03", "宿主与应用", "sub2api-v2-host", "host"),
]


def nav(current):
    left = []
    for num, title, uid, key in MODULES:
        active = key == current
        background = "#1c2229" if active else "transparent"
        color = "#f4f5f5" if active else "#828c97"
        border = "#323a43" if active else "transparent"
        weight = "600" if active else "500"
        left.append(
            f'<a href="/d/{uid}" style="display:inline-block;padding:5px 12px;margin-right:5px;'
            f'border-radius:5px;font-size:12px;text-decoration:none;font-weight:{weight};'
            f'background:{background};color:{color};border:1px solid {border}">{num} {title}</a>'
        )
    right = (
        '<a href="${admin_url}/admin/ops#realtime-requests" target="_blank" rel="noopener noreferrer" '
        'title="实时请求流与单请求明细运行在 Sub2API 管理后台（基于 Redis 环形缓冲；可在顶部「管理后台地址」变量修改域名/端口）" '
        'style="display:inline-block;padding:5px 12px;border-radius:999px;font-size:12px;'
        'text-decoration:none;font-weight:500;color:#8cc8ff;'
        'border:1px solid rgba(57,135,229,.45);background:rgba(57,135,229,.12)">'
        '实时请求流（管理后台）↗</a>'
    )
    return (
        '<div style="display:flex;align-items:center;justify-content:space-between;'
        'flex-wrap:wrap;gap:6px">'
        '<div style="display:flex;align-items:center;flex-wrap:wrap">'
        + "".join(left)
        + "</div>"
        + right
        + "</div>"
    )


def P(pid, kind, title, span, height, **kw):
    panel = dict(id=pid, kind=kind, title=title, span=span, height=height, desc="")
    panel.update(kw)
    return panel


def join_and_hide_time(field, count):
    exclude = {"Time": True}
    for index in range(1, count + 1):
        exclude[f"Time {index}"] = True
    return [
        {"id": "joinByField", "options": {"byField": field, "mode": "outer"}},
        {"id": "organize", "options": {"excludeByName": exclude}},
    ]


# ---------------------------------------------------------------------------
# Dashboard layout (3 canonical pure-Prometheus dashboards)
# ---------------------------------------------------------------------------
DASHBOARDS = [
    dict(
        id="overview", uid="sub2api-v2-overview", num="01",
        title="01 · 总览", short="总览", file="01-overview.json",
        refresh="30s", time_from="now-15m",
        panels=[
            P("ov_nav", "text", "", 24, 2, content=nav("overview"), mode="html", nav=True,
              desc="模块导航。Grafana 原生没有 Tab，使用文本链接等价实现；右侧跳转管理后台 Redis 实时请求流。"),
            P("ov_qps", "stat", "网关 QPS", 3, 4, unit="reqps", decimals=1,
              desc="网关请求速率（含 0-attempt 失败）；不是上游 attempt 速率。重试会让上游 attempt 高于此值。"),
            P("ov_success", "stat", "成功率（1h）", 3, 4, unit="percentunit", decimals=2,
              desc="1h 内 M-10 成功网关请求占比。业务失败可能发生在已写出 200 的 SSE 中，不能只看 HTTP 5xx。"),
            P("ov_p95_total", "stat", "P95 总耗时", 3, 4, unit="s", decimals=2,
              desc="当前 $__range 内网关总耗时直方图 P95；受桶边界插值影响，不是各段 P95 相加。"),
            P("ov_ttft", "stat", "P95 TTFT", 3, 4, unit="s", decimals=2,
              desc="最终 attempt 首次发送到尾响应首字节；与 upstream 重叠，不参与段堆叠。无首字节不观察。"),
            P("ov_in", "stat", "入向带宽", 3, 4, unit="Bps", decimals=2,
              desc="当前 $__range 内客户端请求体 in_wire 实际读取字节平均速率；gzip 按压缩后长度计，不使用 Content-Length。"),
            P("ov_out", "stat", "出向带宽", 3, 4, unit="Bps", decimals=2,
              desc="当前 $__range 内实际写给客户端的响应体平均速率。客户端慢读/断开会让该值低于上游返回量。"),
            P("ov_amp", "stat", "上游放大比", 3, 4, unit="x", decimals=2,
              desc="当前 $__range 内 resp_upstream / resp_downstream。>1.2 可能是客户端 abort、协议转换或流式重写；不能单独定性截断。"),
            P("ov_cov", "stat", "数据完整性", 3, 4, unit="percentunit", decimals=1,
              desc="M-9 request 覆盖率。0~1 比例；-1 表示无样本，不是 0% 或成功率。"),
            P("ov_share", "bargauge", "链路段耗时占比", 9, 10, unit="s", decimals=3,
              desc="当前 $__range 内 6 个互斥链路段平均耗时（sum/count，排除与 upstream 重叠的 headers_end）。均值受长流式请求拉高，排查看 02 页 P95。"),
            P("ov_fail", "bargauge", "失败根因 Top 6", 7, 10, unit="short", decimals=0,
              desc="近 $__range 内 M-10 失败请求数 Top 6；无失败时无数据是健康状态，不是未采集。unknown 长期偏高说明分类缺失。"),
            P("ov_coverage", "bargauge", "数据完整性明细", 8, 10, unit="percentunit", decimals=1,
              desc="4 类 M-9 覆盖率（request/account/bytes/user）。<90% 黄；-1 灰=无样本；缺序列不等于健康。"),
            P("ov_acct", "table", "上游账号速览", 14, 10,
              transformations=join_and_hide_time("account", 3),
              desc="当前 $__range 内按账号聚合的 Prometheus 上游 attempt QPS、失败率与 P95 TTFT；低样本百分比噪声大。"),
            P("ov_abort_retry", "bargauge", "客户端中断与重试概览", 10, 10, unit="short", decimals=0,
              desc="当前 $__range 内客户端按阶段中断次数与 attempt 结果分布；区分客户端主动取消、首次成功与重试恢复。"),
        ],
    ),
    dict(
        id="nodes-chain", uid="sub2api-v2-nodes-chain", num="02",
        title="02 · 上游节点与链路", short="上游节点与链路", file="02-nodes-chain.json",
        refresh="30s", time_from="now-15m",
        panels=[
            P("nc_nav", "text", "", 24, 2, content=nav("nodes-chain"), mode="html", nav=True,
              desc="模块导航。Grafana 原生没有 Tab，使用文本链接等价实现；右侧跳转管理后台 Redis 实时请求流。"),
            P("nc_online", "stat", "在线账号", 6, 3, unit="short", decimals=0,
              desc="应用上报的活跃可调度账号数（sub2api_app_accounts_active）；代表配置状态，不代表上游此刻全部健康。"),
            P("nc_capacity", "stat", "账号并发上限", 6, 3, unit="short", decimals=0,
              desc="活跃账号并发配额之和（sub2api_app_accounts_concurrency_capacity）；不是实时在途占用。"),
            P("nc_retry", "stat", "重试后成功", 6, 3, unit="percentunit", decimals=1,
              desc="M-6 retried_success / 全部 attempt 结束事件，窗口内按 $__range。比例高说明账号仍可用但需要重试；无流量时无数据。"),
            P("nc_switch", "stat", "账号切换 / 5m", 6, 3, unit="short", decimals=0,
              desc="M-7 跨账号切换次数的 5m increase。同账号重试不算；突增通常意味着上游账号故障或限流。"),
            P("nc_accounts", "table", "账号健康矩阵", 14, 10,
              transformations=join_and_hide_time("account", 4),
              desc="当前 $__range 内 Prometheus 上游 attempt 口径的请求数、QPS、失败率与 P95 TTFT；摘除/降权前先核实样本量。"),
            P("nc_models", "table", "模型档位对比", 10, 10,
              transformations=join_and_hide_time("model_class", 2),
              desc="当前 $__range 内按模型档位（model_class）聚合的请求数与成功率；重试会产生多次 attempt 计数。"),
            P("nc_phase_p95", "bargauge", "6 段耗时 P95", 6, 9, unit="s", decimals=3,
              desc="当前 $__range 内 6 个互斥链路段直方图 P95（排除 headers_end）。各段 P95 不可直接相加等于总耗时 P95。"),
            P("nc_transfer", "bargauge", "5 方向字节传输速率", 6, 9, unit="Bps", decimals=2,
              desc="当前 $__range 内 5 方向字节吞吐速率。上游/下发不相等属正常（gzip 解压、协议转换、重试或客户端中断）。"),
            P("nc_body_p95", "bargauge", "请求体大小 P95（按端点类）", 6, 9, unit="bytes", decimals=1,
              desc="当前 $__range 内按 endpoint_class 聚合的入站请求体实际字节 P95；大上下文请求会显著拉高 body_read 耗时。"),
            P("nc_aborts", "bargauge", "客户端中断分布", 6, 9, unit="short", decimals=0,
              desc="当前 $__range 内客户端断开阶段分布（body_read / response）；无中断时无数据属健康。"),
        ],
    ),
    dict(
        id="host", uid="sub2api-v2-host", num="03",
        title="03 · 宿主与应用", short="宿主与应用", file="03-host.json",
        refresh="30s", time_from="now-1h",
        panels=[
            P("hs_nav", "text", "", 24, 2, content=nav("host"), mode="html", nav=True,
              desc="模块导航。Grafana 原生没有 Tab，使用文本链接等价实现；右侧跳转管理后台 Redis 实时请求流。"),
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
            P("hs_db", "stat", "DB 连接", 3, 5, unit="short", decimals=0,
              desc="应用侧 DB 连接池各状态连接数（sub2api_app_db_connections）；与 pg_stat_activity 可能存在短暂采样差。"),
            P("hs_queue", "stat", "并发队列", 3, 5, unit="short", decimals=0,
              desc="应用上报的并发等待队列深度（sub2api_app_concurrency_queue_depth）；与 02 页 wait 段耗时互相印证。"),
            P("hs_redis", "stat", "Redis", 3, 5, unit="short", decimals=0,
              desc="应用侧 Redis 探活状态（sub2api_app_redis_up：1=OK，0=DOWN）；不代表 Redis 内部延迟或内存水位。"),
            P("hs_qps", "stat", "应用 HTTP QPS", 3, 5, unit="reqps", decimals=2,
              desc="应用整体 HTTP 请求速率（sub2api_http_requests_total，含管理接口与健康检查）；与网关 QPS 口径不同。"),
            P("hs_services", "table", "服务与进程", 14, 10,
              desc="Prometheus 抓取目标 up/版本信息。PostgreSQL/Redis 为应用探活，Grafana/Alertmanager 未纳入抓取；不要伪造未观测服务状态。"),
            P("hs_appinfo", "table", "应用信息", 10, 10,
              desc="Go/process 指标；构建版本与 commit 当前未作为指标暴露。启动时间来自 process_start_time_seconds。"),
        ],
    ),
]


# ---------------------------------------------------------------------------
# Queries. Each entry is a list of Prometheus targets for one panel.
# ---------------------------------------------------------------------------
QUERIES = {
    # 01 overview
    "ov_qps": [dict(
        expr="sum(rate(sub2api_gateway_requests_total[$__rate_interval]))",
        legend="网关 QPS",
    )],
    "ov_success": [dict(
        expr='(sum(rate(sub2api_gateway_requests_total{result="success"}[1h])) or sum(rate(sub2api_gateway_requests_total[1h])) * 0) / clamp_min(sum(rate(sub2api_gateway_requests_total[1h])), 1e-9)',
        legend="成功率",
    )],
    "ov_p95_total": [dict(
        expr="histogram_quantile(0.95, sum by (le)(rate(sub2api_gateway_request_duration_seconds_bucket[$__range])))",
        legend="P95 总耗时",
    )],
    "ov_ttft": [dict(
        expr='histogram_quantile(0.95, sum by (le)(rate(sub2api_request_phase_seconds_bucket{phase="headers_end"}[$__range])))',
        legend="P95 TTFT",
    )],
    "ov_in": [dict(
        expr='sum(rate(sub2api_transfer_bytes_total{direction="in_wire"}[$__range]))',
        legend="入向带宽",
    )],
    "ov_out": [dict(
        expr='sum(rate(sub2api_transfer_bytes_total{direction="resp_downstream"}[$__range]))',
        legend="出向带宽",
    )],
    "ov_amp": [dict(
        expr='sum(rate(sub2api_transfer_bytes_total{direction="resp_upstream"}[$__range])) / clamp_min(sum(rate(sub2api_transfer_bytes_total{direction="resp_downstream"}[$__range])), 1)',
        legend="上游放大比",
    )],
    "ov_cov": [dict(
        expr='min(sub2api_metric_coverage_ratio{kind="request"})',
        legend="数据完整性",
    )],
    "ov_share": [dict(
        expr='sum by (phase)(increase(sub2api_request_phase_seconds_sum{phase!="headers_end"}[$__range])) / clamp_min(sum by (phase)(increase(sub2api_request_phase_seconds_count{phase!="headers_end"}[$__range])), 1)',
        legend="{{phase}}",
    )],
    "ov_fail": [dict(
        expr='topk(6, sum by (failure_reason)(increase(sub2api_gateway_requests_total{result!="success"}[$__range])))',
        legend="{{failure_reason}}",
    )],
    "ov_coverage": [dict(
        expr="sub2api_metric_coverage_ratio",
        legend="{{kind}}",
    )],
    "ov_acct": [
        dict(
            expr="sum by (account)(rate(sub2api_upstream_requests_total[$__range]))",
            legend="", prom_format="table",
        ),
        dict(
            expr='sum by (account)(rate(sub2api_upstream_requests_total{result!="success"}[$__range])) / clamp_min(sum by (account)(rate(sub2api_upstream_requests_total[$__range])), 1e-9)',
            legend="", prom_format="table",
        ),
        dict(
            expr="histogram_quantile(0.95, sum by (account, le)(rate(sub2api_upstream_ttft_seconds_bucket[$__range])))",
            legend="", prom_format="table",
        ),
    ],
    "ov_abort_retry": [
        dict(
            expr="sum by (stage)(increase(sub2api_client_abort_total[$__range]))",
            legend="中断 · {{stage}}",
        ),
        dict(
            expr="sum by (outcome)(increase(sub2api_attempts_total[$__range]))",
            legend="尝试 · {{outcome}}",
        ),
    ],

    # 02 nodes & chain
    "nc_online": [dict(
        expr="max(sub2api_app_accounts_active)",
        legend="在线账号",
    )],
    "nc_capacity": [dict(
        expr="max(sub2api_app_accounts_concurrency_capacity)",
        legend="账号并发上限",
    )],
    "nc_retry": [dict(
        expr='sum(increase(sub2api_attempts_total{outcome="retried_success"}[$__range])) / clamp_min(sum(increase(sub2api_attempts_total[$__range])), 1)',
        legend="重试后成功",
    )],
    "nc_switch": [dict(
        expr="sum(increase(sub2api_account_switches_total[5m]))",
        legend="账号切换",
    )],
    "nc_accounts": [
        dict(
            expr="sum by (account)(increase(sub2api_upstream_requests_total[$__range]))",
            legend="", prom_format="table",
        ),
        dict(
            expr="sum by (account)(rate(sub2api_upstream_requests_total[$__range]))",
            legend="", prom_format="table",
        ),
        dict(
            expr='sum by (account)(rate(sub2api_upstream_requests_total{result!="success"}[$__range])) / clamp_min(sum by (account)(rate(sub2api_upstream_requests_total[$__range])), 1e-9)',
            legend="", prom_format="table",
        ),
        dict(
            expr="histogram_quantile(0.95, sum by (account, le)(rate(sub2api_upstream_ttft_seconds_bucket[$__range])))",
            legend="", prom_format="table",
        ),
    ],
    "nc_models": [
        dict(
            expr="sum by (model_class)(increase(sub2api_model_requests_total[$__range]))",
            legend="", prom_format="table",
        ),
        dict(
            expr='(sum by (model_class)(rate(sub2api_model_requests_total{result="success"}[$__range])) or sum by (model_class)(rate(sub2api_model_requests_total[$__range])) * 0) / clamp_min(sum by (model_class)(rate(sub2api_model_requests_total[$__range])), 1e-9)',
            legend="", prom_format="table",
        ),
    ],
    "nc_phase_p95": [dict(
        expr='histogram_quantile(0.95, sum by (phase, le)(rate(sub2api_request_phase_seconds_bucket{phase!="headers_end"}[$__range])))',
        legend="{{phase}}",
    )],
    "nc_transfer": [dict(
        expr="sum by (direction)(rate(sub2api_transfer_bytes_total[$__range]))",
        legend="{{direction}}",
    )],
    "nc_body_p95": [dict(
        expr="histogram_quantile(0.95, sum by (endpoint_class, le)(rate(sub2api_request_body_bytes_bucket[$__range])))",
        legend="{{endpoint_class}}",
    )],
    "nc_aborts": [dict(
        expr="sum by (stage)(increase(sub2api_client_abort_total[$__range]))",
        legend="{{stage}}",
    )],

    # 03 host & app
    "hs_hostname": [dict(expr="node_uname_info", legend="{{nodename}}")],
    "hs_uptime": [dict(expr="time() - node_boot_time_seconds", legend="运行时长")],
    "hs_go": [dict(expr='go_info{job="sub2api"}', legend="{{version}}")],
    "hs_targets": [dict(expr="count(up == 1)", legend="采集目标")],
    "hs_cpu": [dict(expr='100 - avg by(instance)(rate(node_cpu_seconds_total{mode="idle"}[$__rate_interval])) * 100', legend="CPU")],
    "hs_mem": [dict(expr="(1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes) * 100", legend="内存")],
    "hs_disk": [dict(expr='(1 - node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"}) * 100', legend="根分区")],
    "hs_load": [dict(expr='node_load5 / count without(cpu, mode)(node_cpu_seconds_total{mode="idle"})', legend="load5 / 核数")],
    "hs_net_rx": [dict(expr='sum(rate(node_network_receive_bytes_total{device!~"lo|veth.*|docker.*|br-.*"}[$__rate_interval]))', legend="接收")],
    "hs_net_tx": [dict(expr='sum(rate(node_network_transmit_bytes_total{device!~"lo|veth.*|docker.*|br-.*"}[$__rate_interval]))', legend="发送")],
    "hs_disk_r": [dict(expr="sum(rate(node_disk_read_bytes_total[$__rate_interval]))", legend="磁盘读")],
    "hs_disk_w": [dict(expr="sum(rate(node_disk_written_bytes_total[$__rate_interval]))", legend="磁盘写")],
    "hs_goroutines": [dict(expr='max(go_goroutines{job="sub2api"})', legend="goroutines")],
    "hs_heap": [dict(expr='max(go_memstats_heap_inuse_bytes{job="sub2api"})', legend="Go 堆内存")],
    "hs_rss": [dict(expr='max(process_resident_memory_bytes{job="sub2api"})', legend="进程 RSS")],
    "hs_gc": [dict(expr='max(go_gc_duration_seconds{quantile="0.95",job="sub2api"})', legend="GC Pause P95")],
    "hs_db": [dict(expr="max by (state)(sub2api_app_db_connections)", legend="{{state}}")],
    "hs_queue": [dict(expr="max(sub2api_app_concurrency_queue_depth)", legend="队列")],
    "hs_redis": [dict(expr="max(sub2api_app_redis_up)", legend="Redis")],
    "hs_qps": [dict(expr="sum(rate(sub2api_http_requests_total[$__rate_interval]))", legend="HTTP QPS")],
    "hs_services": [dict(expr='up{job=~"sub2api|prometheus|node"}', legend="", prom_format="table")],
    "hs_appinfo": [
        dict(expr='go_info{job="sub2api"}', legend="", prom_format="table"),
        dict(expr='process_start_time_seconds{job="sub2api"}', legend="", prom_format="table"),
        dict(expr='time() - process_start_time_seconds{job="sub2api"}', legend="", prom_format="table"),
    ],
}


# ---------------------------------------------------------------------------
# Styles, series mappings and table columns
# ---------------------------------------------------------------------------
# Empty traffic queries are not evidence of missing instrumentation.
# Coverage, host collection, and app state gauges retain 未采集;
# coverage's explicit -1 mapping remains 无样本.
NO_VALUE = {
    **dict.fromkeys([
        "ov_qps", "ov_success", "ov_p95_total", "ov_ttft", "ov_in", "ov_out",
        "ov_amp", "ov_share", "ov_acct", "ov_abort_retry",
        "nc_retry", "nc_switch", "nc_accounts", "nc_models",
        "nc_phase_p95", "nc_transfer", "nc_body_p95",
        "hs_qps",
    ], "无流量"),
    "ov_fail": "无失败",
    "nc_aborts": "无中断",
}

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
    "ov_share": dict(unit="s", decimals=3, min=0),
    "ov_fail": dict(unit="short", decimals=0, min=0),
    "ov_abort_retry": dict(unit="short", decimals=0, min=0),
    "nc_retry": dict(thresholds=thresholds((GRAY, None), (GREEN, 0), (YELLOW, 0.2), (RED, 0.4)), color_mode="value"),
    "nc_phase_p95": dict(unit="s", decimals=3, min=0),
    "nc_transfer": dict(unit="Bps", decimals=2, min=0),
    "nc_body_p95": dict(unit="bytes", decimals=1, min=0),
    "nc_aborts": dict(unit="short", decimals=0, min=0),
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
    "hs_redis": dict(
        thresholds=thresholds((RED, None), (GREEN, 1)),
        mappings=value_map({"1": {"text": "OK", "color": "green"}, "0": {"text": "DOWN", "color": "red"}}),
        color_mode="value",
    ),
}

SERIES = {
    "ov_share": [(key, name, color) for key, name, color in PHASE],
    "ov_fail": [(key, name, color) for key, name, color in REASONS],
    "ov_coverage": [
        ("request", "request（埋点完整）", None),
        ("account", "account（账号可知）", None),
        ("bytes", "bytes（四方向完整）", None),
        ("user", "user（用户 + 密钥）", None),
    ],
    "ov_abort_retry": ABORT_RETRY_SERIES,
    "nc_phase_p95": [(key, name, color) for key, name, color in PHASE],
    "nc_transfer": [(key, name, color) for key, name, color in DIRECTIONS],
    "nc_aborts": ABORT_STAGES,
}


def col(name, width=None, unit=None, decimals=None, align=None, cell=None, color=None,
        mappings=None, links=None, minv=None, maxv=None, wrap=None, display_name=None,
        thresh=None):
    props = []
    if display_name is not None:
        props.append({"id": "displayName", "value": display_name})
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
    if thresh is not None:
        props.append({"id": "thresholds", "value": thresh})
    elif color is not None:
        props.append({"id": "thresholds", "value": fixed(color)})
    if mappings is not None:
        props.append({"id": "mappings", "value": mappings})
    if links is not None:
        props.append({"id": "links", "value": links})
    return {"matcher": {"id": "byName", "options": name}, "properties": props}


COLUMNS = {
    "ov_acct": [
        col("account", width=180, display_name="账号"),
        col("Value #A", width=100, unit="reqps", decimals=2, align="right", display_name="QPS"),
        col("Value #B", width=100, unit="percentunit", decimals=1, align="right", display_name="失败率",
            cell="color-text", thresh=thresholds((GREEN, None), (YELLOW, 0.02), (RED, 0.05))),
        col("Value #C", width=110, unit="s", decimals=2, align="right", display_name="P95 TTFT"),
    ],
    "nc_accounts": [
        col("account", width=180, display_name="账号"),
        col("Value #A", width=100, unit="short", decimals=0, align="right", display_name="请求数"),
        col("Value #B", width=100, unit="reqps", decimals=2, align="right", display_name="QPS"),
        col("Value #C", width=100, unit="percentunit", decimals=1, align="right", display_name="失败率",
            cell="color-text", thresh=thresholds((GREEN, None), (YELLOW, 0.02), (RED, 0.05))),
        col("Value #D", width=110, unit="s", decimals=2, align="right", display_name="P95 TTFT"),
    ],
    "nc_models": [
        col("model_class", width=140, display_name="模型档位"),
        col("Value #A", width=110, unit="short", decimals=0, align="right", display_name="请求数"),
        col("Value #B", width=110, unit="percentunit", decimals=1, align="right", display_name="成功率",
            cell="color-text", thresh=thresholds((RED, None), (YELLOW, 0.95), (GREEN, 0.99))),
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
}


def make_targets(p):
    targets = []
    for index, query in enumerate(QUERIES.get(p["id"], [])):
        target = {"refId": chr(65 + index), "datasource": PROM}
        expr = query["expr"]
        if query.get("fallback"):
            expr = with_no_data(expr)
        target.update(expr=expr, legendFormat=query.get("legend", ""), range=False, instant=True)
        if query.get("prom_format"):
            target["format"] = query["prom_format"]
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
    panel = {
        "id": panel_id,
        "title": p["title"],
        "type": kind,
        "description": p["desc"],
        "datasource": PROM,
        "gridPos": {"h": p["height"], "w": p["span"], "x": x, "y": y},
        "targets": [] if kind == "text" else make_targets(p),
        "interval": "15s",
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
        "noValue": NO_VALUE.get(p["id"], "未采集"),
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
        elif p["id"].startswith("hs_"):
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
            "axisPlacement": "hidden",
            "axisLabel": "",
        }
        panel["options"] = {
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
        panel["options"] = {
            "frameIndex": 0,
            "showHeader": True,
            "showTypeIcons": False,
            "cellHeight": p.get("cell_height", "sm"),
            "maxRowHeight": p.get("max_row_height", 80),
            "enablePagination": False,
            "footer": footer,
            "sortBy": p.get("sort_by", []),
        }
        panel["fieldConfig"]["overrides"] = COLUMNS.get(p["id"], []) + [no_data_override()]
        if p.get("transformations"):
            panel["transformations"] = p["transformations"]
        if p["id"] == "hs_appinfo":
            panel["transformations"] = [
                {"id": "joinByField", "options": {"byField": "instance", "mode": "outer"}}
            ]
        return panel

    raise SystemExit(f"unsupported panel type: {kind} ({p['id']})")


ADMIN_URL_VARIABLE = {
    "name": "admin_url",
    "label": "管理后台地址",
    "type": "textbox",
    "query": "http://127.0.0.1:8080",
    "current": {"text": "http://127.0.0.1:8080", "value": "http://127.0.0.1:8080"},
    "options": [{"selected": True, "text": "http://127.0.0.1:8080", "value": "http://127.0.0.1:8080"}],
    "hide": 0,
    "skipUrlSync": False,
}


def build(spec):
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
        "tags": ["sub2api", "v2"],
        "panels": [],
        "templating": {"list": [ADMIN_URL_VARIABLE]},
        "links": [],
    }

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
        path = directory / spec["file"]
        result = json.dumps(build(spec), ensure_ascii=False, indent=2) + "\n"
        if args.check:
            if not path.exists() or path.read_text(encoding="utf-8") != result:
                raise SystemExit(f"OUT OF DATE: {path}")
        else:
            # Write in place: provisioned dashboard files may be bind-mounted.
            # Atomic replacement would leave the mount pointing at the old inode.
            with path.open("w", encoding="utf-8") as output:
                output.write(result)
    print("PASS: 3 pure-Prometheus dashboards " + ("match generator" if args.check else "generated"))


if __name__ == "__main__":
    main()
