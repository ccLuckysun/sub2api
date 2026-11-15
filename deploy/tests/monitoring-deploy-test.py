#!/usr/bin/env python3
"""Deployment regression tests. Requires Docker + Compose; never contacts SSH.

Real Compose merging and promtool validate configurations. A Docker shim intercepts
service mutations, image loading and pg_dump for remote-deployment failure tests.
"""
import json
import os
import re
import shlex
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[2]
DEPLOY = REPO / 'deploy'
DOCKER = shutil.which('docker')
ASSETS = [
    'compose-deploy.sh', 'docker-compose.monitoring.yml', 'monitoring.env.example',
    'monitoring/render-prometheus-config.sh', 'monitoring/prometheus.yml.tmpl',
    'monitoring/backup/snapshot.sh', 'monitoring/backup/crontab.example',
    'monitoring/grafana/dashboard-preflight.sh',
    'monitoring/grafana/provisioning', 'monitoring/grafana/dashboards',
    'monitoring/prometheus/rules', 'monitoring/alertmanager', 'monitoring/sql',
]


class MonitoringDeployment(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='sub2api-monitoring-test-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.work = self.root / 'deployment with spaces'
        self.work.mkdir()
        for name in ASSETS:
            source, dest = DEPLOY / name, self.work / name
            dest.parent.mkdir(parents=True, exist_ok=True)
            if source.is_dir():
                shutil.copytree(source, dest)
            else:
                shutil.copy2(source, dest)
        shutil.copy2(DEPLOY / 'docker-compose.local.yml', self.work / 'docker-compose.yml')
        self.business_env = b'POSTGRES_PASSWORD=' + b'fixture-' + b'postgres\nREDIS_PASSWORD=' + b'fixture-' + b'redis\n'
        (self.work / '.env').write_bytes(self.business_env)
        self.env = os.environ.copy()
        for key in ['COMPOSE_FILE', 'COMPOSE_PROFILES', 'COMPOSE_PROJECT_NAME',
                    'GRAFANA_ADMIN_PASSWORD', 'MONITORING_HOST_METRICS_ENABLED',
                    'NODE_EXPORTER_HOST_IP', 'PROM_SCRAPE_INTERVAL', 'PROM_SCRAPE_TIMEOUT',
                    'POSTGRES_PASSWORD', 'REDIS_PASSWORD', 'MONITORING_ENV_FILE']:
            self.env.pop(key, None)

    def run_command(self, args, success=True, cwd=None, env=None):
        p = subprocess.run([str(a) for a in args], cwd=cwd or self.root,
                           env=env or self.env, text=True, capture_output=True)
        if success:
            self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        else:
            self.assertNotEqual(p.returncode, 0, p.stdout + p.stderr)
        return p

    def configure(self, *args, success=True):
        return self.run_command(['bash', self.work / 'compose-deploy.sh',
                                 '--no-start', '--project-name', 'monitoring-regression', *args],
                                success=success)

    def config(self):
        return json.loads(self.run_command([self.work / 'sub2api-compose',
                                          'config', '--format', 'json']).stdout)

    def install_docker_shim(self):
        self.log = self.root / 'docker-events.jsonl'
        bindir = self.root / 'bin'
        bindir.mkdir()
        shim = bindir / 'docker'
        shim.write_text('''#!/usr/bin/env python3
import json,os,sys
from pathlib import Path
args=sys.argv[1:]
with open(os.environ['DOCKER_EVENT_LOG'],'a') as f: f.write(json.dumps(args)+'\\n')
if args[0]=='inspect':
    template=args[-1]
    answers={'.Config.Image':'old/sub2api:1.0.0', '.Image':'sha256:old-running-image',
      'project.working_dir':os.environ['FAKE_WORKDIR'],
      'project.config_files':os.environ['FAKE_CONFIG_FILES'],
      'project.environment_file':os.environ.get('FAKE_ENV_FILES',''),
      'com.docker.compose.project"':'production-project'}
    for key,value in answers.items():
        if key in template: print(value); sys.exit(0)
    raise SystemExit('Unexpected inspect: '+template)
if args[0]=='tag': sys.exit(0)
if args[0]=='load': sys.stdin.buffer.read(); sys.exit(0)
if args[0]=='exec' and args[1]=='existing-postgres':
    if os.environ.get('FAIL_DUMP'): sys.exit(1)
    sys.stdout.buffer.write(b'PGDMP-fixture'); sys.exit(0)
if args[0]=='logs': sys.exit(0)
if args[0]=='compose':
    if 'up' in args: sys.exit(int(os.environ.get('FAIL_UP','0')))
    if 'exec' in args: print('{}'); sys.exit(0)
os.execv(os.environ['REAL_DOCKER'],[os.environ['REAL_DOCKER'],*args])
''')
        shim.chmod(0o755)
        self.env.update(PATH=str(bindir) + os.pathsep + self.env['PATH'],
                        DOCKER_EVENT_LOG=str(self.log), REAL_DOCKER=DOCKER,
                        FAKE_WORKDIR=str(self.work),
                        FAKE_CONFIG_FILES=str(self.work / 'docker-compose.yml') + ',' + str(self.work / 'caddy.yml'))

    def remote_fixture(self):
        self.install_docker_shim()
        (self.work / 'caddy.yml').write_text('services:\n  caddy:\n    image: caddy:2\n')
        stage = self.root / 'upload'
        stage.mkdir()
        (stage / 'image.tar.gz').write_bytes(b'image-fixture')
        with tarfile.open(stage / 'monitoring-assets.tar.gz', 'w:gz') as archive:
            for name in ASSETS:
                archive.add(DEPLOY / name, arcname=name)
        self.env['SUB2API_BACKUP_DIR'] = str(self.root)
        return ['bash', DEPLOY / 'remote-deploy.sh', '1.0.1', stage, 'monitoring',
                'existing-app', 'existing-postgres', 'example/sub2api']

    def test_five_services_and_idempotent_credentials(self):
        self.configure('--monitoring', '--image', 'example/sub2api:1.0.1')
        first_env = (self.work / 'monitoring.env').read_bytes()
        self.assertEqual((self.work / 'monitoring.env').stat().st_mode & 0o777, 0o600)
        config = self.config()
        self.assertEqual(set(config['services']), {'sub2api', 'postgres', 'redis', 'grafana', 'prometheus', 'alertmanager'})
        self.assertEqual(config['services']['sub2api']['environment']['DATABASE_PASSWORD'], self.business_env.decode().splitlines()[0].split('=', 1)[1])
        self.assertEqual(config['services']['grafana']['ports'][0]['host_ip'], '127.0.0.1')
        self.assertNotIn('ports', config['services']['prometheus'])
        self.assertEqual(config['services']['sub2api']['environment']['MONITORING_METRICS_ENABLED'], 'true')
        self.assertNotIn('job_name: node', (self.work / 'monitoring/prometheus.yml').read_text())
        # Re-rendering identical content must keep the inode used by a bind mount.
        inode = (self.work / 'monitoring/prometheus.yml').stat().st_ino
        # Subsequent invocations preserve monitoring even if the flag is omitted.
        self.configure('--image', 'example/sub2api:1.0.2')
        self.assertEqual((self.work / 'monitoring.env').read_bytes(), first_env)
        self.assertEqual((self.work / 'monitoring/prometheus.yml').stat().st_ino, inode)
        self.assertEqual((self.work / '.env').read_bytes(), self.business_env)
        self.assertIn('grafana', self.config()['services'])

    def test_shell_only_business_environment_without_dotenv(self):
        (self.work / '.env').unlink()
        self.env['POSTGRES_PASSWORD'] = 'fixture-' + 'postgres'
        self.configure('--monitoring')
        self.assertEqual(len(self.config()['services']), 6)

    def test_preparation_script_preserves_existing_and_partial_business_configuration(self):
        # Exercise the user-facing one-click entry, including a partially prepared install.
        (self.work / 'docker-compose.yml').unlink()
        shutil.rmtree(self.work / 'monitoring')
        self.run_command(['bash', DEPLOY / 'docker-deploy.sh', '--monitoring', '--no-start',
                          '--image', 'example/sub2api:1.0.1'], cwd=self.work)
        self.assertEqual((self.work / '.env').read_bytes(), self.business_env)
        self.assertEqual(len(self.config()['services']), 6)
        self.assertEqual((self.work / 'monitoring/grafana/provisioning').stat().st_mode & 0o777, 0o755)
        for directory in ('plugins', 'alerting'):
            optional = self.work / 'monitoring/grafana/provisioning' / directory
            self.assertTrue(optional.is_dir(), directory)
            self.assertEqual(optional.stat().st_mode & 0o777, 0o755)
        self.assertEqual((self.work / 'monitoring/grafana/provisioning/datasources/prometheus.yml').stat().st_mode & 0o777, 0o644)
        password_file = (self.work / 'monitoring.env').read_bytes()
        self.run_command(['bash', DEPLOY / 'docker-deploy.sh', '--monitoring', '--no-start'], cwd=self.work)
        self.assertEqual((self.work / 'monitoring.env').read_bytes(), password_file)
        self.assertEqual((self.work / '.env').read_bytes(), self.business_env)

    def test_legacy_preparation_still_generates_three_service_configuration(self):
        (self.work / 'docker-compose.yml').unlink()
        (self.work / '.env').unlink()
        self.run_command(['bash', DEPLOY / 'docker-deploy.sh'], cwd=self.work)
        result = self.run_command([DOCKER, 'compose', '--project-directory', self.work,
                                  '-f', self.work / 'docker-compose.yml', 'config', '--services'])
        self.assertEqual(set(result.stdout.split()), {'sub2api', 'postgres', 'redis'})
        self.assertFalse((self.work / 'sub2api-compose').exists())
        self.assertEqual((self.work / '.env').stat().st_mode & 0o777, 0o600)

    def test_render_failure_preserves_last_valid_file(self):
        self.configure('--monitoring')
        output = self.work / 'monitoring/prometheus.yml'
        original = output.read_bytes()
        self.env['PROM_SCRAPE_INTERVAL'] = 'invalid-duration'
        self.run_command(['sh', self.work / 'monitoring/render-prometheus-config.sh'], success=False)
        self.assertEqual(output.read_bytes(), original)
        self.assertEqual(list(output.parent.glob('prometheus.yml.tmp.*')), [])

    def test_host_metrics_and_backup_are_separate_optional_containers(self):
        self.configure('--host-metrics', '--backup')
        config = self.config()
        node = config['services']['node-exporter']
        self.assertEqual(node['network_mode'], 'host')
        self.assertNotIn('networks', node)
        gateway = self.run_command([DOCKER, 'network', 'inspect', 'bridge', '--format',
                                   '{{(index .IPAM.Config 0).Gateway}}']).stdout.strip()
        self.assertIn('--web.listen-address=' + gateway + ':9100', node['command'])
        self.assertIn(gateway + ':9100', (self.work / 'monitoring/prometheus.yml').read_text())
        self.assertEqual(config['services']['backup']['volumes'][0]['volume']['subpath'], 'snapshots')
        self.assertIn('snapshots-init', config['services'])
        self.assertFalse(config['services']['prometheus']['depends_on']['snapshots-init']['required'])

    def test_public_exporter_address_is_rejected(self):
        self.env.update(MONITORING_HOST_METRICS_ENABLED='true', NODE_EXPORTER_HOST_IP='8.8.8.8')
        self.run_command(['sh', self.work / 'monitoring/render-prometheus-config.sh'], success=False)
        self.assertFalse((self.work / 'monitoring/prometheus.yml').exists())

    def test_remote_preserves_project_files_and_only_recreates_app_and_monitoring(self):
        command = self.remote_fixture()
        p = self.run_command(command)
        events = [json.loads(line) for line in self.log.read_text().splitlines()]
        tag_index = next(i for i, e in enumerate(events) if e[0] == 'tag')
        load_index = next(i for i, e in enumerate(events) if e[0] == 'load')
        self.assertLess(tag_index, load_index)
        self.assertEqual(events[tag_index][1], 'sha256:old-running-image')
        ups = [e for e in events if e[0] == 'compose' and 'up' in e]
        self.assertEqual(len(ups), 2)
        for command in ups:
            tail = command[command.index('up') + 1:]
            self.assertIn('--no-deps', tail)
            self.assertNotIn('postgres', tail)
            self.assertNotIn('redis', tail)
            self.assertNotIn('caddy', tail)
        self.assertIn(str(self.work / 'caddy.yml'), (self.work / 'sub2api-compose').read_text().replace('\\ ', ' '))
        self.assertEqual((self.work / '.env').read_bytes(), self.business_env)
        rollback = next(self.work.glob('sub2api-rollback-*.sh'))
        self.run_command(['bash', '-n', rollback])
        self.assertIn('rollback-', rollback.read_text())
        self.assertNotIn('GRAFANA_ADMIN_PASSWORD=', p.stdout)

    def test_remote_preserves_custom_business_environment_files(self):
        command = self.remote_fixture()
        custom = self.work / 'production.env'
        (self.work / '.env').rename(custom)
        self.env['FAKE_ENV_FILES'] = str(custom)
        self.run_command(command)
        config = self.config()
        expected = self.business_env.decode().splitlines()[0].split('=', 1)[1]
        self.assertEqual(config['services']['sub2api']['environment']['DATABASE_PASSWORD'], expected)
        self.assertEqual(custom.read_bytes(), self.business_env)

    def test_remote_failed_backup_never_starts_services(self):
        command = self.remote_fixture()
        self.env['FAIL_DUMP'] = '1'
        self.run_command(command, success=False)
        events = [json.loads(line) for line in self.log.read_text().splitlines()]
        self.assertFalse(any('up' in e for e in events))
        self.assertFalse((self.work / 'docker-compose.release.yml').exists())

    def test_remote_health_failure_emits_executable_rollback(self):
        command = self.remote_fixture()
        self.env['FAIL_UP'] = '1'
        p = self.run_command(command, success=False)
        self.assertIn('Rollback application:', p.stdout)
        self.assertTrue(list(self.work.glob('sub2api-rollback-*.sh')))


class MonitoringDeploymentSafety(unittest.TestCase):
    """Read-only shell/packaging fixtures; no Docker daemon or SSH required.

    Run independently: python3 deploy/tests/monitoring-deploy-test.py MonitoringDeploymentSafety
    """
    NAMES = ['01-overview', '02-requests', '03-nodes', '04-users', '05-host', '06-request']

    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix='monitoring-deployment-safety-')
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.dashboards = self.root / 'provider with spaces'
        self.dashboards.mkdir()
        self.provisioning = self.root / 'provisioning with spaces'
        self.provisioning.mkdir()
        for name in self.NAMES:
            (self.dashboards / (name + '.json')).write_text('{"panels": []}\n')
        self.preflight = DEPLOY / 'monitoring/grafana/dashboard-preflight.sh'

    def check(self, mode='--source', directory=None):
        return subprocess.run(['sh', str(self.preflight), mode, str(directory or self.dashboards)],
                              text=True, capture_output=True)

    def test_six_canonical_source_and_volume_pass(self):
        for mode in ['--source', '--volume']:
            result = self.check(mode)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn('six canonical', result.stdout)

    def test_zero_byte_json_fails_source_and_volume_without_deletion(self):
        for name in ['02-requests.json', '02-chain.json', 'unrelated.json']:
            for nested in [False, True]:
                with self.subTest(name=name, nested=nested):
                    target = self.dashboards / ('nested' if nested else '') / name
                    target.parent.mkdir(exist_ok=True)
                    original = target.read_bytes() if target.exists() else None
                    target.write_bytes(b'')
                    for mode in ['--source', '--volume']:
                        result = self.check(mode)
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn('zero-byte dashboard JSON', result.stderr)
                        self.assertIn(str(target), result.stderr)
                        self.assertTrue(target.exists())
                        self.assertEqual(target.stat().st_size, 0)
                    if original is None:
                        target.unlink()
                    else:
                        target.write_bytes(original)

    def test_nonempty_binary_metadata_fails_source_and_volume_without_deletion(self):
        content = b'\x00\x05\x16\x07\x00\x02\x00\x00AppleDouble binary fixture\xff'
        for name in ['._02-requests.json', '.DS_Store', '._datasource.yml']:
            for nested in [False, True]:
                with self.subTest(name=name, nested=nested):
                    target = self.dashboards / ('nested' if nested else '') / name
                    target.parent.mkdir(exist_ok=True)
                    target.write_bytes(content)
                    for mode in ['--source', '--volume']:
                        result = self.check(mode)
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn('macOS metadata artifact', result.stderr)
                        self.assertIn(str(target), result.stderr)
                        self.assertEqual(target.read_bytes(), content)
                    target.unlink()

    def test_provisioning_metadata_mode_without_canonical_dashboard_requirement(self):
        result = self.check('--metadata', self.provisioning)
        self.assertEqual(result.returncode, 0, result.stderr)
        datasource = self.provisioning / 'datasources'
        datasource.mkdir()
        for name in ['._postgres.yml', '.DS_Store']:
            target = datasource / name
            target.write_bytes(b'\x00\x05\x16\x07nonempty binary fixture')
            result = self.check('--metadata', self.provisioning)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('macOS metadata artifact', result.stderr)
            self.assertIn(str(target), result.stderr)
            self.assertTrue(target.exists())
            target.unlink()

    def test_nonempty_historical_chain_is_explicitly_rejected(self):
        target = self.dashboards / '02-chain.json'
        target.write_text('{"historical": true}')
        for mode in ['--source', '--volume']:
            result = self.check(mode)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('retired 02-chain.json artifact', result.stderr)
            self.assertTrue(target.exists())

    def test_missing_required_json_fails(self):
        (self.dashboards / '06-request.json').unlink()
        result = self.check()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('06-request.json', result.stderr)

    def test_deployment_aborts_on_empty_json_before_any_docker_call(self):
        work = self.root / 'deploy'
        assets = work / 'monitoring/grafana'
        assets.mkdir(parents=True)
        shutil.copy2(DEPLOY / 'compose-deploy.sh', work)
        shutil.copy2(self.preflight, assets)
        shutil.copytree(self.dashboards, assets / 'dashboards')
        (assets / 'dashboards/02-chain.json').write_bytes(b'')
        bindir = self.root / 'bin'
        bindir.mkdir()
        marker = self.root / 'docker-was-called'
        shim = bindir / 'docker'
        shim.write_text('#!/bin/sh\ntouch "$DOCKER_MARKER"\nexit 99\n')
        shim.chmod(0o755)
        env = dict(os.environ, PATH=str(bindir) + os.pathsep + os.environ['PATH'],
                   DOCKER_MARKER=str(marker))
        result = subprocess.run(['bash', str(work / 'compose-deploy.sh'), '--monitoring', '--no-start'],
                                env=env, text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('zero-byte dashboard JSON', result.stderr)
        self.assertFalse(marker.exists())

    def test_grafana_entrypoint_checks_volume_before_run_and_mounts_only_six(self):
        compose = (DEPLOY / 'docker-compose.monitoring.yml').read_text()
        self.assertIn('--volume /var/lib/grafana/dashboards/v2 && exec /run.sh', compose)
        mounted = re.findall(r'source: ./monitoring/grafana/dashboards/([^\n]+)', compose)
        self.assertEqual(mounted, [name + '.json' for name in self.NAMES])
        self.assertGreaterEqual(compose.count('bind: {create_host_path: false}'), 7)
        self.assertIn('monitoring/grafana/dashboard-preflight.sh', (DEPLOY / 'docker-deploy.sh').read_text())

    def fixture_grafana_entrypoint(self, fake_run):
        compose = (DEPLOY / 'docker-compose.monitoring.yml').read_text()
        grafana = compose.split('\n  grafana:\n', 1)[1]
        block = grafana.split('    entrypoint:\n', 1)[1].split('    restart:', 1)[0]
        # Read the actual YAML list without requiring PyYAML, emulating Compose's
        # $$ -> $ interpolation and retaining the shell's explicit $0 argument.
        entrypoint = [line.strip()[2:].replace('$$', '$')
                      for line in block.splitlines() if line.strip().startswith('- ')]
        self.assertEqual(entrypoint[:2], ['/bin/sh', '-ec'])
        self.assertEqual(entrypoint[3:], ['grafana-dashboard-preflight'])
        command = entrypoint[2]
        command = command.replace('/etc/grafana/dashboard-preflight.sh', shlex.quote(str(self.preflight)))
        command = command.replace('/var/lib/grafana/dashboards/v2', shlex.quote(str(self.dashboards)))
        command = command.replace('/etc/grafana/provisioning', shlex.quote(str(self.provisioning)))
        command = command.replace('/run.sh', shlex.quote(str(fake_run)))
        return [*entrypoint[:2], command, *entrypoint[3:]]

    def test_real_entrypoint_does_not_start_grafana_with_stale_empty_volume_json(self):
        marker = self.root / 'grafana-started'
        fake_run = self.root / 'run.sh'
        fake_run.write_text('#!/bin/sh\ntouch "$GRAFANA_START_MARKER"\n')
        fake_run.chmod(0o755)
        entrypoint = self.fixture_grafana_entrypoint(fake_run)
        env = dict(os.environ, GRAFANA_START_MARKER=str(marker))
        result = subprocess.run(entrypoint, env=env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(marker.exists())
        marker.unlink()
        (self.dashboards / '02-chain.json').write_bytes(b'')
        result = subprocess.run(entrypoint, env=env, text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('02-chain.json', result.stderr)
        self.assertFalse(marker.exists())

    def test_real_entrypoint_rejects_binary_dashboard_and_provisioning_metadata(self):
        marker = self.root / 'grafana-started'
        fake_run = self.root / 'run.sh'
        fake_run.write_text('#!/bin/sh\ntouch "$GRAFANA_START_MARKER"\n')
        fake_run.chmod(0o755)
        entrypoint = self.fixture_grafana_entrypoint(fake_run)
        env = dict(os.environ, GRAFANA_START_MARKER=str(marker))
        for directory in [self.dashboards, self.provisioning]:
            for name in ['._02-requests.json', '.DS_Store', 'nested/._postgres.yml']:
                with self.subTest(directory=directory, name=name):
                    target = directory / name
                    target.parent.mkdir(exist_ok=True)
                    data = b'\x00\x05\x16\x07nonempty binary fixture\xff'
                    target.write_bytes(data)
                    result = subprocess.run(entrypoint, env=env, text=True, capture_output=True)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn('macOS metadata artifact', result.stderr)
                    self.assertIn(str(target), result.stderr)
                    self.assertFalse(marker.exists())
                    self.assertEqual(target.read_bytes(), data)
                    target.unlink()

    def test_real_entrypoint_forwards_command_flags_literally(self):
        capture = self.root / 'grafana-args'
        fake_run = self.root / 'run.sh'
        fake_run.write_text('#!/bin/sh\nprintf \'%s\\0\' "$@" > "$GRAFANA_ARGS_CAPTURE"\n')
        fake_run.chmod(0o755)
        entrypoint = self.fixture_grafana_entrypoint(fake_run)
        sentinel = self.root / 'must-not-be-created'
        args = ['--homepath=/usr/share/grafana', '--config', '/path with spaces/custom.ini',
                'cfg:server.domain=literal $HOME; * ? [abc] "quoted" \'single\'',
                '$(touch ' + str(sentinel) + ')', '', 'line one\nline two', '--last-flag']
        env = dict(os.environ, GRAFANA_ARGS_CAPTURE=str(capture))
        result = subprocess.run([*entrypoint, *args], env=env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(capture.read_bytes(), b''.join(arg.encode() + b'\0' for arg in args))
        self.assertFalse(sentinel.exists())

    def test_generated_wrapper_checks_up_start_restart_but_allows_diagnostics(self):
        work = self.root / 'deployment with spaces'
        for name in ASSETS:
            source, dest = DEPLOY / name, work / name
            dest.parent.mkdir(parents=True, exist_ok=True)
            if source.is_dir():
                shutil.copytree(source, dest)
            else:
                shutil.copy2(source, dest)
        shutil.copy2(DEPLOY / 'docker-compose.local.yml', work / 'docker-compose.yml')
        (work / 'monitoring.env').write_text('GRAFANA_ADMIN_PASSWORD=local-fixture-admin\nGRAFANA_PG_PASSWORD=local-fixture-pg')
        bindir = self.root / 'bin'
        bindir.mkdir()
        log = self.root / 'docker-events'
        docker = bindir / 'docker'
        docker.write_text('''#!/bin/sh
printf '%s\\n' "$*" >> "$DOCKER_EVENT_LOG"
case "$*" in
  info) exit 0 ;;
  'compose version --short') printf '2.35.0\\n'; exit 0 ;;
  'compose '*config*|'compose '*logs*) exit 0 ;;
  *) printf 'Unexpected fake Docker call\\n' >&2; exit 97 ;;
esac
''')
        docker.chmod(0o755)
        promtool = bindir / 'promtool'
        promtool.write_text('#!/bin/sh\nexit 0\n')
        promtool.chmod(0o755)
        env = dict(os.environ, PATH=str(bindir) + os.pathsep + os.environ['PATH'],
                   DOCKER_EVENT_LOG=str(log), MONITORING_HOST_METRICS_ENABLED='false')
        # File-by-file installers do not fetch .gitkeep placeholders. Preparation
        # must create readable optional provisioning roots even under umask 077.
        for directory in ('plugins', 'alerting'):
            optional = work / 'monitoring/grafana/provisioning' / directory
            if optional.exists():
                shutil.rmtree(optional)
        result = subprocess.run(['bash', '-c', 'umask 077; exec bash "$@"', 'prepare',
                                 str(work / 'compose-deploy.sh'), '--monitoring', '--no-start'],
                                env=env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        for directory in ('plugins', 'alerting'):
            optional = work / 'monitoring/grafana/provisioning' / directory
            self.assertTrue(optional.is_dir(), directory)
            self.assertEqual(optional.stat().st_mode & 0o777, 0o755)
        original_log = log.read_bytes()
        (work / 'monitoring/grafana/dashboards/02-requests.json').write_bytes(b'')
        for action in ['up', 'start', 'restart']:
            result = subprocess.run([str(work / 'sub2api-compose'), action, '-d'],
                                    env=env, text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('zero-byte dashboard JSON', result.stderr)
            self.assertEqual(log.read_bytes(), original_log)
        (work / 'monitoring/grafana/dashboards/02-requests.json').write_text('{}')
        (work / 'monitoring/grafana/provisioning/._postgres.yml').write_bytes(b'\x00binary metadata')
        result = subprocess.run(['bash', str(work / 'compose-deploy.sh'), '--monitoring', '--no-start'],
                                env=env, text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('macOS metadata artifact', result.stderr)
        self.assertEqual(log.read_bytes(), original_log)
        for action in ['up', 'start', 'restart']:
            result = subprocess.run([str(work / 'sub2api-compose'), action, '-d'],
                                    env=env, text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('macOS metadata artifact', result.stderr)
            self.assertEqual(log.read_bytes(), original_log)
        result = subprocess.run([str(work / 'sub2api-compose'), 'logs', 'grafana'],
                                env=env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotEqual(log.read_bytes(), original_log)

    def test_actual_release_tar_excludes_macos_metadata_recursively(self):
        # Execute only the real local tar stanza, never the build/SSH/deploy script.
        script = (REPO / 'deploy-us1.sh').read_text()
        start = script.index('COPYFILE_DISABLE=1 tar ')
        command = script[start:script.index('\nREMOTE_DIR=', start)]
        deploy = self.root / 'deploy'
        for name in ASSETS:
            source, dest = DEPLOY / name, deploy / name
            dest.parent.mkdir(parents=True, exist_ok=True)
            if source.is_dir():
                shutil.copytree(source, dest)
            else:
                shutil.copy2(source, dest)
        for directory in [deploy, deploy / 'monitoring/grafana/dashboards',
                          deploy / 'monitoring/grafana/provisioning']:
            (directory / '.DS_Store').write_bytes(b'metadata')
            (directory / '._dashboard.json').write_bytes(b'appledouble')
        env = dict(os.environ, LOCAL_DIR=str(self.root))
        result = subprocess.run(['bash', '-ec', command], cwd=self.root, env=env,
                                text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        with tarfile.open(self.root / 'monitoring-assets.tar.gz') as archive:
            names = archive.getnames()
        self.assertIn('monitoring/grafana/dashboard-preflight.sh', names)
        for name in self.NAMES:
            self.assertIn('monitoring/grafana/dashboards/' + name + '.json', names)
        self.assertFalse(any(part.startswith('._') or part == '.DS_Store'
                             for name in names for part in Path(name).parts))
        self.assertFalse(any(Path(name).name == '02-chain.json' for name in names))

    def test_all_monitoring_tar_paths_disable_metadata_and_no_json_sed_in_place(self):
        paths = [REPO / 'deploy-us1.sh', DEPLOY / 'remote-deploy.sh',
                 DEPLOY / 'monitoring/backup/snapshot.sh']
        count = 0
        for path in paths:
            for line in path.read_text().splitlines():
                if re.search(r'\btar\s+--exclude', line):
                    count += 1
                    self.assertIn('COPYFILE_DISABLE=1', line)
                    self.assertIn("--exclude='._*'", line)
                    self.assertIn("--exclude='.DS_Store'", line)
        self.assertEqual(count, 4)  # release, config backup, extraction, snapshot
        for path in [*paths, DEPLOY / 'compose-deploy.sh', DEPLOY / 'docker-deploy.sh']:
            self.assertNotRegex(path.read_text(), r'sed\b[^\n]*\s-i[^\n]*(?:dashboard|\.json)')
        local_doc = (DEPLOY / 'MONITORING_LOCAL.md').read_text()
        self.assertNotIn('部署脚本替换挂载文件后最多 30s 生效，无需重启', local_doc)
        self.assertIn('--force-recreate grafana', local_doc)


if __name__ == '__main__':
    unittest.main(verbosity=2)
