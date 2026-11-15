#!/usr/bin/env python3
"""Authoritative V2 dashboard generator. Edit queries here, never generated JSON.
Layout/order copied from docs/dashboard-v2-mockup.html; query semantics follow API.
Grafana has dashboard refresh only; PG interval is a minimum query bucket, see docs.
"""
import argparse
import json
from pathlib import Path

PROM = {"type": "prometheus", "uid": "sub2api-prometheus"}
PG = {"type": "postgres", "uid": "sub2api-pg"}
LAYOUT = [
    {'id': 'overview', 'num': '01', 'title': '01 · 总览', 'panels': [{'id': 'k1', 'type': 'stat', 'span': 4, 'height': 'h-stat', 'title': '采集目标健康', 'unit': 'target', 'desc': '只看抓取目标是否可达，不代表业务健康。alertmanager 未部署时该目标不存在，需按实际 job 调整。'}, {'id': 'k2', 'type': 'stat', 'span': 4, 'height': 'h-stat', 'title': '入向带宽', 'unit': 'Bps', 'desc': '客户端请求体进入网关的速率。gzip 请求按压缩后长度计；不含响应方向。'}, {'id': 'k3', 'type': 'stat', 'span': 4, 'height': 'h-stat', 'title': '出向带宽', 'unit': 'Bps', 'desc': '实际写给客户端的响应体字节速率。客户端慢读/断开会让该值低于上游返回量。'}, {'id': 'k4', 'type': 'stat', 'span': 4, 'height': 'h-stat', 'title': '上游放大比', 'unit': 'x', 'desc': '上游回了多少、客户端实际收到多少的比值。>1.2 说明下游少于上游，可能是客户端 abort、协议转换或流式重写；不能单独定性为截断。'}, {'id': 'k5', 'type': 'stat', 'span': 4, 'height': 'h-stat', 'title': '成功率（1h）', 'unit': 'percent', 'desc': '5xx 占全部 HTTP 请求的比例，含管理接口与认证失败。业务失败（SSE 已开始输出后失败）不一定改状态码，需结合 02 的失败根因看。'}, {'id': 'k6', 'type': 'stat', 'span': 4, 'height': 'h-stat', 'title': 'P95 TTFT', 'unit': 's', 'desc': 'TTFT = 首个上游请求发出 → 最终响应首字节。与 upstream 重叠，不参与段堆叠；无首字节的请求不参与计算。'}, {'id': 'o7', 'type': 'line', 'span': 12, 'height': 'h-md', 'title': '链路流量漏斗', 'unit': 'Bps', 'desc': '四个关键方向的字节速率。漏斗不是严格递减：上游响应可能大于请求体，下游又可能小于上游。'}, {'id': 'o8', 'type': 'stack', 'span': 12, 'height': 'h-md', 'title': '分层错误瀑布', 'unit': 'reqps', 'desc': '按 result 堆叠的失败构成。看“谁在涨”，不要把所有非 2xx 混在一起。'}, {'id': 'o9', 'type': 'line', 'span': 6, 'height': 'h-sm', 'title': '请求速率 QPS', 'unit': 'reqps', 'desc': '网关请求与上游尝试分开：重试会让上游尝试高于网关请求。'}, {'id': 'o10', 'type': 'line', 'span': 6, 'height': 'h-sm', 'title': '4xx / 5xx 速率', 'unit': 'reqps', 'desc': 'V1 HTTP 指标口径，含管理接口；与 02 的业务失败根因不可直接对账。'}, {'id': 'o12', 'type': 'donut', 'span': 6, 'height': 'h-sm', 'title': '段耗时占比（均值）', 'unit': 'percent', 'desc': '6 个互斥段的均值占比，可堆叠。headers_end 不在其中（与 upstream 重叠）。'}, {'id': 'o13', 'type': 'bars', 'span': 6, 'height': 'h-sm', 'title': '失败根因 Top', 'unit': 'reqps', 'desc': '请求视角的 13 类根因。unknown 长期 >10% 说明分类缺失，不能当成“其他”。'}, {'id': 'o11', 'type': 'table', 'span': 16, 'height': 'h-lg', 'title': '节点健康矩阵', 'unit': '', 'desc': '10 个账号同屏对比；账号名启动时快照，新账号在重启前显示 unknown。'}, {'id': 'o14', 'type': 'line', 'span': 8, 'height': 'h-lg', 'title': '客户端中断', 'unit': 'reqps', 'desc': '只统计确认的客户端断开；超时与服务端 shutdown 不算。正常 EOF 不是 abort。'}, {'id': 'o15', 'type': 'gauge', 'span': 8, 'height': 'h-lg', 'title': '数据完整性', 'unit': 'percent', 'desc': '覆盖率不是“成功率”。bytes 96.1% 表示约 4% 请求缺某个方向的字节，带宽会被低估；低于 90% 时看板变黄。'}, {'id': 'o16', 'type': 'text', 'span': 16, 'height': 'h-lg', 'title': '定位 Playbook（值班先看这里）', 'unit': '', 'desc': '该面板是文字说明，不产生查询；上线后放在 01 右下角，值班可直接照做。'}]},
    {'id': 'chain', 'num': '02', 'title': '02 · 链路归因', 'panels': [{'id': 'c1', 'type': 'stack', 'span': 14, 'height': 'h-lg', 'title': '段耗时 P95 堆叠（6 互斥段）', 'unit': 's', 'desc': '6 个互斥段可堆叠，和等于请求总时长。headers_end 不在此图（与 upstream 重叠，单独看 TTFT）。各段 P95 不是同一请求的分解。'}, {'id': 'c2', 'type': 'donut', 'span': 10, 'height': 'h-lg', 'title': '段均值占比', 'unit': 'percent', 'desc': '均值占比可堆叠；P95 占比不可堆叠。两者不要混用。'}, {'id': 'c3', 'type': 'line', 'span': 8, 'height': 'h-md', 'title': 'TTFT 分位（P50 / P95 / P99）', 'unit': 's', 'desc': 'TTFT 只看最终 attempt 的首字节；空响应无首字节不参与。与「上游总耗时」不是一回事。'}, {'id': 'c4', 'type': 'line', 'span': 8, 'height': 'h-md', 'title': '每方向字节速率', 'unit': 'Bps', 'desc': '5 个方向的实际读写字节。resp_upstream 是解压后长度，无法与 wire 完全对账。'}, {'id': 'c5', 'type': 'line', 'span': 8, 'height': 'h-md', 'title': '字节平衡（上游 vs 下发）', 'unit': 'Bps', 'desc': '差额是上游返回与客户端收到的差。协议转换、重试失败体、客户端 abort、网关截断都会造成差额；不能只看差额就下结论。'}, {'id': 'c6', 'type': 'line', 'span': 8, 'height': 'h-md', 'title': '上游 − 下发差额占比', 'unit': 'percent', 'desc': '差额 / 上游字节。>20% 时结合 02 的客户端中断、错误根因和单请求样本判断，不直接等同“网关截断”。'}, {'id': 'c7', 'type': 'stack', 'span': 8, 'height': 'h-md', 'title': '失败根因速率（请求视角）', 'unit': 'reqps', 'desc': 'M-10 请求视角，含 0-attempt 的网关拒绝。与 M-4 的上游 attempt 视角不要混用。'}, {'id': 'c8', 'type': 'line', 'span': 8, 'height': 'h-md', 'title': '账号失败率（Top 5）', 'unit': 'percent', 'desc': '按账号的失败率。低样本账号会出现 0% 或 100% 的噪声，必须结合样本量看。'}, {'id': 'c9', 'type': 'line', 'span': 8, 'height': 'h-md', 'title': '客户端中断（body_read / response）', 'unit': 'reqps', 'desc': '只统计确认的客户端断开；超时不在此列。response 段的中断常伴随下游字节小于上游。'}, {'id': 'c10', 'type': 'bars', 'span': 8, 'height': 'h-md', 'title': '尝试结果分布', 'unit': 'count', 'desc': '每网关请求一次：首次成功 / 重试后成功 / 全失败。all_failed 含 0-attempt 的网关拒绝。'}, {'id': 'c11', 'type': 'stat', 'span': 8, 'height': 'h-md', 'title': '账号切换速率', 'unit': 'reqps', 'desc': '跨账号切换次数；同账号重试不算。切换速率上升通常意味着某些账号在持续失败。'}, {'id': 'c12', 'type': 'gauge', 'span': 8, 'height': 'h-lg', 'title': '数据完整性', 'unit': 'percent', 'desc': '覆盖不足时先看是哪个 kind：bytes 低会让带宽与差额面板失真；request 低说明有路径没接入埋点。'}, {'id': 'c13', 'type': 'heatmap', 'span': 16, 'height': 'h-lg', 'title': '请求体大小分布', 'unit': 'count', 'desc': '按 endpoint_class 的请求体直方图。超大请求会拉长 body_read，并可能触发上游 413/超时。'}, {'id': 'c14', 'type': 'stat', 'span': 8, 'height': 'h-lg', 'title': '大请求体占比', 'unit': 'percent', 'desc': '大请求体与超时/5xx 的相关性要结合 02 的慢样本看，单看占比不能定性。'}, {'id': 'c15', 'type': 'table', 'span': 16, 'height': 'h-lg', 'title': '错误构成（platform × account × result）', 'unit': '', 'desc': '错误构成表用于定位“集中在谁”。count 是窗口内样本数，rate 是该账号请求占比。'}, {'id': 'c16', 'type': 'table', 'span': 14, 'height': 'h-table', 'title': '慢样本 Top 40（PG 明细）', 'unit': '', 'desc': 'PG 明细，用来把“哪一段慢”落到单请求。不展示 IP；不假设样本可代表总体分位。'}, {'id': 'c17', 'type': 'bars', 'span': 10, 'height': 'h-table', 'title': '单请求分段分解（选中样本）', 'unit': 'ms', 'desc': '选中 Top 40 中的一行后展示该请求的真实分解。总和应等于 total_ms；不等说明有未归属时间。'}, {'id': 'c18', 'type': 'text', 'span': 16, 'height': 'h-md', 'title': '判定树（与 01 Playbook 同源）', 'unit': '', 'desc': '文字面板；上线后放在 02 底部，作为值班 SOP。'}, {'id': 'c19', 'type': 'text', 'span': 8, 'height': 'h-md', 'title': '口径与“不可信数字”', 'unit': '', 'desc': '把容易误读的数字集中在面板上说明，避免值班时把相关当因果。'}]},
    {'id': 'nodes', 'num': '03', 'title': '03 · 上游节点', 'panels': [{'id': 'n1', 'type': 'table', 'span': 24, 'height': 'h-table', 'title': '账号健康矩阵', 'unit': '', 'desc': '行=账号；摘除/降权前确认样本量与持续时间。低 QPS 账号的百分比噪声大。'}, {'id': 'n2', 'type': 'line', 'span': 8, 'height': 'h-md', 'title': '账号 QPS', 'unit': 'reqps', 'desc': '每账号的上游尝试 QPS（含重试）。与「网关请求 QPS」不同。'}, {'id': 'n3', 'type': 'line', 'span': 8, 'height': 'h-md', 'title': '账号失败率', 'unit': 'percent', 'desc': '失败率 = 该账号非 success 尝试 / 该账号全部尝试。重试失败会推高该值。'}, {'id': 'n4', 'type': 'line', 'span': 8, 'height': 'h-md', 'title': '账号 P95 TTFT', 'unit': 's', 'desc': 'M-11 是每 attempt 的 TTFT 直方图，不是每请求；重试会贡献多个样本。'}, {'id': 'n5', 'type': 'stack', 'span': 12, 'height': 'h-md', 'title': '账号带宽（Top 5）', 'unit': 'Bps', 'desc': '按账号的下发字节速率（M-2 没有 account 标签，需按请求上下文分桶；实现为每账号聚合计数）。'}, {'id': 'n6', 'type': 'bars', 'span': 12, 'height': 'h-md', 'title': '在途与并发利用率', 'unit': 'percent', 'desc': '在途 = 正在该账号上执行的 attempt；利用率接近 100% 时新增请求会进入 wait。'}, {'id': 'n7', 'type': 'line', 'span': 16, 'height': 'h-md', 'title': '重试与降权信号', 'unit': 'percent', 'desc': '辅助判断“该不该摘”：重试后成功比例高说明账号仍可用但慢；全失败比例高且集中在单账号才考虑降权。'}, {'id': 'n8', 'type': 'table', 'span': 8, 'height': 'h-md', 'title': '今日请求 / Token / 成本', 'unit': '', 'desc': 'PG 口径是 usage_logs 落库样本，与 Prometheus 的请求数不可直接对账。'}, {'id': 'n9', 'type': 'table', 'span': 24, 'height': 'h-md', 'title': '模型档位对比', 'unit': '', 'desc': '模型按 4+1 档聚合，不使用原始 model 标签。请求数是每 attempt 计数，重试会重复。'}]},
    {'id': 'users', 'num': '04', 'title': '04 · 用户/密钥', 'panels': [{'id': 'u1', 'type': 'line', 'span': 12, 'height': 'h-md', 'title': '用户请求量', 'unit': 'count', 'desc': 'PG 5 分钟桶。变量 $user 为空（-1）时显示 Top 用户；选中后只看该用户。'}, {'id': 'u2', 'type': 'statgrid', 'span': 12, 'height': 'h-md', 'title': '今日 Token / 成本', 'unit': '', 'desc': 'PG 口径，含缓存 token；与账单系统的最终金额可能有四舍五入差异。'}, {'id': 'u3', 'type': 'table', 'span': 8, 'height': 'h-md', 'title': 'API Key 流量', 'unit': '', 'desc': 'Key 名称是用户可改的展示名，不作为 Prometheus 标签；只在 PG 面板出现。'}, {'id': 'u4', 'type': 'table', 'span': 16, 'height': 'h-md', 'title': '慢请求明细（>30s）', 'unit': '', 'desc': '按 duration_ms 降序的 PG 明细。IP 字段全是 Cloudflare 边缘 IP，按 Q10 决定不展示。'}, {'id': 'u5', 'type': 'line', 'span': 12, 'height': 'h-md', 'title': '用户延迟分位', 'unit': 's', 'desc': 'PG 精确分位，基于 usage_logs 全量样本（不是采样）。与 Prometheus 的 HTTP 延迟口径不同。'}, {'id': 'u6', 'type': 'bars', 'span': 12, 'height': 'h-md', 'title': '用户错误构成', 'unit': 'count', 'desc': '用户维度只看 PG 的 result，不细分 failure_reason（12 类只在 02 的链路面板展开）。'}]},
    {'id': 'host', 'num': '05', 'title': '05 · 宿主与应用', 'panels': [{'id': 'h1', 'type': 'line', 'span': 8, 'height': 'h-md', 'title': '宿主 CPU 使用率', 'unit': 'percent', 'desc': '2 vCPU 机器的整机 CPU。process_* 是应用进程，不能与整机百分比直接对比。'}, {'id': 'h2', 'type': 'line', 'span': 8, 'height': 'h-md', 'title': '宿主内存', 'unit': 'percent', 'desc': 'swap 已用 800MB+ 时，内存压力会先体现在 load 与响应抖动，而不是 OOM。'}, {'id': 'h3', 'type': 'gauge', 'span': 8, 'height': 'h-md', 'title': '根分区使用率', 'unit': 'percent', 'desc': '只代表根分区；Prometheus TSDB 与备份目录若分开挂载需另看。'}, {'id': 'h4', 'type': 'line', 'span': 8, 'height': 'h-md', 'title': '磁盘 IO', 'unit': 'Bps', 'desc': '宿主磁盘吞吐。Prometheus 压缩/快照与 PG WAL 都会造成写峰值。'}, {'id': 'h5', 'type': 'line', 'span': 8, 'height': 'h-md', 'title': '宿主网卡（排除虚拟设备）', 'unit': 'Bps', 'desc': '整机聚合，排除 lo/veth/docker/bridge。与网关自报字节口径不同，不可精确对账。'}, {'id': 'h6', 'type': 'line', 'span': 8, 'height': 'h-md', 'title': 'load5 / 核数', 'unit': 'x', 'desc': '按核数归一化的 load，避免硬编码 2 核。>2 说明有明显排队。'}, {'id': 'h7', 'type': 'line', 'span': 8, 'height': 'h-md', 'title': 'goroutines', 'unit': 'count', 'desc': 'Go 运行时 goroutine 数。与并发队列深度一起看，区分“真并发”与“卡住”。'}, {'id': 'h8', 'type': 'line', 'span': 8, 'height': 'h-md', 'title': 'Go 堆内存', 'unit': 'bytes', 'desc': 'Go heap 不等于 RSS；容器内存 limit 是 RSS 口径。'}, {'id': 'h9', 'type': 'stat', 'span': 8, 'height': 'h-md', 'title': '进程 RSS', 'unit': 'bytes', 'desc': '应用进程 RSS，含 Go runtime 与未归还内存；与 node 的整机内存不同口径。'}, {'id': 'h10', 'type': 'line', 'span': 8, 'height': 'h-md', 'title': 'DB 连接', 'unit': 'count', 'desc': '来自 ops_system_metrics 的应用侧连接数；与 PG 侧 pg_stat_activity 可能有时差。'}, {'id': 'h11', 'type': 'stat', 'span': 4, 'height': 'h-md', 'title': 'Redis 状态', 'unit': '', 'desc': '应用探活结果，不代表 Redis 内部指标；连接池接近上限时需看应用队列。'}, {'id': 'h12', 'type': 'line', 'span': 6, 'height': 'h-md', 'title': '并发队列深度', 'unit': 'count', 'desc': '应用调度队列深度；与 02 的 wait 段、03 的并发利用率互相印证。'}, {'id': 'h13', 'type': 'line', 'span': 6, 'height': 'h-md', 'title': '应用 QPS / TPS', 'unit': 'reqps', 'desc': 'ops_system_metrics 的应用口径，与 Prometheus 的 HTTP QPS 不可对账。'}, {'id': 'h14', 'type': 'line', 'span': 8, 'height': 'h-table', 'title': '账号切换计数', 'unit': 'count', 'desc': '应用侧每分钟聚合的切换次数；与 M-7 的 Prometheus 计数口径不同，不可对账。'}, {'id': 'h15', 'type': 'table', 'span': 16, 'height': 'h-table', 'title': '应用系统参数（窗口聚合）', 'unit': '', 'desc': '应用侧每分钟聚合值，和宿主/进程指标语义不同；表头注明“窗口聚合值”。'}]},
]

# One target query per line. Keys follow the reviewed mockup panel IDs.
PG_IDS = {'h12', 'u3', 'u2', 'u4', 'h13', 'n5', 'c17', 'c16', 'n8', 'h15', 'u5', 'u6', 'c15', 'h11', 'h10', 'n1', 'h14', 'u1'}
QUERIES = {
    'k1': [
        'min(up) by (job)',
    ],
    'k2': [
        'sum(rate(sub2api_transfer_bytes_total{direction="in_wire"}[5m]))',
    ],
    'k3': [
        'sum(rate(sub2api_transfer_bytes_total{direction="resp_downstream"}[5m]))',
    ],
    'k4': [
        '(sum(rate(sub2api_transfer_bytes_total{direction="resp_upstream"}[10m]))) / clamp_min(sum(rate(sub2api_transfer_bytes_total{direction="resp_downstream"}[10m])), 1)',
    ],
    'k5': [
        '(1 - sum(rate(sub2api_http_requests_total{status_code=~"5.."}[1h])) / clamp_min(sum(rate(sub2api_http_requests_total[1h])),1e-9))*100',
    ],
    'k6': [
        'histogram_quantile(0.95, sum by (le) (rate(sub2api_request_phase_seconds_bucket{phase="headers_end"}[10m])))',
    ],
    'o7': [
        'sum by (direction) (rate(sub2api_transfer_bytes_total[$__rate_interval]))',
    ],
    'o8': [
        'sum by (result) (rate(sub2api_gateway_requests_total{result!="success"}[$__rate_interval]))',
    ],
    'o9': [
        'sum(rate(sub2api_gateway_requests_total[$__rate_interval]))',
        'sum(rate(sub2api_upstream_requests_total[$__rate_interval]))',
    ],
    'o10': [
        'sum by (status_code) (rate(sub2api_http_requests_total{status_code=~"4..|5.."}[5m]))',
    ],
    'o12': [
        'sum by (phase)(rate(sub2api_request_phase_seconds_sum{phase!="headers_end"}[$__rate_interval])) / clamp_min(sum(rate(sub2api_request_phase_seconds_sum{phase!="headers_end"}[$__rate_interval])),1e-9)*100',
    ],
    'o13': [
        'sum by (failure_reason) (rate(sub2api_gateway_requests_total{result!="success"}[$__rate_interval]))',
    ],
    'o11': [
        'sum by (account)(rate(sub2api_upstream_requests_total[$__rate_interval]))',
        'sum by (account)(rate(sub2api_upstream_requests_total{result!="success"}[5m])) / clamp_min(sum by (account)(rate(sub2api_upstream_requests_total[5m])),1e-9)*100',
        'histogram_quantile(0.95, sum by (le,account)(rate(sub2api_upstream_ttft_seconds_bucket[$__rate_interval])))',
    ],
    'o14': [
        'sum by (stage) (rate(sub2api_client_abort_total[$__rate_interval]))',
    ],
    'o15': [
        'sub2api_metric_coverage_ratio',
    ],
    'c1': [
        'histogram_quantile(0.95, sum by (le,phase)(rate(sub2api_request_phase_seconds_bucket{phase!="headers_end"}[$__rate_interval])))',
    ],
    'c2': [
        'sum by (phase)(rate(sub2api_request_phase_seconds_sum{phase!="headers_end"}[$__rate_interval])) / clamp_min(sum(rate(sub2api_request_phase_seconds_sum{phase!="headers_end"}[$__rate_interval])),1e-9)*100',
    ],
    'c3': [
        'histogram_quantile(0.5, sum by (le)(rate(sub2api_request_phase_seconds_bucket{phase="headers_end"}[$__rate_interval])))',
        'histogram_quantile(0.95, sum by (le)(rate(sub2api_request_phase_seconds_bucket{phase="headers_end"}[$__rate_interval])))',
        'histogram_quantile(0.99, sum by (le)(rate(sub2api_request_phase_seconds_bucket{phase="headers_end"}[$__rate_interval])))',
    ],
    'c4': [
        'sum by (direction) (rate(sub2api_transfer_bytes_total[$__rate_interval]))',
    ],
    'c5': [
        'sum(rate(sub2api_transfer_bytes_total{direction="resp_upstream"}[$__rate_interval]))',
        'sum(rate(sub2api_transfer_bytes_total{direction="resp_downstream"}[$__rate_interval]))',
        'sum(rate(sub2api_transfer_bytes_total{direction="resp_upstream"}[$__rate_interval])) - sum(rate(sub2api_transfer_bytes_total{direction="resp_downstream"}[$__rate_interval]))',
    ],
    'c6': [
        '(sum(rate(sub2api_transfer_bytes_total{direction="resp_upstream"}[10m])) - sum(rate(sub2api_transfer_bytes_total{direction="resp_downstream"}[10m]))) / clamp_min(sum(rate(sub2api_transfer_bytes_total{direction="resp_upstream"}[10m])),1)*100',
    ],
    'c7': [
        'sum by (result, failure_reason) (rate(sub2api_gateway_requests_total{result!="success"}[$__rate_interval]))',
    ],
    'c8': [
        'sum by (account)(rate(sub2api_upstream_requests_total{result!="success"}[5m])) / clamp_min(sum by (account)(rate(sub2api_upstream_requests_total[5m])),1e-9)*100',
    ],
    'c9': [
        'sum by (stage) (rate(sub2api_client_abort_total[$__rate_interval]))',
    ],
    'c10': [
        'sum by (outcome) (rate(sub2api_attempts_total[$__rate_interval]))',
    ],
    'c11': [
        'sum(rate(sub2api_account_switches_total[$__rate_interval]))',
    ],
    'c12': [
        'sub2api_metric_coverage_ratio',
    ],
    'c13': [
        'sum by (le) (increase(sub2api_request_body_bytes_bucket[$__interval]))',
    ],
    'c14': [
        '(sum(rate(sub2api_request_body_bytes_count[5m])) - sum(rate(sub2api_request_body_bytes_bucket{le="262144"}[5m]))) / clamp_min(sum(rate(sub2api_request_body_bytes_count[5m])),1e-9)*100',
    ],
    'c15': [
        'SELECT platform, account_id, result, count(*) AS requests, count(*)*100.0/sum(count(*)) OVER (PARTITION BY account_id) AS percent FROM ops_request_transfer_samples WHERE $__timeFilter(observed_at) GROUP BY platform,account_id,result ORDER BY requests DESC LIMIT 40',
    ],
    'c16': [
        'SELECT request_id, observed_at,total_ms,body_read_ms,gateway_ms,wait_ms,upstream_ms,headers_end_ms,response_ms,finish_ms,result,failure_reason,account_id,model FROM ops_request_transfer_samples WHERE $__timeFilter(observed_at) ORDER BY total_ms DESC LIMIT 40',
    ],
    'c17': [
        'SELECT body_read_ms,gateway_ms,wait_ms,upstream_ms,response_ms,finish_ms,total_ms FROM ops_request_transfer_samples WHERE request_id = ${request:sqlstring} AND $__timeFilter(observed_at) ORDER BY observed_at DESC LIMIT 1',
    ],
    'n1': [
        "SELECT account_id, max(platform) AS platform, count(*) AS requests, count(*) FILTER (WHERE result != 'success')*100.0/count(*) AS failure_percent, percentile_cont(0.95) WITHIN GROUP (ORDER BY headers_end_ms) AS request_ttft_p95_ms FROM ops_request_transfer_samples WHERE $__timeFilter(observed_at) GROUP BY account_id ORDER BY requests DESC LIMIT 40",
    ],
    'n2': [
        'sum by (account)(rate(sub2api_upstream_requests_total[$__rate_interval]))',
    ],
    'n3': [
        'sum by (account)(rate(sub2api_upstream_requests_total{result!="success"}[5m])) / clamp_min(sum by (account)(rate(sub2api_upstream_requests_total[5m])),1e-9)*100',
    ],
    'n4': [
        'histogram_quantile(0.95, sum by (le,account)(rate(sub2api_upstream_ttft_seconds_bucket[$__rate_interval])))',
    ],
    'n5': [
        "SELECT $__timeGroupAlias(observed_at,'1m'), account_id::text AS metric, sum(bytes_resp_downstream)/60.0 AS bytes_per_second FROM ops_request_transfer_samples WHERE $__timeFilter(observed_at) GROUP BY 1,2 ORDER BY 1",
    ],
    'n7': [
        'sum by(outcome)(rate(sub2api_attempts_total[$__rate_interval])) / clamp_min(sum(rate(sub2api_attempts_total[$__rate_interval])),1e-9)*100',
        'sum(rate(sub2api_account_switches_total[$__rate_interval])) / clamp_min(sum(rate(sub2api_gateway_requests_total[$__rate_interval])),1e-9)*100',
    ],
    'n8': [
        "SELECT account_id,count(*) AS requests,sum(input_tokens+output_tokens) AS tokens,sum(total_cost) AS cost FROM usage_logs WHERE created_at >= date_trunc('day',now()) GROUP BY account_id ORDER BY requests DESC LIMIT 40",
    ],
    'n9': [
        'sum by(model_class,result)(rate(sub2api_model_requests_total[$__rate_interval]))',
    ],
    'u1': [
        "SELECT $__timeGroupAlias(created_at,'5m'), user_id::text AS metric,count(*) AS requests FROM usage_logs WHERE $__timeFilter(created_at) AND ($user = -1 OR user_id = $user) AND ($api_key = -1 OR api_key_id = $api_key) GROUP BY 1,2 ORDER BY 1",
    ],
    'u2': [
        "SELECT sum(input_tokens+output_tokens) AS tokens,sum(total_cost) AS cost,sum(cache_read_tokens)::float/nullif(sum(input_tokens),0) AS cache_ratio,count(*) AS requests FROM usage_logs WHERE created_at >= date_trunc('day',now()) AND ($user = -1 OR user_id = $user) AND ($api_key = -1 OR api_key_id = $api_key)",
    ],
    'u3': [
        'SELECT api_key_id,count(*) AS requests,sum(input_tokens+output_tokens) AS tokens FROM usage_logs WHERE $__timeFilter(created_at) AND ($user = -1 OR user_id = $user) AND ($api_key = -1 OR api_key_id = $api_key) GROUP BY api_key_id ORDER BY requests DESC LIMIT 40',
    ],
    'u4': [
        'SELECT created_at,user_id,api_key_id,model,duration_ms,first_token_ms,body_bytes FROM usage_logs WHERE $__timeFilter(created_at) AND duration_ms > 30000 AND ($user = -1 OR user_id = $user) AND ($api_key = -1 OR api_key_id = $api_key) ORDER BY duration_ms DESC LIMIT 40',
    ],
    'u5': [
        "SELECT $__timeGroupAlias(created_at,'5m'),percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_ms)/1000 AS p50,percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms)/1000 AS p95,percentile_cont(0.99) WITHIN GROUP (ORDER BY duration_ms)/1000 AS p99 FROM usage_logs WHERE $__timeFilter(created_at) AND ($user = -1 OR user_id = $user) AND ($api_key = -1 OR api_key_id = $api_key) GROUP BY 1 ORDER BY 1",
    ],
    'u6': [
        'SELECT result,count(*) AS requests FROM ops_request_transfer_samples WHERE $__timeFilter(observed_at) AND ($user = -1 OR user_id = $user) AND ($api_key = -1 OR api_key_id = $api_key) GROUP BY result',
    ],
    'h1': [
        '100 - avg by(instance) (rate(node_cpu_seconds_total{mode="idle"}[$__rate_interval])) * 100',
    ],
    'h2': [
        '(1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes) * 100',
    ],
    'h3': [
        '(1 - node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"}) * 100',
    ],
    'h4': [
        'rate(node_disk_read_bytes_total[$__rate_interval])',
        'rate(node_disk_written_bytes_total[$__rate_interval])',
    ],
    'h5': [
        'sum(rate(node_network_receive_bytes_total{device!~"lo|veth.*|docker.*|br-.*"}[$__rate_interval]))',
        'sum(rate(node_network_transmit_bytes_total{device!~"lo|veth.*|docker.*|br-.*"}[$__rate_interval]))',
    ],
    'h6': [
        'node_load5 / count without(cpu, mode)(node_cpu_seconds_total{mode="idle"})',
    ],
    'h7': [
        'go_goroutines{job="sub2api"}',
    ],
    'h8': [
        'go_memstats_heap_inuse_bytes{job="sub2api"}',
        'go_memstats_heap_sys_bytes{job="sub2api"}',
    ],
    'h9': [
        'process_resident_memory_bytes{job="sub2api"}',
    ],
    'h10': [
        "SELECT $__timeGroupAlias(created_at,'1m'),avg(db_conn_active) AS active,avg(db_conn_idle) AS idle FROM ops_system_metrics WHERE $__timeFilter(created_at) GROUP BY 1 ORDER BY 1",
    ],
    'h11': [
        "SELECT $__timeGroupAlias(created_at,'1m'),min(redis_ok::int) AS redis_ok FROM ops_system_metrics WHERE $__timeFilter(created_at) GROUP BY 1 ORDER BY 1",
    ],
    'h12': [
        "SELECT $__timeGroupAlias(created_at,'1m'),avg(concurrency_queue_depth) AS queue_depth FROM ops_system_metrics WHERE $__timeFilter(created_at) GROUP BY 1 ORDER BY 1",
    ],
    'h13': [
        "SELECT $__timeGroupAlias(created_at,'1m'),avg(qps) AS qps,avg(tps) AS tps FROM ops_system_metrics WHERE $__timeFilter(created_at) GROUP BY 1 ORDER BY 1",
    ],
    'h14': [
        "SELECT $__timeGroupAlias(created_at,'1m'),sum(account_switch_count) AS switches FROM ops_system_metrics WHERE $__timeFilter(created_at) GROUP BY 1 ORDER BY 1",
    ],
    'h15': [
        "SELECT $__timeGroupAlias(created_at,'1m'),avg(cpu_usage_percent) AS cpu,avg(memory_usage_percent) AS memory,min(db_ok::int) AS db_ok,min(redis_ok::int) AS redis_ok,avg(goroutine_count) AS goroutines,avg(concurrency_queue_depth) AS queue_depth,sum(account_switch_count) AS switches FROM ops_system_metrics WHERE $__timeFilter(created_at) GROUP BY 1 ORDER BY 1 DESC LIMIT 40",
    ],
}

VARIABLES = [
    ('user','SELECT id AS __value, COALESCE(email, id::text) AS __text FROM users ORDER BY 2 LIMIT 1000'),
    ('api_key','SELECT id AS __value, name AS __text FROM api_keys WHERE ($user = -1 OR user_id = $user) ORDER BY 2 LIMIT 1000'),
    ('request','SELECT request_id AS __value, request_id AS __text FROM ops_request_transfer_samples WHERE $__timeFilter(observed_at) ORDER BY total_ms DESC LIMIT 40'),
]


def with_no_data(query):
    # Only synthesize the labelled zero when the whole original vector is empty.
    # `or` alone would add it even alongside real labelled data or a real zero.
    return f'({query}) or (label_replace(vector(0), "collection", "未采集", "", "") unless on() ({query}))'


def build(spec):
    dashboard = dict(uid='sub2api-v2-'+spec['id'],title=spec['title'],schemaVersion=39,version=1,editable=False,allowUiUpdates=False,timezone='browser',refresh='10s' if spec['id']=='chain' else '30s',time={'from':'now-6h','to':'now'},tags=['sub2api','v2'],panels=[],templating={'list':[]},links=[])
    varnames = ['user','api_key'] if spec['id']=='users' else (['request'] if spec['id']=='chain' else [])
    for name,sql in VARIABLES:
        if name in varnames:
            dashboard['templating']['list'].append(dict(name=name,label=name,type='query',datasource=PG,query=sql,definition=sql,refresh=2,includeAll=name!='request',allValue='-1' if name!='request' else None,multi=False,current={'text':'All','value':'$__all'} if name!='request' else {},options=[]))
    x=y=row_height=0
    for i,p in enumerate(spec['panels'],1):
        key=p['id']; width=p['span']; height={'h-stat':4,'h-sm':7,'h-md':8,'h-lg':10,'h-table':10}[p['height']]
        if x+width>24: y+=row_height; x=row_height=0
        source=PG if key in PG_IDS else PROM
        text=p['type']=='text' or key=='n6'
        kind='text' if text else {'line':'timeseries','stack':'timeseries','donut':'piechart','bars':'barchart','statgrid':'stat'}.get(p['type'],p['type'])
        desc=p['desc']+' 口径按 V2 契约；缺序列显示未采集，0 不代表请求成功。'
        if key in {'k6','c3'}: desc='最终 attempt 首次发送至其首字节；与 upstream 重叠，无首字节不观察。不得与段耗时相加。'
        if key=='c1': desc='各互斥段分别取 P95，仅用于识别主导段；各段 P95 之和不是总 P95，也不是同一请求分解。排除 headers_end。'
        if key in {'n1','n5','u6'}: desc+=' 使用 PG 请求样本（非 attempt），受落库覆盖率影响。'
        if key in {'o11','n2','n3','n4'}: desc+=' 账号启动时快照最多十个；新增/改名重启前为 unknown。'
        if key=='n9': desc='按 model_class/result 的上游 attempt 速率；M-11 无 model_class 标签，因此不展示模型 TTFT，重试会重复计数。'
        if key=='c14': desc='请求体 >256 KiB（262144 byte）的占比，使用 M-3 已有直方图桶；零流量为 0，未采集单独标记。大请求体与超时的相关性不能单独定性。'
        if key in {'o15','c12'}: desc='M-9 覆盖率为 0~1 ratio，按百分比显示；<90% 黄、≥90% 绿；-1 表示无样本，不是 0% 或成功率。'
        if source==PG: desc+=' PG 最小查询间隔 1m；混合看板刷新限制见本地验收文档。'
        panel=dict(id=i,title=p['title'],type=kind,description=desc,datasource=source,gridPos={'x':x,'y':y,'w':width,'h':height},targets=[],interval='1m' if source==PG else '15s',fieldConfig={'defaults':{'unit':{'count':'short','target':'short'}.get(p['unit'],p['unit']),'noValue':'未采集','mappings':[],'thresholds':{'mode':'absolute','steps':[{'color':'green','value':None}]}},'overrides':[]},options={})
        if key=='c14': panel['title']='大请求体占比（>256 KiB / 262144 byte）'
        if key in {'o15','c12'}:
            panel['fieldConfig']['defaults'].update(unit='percentunit', min=0, max=1,
                mappings=[{'type':'value','options':{'-1':{'text':'无样本','color':'gray'}}}],
                thresholds={'mode':'absolute','steps':[{'color':'gray','value':None},{'color':'yellow','value':0},{'color':'green','value':0.9}]})
        x+=width;row_height=max(row_height,height)
        if text:
            content='未采集：当前契约没有账号在途/并发利用率指标，不能从请求完成样本推算实时在途。' if key=='n6' else ('先看流量与错误 → 段均值与 TTFT → 账号样本量 → PG 单请求。\n\n字节差不等于截断；各段 P95 不可相加；headers_end 不参与堆叠。')
            panel['options']={'mode':'markdown','content':content}
        else:
            for j,query in enumerate(QUERIES[key]):
                target={'refId':chr(65+j),'datasource':source}
                if source==PG: target.update(rawSql=query,format='time_series' if kind=='timeseries' else 'table',rawQuery=True,editorMode='code')
                else:
                    # Label the synthetic fallback only, keeping real zero values intact.
                    target.update(expr=with_no_data(query),legendFormat='{{collection}} {{account}} {{phase}} {{result}} {{direction}} {{stage}} {{kind}} {{model_class}}',range=True,instant=kind in {'stat','gauge','table','barchart','piechart'})
                panel['targets'].append(target)
            if p['type']=='stack': panel['fieldConfig']['defaults']['custom']={'stacking':{'mode':'normal','group':'A'}}
            if kind=='table' and source==PROM: panel['transformations']=[{'id':'joinByField','options':{'byField':'account','mode':'outer'}}]
            if key in {'c15','c16','u3','u4','n8','n1','h15'}:panel['title']+=' · Top 40'
            if key=='c16':
                panel['fieldConfig']['overrides'].append({'matcher':{'id':'byName','options':'request_id'},'properties':[{'id':'links','value':[{'title':'查看请求分解','url':'/d/sub2api-v2-chain?var-request=${__value.raw}&from=${__from}&to=${__to}','targetBlank':False}]}]})
            panel['fieldConfig']['overrides'].append({'matcher':{'id':'byRegexp','options':'.*未采集.*'},'properties':[{'id':'displayName','value':'未采集'}]})
        dashboard['panels'].append(panel)
    return dashboard


def main():
    parser=argparse.ArgumentParser();parser.add_argument('--check',action='store_true');args=parser.parse_args()
    directory=Path(__file__).parent/'dashboards'
    for spec in LAYOUT:
        path=directory/(spec['num']+'-'+spec['id']+'.json')
        result=json.dumps(build(spec),ensure_ascii=False,indent=2)+'\n'
        if args.check:
            if not path.exists() or path.read_text()!=result: raise SystemExit(f'OUT OF DATE: {path}')
        else:path.write_text(result)
    print('PASS: five V2 dashboards '+('match generator' if args.check else 'generated'))

if __name__=='__main__': main()
