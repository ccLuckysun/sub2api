#!/usr/bin/env python3
"""Verify the immutable migration inventory without connecting to a database.
Checksums match the runner: SHA256 of UTF-8 SQL after surrounding whitespace trim.
"""
import argparse
from collections import defaultdict
import hashlib
import json
from pathlib import Path
import re
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
VERSION = re.compile(r'(?P<version>[0-9]{3,}[a-z]?)_[a-z0-9_]+\.sql\Z')
SHA = re.compile(r'[0-9a-f]{64}\Z')


def inventory(directory):
    if not directory.is_dir():
        raise ValueError(f'migration directory missing: {directory}')
    files = sorted(directory.glob('*.sql'))
    if not files:
        raise ValueError(f'no SQL migrations: {directory}')
    entries = []
    groups = defaultdict(list)
    for path in files:
        match = VERSION.fullmatch(path.name)
        if not match or not path.is_file():
            raise ValueError(f'invalid migration filename: {path.name}')
        version = match['version']
        entries.append({'filename': path.name, 'version': version,
                        'sha256': hashlib.sha256(path.read_bytes().decode('utf-8').strip().encode('utf-8')).hexdigest()})
        groups[version].append(path.name)
    conflicts = {key: names for key, names in sorted(groups.items()) if len(names) > 1}
    return {'format_version': 1, 'checksum_algorithm': 'sha256-trimmed-utf8',
            'files': entries, 'acknowledged_number_conflicts': conflicts}


def read_manifest(path):
    data = json.loads(path.read_text(encoding='utf-8'))
    if data.get('format_version') != 1 or data.get('checksum_algorithm') != 'sha256-trimmed-utf8':
        raise ValueError('unsupported manifest format/checksum algorithm')
    names = set()
    groups = defaultdict(list)
    for entry in data['files']:
        name = entry['filename']
        match = VERSION.fullmatch(name)
        if name in names:
            raise ValueError(f'duplicate manifest filename (same-name conflict): {name}')
        if not match or entry['version'] != match['version'] or not SHA.fullmatch(entry['sha256']):
            raise ValueError(f'invalid manifest entry: {name}')
        names.add(name)
        groups[match['version']].append(name)
    if not names:
        raise ValueError('empty manifest')
    expected = {key: sorted(values) for key, values in groups.items() if len(values) > 1}
    if data.get('acknowledged_number_conflicts') != expected:
        raise ValueError('manifest conflict acknowledgments do not match full file groups')
    return data


def write_manifest(path, data):
    # An explicit --write is the only baseline mutation path. Atomic replacement
    # avoids publishing a partially written inventory to concurrent checkers.
    with tempfile.NamedTemporaryFile(mode='w',encoding='utf-8',dir=path.parent,
                                     prefix=path.name+'.',suffix='.tmp',delete=False) as temp:
        staged = Path(temp.name)
        try:
            json.dump(data,temp,ensure_ascii=False,indent=2)
            temp.write('\n')
        except BaseException:
            staged.unlink(missing_ok=True)
            raise
    try:
        staged.chmod(0o644)
        staged.replace(path)
    finally:
        staged.unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--migrations',type=Path,default=ROOT/'backend/migrations')
    parser.add_argument('--manifest',type=Path,default=ROOT/'deploy/migrations.manifest.json')
    parser.add_argument('--write',action='store_true',help='explicitly write/update the baseline')
    parser.add_argument('--acknowledge-existing-number-conflicts',action='store_true')
    args = parser.parse_args()
    if args.acknowledge_existing_number_conflicts and not args.write:
        parser.error('--acknowledge-existing-number-conflicts is only valid with --write')
    actual = inventory(args.migrations)
    if args.write:
        if actual['acknowledged_number_conflicts'] and not args.acknowledge_existing_number_conflicts:
            for version,names in actual['acknowledged_number_conflicts'].items():
                print(f'FAIL: unacknowledged number conflict {version}: {", ".join(names)}')
            raise ValueError('--write requires --acknowledge-existing-number-conflicts for this inventory')
        write_manifest(args.manifest,actual)
        baseline = actual
        print(f'WROTE: {args.manifest}')
    else:
        baseline = read_manifest(args.manifest)
    before = {entry['filename']:entry for entry in baseline['files']}
    after = {entry['filename']:entry for entry in actual['files']}
    differences = []
    for name in sorted(before.keys()-after.keys()):
        differences.append(f'MISSING: {name}')
    for name in sorted(after.keys()-before.keys()):
        differences.append(f'ADDED: {name}')
    for name in sorted(before.keys()&after.keys()):
        if before[name]['sha256'] != after[name]['sha256']:
            differences.append(f'CHANGED (same name, different content): {name} baseline={before[name]["sha256"]} actual={after[name]["sha256"]}')
    known = baseline['acknowledged_number_conflicts']
    current = actual['acknowledged_number_conflicts']
    for version in sorted(known.keys()|current.keys()):
        if known.get(version) != current.get(version):
            differences.append(f'NUMBER CONFLICT CHANGED: {version} baseline={known.get(version,[])} actual={current.get(version,[])}')
        else:
            print(f'WARN: acknowledged number conflict {version}: {", ".join(known[version])}')
    if differences:
        for difference in differences:
            print('FAIL: '+difference)
        return 1
    versions = [int(re.match(r'\d+',entry['version'])[0]) for entry in actual['files']]
    print(f'PASS: {len(after)} migrations match; maximum numeric version={max(versions)}; acknowledged conflict groups={len(known)}')
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except (OSError,ValueError,KeyError,TypeError) as error:
        print(f'FAIL: {error}',file=sys.stderr)
        sys.exit(1)
