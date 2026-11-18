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
                    'GRAFANA_ADMIN_PASSWORD', 'GRAFANA_PG_PASSWORD', 'MONITORING_HOST_METRICS_ENABLED',
                    'MONITORING_BACKUP_ENABLED', 'SUB2API_DEV_ENV_FILE',
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
import json,os,subprocess,sys
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
    if 'up' in args:
        sys.exit(int(os.environ.get('FAIL_UP','0')) or int(os.environ.get('FAIL_WAIT_FOR') in args))
    if 'run' in args and args[-1]=='snapshots-init': sys.exit(0)
    if 'exec' in args:
        index=args.index('exec')
        service,command,*params=args[index+2:]
        if service=='postgres' and command=='printenv':
            print({'POSTGRES_USER':'fixture_user','POSTGRES_DB':'fixture_db'}[params[0]]); sys.exit(0)
        if service=='grafana' and command=='sh':
            config=subprocess.check_output([os.environ['REAL_DOCKER'],*args[:index],
                                            'config','--format','json'])
            grafana_env={key:value.replace('$$','$') for key,value in
                         json.loads(config)['services']['grafana']['environment'].items()}
            os.execvpe(command,[command,*params],dict(os.environ,**grafana_env))
        if service=='postgres' and command=='sh':
            os.execvp(command,[command,*params])
        if service=='sub2api' and command=='printenv' and params==['MONITORING_METRICS_ENABLED']:
            print(os.environ.get('FIXTURE_METRICS_ENABLED','true')); sys.exit(0)
        if service=='prometheus' and command=='sh':
            sys.exit(int(os.environ.get('FAIL_APP_METRICS','0')))
        raise SystemExit('Unexpected Compose exec: '+str(args))
    if 'config' not in args and 'version' not in args:
        raise SystemExit('Unexpected Compose command: '+str(args))
os.execv(os.environ['REAL_DOCKER'],[os.environ['REAL_DOCKER'],*args])
''')
        shim.chmod(0o755)
        psql = bindir / 'psql'
        psql.write_text('''#!/usr/bin/env python3
import json,os,sys
from pathlib import Path
Path(os.environ['PSQL_CAPTURE']).write_text(json.dumps({
    'args':sys.argv[1:], 'credential':os.environ['GRAFANA_PG_PASSWORD'], 'sql':sys.stdin.read(),
}))
''')
        psql.chmod(0o755)
        self.psql_capture = self.root / 'psql.json'
        self.env.update(PATH=str(bindir) + os.pathsep + self.env['PATH'],
                        DOCKER_EVENT_LOG=str(self.log), REAL_DOCKER=DOCKER,
                        PSQL_CAPTURE=str(self.psql_capture),
                        FAKE_WORKDIR=str(self.work),
                        FAKE_CONFIG_FILES=str(self.work / 'docker-compose.yml') + ',' + str(self.work / 'caddy.yml'))

    def docker_events(self):
        return [json.loads(line) for line in self.log.read_text().splitlines()]

    def remote_fixture(self, mode='monitoring'):
        self.install_docker_shim()
        (self.work / 'caddy.yml').write_text('services:\n  caddy:\n    image: caddy:2\n')
        stage = self.root / 'upload'
        stage.mkdir()
        (stage / 'image.tar.gz').write_bytes(b'image-fixture')
        with tarfile.open(stage / 'monitoring-assets.tar.gz', 'w:gz') as archive:
            for name in ASSETS:
                archive.add(DEPLOY / name, arcname=name)
        self.env['SUB2API_BACKUP_DIR'] = str(self.root)
        return ['bash', DEPLOY / 'remote-deploy.sh', '1.0.1', stage, mode,
                'existing-app', 'existing-postgres', 'example/sub2api']

    def test_six_services_and_idempotent_credentials(self):
        self.configure('--monitoring', '--image', 'example/sub2api:1.0.1')
        first_env = (self.work / 'monitoring.env').read_bytes()
        self.assertEqual((self.work / 'monitoring.env').stat().st_mode & 0o777, 0o600)
        config = self.config()
        self.assertEqual(set(config['services']), {'sub2api', 'postgres', 'redis', 'grafana', 'prometheus', 'alertmanager'})
        self.assertEqual(config['services']['sub2api']['environment']['DATABASE_PASSWORD'], self.business_env.decode().splitlines()[0].split('=', 1)[1])
        self.assertEqual(config['services']['grafana']['ports'][0]['host_ip'], '127.0.0.1')
        self.assertNotIn('ports', config['services']['prometheus'])
        self.assertIn('http://localhost:9093/-/ready', config['services']['alertmanager']['healthcheck']['test'])
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

    def test_metrics_check_rejects_incompatible_image_unless_explicitly_disabled(self):
        self.install_docker_shim()
        self.configure('--monitoring')
        self.env['FAIL_APP_METRICS'] = '1'
        command = ['bash', self.work / 'compose-deploy.sh', '--check-monitoring']
        result = self.run_command(command, success=False)
        self.assertIn('image lacks the V2 monitoring baseline', result.stderr)
        self.env['FIXTURE_METRICS_ENABLED'] = 'false'
        result = self.run_command(command)
        self.assertIn('explicitly disabled', result.stdout)

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
        events = self.docker_events()
        tag_index = next(i for i, e in enumerate(events) if e[0] == 'tag')
        load_index = next(i for i, e in enumerate(events) if e[0] == 'load')
        self.assertLess(tag_index, load_index)
        self.assertEqual(events[tag_index][1], 'sha256:old-running-image')
        ups = [e for e in events if e[0] == 'compose' and 'up' in e]
        self.assertEqual([command[command.index('--force-recreate') + 1:] for command in ups],
                         [['sub2api'], ['prometheus', 'alertmanager'], ['grafana']])
        for command in ups:
            tail = command[command.index('up') + 1:]
            self.assertIn('--no-deps', tail)
            self.assertIn('--wait', tail)
            self.assertNotIn('postgres', tail)
            self.assertNotIn('redis', tail)
            self.assertNotIn('caddy', tail)
        self.assertIn(str(self.work / 'caddy.yml'), (self.work / 'sub2api-compose').read_text().replace('\\ ', ' '))
        self.assertEqual((self.work / '.env').read_bytes(), self.business_env)
        rollback = next(self.work.glob('sub2api-rollback-*.sh'))
        self.run_command(['bash', '-n', rollback])
        self.assertIn('rollback-', rollback.read_text())
        self.assertNotIn('GRAFANA_ADMIN_PASSWORD=', p.stdout)

    def test_remote_waits_for_alertmanager_and_does_not_report_success_on_failure(self):
        command = self.remote_fixture()
        self.env['FAIL_WAIT_FOR'] = 'alertmanager'
        result = self.run_command(command, success=False)
        self.assertIn('Rollback application:', result.stdout)
        self.assertNotIn('Deployment complete.', result.stdout)
        self.assertFalse(self.psql_capture.exists())

    def test_remote_archives_retired_source_dashboard_before_upgrade(self):
        command = self.remote_fixture()
        relative = 'monitoring/grafana/dashboards/02-chain.json'
        retired = self.work / relative
        original = b'{"title":"Legacy chain dashboard"}\n'
        retired.write_bytes(original)
        result = self.run_command(command)
        self.assertIn('Archived retired dashboard:', result.stdout)
        self.assertIn('Deployment complete.', result.stdout)
        self.assertFalse(retired.exists())
        archived, = self.work.glob('sub2api-retired-dashboards-*/02-chain.json')
        self.assertEqual(archived.read_bytes(), original)
        self.assertEqual(archived.parent.stat().st_mode & 0o777, 0o700)
        backup, = self.work.glob('sub2api-deploy-config-*.tar.gz')
        with tarfile.open(backup) as archive:
            self.assertEqual(archive.extractfile(relative).read(), original)

    def test_remote_will_not_move_a_symlink_at_the_retired_dashboard_path(self):
        command = self.remote_fixture()
        retired = self.work / 'monitoring/grafana/dashboards/02-chain.json'
        retired.symlink_to('02-requests.json')
        result = self.run_command(command, success=False)
        self.assertIn('Cannot archive unexpected file type:', result.stderr)
        self.assertTrue(retired.is_symlink())
        self.assertFalse(any(event[0] != 'inspect' for event in self.docker_events()))

    def test_remote_starts_enabled_profiles_and_initializes_snapshots_first(self):
        command = self.remote_fixture()
        (self.work / 'monitoring.env').write_text(
            'MONITORING_HOST_METRICS_ENABLED=true\nMONITORING_BACKUP_ENABLED=true\n')
        self.run_command(command)
        mutations = [event for event in self.docker_events()
                     if event[0] == 'compose' and ('up' in event or 'run' in event)]
        self.assertEqual(len(mutations), 4)
        self.assertEqual(mutations[1][mutations[1].index('run'):],
                         ['run', '--rm', '--no-deps', '-T', 'snapshots-init'])
        self.assertEqual(mutations[2][-3:], ['prometheus', 'alertmanager', 'node-exporter'])
        self.assertEqual(mutations[3][-2:], ['grafana', 'backup'])
        for event in mutations:
            self.assertIn('--no-deps', event)
            self.assertIn('host-metrics', event)
            self.assertIn('backup', event)

    def test_remote_app_only_needs_no_monitoring_env_or_db_role(self):
        self.run_command(self.remote_fixture('app-only'))
        ups = [event for event in self.docker_events() if event[0] == 'compose' and 'up' in event]
        self.assertEqual(len(ups), 1)
        self.assertEqual(ups[0][-1], 'sub2api')
        self.assertFalse((self.work / 'monitoring.env').exists())
        self.assertFalse(self.psql_capture.exists())

    def test_remote_app_only_recovers_missing_managed_env_for_existing_monitoring(self):
        command = self.remote_fixture('app-only')
        self.env['FAKE_CONFIG_FILES'] += ',' + str(self.work / 'docker-compose.monitoring.yml')
        self.env['FAKE_ENV_FILES'] = ','.join(str(self.work / file) for file in ['.env', 'monitoring.env'])
        self.run_command(command)
        self.assertTrue((self.work / 'monitoring.env').exists())
        self.assertEqual(self.config()['services']['sub2api']['environment']['DATABASE_PASSWORD'],
                         self.business_env.decode().splitlines()[0].split('=', 1)[1])
        self.assertTrue(self.psql_capture.exists())

    def test_db_role_uses_effective_compose_credential_only_on_stdin(self):
        self.install_docker_shim()
        env_file = self.work / 'monitoring.env'
        dotenv_credential = 'quoted DOTENV # not a comment $literal \\ test'
        env_file.write_text(f"GRAFANA_PG_PASSWORD='{dotenv_credential}'\n")
        self.configure('--monitoring')
        for expected in [dotenv_credential, "override with spaces 'quotes' \\ and $literal"]:
            with self.subTest(expected=expected):
                if expected != dotenv_credential:
                    self.env['GRAFANA_PG_PASSWORD'] = expected
                result = self.run_command([self.work / 'compose-deploy.sh', '--setup-grafana-db'])
                captured = json.loads(self.psql_capture.read_text())
                self.assertEqual(captured['credential'], expected)
                self.assertEqual(captured['args'], ['-X', '-v', 'ON_ERROR_STOP=1', '-U', 'fixture_user',
                                                     '-d', 'fixture_db'])
                sql = (self.work / 'monitoring/sql/grafana-ro.sql').read_text()
                self.assertEqual(captured['sql'], '\\getenv password GRAFANA_PG_PASSWORD\n' + sql)
                self.assertNotIn(expected, result.stdout + result.stderr + self.log.read_text())

    def test_db_role_rejects_multiline_credential_before_psql(self):
        self.install_docker_shim()
        self.configure('--monitoring')
        self.env['GRAFANA_PG_PASSWORD'] = 'fixture-with-trailing-newline\n'
        result = self.run_command([self.work / 'compose-deploy.sh', '--setup-grafana-db'], success=False)
        self.assertIn('must be configured and single-line', result.stderr)
        self.assertFalse(self.psql_capture.exists())

    def test_db_role_stdin_transport_with_real_psql(self):
        self.install_docker_shim()
        self.configure('--monitoring')
        self.env['GRAFANA_PG_PASSWORD'] = "  literal with 'quotes', \\backslash and $dollar  "
        sql = self.work / 'monitoring/sql/grafana-ro.sql'
        sql.write_text("SELECT :'password';\n")
        self.run_command([self.work / 'compose-deploy.sh', '--setup-grafana-db'])
        captured = json.loads(self.psql_capture.read_text())
        transport = next(event for event in self.docker_events()
                         if 'exec' in event and 'postgres' in event and 'sh' in event)
        shell = transport[transport.index('exec') + 3:]
        result = subprocess.run([DOCKER, 'run', '--rm', '-i', '--network', 'none',
                                 '--user', 'postgres', '--entrypoint', 'sh', 'postgres:18-alpine', '-ec',
                                 'export PGDATA="$(mktemp -d)"; initdb -A trust >/dev/null; '
                                 'pg_ctl -l "$PGDATA/server.log" -o "-c listen_addresses=" -w start >/dev/null; '
                                 'createuser fixture_user; createdb -O fixture_user fixture_db; exec "$@"',
                                 'fixture', *shell],
                                input=captured['credential'] + '\n' + captured['sql'],
                                text=True, capture_output=True, env=self.env)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn(self.env['GRAFANA_PG_PASSWORD'], result.stdout)

    def test_local_deploy_rebinds_rules_even_when_rendered_config_is_unchanged(self):
        self.install_docker_shim()
        self.configure('--monitoring')
        rendered = self.work / 'monitoring/prometheus.yml'
        inode = rendered.stat().st_ino
        rules = self.work / 'monitoring/prometheus/rules/slo.yml'
        replacement = rules.with_suffix('.tmp')
        replacement.write_text(rules.read_text() + '\n# replacement fixture\n')
        replacement.replace(rules)
        self.run_command([self.work / 'compose-deploy.sh', '--project-name', 'monitoring-regression'])
        self.assertEqual(rendered.stat().st_ino, inode)
        self.assertTrue(any('--force-recreate' in event and event[-1] == 'prometheus'
                            for event in self.docker_events()))

    def test_dev_wrapper_layers_env_and_enables_only_configured_profiles(self):
        self.install_docker_shim()
        for name in ['dev.sh', 'docker-compose.dev.yml']:
            shutil.copy2(DEPLOY / name, self.work / name)
        override = self.work / 'dev-monitoring.env'
        self.env['SUB2API_DEV_ENV_FILE'] = str(override)
        override.write_text('GRAFANA_ADMIN_PASSWORD=fixture\nMONITORING_BACKUP_ENABLED=true\n')
        self.run_command([self.work / 'dev.sh', 'config'])
        config = self.docker_events()[-1]
        self.assertEqual(config[1:5], ['--env-file', '.env', '--env-file', str(override)])
        self.assertEqual(config[config.index('--profile'):], ['--profile', 'backup', 'config', '-q'])

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
        renderer = work / 'monitoring/render-prometheus-config.sh'
        renderer.chmod(0o700)
        for directory in ('plugins', 'alerting'):
            optional = work / 'monitoring/grafana/provisioning' / directory
            if optional.exists():
                shutil.rmtree(optional)
        result = subprocess.run(['bash', '-c', 'umask 077; exec bash "$@"', 'prepare',
                                 str(work / 'compose-deploy.sh'), '--monitoring', '--no-start'],
                                env=env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(renderer.stat().st_mode & 0o777, 0o755)
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
        start = script.index('TAR_METADATA_FLAGS=()')
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
        if shutil.which('xattr'):
            subprocess.run(['xattr', '-w', 'com.sub2api.fixture', 'metadata',
                            str(deploy / 'monitoring/grafana/dashboards/01-overview.json')], check=True)
        env = dict(os.environ, LOCAL_DIR=str(self.root))
        result = subprocess.run(['bash', '-ec', command], cwd=self.root, env=env,
                                text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        with tarfile.open(self.root / 'monitoring-assets.tar.gz') as archive:
            names = archive.getnames()
            self.assertFalse(any('xattr' in key.lower() for member in archive for key in member.pax_headers))
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
                if re.search(r'\btar\s+.*--exclude', line):
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
