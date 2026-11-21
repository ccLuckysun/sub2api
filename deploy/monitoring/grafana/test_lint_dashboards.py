#!/usr/bin/env python3
"""Focused, dependency-free regression tests for lint-dashboards.py."""

import contextlib
import io
import json
from pathlib import Path
import runpy
import tempfile
import unittest


LINTER = runpy.run_path(str(Path(__file__).with_name("lint-dashboards.py")))
lint = LINTER["lint"]
main = LINTER["main"]

CANONICAL = ("01-overview.json", "02-nodes-chain.json", "03-host.json")
PROM = {"type": "prometheus", "uid": "sub2api-prometheus"}


class DashboardLintTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.monitoring = self.root / "deploy/monitoring"
        self.grafana = self.monitoring / "grafana"
        self.dashboards = {name: self.grafana / "dashboards" / name for name in CANONICAL}
        self.dashboard = self.dashboards["01-overview.json"]
        self.provider = self.grafana / "provisioning/dashboards/dashboards.yml"
        self.prometheus_ds = self.grafana / "provisioning/datasources/prometheus.yml"
        self.compose = self.root / "deploy/docker-compose.monitoring.yml"
        for name, path in self.dashboards.items():
            self.write(path, json.dumps(self.safe_dashboard(uid=name[:-5])))
        self.write(self.prometheus_ds, """apiVersion: 1
datasources:
  - name: Prometheus
    uid: sub2api-prometheus
    type: prometheus
    url: http://prometheus:9090
""")
        self.write(self.provider, """apiVersion: 1
providers:
  - name: 'Sub2API # dashboard'
    type: file
    options:
      path: '/etc/grafana/dashboards/v2' # provider root
""")
        self.write(self.compose, """services:
  unrelated:
    volumes:
      - ./missing:/unrelated:ro
  grafana:
    volumes:
      - './monitoring/grafana/provisioning:/etc/grafana/provisioning:ro'
      - "./monitoring/grafana/dashboards/01-overview.json:/etc/grafana/dashboards/v2/01-overview.json:ro" # dashboard
      - "./monitoring/grafana/dashboards/02-nodes-chain.json:/etc/grafana/dashboards/v2/02-nodes-chain.json:ro"
      - "./monitoring/grafana/dashboards/03-host.json:/etc/grafana/dashboards/v2/03-host.json:ro"
      - grafana_data:/var/lib/grafana
volumes:
  grafana_data:
""")

    def write(self, path, text):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def safe_dashboard(self, uid="fixture"):
        return {
            "uid": uid,
            "templating": {"list": []},
            "panels": [
                {
                    "id": 1,
                    "type": "text",
                    "title": "",
                    "description": "navigation bar",
                    "datasource": PROM,
                    "targets": [],
                },
                {
                    "id": 2,
                    "type": "stat",
                    "title": "QPS",
                    "description": "gateway QPS",
                    "datasource": PROM,
                    "targets": [
                        {
                            "refId": "A",
                            "datasource": PROM,
                            "expr": "sum(rate(sub2api_gateway_requests_total[$__rate_interval]))",
                        }
                    ],
                },
            ],
        }

    def assert_error(self, message):
        errors = lint(self.root)
        self.assertTrue(any(message in error for error in errors), errors)
        return errors

    def test_valid_canonical_inventory_passes(self):
        self.assertEqual(lint(self.root), [])
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            self.assertEqual(main(["--root", str(self.root)]), 0)
        self.assertIn("PASS:", output.getvalue())

    def test_postgres_datasource_provisioning_is_rejected(self):
        self.write(self.grafana / "provisioning/datasources/postgres.yml", """datasources:
  - type: postgres
    uid: sub2api-pg
""")
        self.assert_error("PostgreSQL datasource provisioning file must be removed")

    def test_postgres_type_in_arbitrary_datasource_file_is_rejected(self):
        self.write(self.grafana / "provisioning/datasources/custom.yml", """apiVersion: 1
datasources:
  - name: Legacy
    type: postgres
    uid: sub2api-pg
""")
        self.assert_error("must not reference PostgreSQL")

    def test_raw_sql_and_pg_datasource_in_dashboard_are_rejected(self):
        data = self.safe_dashboard()
        data["panels"][1]["datasource"] = {"type": "postgres", "uid": "sub2api-pg"}
        data["panels"][1]["targets"][0] = {
            "refId": "A",
            "datasource": {"type": "postgres", "uid": "sub2api-pg"},
            "rawSql": "SELECT 1",
        }
        self.write(self.dashboard, json.dumps(data))
        self.assert_error("rawSql is forbidden")
        self.assert_error("must not reference PostgreSQL datasource")

    def test_forbidden_line_chart_types_are_rejected(self):
        for kind in ("timeseries", "graph", "heatmap"):
            with self.subTest(kind=kind):
                data = self.safe_dashboard()
                data["panels"][1]["type"] = kind
                self.write(self.dashboard, json.dumps(data))
                self.assert_error("forbidden line/heatmap panel type")

    def test_missing_panel_description_is_rejected(self):
        data = self.safe_dashboard()
        data["panels"][1]["description"] = "   "
        self.write(self.dashboard, json.dumps(data))
        self.assert_error("must carry a non-empty description")

    def test_or_vector_1_is_rejected(self):
        data = self.safe_dashboard()
        data["panels"][1]["targets"][0]["expr"] = "up or vector(1)"
        self.write(self.dashboard, json.dumps(data))
        self.assert_error("must not contain 'or vector(1)'")

    def test_retired_dashboard_json_is_rejected(self):
        for name in ("02-chain.json", "02-requests.json", "03-nodes.json",
                     "04-users.json", "05-host.json", "06-request.json"):
            with self.subTest(name=name):
                retired = self.grafana / "dashboards" / name
                self.write(retired, json.dumps(self.safe_dashboard()))
                self.assert_error("retired dashboard JSON must not exist")
                retired.unlink()

    def test_persistent_volume_provider_path_is_rejected(self):
        self.write(self.provider, self.provider.read_text().replace(
            "/etc/grafana/dashboards/v2", "/var/lib/grafana/dashboards/v2"))
        self.write(self.compose, self.compose.read_text().replace(
            "/etc/grafana/dashboards/v2", "/var/lib/grafana/dashboards/v2"))
        self.assert_error("outside persistent /var/lib/grafana volume")

    def test_empty_json(self):
        self.write(self.dashboard, " \n\t")
        self.assert_error("empty JSON file")

    def test_malformed_json(self):
        self.write(self.dashboard, '{"panels": [}')
        errors = self.assert_error("invalid JSON")
        self.assertTrue(any(self.dashboard.name in error for error in errors))

    def test_nonempty_json_object_required(self):
        for value in ("{}", "[]", "null", '"dashboard"'):
            with self.subTest(value=value):
                self.write(self.dashboard, value)
                self.assert_error("expected a nonempty JSON object")

    def test_duplicate_keys_and_non_json_constants(self):
        for value, message in (('{"uid":"one","uid":"two"}', "duplicate JSON key"),
                               ('{"value":NaN}', "non-JSON numeric constant")):
            with self.subTest(value=value):
                self.write(self.dashboard, value)
                self.assert_error(message)

    def test_provisioning_json_is_validated_even_if_not_a_dashboard(self):
        self.write(self.grafana / "provisioning/datasources/extra.json", "")
        self.assert_error("empty JSON file")

    def test_metadata_anywhere_in_monitoring_including_provisioning(self):
        for relative in ("._prometheus.yml", "grafana/provisioning/dashboards/._dashboards.yml",
                         "grafana/provisioning/datasources/.DS_Store", "backup-data/._snapshot.tar.gz"):
            with self.subTest(path=relative):
                path = self.monitoring / relative
                self.write(path, "metadata")
                self.assert_error("macOS metadata must not be deployed")
                path.unlink()

    def test_metadata_directory(self):
        (self.monitoring / "._directory").mkdir()
        self.assert_error("macOS metadata must not be deployed")

    def test_archived_json_not_loaded_or_parsed_when_not_mounted(self):
        self.write(self.grafana / "dashboards/backup/old.json", "not JSON")
        self.assertEqual(lint(self.root), [])

    def test_archive_is_rejected_if_accidentally_exposed_by_directory_mount(self):
        self.write(self.grafana / "dashboards/backup/old.json", '{"uid":"legacy"}')
        self.replace_all_dashboard_mounts("./monitoring/grafana/dashboards:/etc/grafana/dashboards/v2:ro")
        self.assert_error("archived dashboard would be provisioned")

    def replace_first_dashboard_mount(self, spec):
        text = self.compose.read_text(encoding="utf-8")
        text = text.replace(
            '"./monitoring/grafana/dashboards/01-overview.json:/etc/grafana/dashboards/v2/01-overview.json:ro"',
            spec,
        )
        self.write(self.compose, text)

    def replace_all_dashboard_mounts(self, spec):
        self.write(self.compose, f"""services:
  grafana:
    volumes:
      - './monitoring/grafana/provisioning:/etc/grafana/provisioning:ro'
      - {spec}
      - grafana_data:/var/lib/grafana
volumes:
  grafana_data:
""")

    def test_unmounted_active_json(self):
        self.write(self.grafana / "dashboards/new.json", json.dumps(self.safe_dashboard()))
        self.assert_error("active dashboard is not mounted")
        self.assert_error("unexpected non-canonical dashboard")

    def test_missing_mount_source(self):
        self.dashboard.unlink()
        self.assert_error("bind source does not exist")
        self.assert_error("missing canonical dashboard")

    def test_wrong_provider_path(self):
        self.write(self.provider, self.provider.read_text().replace("dashboards/v2", "dashboards/other"))
        self.assert_error("dashboard target is outside all provider paths")

    def test_provider_configuration_must_be_mounted(self):
        self.write(self.compose, self.compose.read_text().replace(
            "./monitoring/grafana/provisioning:/etc/grafana/provisioning:ro",
            "./monitoring/grafana/provisioning:/elsewhere:ro"))
        self.assert_error("provider configuration is not mounted")

    def test_duplicate_mount(self):
        self.write(self.compose, self.compose.read_text().replace(
            "      - grafana_data:/var/lib/grafana",
            "      - ./monitoring/grafana/dashboards/01-overview.json:/etc/grafana/dashboards/v2/duplicate.json:ro\n"
            "      - grafana_data:/var/lib/grafana"))
        self.assert_error("dashboard is provisioned 2 times")

    def test_directory_mount_is_supported(self):
        self.replace_all_dashboard_mounts("./monitoring/grafana/dashboards:/etc/grafana/dashboards/v2:ro")
        self.assertEqual(lint(self.root), [])

    def test_long_syntax_mount_is_supported(self):
        self.replace_first_dashboard_mount("""type: bind
        source: ./monitoring/grafana/dashboards/01-overview.json
        target: /etc/grafana/dashboards/v2/01-overview.json
        read_only: true""")
        self.assertEqual(lint(self.root), [])

    def test_long_syntax_with_standalone_sequence_marker(self):
        self.replace_first_dashboard_mount("""
        type: bind
        source: ./monitoring/grafana/dashboards/01-overview.json
        target: /etc/grafana/dashboards/v2/01-overview.json
        read_only: true""")
        self.assertEqual(lint(self.root), [])

    def test_json_provider_configuration(self):
        self.provider.unlink()
        self.provider = self.provider.with_suffix(".json")
        self.write(self.provider, json.dumps({"providers": [{
            "type": "file", "options": {"path": "/etc/grafana/dashboards/v2"},
        }]}))
        self.assertEqual(lint(self.root), [])

    def test_malformed_json_provider_schema_is_an_error_not_a_traceback(self):
        self.provider.unlink()
        self.provider = self.provider.with_suffix(".json")
        for value in ([1], {"providers": [1]}, {"providers": [{"type": "file", "options": []}]}):
            with self.subTest(value=value):
                self.write(self.provider, json.dumps(value))
                self.assert_error("cannot inspect dashboard provider")

    def test_duplicate_target_is_rejected(self):
        self.write(self.compose, self.compose.read_text().replace(
            "      - grafana_data:/var/lib/grafana",
            "      - ./monitoring/grafana/dashboards/03-host.json:/etc/grafana/dashboards/v2/01-overview.json:ro\n"
            "      - grafana_data:/var/lib/grafana"))
        self.assert_error("multiple dashboard mounts share target")

    def test_writable_config_mount_is_rejected(self):
        self.replace_first_dashboard_mount(
            "./monitoring/grafana/dashboards/01-overview.json:/etc/grafana/dashboards/v2/01-overview.json"
        )
        self.assert_error("configuration bind must be read-only")

    def test_unsupported_yaml_fails_closed(self):
        self.write(self.provider, "providers: [{type: file, options: {path: /tmp}}]\n")
        self.assert_error("providers must use a nonempty YAML block")

    def test_missing_provider_and_compose_are_actionable_errors(self):
        self.provider.unlink()
        self.compose.unlink()
        self.assert_error("cannot inspect Grafana mounts")
        self.assert_error("no dashboard provider configuration found")

    def test_lint_is_read_only(self):
        paths = [self.dashboard, self.compose, self.provider]
        before = {path: (path.read_bytes(), path.stat().st_ino) for path in paths}
        self.assertEqual(lint(self.root), [])
        self.assertEqual(before, {path: (path.read_bytes(), path.stat().st_ino) for path in paths})


if __name__ == "__main__":
    unittest.main()
