#!/usr/bin/env python3
"""AC-8 live Drill 1 with a local V2 image and exact isolated production rule.

Reuses the existing disposable smoke stack/seed/cleanup without editing it.
No Alertmanager, email, external traffic, shortened for/window, or fake counters.
Drill 2 (existing phase counter frozen while V1 grows) is not simulated here.
"""
from datetime import datetime, timezone
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import time
from urllib.parse import urlencode

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('ac8_smoke', Path(__file__).with_name('monitoring-v2-deploy-smoke.py'))
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)
ALERT = 'Sub2apiInstrumentationStalled'


class Drill:
    def configure(self, config, work, evidence, report):
        self.config, self.work = config, work
        text = (ROOT / 'deploy/monitoring/prometheus/rules/chain.yml').read_text()
        source = json.loads('\n'.join(line for line in text.splitlines() if not line.lstrip().startswith('#')))
        rule = next(r for g in source['groups'] for r in g['rules'] if r.get('alert') == ALERT)
        assert rule['for'] == '1m' and '[3m]' in rule['expr']
        rules = {'groups': [{'name': 'ac8-isolated', 'interval': '15s', 'rules': [rule]}]}
        prom = {'global': {'scrape_interval': '15s', 'evaluation_interval': '15s'},
                'scrape_configs': [{'job_name': 'sub2api', 'static_configs': [{'targets': ['sub2api:9091']}]}],
                'rule_files': ['/etc/prometheus/ac8-rule.json']}
        for name, value in [('ac8-rule.json', rules), ('ac8-prom.json', prom)]:
            path = work / name
            path.write_text(json.dumps(value)); path.chmod(0o644)
            (evidence / name).write_text(json.dumps(value, indent=2) + '\n')
        config['services']['prometheus'] = {
            'image': 'prom/prometheus:v3.16.0', 'profiles': ['ac8-drill'],
            'command': ['--config.file=/etc/prometheus/prometheus.yml', '--storage.tsdb.path=/prometheus',
                        '--storage.tsdb.retention.time=2h'],
            'volumes': [str(work / 'ac8-prom.json') + ':/etc/prometheus/prometheus.yml:ro',
                        str(work / 'ac8-rule.json') + ':/etc/prometheus/ac8-rule.json:ro', 'ac8-prom:/prometheus']}
        config['volumes']['ac8-prom'] = {}
        report['AC-8'] = {'drill_1': 'NOT EXECUTED', 'drill_2': 'NOT EXECUTED',
                          'drill_2_reason': 'No stable phase-only freeze mechanism in the unchanged image; disabling V2 removes series and is not Drill 2.',
                          'notifications': 'No Alertmanager configured; no mail or webhook',
                          'rule': rule, 'scrape_seconds': 15, 'evaluation_seconds': 15}

    def verify(self, http, sql, report, evidence, api_key):
        try:
            self._verify(http, sql, report, evidence, api_key)
        finally:
            # The base smoke cleanup does not enable this optional profile.
            # Remove its container/volume first so base down can remove network.
            compose = ['docker', 'compose', '-p', report['project'], '-f', str(self.work / 'compose.json')]
            result = subprocess.run(compose + ['--profile', 'ac8-drill', 'rm', '-s', '-f', 'prometheus'],
                                    text=True, capture_output=True, timeout=60)
            volume = subprocess.run(['docker', 'volume', 'rm', report['project'] + '_ac8-prom'],
                                    text=True, capture_output=True, timeout=30)
            (evidence / 'ac8-prom-cleanup.log').write_text(result.stdout + result.stderr + volume.stdout + volume.stderr)
            assert result.returncode == 0 and volume.returncode == 0, 'AC8 Prom profile cleanup failed'

    def _verify(self, http, sql, report, evidence, api_key):
        info = report['AC-8']
        compose = ['docker', 'compose', '-p', report['project'], '-f', str(self.work / 'compose.json')]
        sequence = 0
        events = []

        def event(name, **data):
            item = {'utc': datetime.now(timezone.utc).isoformat(), 'event': name, **data}
            events.append(item)
            (evidence / 'ac8-timeline.json').write_text(json.dumps(events, indent=2) + '\n')
            print('AC8_TIMELINE', json.dumps(item), flush=True)

        def run(*args):
            result = subprocess.run(compose + list(args), text=True, capture_output=True, timeout=90)
            if result.returncode:
                # Do not print expanded Compose/environment secrets.
                raise smoke.SmokeFailure('AC8 compose operation failed: ' + str(args[:3]))
            return result.stdout.strip()

        def raw_query(query, label):
            nonlocal sequence
            sequence += 1
            path = '/api/v1/query?' + urlencode({'query': query})
            status, _, raw = http('http://prometheus:9090' + path)
            filename = f'ac8-{sequence:03d}-{label}.json'
            (evidence / filename).write_bytes(raw)
            value = json.loads(raw)
            assert status == 200 and value['status'] == 'success', value
            return value['data']['result'], filename

        def snapshot(label):
            rows, filename = raw_query('ALERTS{alertname="' + ALERT + '"}', label + '-ALERTS')
            expression, exprfile = raw_query(info['rule']['expr'], label + '-expression')
            v1, _ = raw_query('sum(increase(sub2api_http_requests_total{route="/v1/messages"}[3m]))', label + '-v1')
            phase, _ = raw_query('sum(increase(sub2api_request_phase_seconds_count[3m]))', label + '-phase')
            states = [r['metric'].get('alertstate') for r in rows]
            event(label, states=states, alerts_raw=filename, expression_raw=exprfile,
                  v1_increase=v1, phase_increase=phase)
            return states, expression, phase, filename

        def healthy():
            deadline = time.monotonic() + 120
            while time.monotonic() < deadline:
                try:
                    if http('http://sub2api:8080/health')[0] == 200:
                        return
                except OSError:
                    pass
                time.sleep(1)
            raise smoke.SmokeFailure('AC8 app readiness timeout')

        def toggle(enabled):
            self.config['services']['sub2api']['environment']['MONITORING_OPS_METRICS_ENABLED'] = str(enabled).lower()
            # The image requires sample persistence to be disabled with V2.
            self.config['services']['sub2api']['environment']['MONITORING_OPS_SAMPLES_ENABLED'] = str(enabled).lower()
            path = self.work / 'compose.json'
            path.write_text(json.dumps(self.config)); path.chmod(0o600)
            run('up', '-d', '--no-deps', '--force-recreate', 'sub2api')
            healthy()
            cid = run('ps', '-q', 'sub2api')
            actual = subprocess.check_output(['docker', 'inspect', '--format', '{{.Image}}', cid], text=True).strip()
            assert actual == report['image_id'], (actual, report['image_id'])
            status, _, raw = http('http://sub2api:9091/metrics')
            assert status == 200 and 'sub2api_http_requests_in_flight' in raw.decode()
            # The phase histogram may be absent until the first enabled gateway request.
            if not enabled:
                assert 'sub2api_request_phase_seconds_count' not in raw.decode()
            event('v2-enabled' if enabled else 'v2-disabled', image_id=actual, v1_metrics_enabled=True)

        def request():
            body = json.dumps({'model': smoke.MODEL, 'max_tokens': 32, 'stream': False,
                               'messages': [{'role': 'user', 'content': 'ac8-controlled-gateway'}]}).encode()
            status, _, raw = http('http://sub2api:8080/v1/messages', body,
                                  {'Authorization': 'Bearer ' + api_key, 'Content-Type': 'application/json',
                                   'anthropic-version': '2023-06-01'})
            assert status == 200, (status, raw[:100])

        # Start a fresh TSDB only after disabled app readiness: no historical
        # enabled phase samples can mask this missing-series exercise.
        toggle(False)
        run('--profile', 'ac8-drill', 'up', '-d', 'prometheus')
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            try:
                if http('http://prometheus:9090/-/ready')[0] == 200:
                    break
            except OSError:
                pass
            time.sleep(1)
        else:
            raise smoke.SmokeFailure('AC8 Prometheus readiness timeout')
        # No gateway probes in idle window; health/metrics paths are not routes
        # matched by the rule. Observe longer than for + two evaluation ticks.
        idle_start = time.monotonic()
        event('idle-start', duration_seconds=95)
        while time.monotonic() - idle_start < 95:
            states, expression, _, _ = snapshot('idle')
            assert not states and not expression, 'Idle false positive'
            time.sleep(10)
        info['idle_before'] = 'PASS (>=95s, V2 disabled, no gateway traffic)'
        flow_start = time.monotonic()
        event('disabled-flow-start')
        firefile = None
        while time.monotonic() - flow_start <= 300:
            request()
            states, _, _, filename = snapshot('disabled-flow')
            if 'firing' in states:
                elapsed = time.monotonic() - flow_start
                assert elapsed <= 300
                firefile = filename
                info['fire_elapsed_seconds'] = elapsed
                event('firing-observed', elapsed_seconds=elapsed, alerts_raw=filename)
                break
            time.sleep(5)
        assert firefile, 'InstrumentationStalled did not fire within 5min'
        restore_start = time.monotonic()
        event('restore-start')
        toggle(True)
        resolved = False
        while time.monotonic() - restore_start < 120:
            request()
            states, expression, phase, filename = snapshot('recovery-flow')
            if not states and not expression and phase and float(phase[0]['value'][1]) > 0:
                elapsed = time.monotonic() - restore_start
                info['resolved_elapsed_seconds'] = elapsed
                info['resolved_alerts_raw'] = filename
                event('resolved-observed', elapsed_seconds=elapsed, alerts_raw=filename)
                resolved = True
                break
            time.sleep(5)
        assert resolved, 'Alert failed to resolve after real phase observations resumed'
        # No requests for longer than the 3m increase window. Continued absent
        # ALERTS proves both idle gating and recovery without restarted Prom.
        event('recovered-idle-start', duration_seconds=200)
        idle_start = time.monotonic()
        while time.monotonic() - idle_start < 200:
            states, expression, _, _ = snapshot('recovered-idle')
            assert not states and not expression, 'Idle/recovery false positive'
            time.sleep(10)
        states, expression, _, filename = snapshot('idle-window-expired')
        assert not states and not expression
        info.update(drill_1='PASS', idle_after='PASS (>3m no gateway traffic)', firing_alerts_raw=firefile,
                    final_alerts_raw=filename, timeline='ac8-timeline.json')
        event('drill-1-pass', drill_2='NOT EXECUTED')


if __name__ == '__main__':
    sys.exit(smoke.main(Drill()))
