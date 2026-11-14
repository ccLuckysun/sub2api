#!/usr/bin/env python3
"""Deployment regression tests. Requires Docker + Compose; never contacts SSH.

Real Compose merging and promtool validate configurations. A Docker shim intercepts
service mutations, image loading and pg_dump for remote-deployment failure tests.
"""
import json
import os
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
    'monitoring/grafana/provisioning', 'monitoring/grafana/dashboards',
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
        self.assertEqual(set(config['services']), {'sub2api', 'postgres', 'redis', 'grafana', 'prometheus'})
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
        self.assertEqual(len(self.config()['services']), 5)

    def test_preparation_script_preserves_existing_and_partial_business_configuration(self):
        # Exercise the user-facing one-click entry, including a partially prepared install.
        (self.work / 'docker-compose.yml').unlink()
        shutil.rmtree(self.work / 'monitoring')
        self.run_command(['bash', DEPLOY / 'docker-deploy.sh', '--monitoring', '--no-start',
                          '--image', 'example/sub2api:1.0.1'], cwd=self.work)
        self.assertEqual((self.work / '.env').read_bytes(), self.business_env)
        self.assertEqual(len(self.config()['services']), 5)
        self.assertEqual((self.work / 'monitoring/grafana/provisioning').stat().st_mode & 0o777, 0o755)
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


if __name__ == '__main__':
    unittest.main(verbosity=2)
