#!/usr/bin/env python3
"""Exercise the public installer in a disposable Compose project using a local image."""

import argparse
import base64
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tarfile
import tempfile
import time
import urllib.parse
import urllib.request

REPO = Path(__file__).resolve().parents[2]
CORE = {'sub2api', 'postgres', 'redis', 'grafana', 'prometheus', 'alertmanager'}


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True, help='Already-built local image with V2 metrics')
    parser.add_argument('--extensions', action='store_true', help='Also verify Linux host metrics and snapshot backup')
    args = parser.parse_args()
    endpoint = os.environ.get('DOCKER_HOST') if not os.environ.get('DOCKER_CONTEXT') else None
    if not endpoint:
        context = json.loads(subprocess.check_output(['docker', 'context', 'inspect'], text=True))[0]
        endpoint = context['Endpoints']['docker']['Host']
    if not endpoint.startswith(('unix://', 'npipe://')):
        parser.error('a local Docker endpoint is required')
    subprocess.run(['docker', 'image', 'inspect', args.image], stdout=subprocess.DEVNULL, check=True)

    project = 'sub2api-smoke-' + secrets.token_hex(4)
    with tempfile.TemporaryDirectory(prefix=project + '-', dir='/tmp') as directory:
        work = Path(directory)
        work.chmod(0o755)
        base = (REPO / 'deploy/docker-compose.local.yml').read_text()
        for name in ('sub2api-postgres', 'sub2api-redis', 'sub2api'):
            base = base.replace('container_name: ' + name + '\n', 'container_name: ' + project + '-' + name + '\n')
        for source, target in (('data', '/app/data'), ('postgres_data', '/var/lib/postgresql/data'), ('redis_data', '/data')):
            base = base.replace(f'./{source}:{target}:Z', f'{source}:{target}')
        base += '\nvolumes:\n  data: {}\n  postgres_data: {}\n  redis_data: {}\n'
        (work / 'docker-compose.yml').write_text('name: ' + project + '\n' + base)
        grafana_port, admin_secret = free_port(), secrets.token_hex(24)
        (work / 'monitoring.env').write_text(
            f'GRAFANA_ADMIN_PASSWORD={admin_secret}\nGRAFANA_PORT={grafana_port}\n'
            'PROM_SCRAPE_INTERVAL=2s\nPROM_SCRAPE_TIMEOUT=1s\nALERT_SMTP_TO=\n')
        env = {key: value for key, value in os.environ.items() if not key.startswith((
            'COMPOSE_', 'POSTGRES_', 'MONITORING_', 'GRAFANA_', 'ALERT_SMTP_', 'PROM_'))}
        env.update(BIND_HOST='127.0.0.1', SERVER_PORT=str(free_port()), POSTGRES_DB='sub2api_smoke',
                   POSTGRES_SHARED_BUFFERS='128MB', COPYFILE_DISABLE='1')
        wrapper = work / 'sub2api-compose'

        def run(command):
            result = subprocess.run([str(part) for part in command], cwd=work, env=env, text=True, capture_output=True)
            if result.returncode:
                raise RuntimeError(result.stdout + '\n' + result.stderr)
            return result.stdout

        def compose(*command):
            return run([wrapper, *command])

        def query(expression):
            url = 'http://localhost:9090/api/v1/query?' + urllib.parse.urlencode({'query': expression})
            return json.loads(compose('exec', '-T', 'prometheus', 'wget', '-qO-', url))['data']['result']

        def wait_for_targets(expected):
            for _ in range(30):
                targets = {sample['metric']['job']: sample['value'][1] for sample in query('up')}
                if targets == dict.fromkeys(expected, '1'):
                    return
                time.sleep(1)
            raise AssertionError('Scrape targets did not become ready: ' + str(targets))

        def verify_remote_upgrade():
            version = '0.0.1'
            release = project + ':' + version
            postgres_id = compose('ps', '-q', 'postgres').strip()
            redis_id = compose('ps', '-q', 'redis').strip()
            compose('stop', 'grafana', 'prometheus', 'alertmanager')
            compose('rm', '-f', 'grafana', 'prometheus', 'alertmanager')
            run(['docker', 'compose', '-p', project, '-f', 'docker-compose.yml', '-f', 'docker-compose.release.yml',
                'up', '-d', '--no-deps', '--wait', '--force-recreate', 'sub2api'])
            saved_env = work / 'saved-monitoring.env'
            (work / 'monitoring.env').rename(saved_env)
            wrapper.unlink()
            env['SUB2API_BACKUP_DIR'] = str(work)
            run(['docker', 'tag', args.image, release])
            try:
                for mode in ('app-only', 'monitoring'):
                    with tempfile.TemporaryDirectory(prefix='upload-', dir=work) as upload:
                        stage = Path(upload)
                        run(['docker', 'save', '-o', stage / 'image.tar.gz', release])
                        with tarfile.open(stage / 'monitoring-assets.tar.gz', 'w:gz') as archive:
                            for asset in ('compose-deploy.sh', 'docker-compose.monitoring.yml', 'monitoring.env.example',
                                          'monitoring/render-prometheus-config.sh', 'monitoring/prometheus.yml.tmpl',
                                          'monitoring/prometheus/rules/chain.yml', 'monitoring/prometheus/rules/infra.yml',
                                          'monitoring/prometheus/rules/slo.yml', 'monitoring/alertmanager',
                                          'monitoring/backup/snapshot.sh', 'monitoring/backup/crontab.example',
                                          'monitoring/grafana/dashboard-preflight.sh', 'monitoring/grafana/provisioning',
                                          'monitoring/grafana/dashboards'):
                                archive.add(REPO / 'deploy' / asset, arcname=asset)
                        if mode == 'monitoring':
                            saved_env.rename(work / 'monitoring.env')
                        run(['bash', REPO / 'deploy/remote-deploy.sh', version, stage, mode,
                             project + '-sub2api', project + '-sub2api-postgres', project])
                        expected = {'sub2api', 'postgres', 'redis'} if mode == 'app-only' else CORE
                        services = set(compose('ps', '--status', 'running', '--services').split())
                        assert services == expected, services
                        assert compose('ps', '-q', 'postgres').strip() == postgres_id
                        assert compose('ps', '-q', 'redis').strip() == redis_id
                        if mode == 'app-only':
                            assert not (work / 'monitoring.env').exists()
                wait_for_targets({'sub2api', 'prometheus', 'alertmanager'})
                print('PASS: real app-only and monitored upgrades preserve PostgreSQL/Redis and start the correct services.', flush=True)
            finally:
                if saved_env.exists():
                    saved_env.rename(work / 'monitoring.env')
                tags = run(['docker', 'image', 'ls', project, '--format', '{{.Repository}}:{{.Tag}}']).split()
                if tags:
                    run(['docker', 'image', 'rm', *tags])

        credential = base64.b64encode(('admin:' + admin_secret).encode()).decode()
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

        def grafana(path, data=None):
            request = urllib.request.Request(f'http://127.0.0.1:{grafana_port}' + path,
                data=json.dumps(data).encode() if data is not None else None,
                headers={'Authorization': 'Basic ' + credential, 'Content-Type': 'application/json'})
            with opener.open(request, timeout=15) as response:
                return json.load(response)

        try:
            run(['bash', REPO / 'deploy/docker-deploy.sh', '--monitoring', '--image', args.image])
            running = set(compose('ps', '--status', 'running', '--services').split())
            assert running == CORE, running
            assert (work / 'monitoring/render-prometheus-config.sh').stat().st_mode & 0o004
            wait_for_targets({'sub2api', 'prometheus', 'alertmanager'})
            dashboards = grafana('/api/search?type=dash-db')
            assert {item['uid'] for item in dashboards} == {
                'sub2api-v2-' + name for name in ('overview', 'nodes-chain', 'host')}
            datasources = grafana('/api/datasources')
            assert {item['uid'] for item in datasources} == {'sub2api-prometheus'}, datasources
            assert not any(item['type'] == 'postgres' for item in datasources), datasources
            print('PASS: installer starts six services; all three scrapes UP; three pure-Prometheus dashboards work.', flush=True)
            verify_remote_upgrade()

            if args.extensions:
                run(['bash', work / 'compose-deploy.sh', '--monitoring', '--host-metrics', '--backup'])
                wait_for_targets({'sub2api', 'prometheus', 'alertmanager', 'node'})
                host_counters = {}
                for line in compose('exec', '-T', 'node-exporter', 'cat', '/host/proc/1/net/dev').splitlines():
                    if ':' in line:
                        device, counters = line.split(':', 1)
                        device = device.strip()
                        if not device.startswith(('lo', 'veth', 'docker', 'br-')):
                            host_counters[device] = int(counters.split()[0])
                device = max(host_counters, key=host_counters.get)
                sample = next(item for item in query('node_network_receive_bytes_total') if item['metric']['device'] == device)
                actual = float(sample['value'][1])
                expected = host_counters[device]
                assert abs(actual - expected) <= max(expected * 0.05, 100000), (device, actual, expected)
                compose('exec', '-T', 'backup', '/usr/local/bin/snapshot.sh')
                assert list((work / 'monitoring/backup-data').glob('*.tar.gz'))
                print(f'PASS: host counter {device}={actual:.0f}, reference={expected}; snapshot archive created.', flush=True)
        finally:
            if wrapper.exists():
                compose('down', '-v', '--remove-orphans')
            else:
                run(['docker', 'compose', '-p', project, '-f', 'docker-compose.yml',
                     'down', '-v', '--remove-orphans'])
            remaining = run(['docker', 'volume', 'ls', '-q', '--filter', 'label=com.docker.compose.project=' + project]).split()
            if remaining:
                run(['docker', 'volume', 'rm', *remaining])
            print('PASS: isolated containers and volumes removed.', flush=True)


if __name__ == '__main__':
    main()
