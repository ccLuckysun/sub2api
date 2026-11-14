#!/usr/bin/env python3
"""Boot an isolated five-container stack with an already-built application image.

Usage: python3 deploy/tests/monitoring-stack-smoke.py --image IMAGE:TAG [--extensions]
Creates only a new project and removes its containers/volumes after verification.
Never connects to a remote server or reads production credentials.
"""
import argparse
import base64
import json
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.parse
import urllib.request

REPO = Path(__file__).resolve().parents[2]


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True, help='Built image containing application metrics')
    parser.add_argument('--extensions', action='store_true', help='Also verify host metrics and snapshot backup')
    args = parser.parse_args()
    project = 'sub2api-smoke-' + secrets.token_hex(4)
    with tempfile.TemporaryDirectory(prefix=project + '-', dir='/tmp') as directory:
        work = Path(directory)
        base = (REPO / 'deploy/docker-compose.local.yml').read_text()
        for name in ['sub2api-postgres', 'sub2api-redis', 'sub2api']:
            base = base.replace('container_name: ' + name + '\n',
                                'container_name: ' + project + '-' + name + '\n')
        (work / 'docker-compose.yml').write_text('name: ' + project + '\n' + base)
        (work / '.env').write_text(
            'POSTGRES_PASSWORD=' + secrets.token_hex(20) + '\nBIND_HOST=127.0.0.1\n'
            'SERVER_PORT=' + str(free_port()) + '\nADMIN_EMAIL=smoke@example.test\n'
            'ADMIN_PASSWORD=' + secrets.token_hex(20) + '\nJWT_SECRET=' + secrets.token_hex(32) + '\n'
            'TOTP_ENCRYPTION_KEY=' + secrets.token_hex(32) + '\n')
        grafana_port, grafana_password = free_port(), secrets.token_hex(20)
        (work / 'monitoring.env').write_text(
            'GRAFANA_ADMIN_PASSWORD=' + grafana_password + '\nGRAFANA_PORT=' + str(grafana_port) + '\n'
            'PROM_SCRAPE_INTERVAL=2s\nPROM_SCRAPE_TIMEOUT=1s\n')
        wrapper = [str(work / 'sub2api-compose')]

        def run(command, check=True):
            result = subprocess.run([str(part) for part in command], cwd=work, text=True, capture_output=True)
            if check and result.returncode:
                raise RuntimeError(result.stdout + '\n' + result.stderr)
            return result

        def query(expression):
            url = 'http://localhost:9090/api/v1/query?' + urllib.parse.urlencode({'query': expression})
            result = run(wrapper + ['exec', '-T', 'prometheus', 'wget', '-qO-', url])
            return json.loads(result.stdout)['data']['result']

        def wait_for_targets(count):
            for _ in range(30):
                targets = query('up')
                if len(targets) == count and all(item['value'][1] == '1' for item in targets):
                    return
                time.sleep(1)
            raise AssertionError('Scrape targets did not become ready: ' + str(targets))

        try:
            # Exercise the public installer with a genuinely new, private config
            # tree (umask 077), not pre-copied world-readable fixture directories.
            run(['bash', REPO / 'deploy/docker-deploy.sh', '--monitoring', '--image', args.image])
            running = run(wrapper + ['ps', '--format', '{{.Service}}']).stdout.split()
            assert set(running) == {'sub2api', 'postgres', 'redis', 'grafana', 'prometheus'}, running
            wait_for_targets(2)
            credential = base64.b64encode(('admin:' + grafana_password).encode()).decode()
            request = urllib.request.Request(
                f'http://127.0.0.1:{grafana_port}/api/dashboards/uid/sub2api-overview',
                headers={'Authorization': 'Basic ' + credential})
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            with opener.open(request, timeout=10) as response:
                dashboard = json.load(response)
            assert len(dashboard['dashboard']['panels']) == 16
            print('PASS: public installer started exactly five containers; both targets up; Grafana provisioned.', flush=True)

            if args.extensions:
                run(['bash', work / 'compose-deploy.sh', '--monitoring', '--host-metrics', '--backup'])
                wait_for_targets(3)
                host_text = run(wrapper + ['exec', '-T', 'node-exporter', 'cat', '/host/proc/1/net/dev']).stdout
                host_counters = {}
                for line in host_text.splitlines():
                    if ':' in line:
                        device, values = line.split(':', 1)
                        device = device.strip()
                        if not device.startswith(('lo', 'veth', 'docker', 'br-')):
                            host_counters[device] = int(values.split()[0])
                device = max(host_counters, key=host_counters.get)
                samples = query('node_network_receive_bytes_total')
                actual = next(float(sample['value'][1]) for sample in samples
                              if sample['metric']['device'] == device)
                expected = host_counters[device]
                assert abs(actual - expected) <= max(expected * 0.05, 100000), (device, actual, expected)
                run(wrapper + ['exec', '-T', 'backup', '/usr/local/bin/snapshot.sh'])
                assert list((work / 'monitoring/backup-data').glob('*.tar.gz'))
                print(f'PASS: host counter {device}={actual:.0f}, reference={expected}; snapshot archive created.', flush=True)
        finally:
            if (work / 'sub2api-compose').exists():
                result = run(wrapper + ['down', '-v', '--remove-orphans'], check=False)
                if result.returncode:
                    raise RuntimeError('Isolated project cleanup failed: ' + result.stderr)
                print('PASS: isolated containers and volumes removed.', flush=True)


if __name__ == '__main__':
    main()
