#!/usr/bin/env python3
"""Opaque-box E2E test suite for sub2api Ops Monitoring & Troubleshooting (F13-F15).

Covers Tiers 1-4 using Python 3.12 stdlib only (no PyYAML dependency):
  - Tier 1: Baseline build, lint, structural validity, rule/test syntax (>=5 per feature)
  - Tier 2: Boundary & corner cases for 18 canonical failure_reason enums, result split,
            HTTP-only & streaming-response panel notes, WS metric isolation, stale metric
            elimination, WS-101 and 4xx non-firing regressions (>=5 per feature)
  - Tier 3: Cross-stack pairwise checks (Backend Go <-> Frontend TS/i18n <-> Grafana,
            Generator <-> Linter <-> JSON, chain.yml <-> v2_test.yml <-> monitoring-rules-test.yml,
            WS metrics <-> WS dashboard isolation)
  - Tier 4: End-to-end static CI preflight pipeline + conditional live promtool/Docker checks
"""

from __future__ import annotations

import json
import re
import runpy
import shutil
import subprocess
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
GRAFANA_DIR = ROOT / "deploy/monitoring/grafana"
DASHBOARDS_DIR = GRAFANA_DIR / "dashboards"
BUILD_DASHBOARDS_PY = GRAFANA_DIR / "build-dashboards.py"
LINT_DASHBOARDS_PY = GRAFANA_DIR / "lint-dashboards.py"
RULES_DIR = ROOT / "deploy/monitoring/prometheus/rules"
CHAIN_YML = RULES_DIR / "chain.yml"
SLO_YML = RULES_DIR / "slo.yml"
INFRA_YML = RULES_DIR / "infra.yml"
V2_TEST_YML = RULES_DIR / "tests/v2_test.yml"
MONITORING_RULES_TEST_YML = ROOT / "deploy/tests/monitoring-rules-test.yml"
MONITORING_V2_DASHBOARD_TEST_PY = ROOT / "deploy/tests/monitoring-v2-dashboard-test.py"
MONITORING_DEPLOY_TEST_PY = ROOT / "deploy/tests/monitoring-deploy-test.py"

BACKEND_MONITORING_DIR = ROOT / "backend/internal/monitoring"
OPS_LABELS_GO = BACKEND_MONITORING_DIR / "ops_labels.go"
OPS_RECORDER_GO = BACKEND_MONITORING_DIR / "ops_recorder.go"
OPS_METRICS_GO = BACKEND_MONITORING_DIR / "ops_metrics.go"

FRONTEND_FORMATTERS_TS = ROOT / "frontend/src/views/admin/ops/utils/realtimeRequestFormatters.ts"
FRONTEND_ZH_OPS_TS = ROOT / "frontend/src/i18n/locales/zh/admin/ops.ts"
FRONTEND_EN_OPS_TS = ROOT / "frontend/src/i18n/locales/en/admin/ops.ts"

PROM_UID = "sub2api-prometheus"
ALLOWED_PANEL_TYPES = frozenset({"stat", "bargauge", "barchart", "table", "text"})
FORBIDDEN_PANEL_TYPES = frozenset({"timeseries", "graph", "heatmap"})
CANONICAL_DASHBOARD_FILES = ("01-overview.json", "02-nodes-chain.json", "03-host.json")
MAIN_UIDS = (
    "sub2api-v2-overview",
    "sub2api-v2-nodes-chain",
    "sub2api-v2-host",
)
ADMIN_REALTIME_URL = "${admin_url}/admin/ops#realtime-requests"

CANONICAL_RESULTS = frozenset({
    "success",
    "upstream_error",
    "timeout",
    "client_abort",
    "gateway_error",
    "rejected",
})
FAILURE_RESULTS = frozenset({
    "upstream_error",
    "gateway_error",
    "rejected",
    "client_abort",
})
EXISTING_13_REASONS = frozenset({
    "upstream_rate_limit",
    "upstream_auth",
    "upstream_model_or_request",
    "upstream_server_error",
    "upstream_connect_timeout",
    "upstream_read_timeout",
    "proxy_unreachable",
    "client_abort_during_body_read",
    "client_abort_during_response",
    "gateway_panic",
    "gateway_db",
    "gateway_ratelimit",
    "unknown",
})
NEW_4XX_REASONS = frozenset({
    "client_bad_request",
    "client_auth_failed",
    "client_forbidden",
    "client_not_found",
    "body_too_large",
})
CANONICAL_18_REASONS = EXISTING_13_REASONS | NEW_4XX_REASONS
WS_METRICS = frozenset({
    "sub2api_ws_active_connections",
    "sub2api_ws_connections_total",
})

EXPECTED_CHAIN_ALERTS = frozenset({
    "Sub2apiSlowFirstByteP95",
    "Sub2apiUpstreamAccountFailing",
    "Sub2apiGatewaySelfError",
    "Sub2apiInstrumentationStalled",
    "Sub2apiMetricCoverageLow",
    "Sub2apiClientAbortSpike",
    "Sub2apiDownstreamShortfall",
    "Sub2apiUnknownFailureHigh",
})


def _no_duplicate_keys(pairs: list[tuple[str, object]]) -> dict[str, object]:
    out: dict[str, object] = {}
    for k, v in pairs:
        if k in out:
            raise ValueError(f"duplicate JSON key: {k!r}")
        out[k] = v
    return out


def _reject_constant(value: str) -> None:
    raise ValueError(f"non-JSON numeric constant: {value}")


def load_strict_json(path: Path) -> dict:
    text = path.read_text(encoding="utf-8")
    return json.loads(text, object_pairs_hook=_no_duplicate_keys, parse_constant=_reject_constant)


def load_json_with_leading_comments(path: Path) -> tuple[list[str], dict]:
    """Parse Prometheus rule files formatted as JSON preceded by `#` comment lines."""
    lines = path.read_text(encoding="utf-8").splitlines()
    comments: list[str] = []
    idx = 0
    while idx < len(lines):
        stripped = lines[idx].strip()
        if not stripped:
            idx += 1
            continue
        if stripped.startswith("#"):
            comments.append(stripped)
            idx += 1
            continue
        break
    payload = "\n".join(lines[idx:])
    data = json.loads(payload, object_pairs_hook=_no_duplicate_keys, parse_constant=_reject_constant)
    return comments, data


def _strip_yaml_comment(raw: str) -> str:
    quote: str | None = None
    escaped = False
    for i, ch in enumerate(raw):
        if escaped:
            escaped = False
        elif ch == "\\" and quote == '"':
            escaped = True
        elif quote:
            if ch == quote:
                quote = None
        elif ch in ('"', "'") and (i == 0 or raw[i - 1].isspace() or raw[i - 1] == ":"):
            quote = ch
        elif ch == "#" and (i == 0 or raw[i - 1].isspace()):
            return raw[:i].rstrip()
    return raw.rstrip()


def _unquote_yaml_scalar(val: str) -> str:
    s = val.strip()
    if len(s) >= 2 and s[0] == "'" and s[-1] == "'":
        return s[1:-1].replace("''", "'")
    if len(s) >= 2 and s[0] == '"' and s[-1] == '"':
        return json.loads(s)
    return s


def parse_monitoring_rules_test_yml(path: Path) -> dict:
    """Stdlib-only structural parser for Prometheus YAML rule test files."""
    raw_lines = path.read_text(encoding="utf-8").splitlines()
    rule_files: list[str] = []
    evaluation_interval: str | None = None
    tests: list[dict] = []

    section: str | None = None
    cur_test: dict | None = None
    cur_sub: str | None = None
    cur_series: dict | None = None
    cur_alert: dict | None = None

    for raw in raw_lines:
        clean = _strip_yaml_comment(raw)
        if not clean.strip():
            continue
        indent = len(clean) - len(clean.lstrip(" "))
        text = clean.strip()

        if indent == 0:
            if text.startswith("rule_files:"):
                section = "rule_files"
                continue
            if text.startswith("evaluation_interval:"):
                evaluation_interval = _unquote_yaml_scalar(text.partition(":")[2])
                section = None
                continue
            if text.startswith("tests:"):
                section = "tests"
                continue

        if section == "rule_files" and text.startswith("- "):
            rule_files.append(_unquote_yaml_scalar(text[2:]))
            continue

        if section != "tests":
            continue

        if indent == 2 and text.startswith("- name:"):
            cur_test = {
                "name": _unquote_yaml_scalar(text.split(":", 1)[1]),
                "interval": None,
                "input_series": [],
                "alert_rule_test": [],
            }
            tests.append(cur_test)
            cur_sub = None
            cur_series = None
            cur_alert = None
            continue

        if cur_test is None:
            continue

        if indent == 4:
            if text.startswith("interval:"):
                cur_test["interval"] = _unquote_yaml_scalar(text.partition(":")[2])
            elif text.startswith("input_series:"):
                cur_sub = "input_series"
                cur_series = None
            elif text.startswith("alert_rule_test:"):
                cur_sub = "alert_rule_test"
                cur_alert = None
            continue

        if cur_sub == "input_series":
            if indent == 6 and text.startswith("- series:"):
                cur_series = {"series": _unquote_yaml_scalar(text.split(":", 1)[1]), "values": ""}
                cur_test["input_series"].append(cur_series)
            elif indent >= 8 and cur_series is not None and text.startswith("values:"):
                cur_series["values"] = _unquote_yaml_scalar(text.split(":", 1)[1])
            continue

        if cur_sub == "alert_rule_test":
            if indent == 6 and text.startswith("- eval_time:"):
                cur_alert = {
                    "eval_time": _unquote_yaml_scalar(text.split(":", 1)[1]),
                    "alertname": "",
                    "exp_alerts": None,
                    "raw_exp_lines": [],
                }
                cur_test["alert_rule_test"].append(cur_alert)
            elif cur_alert is not None:
                if indent == 8 and text.startswith("alertname:"):
                    cur_alert["alertname"] = _unquote_yaml_scalar(text.split(":", 1)[1])
                elif indent == 8 and text.startswith("exp_alerts:"):
                    rhs = text.split(":", 1)[1].strip()
                    if rhs == "[]":
                        cur_alert["exp_alerts"] = []
                    else:
                        cur_alert["exp_alerts"] = []
                        cur_alert["raw_exp_lines"].append(rhs)
                elif indent > 8:
                    cur_alert["raw_exp_lines"].append(text)
                    if cur_alert["exp_alerts"] is None:
                        cur_alert["exp_alerts"] = []
                    if text.startswith("- "):
                        cur_alert["exp_alerts"].append({"raw": text})

    return {
        "rule_files": rule_files,
        "evaluation_interval": evaluation_interval,
        "tests": tests,
    }


def _extract_braced_block(text: str, anchor_pattern: str) -> str:
    match = re.search(anchor_pattern, text)
    if not match:
        raise AssertionError(f"Could not locate anchor {anchor_pattern!r}")
    start = text.find("{", match.start())
    if start == -1:
        raise AssertionError(f"No opening '{{' after {anchor_pattern!r}")
    depth = 0
    in_str: str | None = None
    escaped = False
    for idx in range(start, len(text)):
        ch = text[idx]
        if escaped:
            escaped = False
            continue
        if in_str:
            if ch == "\\" and in_str != "`":
                escaped = True
            elif ch == in_str:
                in_str = None
            continue
        if ch in ('"', "'", "`"):
            in_str = ch
        elif ch == "{":
            depth += 1
        elif ch == "}":
            depth -= 1
            if depth == 0:
                return text[start : idx + 1]
    raise AssertionError(f"Unbalanced braces for {anchor_pattern!r}")


def _extract_ts_object_values(block: str) -> dict[str, str]:
    entries: dict[str, str] = {}
    for m in re.finditer(r"^\s*([A-Za-z0-9_]+)\s*:\s*(['\"])(.*?)\2\s*,?\s*$", block, re.MULTILINE):
        entries[m.group(1)] = m.group(3)
    return entries


def extract_backend_failure_reasons() -> set[str]:
    labels_src = OPS_LABELS_GO.read_text(encoding="utf-8")
    recorder_src = OPS_RECORDER_GO.read_text(encoding="utf-8")

    classify_failure_block = _extract_braced_block(labels_src, r"func\s+ClassifyFailure\b")
    classify_rejected_block = _extract_braced_block(labels_src, r"func\s+ClassifyRejectedStatus\b")
    priority_block = _extract_braced_block(recorder_src, r"func\s+failurePriority\b")

    reasons: set[str] = set()
    for m in re.finditer(r'return\s+"([^"]+)"', classify_failure_block):
        if m.group(1):
            reasons.add(m.group(1))
    for m in re.finditer(r'return\s+"[^"]*",\s*"([^"]+)"', classify_rejected_block):
        if m.group(1):
            reasons.add(m.group(1))
    for m in re.finditer(r'"([a-z0-9_]+)"', priority_block):
        reasons.add(m.group(1))
    return reasons


def extract_backend_metric_names() -> set[str]:
    src = OPS_METRICS_GO.read_text(encoding="utf-8")
    return set(re.findall(r'Name:\s*"(sub2api_[a-z0-9_]+)"', src))


def extract_frontend_failure_reason_sets() -> tuple[set[str], list[str], dict[str, str], dict[str, str]]:
    fmt_src = FRONTEND_FORMATTERS_TS.read_text(encoding="utf-8")
    m_known = re.search(
        r"export\s+const\s+KNOWN_FAILURE_REASON_KEYS\s*=\s*new\s+Set\(\[(.*?)\]\)",
        fmt_src,
        re.DOTALL,
    )
    if not m_known:
        raise AssertionError("KNOWN_FAILURE_REASON_KEYS not found in realtimeRequestFormatters.ts")
    known_keys = set(re.findall(r"['\"]([a-z0-9_]+)['\"]", m_known.group(1)))

    m_canon = re.search(
        r"export\s+const\s+CANONICAL_FAILURE_REASON_KEYS\b.*?=\s*\[(.*?)\]",
        fmt_src,
        re.DOTALL,
    )
    if not m_canon:
        raise AssertionError("CANONICAL_FAILURE_REASON_KEYS not found in realtimeRequestFormatters.ts")
    canon_keys = re.findall(r"['\"]([a-z0-9_]+)['\"]", m_canon.group(1))

    zh_src = FRONTEND_ZH_OPS_TS.read_text(encoding="utf-8")
    en_src = FRONTEND_EN_OPS_TS.read_text(encoding="utf-8")
    zh_reasons = _extract_ts_object_values(_extract_braced_block(zh_src, r"\bfailureReasons\s*:"))
    en_reasons = _extract_ts_object_values(_extract_braced_block(en_src, r"\bfailureReasons\s*:"))
    return known_keys, canon_keys, zh_reasons, en_reasons


def _has_cjk(text: str) -> bool:
    return bool(re.search(r"[\u4e00-\u9fff]", text))


def _panel_exprs(panel: dict) -> list[str]:
    return [t.get("expr", "") for t in panel.get("targets", []) if isinstance(t, dict)]


def _panel_overrides_by_name(panel: dict) -> dict[str, dict]:
    out: dict[str, dict] = {}
    for ov in panel.get("fieldConfig", {}).get("overrides", []):
        matcher = ov.get("matcher", {})
        if matcher.get("id") == "byName":
            opt = matcher.get("options")
            if isinstance(opt, str):
                props = {p["id"]: p.get("value") for p in ov.get("properties", []) if "id" in p}
                out[opt] = props
    return out


def _find_panel_by_spec_id(dash_data: dict, spec: dict, panel_id: str) -> dict:
    for src, panel in zip(spec["panels"], dash_data["panels"]):
        if src["id"] == panel_id:
            return panel
    raise AssertionError(f"Panel {panel_id!r} not found in dashboard {dash_data.get('uid')!r}")


def _check_non_overlapping_grid(panels: list[dict]) -> None:
    occupied: list[tuple[int, int, int, int, str]] = []
    for p in panels:
        gp = p.get("gridPos", {})
        x, y, w, h = gp.get("x"), gp.get("y"), gp.get("w"), gp.get("h")
        title = p.get("title") or f"id={p.get('id')}"
        if not all(isinstance(v, int) for v in (x, y, w, h)):
            raise AssertionError(f"Invalid gridPos on panel {title!r}: {gp}")
        if w <= 0 or h <= 0 or x < 0 or y < 0 or x + w > 24:
            raise AssertionError(f"Out-of-bounds gridPos on panel {title!r}: {gp}")
        for ox, oy, ow, oh, otitle in occupied:
            if x < ox + ow and x + w > ox and y < oy + oh and y + h > oy:
                raise AssertionError(f"Panel {title!r} {gp} overlaps {otitle!r} ({ox},{oy},{ow},{oh})")
        occupied.append((x, y, w, h, title))


def _load_dashboards_context() -> tuple[dict, dict[str, dict]]:
    ns = runpy.run_path(str(BUILD_DASHBOARDS_PY))
    dashes: dict[str, dict] = {}
    for spec in ns["DASHBOARDS"]:
        dashes[spec["id"]] = load_strict_json(DASHBOARDS_DIR / spec["file"])
    return ns, dashes


def _run_cmd(cmd: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(cmd, cwd=str(ROOT), capture_output=True, text=True)


def _stalled_groups_in_v2_test(v2_data: dict) -> list[dict]:
    out = []
    for grp in v2_data.get("tests", []):
        alerts = {a.get("alertname") for a in grp.get("alert_rule_test", [])}
        if "Sub2apiInstrumentationStalled" in alerts:
            out.append(grp)
    return out


# ===========================================================================
# Tier 1: Baseline Build, Lint, Structural & Rule Syntax Invariants (F13-F15)
# ===========================================================================


class TestTier1MonitoringBaseline(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.gen_ns, cls.dashes = _load_dashboards_context()
        cls.spec_by_id = {s["id"]: s for s in cls.gen_ns["DASHBOARDS"]}

    # --- F13 (5 checks) ---

    def test_t1_f13_build_dashboards_check_matches_generated_jsons(self) -> None:
        proc = _run_cmd([sys.executable, "-B", str(BUILD_DASHBOARDS_PY), "--check"])
        self.assertEqual(proc.returncode, 0, f"build-dashboards.py --check failed:\n{proc.stdout}\n{proc.stderr}")
        self.assertIn("PASS:", proc.stdout)

    def test_t1_f13_overview_ov_fail_has_result_breakdown_target(self) -> None:
        ov_fail = _find_panel_by_spec_id(self.dashes["overview"], self.spec_by_id["overview"], "ov_fail")
        exprs = _panel_exprs(ov_fail)
        result_targets = [
            t for t in ov_fail.get("targets", [])
            if "sub2api_gateway_requests_total" in t.get("expr", "")
            and 'result!="success"' in t.get("expr", "")
            and re.search(r"\bby\s*\(\s*result\b", t.get("expr", ""))
        ]
        self.assertTrue(
            result_targets,
            f"ov_fail must include a target aggregating sub2api_gateway_requests_total{{result!=\"success\"}} by (result); got {exprs}",
        )

    def test_t1_f13_overview_ov_fail_has_failure_reason_top6_target(self) -> None:
        ov_fail = _find_panel_by_spec_id(self.dashes["overview"], self.spec_by_id["overview"], "ov_fail")
        reason_targets = [
            t for t in ov_fail.get("targets", [])
            if "sub2api_gateway_requests_total" in t.get("expr", "")
            and 'result!="success"' in t.get("expr", "")
            and "topk(6" in t.get("expr", "")
            and "failure_reason" in t.get("expr", "")
        ]
        self.assertTrue(
            reason_targets,
            f"ov_fail must include a topk(6, ... failure_reason ...) target; got {_panel_exprs(ov_fail)}",
        )

    def test_t1_f13_build_dashboards_reasons_have_chinese_labels(self) -> None:
        reasons = self.gen_ns.get("REASONS", [])
        self.assertIsInstance(reasons, list)
        self.assertGreaterEqual(len(reasons), len(CANONICAL_18_REASONS))
        for entry in reasons:
            with self.subTest(reason=entry[0] if entry else None):
                self.assertEqual(len(entry), 3)
                key, label, color = entry
                self.assertIsInstance(key, str)
                self.assertTrue(key.strip())
                self.assertIsInstance(label, str)
                self.assertTrue(_has_cjk(label), f"Expected Chinese displayName for {key!r}, got {label!r}")
                self.assertTrue(isinstance(color, str) and color.startswith("#"))

    def test_t1_f13_overview_ov_fail_panel_contract_and_no_value(self) -> None:
        ov_fail = _find_panel_by_spec_id(self.dashes["overview"], self.spec_by_id["overview"], "ov_fail")
        self.assertEqual(ov_fail["type"], "bargauge")
        self.assertEqual(ov_fail["interval"], "15s")
        self.assertEqual(ov_fail["datasource"], {"type": "prometheus", "uid": PROM_UID})
        self.assertEqual(ov_fail["fieldConfig"]["defaults"]["noValue"], "无失败")
        self.assertTrue(ov_fail["description"].strip())

    # --- F14 (5 checks) ---

    def test_t1_f14_lint_dashboards_passes(self) -> None:
        proc = _run_cmd([sys.executable, "-B", str(LINT_DASHBOARDS_PY)])
        self.assertEqual(proc.returncode, 0, f"lint-dashboards.py failed:\n{proc.stdout}\n{proc.stderr}")
        self.assertIn("PASS:", proc.stdout)

    def test_t1_f14_three_canonical_dashboards_structural_validity(self) -> None:
        found_files = sorted(p.name for p in DASHBOARDS_DIR.glob("*.json"))
        self.assertEqual(found_files, list(CANONICAL_DASHBOARD_FILES))
        for spec in self.gen_ns["DASHBOARDS"]:
            dash = self.dashes[spec["id"]]
            with self.subTest(dashboard=spec["file"]):
                self.assertEqual(dash["uid"], spec["uid"])
                self.assertEqual(dash["title"], spec["title"])
                self.assertEqual(len(dash["panels"]), len(spec["panels"]))
                self.assertEqual(dash["refresh"], spec["refresh"])
                self.assertEqual(dash["time"], {"from": spec["time_from"], "to": "now"})
                self.assertIs(dash["editable"], False)
                self.assertIs(dash["allowUiUpdates"], False)
                raw = (DASHBOARDS_DIR / spec["file"]).read_text(encoding="utf-8")
                self.assertNotIn("rawSql", raw)
                self.assertNotIn("sub2api-pg", raw)
                self.assertNotIn("vector(1)", raw)
                for panel in dash["panels"]:
                    self.assertIn(panel["type"], ALLOWED_PANEL_TYPES)
                    self.assertNotIn(panel["type"], FORBIDDEN_PANEL_TYPES)
                    self.assertEqual(panel["interval"], "15s")
                    self.assertTrue(panel["description"].strip())
                    self.assertEqual(panel["datasource"], {"type": "prometheus", "uid": PROM_UID})

    def test_t1_f14_overview_latency_panels_carry_http_only_scope_notes(self) -> None:
        for pid in ("ov_p95_total", "ov_ttft", "ov_share"):
            with self.subTest(panel=pid):
                panel = _find_panel_by_spec_id(self.dashes["overview"], self.spec_by_id["overview"], pid)
                desc = panel["description"]
                self.assertIn("HTTP", desc)
                self.assertTrue(
                    "WebSocket" in desc or "WS" in desc or "长连接" in desc,
                    f"{pid} description must explicitly note WebSocket exclusion: {desc!r}",
                )

    def test_t1_f14_nodes_chain_phase_p95_carries_http_only_and_streaming_notes(self) -> None:
        nc_phase = _find_panel_by_spec_id(self.dashes["nodes-chain"], self.spec_by_id["nodes-chain"], "nc_phase_p95")
        desc = nc_phase["description"]
        self.assertIn("HTTP", desc)
        self.assertTrue("WebSocket" in desc or "WS" in desc or "长连接" in desc, desc)
        self.assertIn("response", desc)
        self.assertIn("流式", desc)

    def test_t1_f14_dashboard_navigation_links_all_three_and_admin_realtime(self) -> None:
        expected_urls = [f"/d/{uid}" for uid in MAIN_UIDS] + [ADMIN_REALTIME_URL]
        for spec in self.gen_ns["DASHBOARDS"]:
            dash = self.dashes[spec["id"]]
            with self.subTest(dashboard=spec["file"]):
                nav_panels = [p for src, p in zip(spec["panels"], dash["panels"]) if src["id"].endswith("_nav")]
                self.assertEqual(len(nav_panels), 1)
                html = nav_panels[0]["options"]["content"]
                urls = re.findall(r'<a\b[^>]*\bhref="([^"]+)"', html)
                self.assertEqual(urls, expected_urls)

    # --- F15 (5 checks) ---

    def test_t1_f15_chain_yml_valid_syntax_and_expected_rules(self) -> None:
        _, chain_data = load_json_with_leading_comments(CHAIN_YML)
        groups = chain_data.get("groups", [])
        self.assertEqual(len(groups), 1)
        self.assertEqual(groups[0]["name"], "sub2api-v2-chain")
        self.assertEqual(groups[0]["interval"], "15s")
        rules = {r["alert"]: r for r in groups[0]["rules"]}
        self.assertEqual(set(rules), EXPECTED_CHAIN_ALERTS)
        for alert_name, rule in rules.items():
            with self.subTest(alert=alert_name):
                self.assertTrue(rule.get("expr", "").strip())
                self.assertTrue(rule.get("annotations", {}).get("summary"))
                self.assertTrue(rule.get("annotations", {}).get("description"))

    def test_t1_f15_chain_yml_core_alert_expressions(self) -> None:
        _, chain_data = load_json_with_leading_comments(CHAIN_YML)
        rules = {r["alert"]: r for r in chain_data["groups"][0]["rules"]}

        stalled_expr = rules["Sub2apiInstrumentationStalled"]["expr"]
        self.assertIn("sub2api_ops_eligible_requests_total", stalled_expr)
        self.assertIn("sub2api_request_phase_seconds_count", stalled_expr)
        self.assertNotIn("sub2api_http_requests_total", stalled_expr)

        self.assertIn('sub2api_gateway_requests_total{result="gateway_error"}', rules["Sub2apiGatewaySelfError"]["expr"])
        self.assertIn('sub2api_gateway_requests_total{failure_reason="unknown"}', rules["Sub2apiUnknownFailureHigh"]["expr"])
        self.assertIn('sub2api_metric_coverage_ratio{kind="account"}', rules["Sub2apiMetricCoverageLow"]["expr"])

    def test_t1_f15_v2_test_yml_valid_json_and_structure(self) -> None:
        v2_data = load_strict_json(V2_TEST_YML)
        self.assertEqual(v2_data.get("rule_files"), ["../slo.yml", "../chain.yml", "../infra.yml"])
        self.assertEqual(v2_data.get("evaluation_interval"), "15s")
        tests = v2_data.get("tests", [])
        self.assertGreater(len(tests), 10)
        for grp in tests:
            self.assertTrue(grp.get("name"))
            self.assertTrue(grp.get("input_series"))
            self.assertTrue(grp.get("alert_rule_test") or grp.get("promql_expr_test"))

    def test_t1_f15_monitoring_rules_test_yml_valid_and_uses_eligible_metric(self) -> None:
        parsed = parse_monitoring_rules_test_yml(MONITORING_RULES_TEST_YML)
        self.assertEqual(
            parsed["rule_files"],
            [
                "../monitoring/prometheus/rules/slo.yml",
                "../monitoring/prometheus/rules/chain.yml",
                "../monitoring/prometheus/rules/infra.yml",
            ],
        )
        self.assertEqual(parsed["evaluation_interval"], "15s")
        stalled_tests = [
            t for t in parsed["tests"]
            if any(a["alertname"] == "Sub2apiInstrumentationStalled" for a in t["alert_rule_test"])
        ]
        self.assertTrue(stalled_tests)
        for t in stalled_tests:
            series_names = [s["series"] for s in t["input_series"]]
            self.assertTrue(any("sub2api_ops_eligible_requests_total" in s for s in series_names), t["name"])
            self.assertTrue(any("sub2api_request_phase_seconds_count" in s for s in series_names), t["name"])

    def test_t1_f15_slo_and_infra_rules_valid_json(self) -> None:
        for path in (SLO_YML, INFRA_YML):
            with self.subTest(file=path.name):
                _, data = load_json_with_leading_comments(path)
                self.assertTrue(data.get("groups"))
                for grp in data["groups"]:
                    self.assertTrue(grp.get("rules"))


# ===========================================================================
# Tier 2: Boundary & Corner Cases (F13-F15, >=5 per feature)
# ===========================================================================


class TestTier2BoundaryAndCornerCases(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.gen_ns, cls.dashes = _load_dashboards_context()
        cls.spec_by_id = {s["id"]: s for s in cls.gen_ns["DASHBOARDS"]}
        cls.ov_fail = _find_panel_by_spec_id(cls.dashes["overview"], cls.spec_by_id["overview"], "ov_fail")
        cls.v2_data = load_strict_json(V2_TEST_YML)
        cls.rules_test_data = parse_monitoring_rules_test_yml(MONITORING_RULES_TEST_YML)

    # --- F13 (5 boundary checks) ---

    def test_t2_f13_every_canonical_18_reason_present_in_build_dashboards_reasons(self) -> None:
        reasons_map = {k: (label, color) for k, label, color in self.gen_ns["REASONS"]}
        for reason in sorted(CANONICAL_18_REASONS):
            with self.subTest(failure_reason=reason):
                self.assertIn(reason, reasons_map, f"{reason!r} missing from build-dashboards.py REASONS")
                label, color = reasons_map[reason]
                self.assertTrue(_has_cjk(label), f"REASONS[{reason!r}] label must be Chinese, got {label!r}")
                self.assertTrue(color and color.startswith("#"))

    def test_t2_f13_every_canonical_18_reason_present_in_ov_fail_overrides(self) -> None:
        overrides = _panel_overrides_by_name(self.ov_fail)
        for reason in sorted(CANONICAL_18_REASONS):
            with self.subTest(failure_reason=reason):
                self.assertIn(reason, overrides, f"{reason!r} missing from 01-overview.json ov_fail overrides")
                display_name = overrides[reason].get("displayName", "")
                self.assertTrue(_has_cjk(display_name), f"ov_fail override[{reason!r}] must be Chinese, got {display_name!r}")

    def test_t2_f13_ov_fail_result_breakdown_covers_all_failure_results(self) -> None:
        overrides = _panel_overrides_by_name(self.ov_fail)
        # Expect either result-scoped keys ("结果 · <result>") or dedicated overrides with Chinese labels
        for result_val in sorted(FAILURE_RESULTS):
            with self.subTest(result=result_val):
                matching = [
                    (k, props.get("displayName", ""))
                    for k, props in overrides.items()
                    if (k == f"结果 · {result_val}" or (result_val in k and k != result_val))
                ]
                self.assertTrue(
                    matching,
                    f"ov_fail overrides missing result-scoped display mapping for {result_val!r}",
                )
                for key, label in matching:
                    self.assertTrue(_has_cjk(label), f"Expected Chinese displayName for {key!r}, got {label!r}")

    def test_t2_f13_gateway_error_and_rejected_not_conflated_as_bare_failure_reasons(self) -> None:
        reason_keys = [k for k, _, _ in self.gen_ns["REASONS"]]
        ov_fail_override_keys = set(_panel_overrides_by_name(self.ov_fail))
        for non_reason_result in ("gateway_error", "rejected"):
            with self.subTest(result_label=non_reason_result):
                self.assertNotIn(
                    non_reason_result,
                    reason_keys,
                    f"{non_reason_result!r} is a result label and must NOT be in REASONS",
                )
                self.assertNotIn(
                    non_reason_result,
                    ov_fail_override_keys,
                    f"{non_reason_result!r} must NOT appear as a bare failure_reason override key on ov_fail",
                )

    def test_t2_f13_client_abort_499_mapped_and_reasons_have_no_duplicate_keys(self) -> None:
        reason_keys = [k for k, _, _ in self.gen_ns["REASONS"]]
        self.assertEqual(len(reason_keys), len(set(reason_keys)), "Duplicate keys in REASONS")
        self.assertIn("client_abort", set(reason_keys))
        overrides = _panel_overrides_by_name(self.ov_fail)
        self.assertIn("client_abort", overrides)
        self.assertTrue(_has_cjk(overrides["client_abort"].get("displayName", "")))

    # --- F14 (5 boundary checks) ---

    def test_t2_f14_each_latency_panel_individually_verified(self) -> None:
        checks = [
            ("overview", "ov_p95_total", "sub2api_gateway_request_duration_seconds_bucket", False),
            ("overview", "ov_ttft", 'sub2api_request_phase_seconds_bucket{phase="headers_end"}', False),
            ("overview", "ov_share", 'sub2api_request_phase_seconds_sum{phase!="headers_end"}', True),
            ("nodes-chain", "nc_phase_p95", 'sub2api_request_phase_seconds_bucket{phase!="headers_end"}', True),
        ]
        for dash_id, pid, expected_metric, needs_streaming_note in checks:
            with self.subTest(panel=pid):
                panel = _find_panel_by_spec_id(self.dashes[dash_id], self.spec_by_id[dash_id], pid)
                desc = panel["description"]
                self.assertIn("HTTP", desc)
                self.assertTrue("WebSocket" in desc or "WS" in desc or "长连接" in desc, desc)
                if needs_streaming_note:
                    self.assertIn("response", desc)
                    self.assertIn("流式", desc)
                    self.assertTrue("模型" in desc or "生成" in desc or "输出" in desc, desc)
                exprs = _panel_exprs(panel)
                self.assertTrue(any(expected_metric in e for e in exprs), (pid, exprs))
                for e in exprs:
                    self.assertNotIn("sub2api_ws_", e)

    def test_t2_f14_all_http_and_overview_panels_isolated_from_ws_metrics(self) -> None:
        for panel in self.dashes["overview"]["panels"]:
            for e in _panel_exprs(panel):
                self.assertNotIn("sub2api_ws_", e, f"Overview panel {panel['title']!r} leaked WS metric")
        for src, panel in zip(self.spec_by_id["nodes-chain"]["panels"], self.dashes["nodes-chain"]["panels"]):
            if src["id"].startswith("nc_ws_"):
                continue
            for e in _panel_exprs(panel):
                self.assertNotIn("sub2api_ws_", e, f"HTTP/chain panel {src['id']!r} leaked WS metric")

    def test_t2_f14_conditional_ws_status_card_validation(self) -> None:
        backend_metrics = extract_backend_metric_names()
        backend_has_ws = bool(WS_METRICS & backend_metrics)
        ws_panels: list[tuple[str, dict]] = []
        for dash_id in ("nodes-chain", "host"):
            for panel in self.dashes[dash_id]["panels"]:
                joined = "\n".join(_panel_exprs(panel))
                if "sub2api_ws_" in joined:
                    ws_panels.append((dash_id, panel))

        if not backend_has_ws and not ws_panels:
            self.skipTest("Optional WS metrics/panels not present in backend or dashboards")

        self.assertTrue(ws_panels, "Backend defines WS metrics so 02-nodes-chain or 03-host must expose WS card(s)")
        queried_ws_metrics: set[str] = set()
        for dash_id, panel in ws_panels:
            with self.subTest(dashboard=dash_id, panel=panel.get("title")):
                self.assertTrue(panel.get("description", "").strip())
                self.assertEqual(panel.get("interval"), "15s")
                self.assertEqual(panel.get("datasource"), {"type": "prometheus", "uid": PROM_UID})
                for e in _panel_exprs(panel):
                    for ws_m in WS_METRICS:
                        if ws_m in e:
                            queried_ws_metrics.add(ws_m)
                    self.assertNotIn("sub2api_request_phase_seconds", e)
                    self.assertNotIn("sub2api_gateway_requests_total", e)
                    self.assertNotIn("sub2api_http_requests_total", e)
        self.assertEqual(queried_ws_metrics, WS_METRICS)

    def test_t2_f14_latency_panels_use_dynamic_range_and_no_vector1(self) -> None:
        for dash_id, pid in (("overview", "ov_p95_total"), ("overview", "ov_ttft"), ("overview", "ov_share"), ("nodes-chain", "nc_phase_p95")):
            panel = _find_panel_by_spec_id(self.dashes[dash_id], self.spec_by_id[dash_id], pid)
            for e in _panel_exprs(panel):
                self.assertIn("[$__range]", e, (pid, e))
                self.assertNotIn("[5m]", e)
                self.assertNotIn("[10m]", e)
                self.assertNotIn("vector(1)", e)

    def test_t2_f14_all_dashboards_have_valid_non_overlapping_grid_positions(self) -> None:
        for dash_id, dash in self.dashes.items():
            with self.subTest(dashboard=dash_id):
                _check_non_overlapping_grid(dash["panels"])

    # --- F15 (5 boundary checks) ---

    def test_t2_f15_zero_stale_http_requests_total_in_v2_test_stalled_groups(self) -> None:
        groups = _stalled_groups_in_v2_test(self.v2_data)
        self.assertGreaterEqual(len(groups), 7)
        for grp in groups:
            name = grp["name"]
            series = [s["series"] for s in grp.get("input_series", [])]
            with self.subTest(group=name):
                self.assertTrue(
                    any("sub2api_ops_eligible_requests_total" in s for s in series),
                    f"{name!r} must use sub2api_ops_eligible_requests_total",
                )
                has_http_total = any("sub2api_http_requests_total" in s for s in series)
                if has_http_total:
                    # Only permitted in explicit excluded-route / WS-101 non-firing regression tests
                    # where every Sub2apiInstrumentationStalled expectation is exp_alerts == [].
                    stalled_expects = [
                        a for a in grp["alert_rule_test"]
                        if a["alertname"] == "Sub2apiInstrumentationStalled"
                    ]
                    self.assertTrue(
                        all(a["exp_alerts"] == [] for a in stalled_expects),
                        f"Stale sub2api_http_requests_total found in firing Sub2apiInstrumentationStalled group {name!r}",
                    )
                    self.assertTrue(
                        ("excluded" in name.lower()) or ("ws" in name.lower()) or ("101" in name.lower()),
                        f"Unexpected sub2api_http_requests_total in Sub2apiInstrumentationStalled group {name!r}",
                    )

    def test_t2_f15_v2_test_ws101_non_firing_regressions_cover_all_four_alerts(self) -> None:
        ws_groups = [
            grp for grp in self.v2_data.get("tests", [])
            if any(
                "101" in s.get("series", "") or "ws" in grp.get("name", "").lower() or "101" in grp.get("name", "").lower()
                for s in grp.get("input_series", [])
            )
        ]
        self.assertTrue(ws_groups, "v2_test.yml must contain WS-101 regression test group(s)")
        required_non_firing = (
            "Sub2apiGatewaySelfError",
            "Sub2apiUnknownFailureHigh",
            "Sub2apiMetricCoverageLow",
            "Sub2apiInstrumentationStalled",
        )
        for alert_name in required_non_firing:
            with self.subTest(alert=alert_name):
                matches = [
                    a for grp in ws_groups for a in grp.get("alert_rule_test", [])
                    if a.get("alertname") == alert_name
                ]
                self.assertTrue(matches, f"WS-101 regression missing assertion for {alert_name}")
                for m in matches:
                    self.assertEqual(m.get("exp_alerts"), [], f"WS-101 regression must assert exp_alerts=[] for {alert_name}")

    def test_t2_f15_v2_test_4xx_failure_reasons_non_firing_regressions(self) -> None:
        target_4xx = NEW_4XX_REASONS | {"gateway_ratelimit"}
        matching_groups = [
            grp for grp in self.v2_data.get("tests", [])
            if any(r in s.get("series", "") for s in grp.get("input_series", []) for r in NEW_4XX_REASONS)
        ]
        self.assertTrue(matching_groups, "v2_test.yml must contain 4xx failure_reason regression test group(s)")
        covered_reasons: set[str] = set()
        for grp in matching_groups:
            series_blob = "\n".join(s.get("series", "") for s in grp.get("input_series", []))
            for r in target_4xx:
                if f'failure_reason="{r}"' in series_blob:
                    covered_reasons.add(r)
            alerts = {a["alertname"]: a for a in grp.get("alert_rule_test", [])}
            self.assertIn("Sub2apiUnknownFailureHigh", alerts)
            self.assertEqual(alerts["Sub2apiUnknownFailureHigh"]["exp_alerts"], [])
            if "Sub2apiGatewaySelfError" in alerts:
                self.assertEqual(alerts["Sub2apiGatewaySelfError"]["exp_alerts"], [])
        self.assertEqual(covered_reasons, target_4xx)

    def test_t2_f15_monitoring_rules_test_yml_includes_ws101_and_4xx_regressions(self) -> None:
        ws_groups = [
            t for t in self.rules_test_data["tests"]
            if "ws" in t["name"].lower() or "101" in t["name"].lower()
        ]
        self.assertTrue(ws_groups, "monitoring-rules-test.yml should include WS-101 regression test")
        ws_alerts = {
            a["alertname"]: a["exp_alerts"]
            for grp in ws_groups
            for a in grp["alert_rule_test"]
        }
        for alert_name in (
            "Sub2apiInstrumentationStalled",
            "Sub2apiGatewaySelfError",
            "Sub2apiMetricCoverageLow",
            "Sub2apiUnknownFailureHigh",
        ):
            self.assertIn(alert_name, ws_alerts)
            self.assertEqual(ws_alerts[alert_name], [])

        r4xx_groups = [
            t for t in self.rules_test_data["tests"]
            if any(r in s["series"] for s in t["input_series"] for r in NEW_4XX_REASONS)
        ]
        self.assertTrue(r4xx_groups, "monitoring-rules-test.yml should include 4xx regression test")
        series_blob = "\n".join(s["series"] for grp in r4xx_groups for s in grp["input_series"])
        for r in NEW_4XX_REASONS | {"gateway_ratelimit"}:
            self.assertIn(f'failure_reason="{r}"', series_blob)

    def test_t2_f15_chain_yml_ws101_comment_and_unknown_failure_volume_gate(self) -> None:
        comments, chain_data = load_json_with_leading_comments(CHAIN_YML)
        comment_blob = "\n".join(comments)
        self.assertIn("isOpsGatewayRoute", comment_blob)
        self.assertIn("101", comment_blob)
        rules = {r["alert"]: r for r in chain_data["groups"][0]["rules"]}
        unk_expr = rules["Sub2apiUnknownFailureHigh"]["expr"]
        self.assertIn("> 0.1", unk_expr)
        self.assertIn("> 100", unk_expr)


# ===========================================================================
# Tier 3: Cross-Feature Pairwise Combinatorial Checks (>= 4 checks)
# ===========================================================================


class TestTier3CrossFeatureParity(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.gen_ns, cls.dashes = _load_dashboards_context()
        cls.spec_by_id = {s["id"]: s for s in cls.gen_ns["DASHBOARDS"]}

    def test_t3_cross_stack_18_failure_reason_enum_parity(self) -> None:
        backend_reasons = extract_backend_failure_reasons()
        fe_known, fe_canon, zh_reasons, en_reasons = extract_frontend_failure_reason_sets()
        grafana_reasons = {k: label for k, label, _ in self.gen_ns["REASONS"]}
        ov_fail = _find_panel_by_spec_id(self.dashes["overview"], self.spec_by_id["overview"], "ov_fail")
        ov_fail_overrides = _panel_overrides_by_name(ov_fail)

        # All 18 canonical reasons must exist in backend Go
        self.assertTrue(
            CANONICAL_18_REASONS <= backend_reasons,
            f"Backend missing canonical reasons: {sorted(CANONICAL_18_REASONS - backend_reasons)}",
        )
        # Full parity for all 18 canonical reasons + client_abort across Frontend & Grafana
        expected_cross_stack = CANONICAL_18_REASONS | {"client_abort"}
        for reason in sorted(expected_cross_stack):
            with self.subTest(failure_reason=reason):
                self.assertIn(reason, fe_known, f"{reason!r} missing from KNOWN_FAILURE_REASON_KEYS")
                self.assertIn(reason, fe_canon, f"{reason!r} missing from CANONICAL_FAILURE_REASON_KEYS")
                self.assertTrue(zh_reasons.get(reason, "").strip(), f"{reason!r} missing/empty in zh ops.ts")
                self.assertTrue(en_reasons.get(reason, "").strip(), f"{reason!r} missing/empty in en ops.ts")
                self.assertTrue(_has_cjk(grafana_reasons.get(reason, "")), f"{reason!r} missing Chinese label in REASONS")
                self.assertTrue(
                    _has_cjk(ov_fail_overrides.get(reason, {}).get("displayName", "")),
                    f"{reason!r} missing Chinese override in 01-overview.json",
                )
        # Frontend zh/en failureReasons must match KNOWN_FAILURE_REASON_KEYS 1:1
        self.assertEqual(set(zh_reasons), fe_known)
        self.assertEqual(set(en_reasons), fe_known)

    def test_t3_dashboard_generator_linter_and_json_parity(self) -> None:
        build_fn = self.gen_ns["build"]
        for spec in self.gen_ns["DASHBOARDS"]:
            with self.subTest(file=spec["file"]):
                generated = json.dumps(build_fn(spec), ensure_ascii=False, indent=2) + "\n"
                on_disk = (DASHBOARDS_DIR / spec["file"]).read_text(encoding="utf-8")
                self.assertEqual(on_disk, generated, f"{spec['file']} drifted from build-dashboards.py")
        lint_proc = _run_cmd([sys.executable, "-B", str(LINT_DASHBOARDS_PY)])
        self.assertEqual(lint_proc.returncode, 0, lint_proc.stderr)

    def test_t3_prometheus_rules_and_test_suites_parity(self) -> None:
        all_defined_alerts: set[str] = set()
        for path in (SLO_YML, CHAIN_YML, INFRA_YML):
            _, data = load_json_with_leading_comments(path)
            for grp in data["groups"]:
                for rule in grp["rules"]:
                    all_defined_alerts.add(rule["alert"])

        v2_data = load_strict_json(V2_TEST_YML)
        for grp in v2_data["tests"]:
            for check in grp.get("alert_rule_test", []):
                self.assertIn(
                    check["alertname"],
                    all_defined_alerts,
                    f"v2_test.yml references undefined alert {check['alertname']!r}",
                )

        rules_test = parse_monitoring_rules_test_yml(MONITORING_RULES_TEST_YML)
        for grp in rules_test["tests"]:
            for check in grp["alert_rule_test"]:
                self.assertIn(
                    check["alertname"],
                    all_defined_alerts,
                    f"monitoring-rules-test.yml references undefined alert {check['alertname']!r}",
                )

        # Core chain.yml alerts must have regression coverage in v2_test.yml
        v2_tested_alerts = {
            check["alertname"]
            for grp in v2_data["tests"]
            for check in grp.get("alert_rule_test", [])
        }
        for core_alert in EXPECTED_CHAIN_ALERTS:
            self.assertIn(core_alert, v2_tested_alerts, f"chain.yml alert {core_alert!r} untested in v2_test.yml")

    def test_t3_backend_ws_metrics_and_dashboard_isolation_parity(self) -> None:
        backend_metrics = extract_backend_metric_names()
        self.assertTrue(WS_METRICS <= backend_metrics, f"Missing WS metrics in ops_metrics.go: {WS_METRICS - backend_metrics}")

        nc_dash = self.dashes["nodes-chain"]
        nc_spec = self.spec_by_id["nodes-chain"]
        ws_panels = [
            (src["id"], p)
            for src, p in zip(nc_spec["panels"], nc_dash["panels"])
            if any("sub2api_ws_" in e for e in _panel_exprs(p))
        ]
        self.assertTrue(ws_panels, "Expected dedicated WS status panels in 02-nodes-chain.json")

        # Verify zero overlap between WS metric panels and HTTP/6-phase metric panels
        http_metrics = backend_metrics - WS_METRICS
        for pid, panel in ws_panels:
            expr_blob = "\n".join(_panel_exprs(panel))
            for hm in http_metrics:
                self.assertNotIn(hm, expr_blob, f"WS panel {pid!r} must not query HTTP metric {hm!r}")

        for spec in self.gen_ns["DASHBOARDS"]:
            dash = self.dashes[spec["id"]]
            for src, panel in zip(spec["panels"], dash["panels"]):
                if src["id"].startswith("nc_ws_"):
                    continue
                expr_blob = "\n".join(_panel_exprs(panel))
                for wm in WS_METRICS:
                    self.assertNotIn(wm, expr_blob, f"Non-WS panel {src['id']!r} must not query WS metric {wm!r}")


# ===========================================================================
# Tier 4: End-to-End Monitoring Pipeline Scenarios (S8)
# ===========================================================================


class TestTier4EndToEndMonitoringPipeline(unittest.TestCase):
    def test_t4_s8_full_static_ci_preflight_pipeline(self) -> None:
        """S8a: Full static CI preflight matching .github/workflows/grafana-lint.yml."""
        steps = [
            (
                "unit test dashboard linter",
                [sys.executable, "-B", "-m", "unittest", "discover", "-s", "deploy/monitoring/grafana", "-p", "test_lint_dashboards.py", "-v"],
                "OK",
            ),
            (
                "lint-dashboards",
                [sys.executable, "-B", str(LINT_DASHBOARDS_PY)],
                "PASS:",
            ),
            (
                "build-dashboards --check",
                [sys.executable, "-B", str(BUILD_DASHBOARDS_PY), "--check"],
                "PASS:",
            ),
            (
                "monitoring-v2-dashboard-test --static-only",
                [sys.executable, "-B", str(MONITORING_V2_DASHBOARD_TEST_PY), "--static-only"],
                "PASS:",
            ),
            (
                "monitoring-deploy-test safety",
                [sys.executable, "-B", str(MONITORING_DEPLOY_TEST_PY), "MonitoringDeploymentSafety"],
                "OK",
            ),
        ]
        for label, cmd, expected_marker in steps:
            with self.subTest(step=label):
                proc = _run_cmd(cmd)
                combined = proc.stdout + "\n" + proc.stderr
                self.assertEqual(proc.returncode, 0, f"Step {label!r} failed:\n{combined}")
                self.assertIn(expected_marker, combined, f"Missing marker {expected_marker!r} in {label!r} output")

    def test_t4_s8_live_promtool_and_promql_execution(self) -> None:
        """S8b: Live promtool check/test rules & full PromQL suite when promtool or docker is available."""
        promtool = shutil.which("promtool")
        docker = shutil.which("docker")
        docker_ok = False
        if not promtool and docker:
            probe = _run_cmd([docker, "info"])
            docker_ok = probe.returncode == 0

        if not promtool and not docker_ok:
            self.skipTest("Neither promtool nor a running docker daemon is available for Tier 4 live PromQL execution")

        if promtool:
            for args in (
                [promtool, "check", "rules", str(SLO_YML), str(CHAIN_YML), str(INFRA_YML)],
                [promtool, "test", "rules", str(V2_TEST_YML)],
                [promtool, "test", "rules", str(MONITORING_RULES_TEST_YML)],
            ):
                proc = _run_cmd(args)
                self.assertEqual(proc.returncode, 0, f"{args} failed:\n{proc.stdout}\n{proc.stderr}")
            return

        # Docker promtool path
        base = [
            docker, "run", "--rm", "--network", "none",
            "-v", f"{ROOT / 'deploy'}:/deploy:ro",
            "--entrypoint", "promtool",
            "prom/prometheus:v3.16.0",
        ]
        for args in (
            base + ["check", "rules", "/deploy/monitoring/prometheus/rules/slo.yml", "/deploy/monitoring/prometheus/rules/chain.yml", "/deploy/monitoring/prometheus/rules/infra.yml"],
            base + ["test", "rules", "/deploy/monitoring/prometheus/rules/tests/v2_test.yml"],
            base + ["test", "rules", "/deploy/tests/monitoring-rules-test.yml"],
            [sys.executable, "-B", str(MONITORING_V2_DASHBOARD_TEST_PY)],
        ):
            proc = _run_cmd(args)
            self.assertEqual(proc.returncode, 0, f"{args} failed:\n{proc.stdout}\n{proc.stderr}")


if __name__ == "__main__":
    unittest.main()
