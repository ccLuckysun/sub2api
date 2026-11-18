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


class DashboardLintTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.monitoring = self.root / "deploy/monitoring"
        self.grafana = self.monitoring / "grafana"
        self.dashboard = self.grafana / "dashboards/17-arbitrary-name.json"
        self.provider = self.grafana / "provisioning/dashboards/dashboards.yml"
        self.compose = self.root / "deploy/docker-compose.monitoring.yml"
        self.write(self.dashboard, json.dumps(self.safe_dashboard()))
        self.write(self.provider, """apiVersion: 1
providers:
  - name: 'Sub2API # dashboard'
    type: file
    options:
      path: '/var/lib/grafana/dashboards/v2' # provider root
""")
        self.write(self.compose, """services:
  unrelated:
    volumes:
      - ./missing:/unrelated:ro
  grafana:
    volumes:
      - './monitoring/grafana/provisioning:/etc/grafana/provisioning:ro'
      - "./monitoring/grafana/dashboards/17-arbitrary-name.json:/var/lib/grafana/dashboards/v2/17-arbitrary-name.json:ro" # dashboard
      - grafana_data:/var/lib/grafana
volumes:
  grafana_data:
""")

    def write(self, path, text):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def safe_dashboard(self):
        return {
            "uid": "fixture",
            "templating": {"list": [{
                "name": "model", "type": "query", "includeAll": False,
                "current": {"text": "All", "value": "__all__"},
            }]},
            "panels": [{"panels": [{"targets": [{
                "rawSql": "SELECT 1 WHERE ${model:sqlstring} = '__all__'",
            }]}]}],
        }

    def assert_error(self, message):
        errors = lint(self.root)
        self.assertTrue(any(message in error for error in errors), errors)
        return errors

    def test_valid_inventory_has_no_required_filename_or_count(self):
        self.assertEqual(lint(self.root), [])
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            self.assertEqual(main(["--root", str(self.root)]), 0)
        self.assertIn("PASS:", output.getvalue())

    def test_datasource_environment_values_are_opaque(self):
        self.write(self.grafana / "provisioning/datasources/postgres.yml", """datasources:
  - type: postgres
    jsonData:
      database: $GRAFANA_PG_DATABASE
    secureJsonData:
      password: $GRAFANA_PG_PASSWORD
""")
        self.assertEqual(lint(self.root), [])

    def test_raw_api_key_relation_is_rejected_in_variable_query(self):
        data = self.safe_dashboard()
        data["templating"]["list"][0]["query"] = 'SELECT id FROM "public"."api_keys"'
        self.write(self.dashboard, json.dumps(data))
        self.assert_error("SQL must use grafana_api_keys")

    def test_legacy_request_link_is_rejected(self):
        data = self.safe_dashboard()
        data["panels"][0]["fieldConfig"] = {"defaults": {"links": [{
            "url": "/d/sub2api-v2-request?var-request=${__value.raw}&from=${__from}&to=${__to}",
        }]}}
        self.write(self.dashboard, json.dumps(data))
        self.assert_error("request drilldown link must pass var-sample_id")

    def test_request_detail_requires_sample_id_variable_and_predicate(self):
        data = self.safe_dashboard()
        data["uid"] = "sub2api-v2-request"
        targets = data["panels"][0]["panels"][0]["targets"]
        self.write(self.dashboard, json.dumps(data))
        self.assert_error("requires a URL-synced, single sample_id textbox")
        self.assert_error("must filter on the sample's primary key")
        data["templating"]["list"] = [{"name": "sample_id", "type": "textbox"}]
        targets[0]["rawSql"] = ("SELECT s.id FROM ops_request_transfer_samples s "
                                "WHERE s.id = CASE WHEN ${sample_id:sqlstring} <> '' THEN 1 END")
        self.write(self.dashboard, json.dumps(data))
        self.assertEqual(lint(self.root), [])

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
        for value, message in (( '{"uid":"one","uid":"two"}', "duplicate JSON key"),
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

    def test_model_custom_all_sqlstring_regression_in_nested_panel(self):
        data = self.safe_dashboard()
        data["templating"]["list"][0].update(includeAll=True, allValue="__all__")
        self.write(self.dashboard, json.dumps(data))
        self.assert_error("model uses custom All value with ${model:sqlstring}")
        output = io.StringIO()
        with contextlib.redirect_stderr(output):
            self.assertEqual(main(["--root", str(self.root)]), 1)
        self.assertIn("ERROR:", output.getvalue())
        self.assertIn(self.dashboard.name, output.getvalue())

    def test_default_all_and_numeric_custom_all_are_not_rejected(self):
        data = self.safe_dashboard()
        data["templating"]["list"][0].update(includeAll=True, allValue="")
        data["templating"]["list"].append({"name": "user", "includeAll": True, "allValue": "-1"})
        self.write(self.dashboard, json.dumps(data))
        self.assertEqual(lint(self.root), [])

    def test_ordinary_all_sentinel_query_option_with_sqlstring_is_accepted(self):
        data = self.safe_dashboard()
        model = data["templating"]["list"][0]
        model.update(
            includeAll=False,
            query="SELECT '__all__' AS __value, '全部' AS __text "
                  "UNION ALL SELECT DISTINCT model AS __value, model AS __text "
                  "FROM ops_request_transfer_samples",
            options=[{"text": "全部", "value": "__all__"},
                     {"text": "concrete-model", "value": "concrete-model"}],
        )
        self.assertNotIn("allValue", model)
        for selected in ("__all__", "concrete-model"):
            with self.subTest(selected=selected):
                model["current"] = {"text": selected, "value": selected}
                self.write(self.dashboard, json.dumps(data))
                self.assertEqual(lint(self.root), [])

    def test_archived_json_not_loaded_or_parsed_when_not_mounted(self):
        self.write(self.grafana / "dashboards/backup/old.json", "not JSON")
        self.assertEqual(lint(self.root), [])

    def test_archive_is_rejected_if_accidentally_exposed_by_directory_mount(self):
        self.write(self.grafana / "dashboards/backup/old.json", '{"uid":"legacy"}')
        self.replace_dashboard_mount("./monitoring/grafana/dashboards:/var/lib/grafana/dashboards/v2:ro")
        self.assert_error("archived dashboard would be provisioned")

    def replace_dashboard_mount(self, spec):
        text = self.compose.read_text(encoding="utf-8")
        text = text.replace('"./monitoring/grafana/dashboards/17-arbitrary-name.json:/var/lib/grafana/dashboards/v2/17-arbitrary-name.json:ro"', spec)
        self.write(self.compose, text)

    def test_unmounted_active_json(self):
        self.write(self.grafana / "dashboards/new.json", '{"uid":"new"}')
        self.assert_error("active dashboard is not mounted")

    def test_missing_mount_source(self):
        self.dashboard.unlink()
        self.assert_error("bind source does not exist")

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
            "      - ./monitoring/grafana/dashboards/17-arbitrary-name.json:/var/lib/grafana/dashboards/v2/duplicate.json:ro\n"
            "      - grafana_data:/var/lib/grafana"))
        self.assert_error("dashboard is provisioned 2 times")

    def test_directory_mount_is_supported(self):
        self.replace_dashboard_mount("./monitoring/grafana/dashboards:/var/lib/grafana/dashboards/v2:ro")
        self.assertEqual(lint(self.root), [])

    def test_long_syntax_mount_is_supported(self):
        self.replace_dashboard_mount("""type: bind
        source: ./monitoring/grafana/dashboards/17-arbitrary-name.json
        target: /var/lib/grafana/dashboards/v2/17-arbitrary-name.json
        read_only: true""")
        self.assertEqual(lint(self.root), [])

    def test_long_syntax_with_standalone_sequence_marker(self):
        self.replace_dashboard_mount("""
        type: bind
        source: ./monitoring/grafana/dashboards/17-arbitrary-name.json
        target: /var/lib/grafana/dashboards/v2/17-arbitrary-name.json
        read_only: true""")
        self.assertEqual(lint(self.root), [])

    def test_json_provider_configuration(self):
        self.provider.unlink()
        self.provider = self.provider.with_suffix(".json")
        self.write(self.provider, json.dumps({"providers": [{
            "type": "file", "options": {"path": "/var/lib/grafana/dashboards/v2"},
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
        second = self.grafana / "dashboards/another.json"
        self.write(second, '{"uid":"another"}')
        self.write(self.compose, self.compose.read_text().replace(
            "      - grafana_data:/var/lib/grafana",
            "      - ./monitoring/grafana/dashboards/another.json:/var/lib/grafana/dashboards/v2/17-arbitrary-name.json:ro\n"
            "      - grafana_data:/var/lib/grafana"))
        self.assert_error("multiple dashboard mounts share target")

    def test_writable_config_mount_is_rejected(self):
        self.replace_dashboard_mount("./monitoring/grafana/dashboards/17-arbitrary-name.json:/var/lib/grafana/dashboards/v2/17-arbitrary-name.json")
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
