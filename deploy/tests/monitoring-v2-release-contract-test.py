#!/usr/bin/env python3
"""Local release-contract tests: fixtures only; no push/deployment/database."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT=Path(__file__).resolve().parents[2]
VERIFY=ROOT/'deploy/tests/verify-migrations.py'
BINARY=ROOT/'deploy/tests/verify-instrumentation.py'
ROLLBACK=ROOT/'deploy/rollback.py'


def execute(*args,env=None):
    return subprocess.run([str(arg) for arg in args],text=True,capture_output=True,env=env)


class ReleaseContractTests(unittest.TestCase):
    def test_manifest_conflict_change_missing_added_recovery(self):
        with tempfile.TemporaryDirectory(prefix='w6-manifest-') as temp:
            directory=Path(temp);migrations=directory/'migrations';migrations.mkdir()
            (migrations/'001_first.sql').write_text('SELECT 1;\n')
            (migrations/'001_legacy.sql').write_text('SELECT 2;\n')
            manifest=directory/'manifest.json'
            command=['python3',VERIFY,'--migrations',migrations,'--manifest',manifest]
            result=execute(*command,'--write')
            self.assertNotEqual(result.returncode,0);self.assertFalse(manifest.exists())
            result=execute(*command,'--write','--acknowledge-existing-number-conflicts')
            self.assertEqual(result.returncode,0,result.stderr);self.assertIn('WARN: acknowledged number conflict 001',result.stdout)
            result=execute(*command);self.assertEqual(result.returncode,0);self.assertIn('WARN:',result.stdout)
            print('PASS: --write requires ack; acknowledged historical conflict is WARN; check passes',flush=True)
            (migrations/'001_new_conflict.sql').write_text('SELECT 3;')
            result=execute(*command);self.assertNotEqual(result.returncode,0);self.assertIn('NUMBER CONFLICT CHANGED: 001',result.stdout)
            (migrations/'001_new_conflict.sql').unlink()
            original=(migrations/'001_first.sql').read_bytes()
            (migrations/'001_first.sql').write_text('SELECT 999;')
            result=execute(*command);self.assertNotEqual(result.returncode,0);self.assertIn('CHANGED (same name, different content): 001_first.sql',result.stdout)
            (migrations/'001_first.sql').write_bytes(original)
            result=execute(*command);self.assertEqual(result.returncode,0)
            print('PASS: new/changed number conflict fails; same-name content mutation fails; restore passes',flush=True)
            (migrations/'001_first.sql').unlink()
            result=execute(*command);self.assertNotEqual(result.returncode,0);self.assertIn('MISSING: 001_first.sql',result.stdout)
            (migrations/'001_first.sql').write_bytes(original)
            (migrations/'002_added.sql').write_text('SELECT 4;')
            result=execute(*command);self.assertNotEqual(result.returncode,0);self.assertIn('ADDED: 002_added.sql',result.stdout)
            result=execute(*command,'--write','--acknowledge-existing-number-conflicts');self.assertEqual(result.returncode,0)
            result=execute(*command);self.assertEqual(result.returncode,0);self.assertIn('WARN:',result.stdout)
            data=json.loads(manifest.read_text());data['files'].append(data['files'][0])
            manifest.write_text(json.dumps(data));result=execute(*command)
            self.assertNotEqual(result.returncode,0);self.assertIn('duplicate manifest filename',result.stderr)
            print('PASS: missing/added files fail; explicit --write --ack rebaseline passes with WARN; duplicate manifest names fail',flush=True)

    def test_repository_copy_mutate_restore(self):
        with tempfile.TemporaryDirectory(prefix='w6-repo-copy-') as temp:
            directory=Path(temp);migrations=directory/'migrations';migrations.mkdir()
            for path in (ROOT/'backend/migrations').glob('*.sql'):shutil.copy2(path,migrations/path.name)
            manifest=directory/'manifest.json'
            command=['python3',VERIFY,'--migrations',migrations,'--manifest',manifest]
            result=execute(*command,'--write','--acknowledge-existing-number-conflicts');self.assertEqual(result.returncode,0,result.stderr)
            target=migrations/'252_add_usage_logs_v2_fields.sql';original=target.read_bytes()
            target.write_bytes(original+b'\n-- intentional W6 checksum conflict fixture\n')
            result=execute(*command);self.assertEqual(result.returncode,1);self.assertIn('CHANGED (same name, different content): 252_add_usage_logs_v2_fields.sql',result.stdout)
            target.write_bytes(original);result=execute(*command);self.assertEqual(result.returncode,0)
            # Match the migration runner: trim outer whitespace, but preserve
            # interior CRLF bytes instead of Python universal-newline folding.
            crlf=original.replace(b'\r\n',b'\n').replace(b'\n',b'\r\n')
            self.assertNotEqual(original,crlf)
            target.write_bytes(crlf)
            result=execute(*command);self.assertEqual(result.returncode,1)
            self.assertIn('CHANGED (same name, different content): 252_add_usage_logs_v2_fields.sql',result.stdout)
            target.write_bytes(original);result=execute(*command);self.assertEqual(result.returncode,0)
            print('PASS: LF -> CRLF changes runner checksum and fails verification; original bytes restored -> PASS',flush=True)
            self.assertIn('WARN: acknowledged number conflict 249:',result.stdout)
            print('PASS: repository SQL COPY baseline -> mutate 252 -> named checksum failure -> restore -> PASS+WARN (backend unchanged)',flush=True)

    def test_compiled_binary_strings(self):
        with tempfile.TemporaryDirectory(prefix='w6-binaries-') as temp:
            directory=Path(temp)
            positive=directory/'positive.c';negative=directory/'negative.c'
            positive.write_text('#include <stdio.h>\nint main(void){puts("sub2api_request_phase_seconds sub2api_gateway_requests_total sub2api_upstream_ttft_seconds");return 0;}\n')
            negative.write_text('#include <stdio.h>\nint main(void){puts("legacy binary fixture");return 0;}\n')
            for source in [positive,negative]:
                result=execute('cc',source,'-o',source.with_suffix(''));self.assertEqual(result.returncode,0,result.stderr)
            yes=execute('python3',BINARY,positive.with_suffix(''));no=execute('python3',BINARY,negative.with_suffix(''))
            self.assertEqual(yes.returncode,0);self.assertEqual(no.returncode,1)
            self.assertIn('仅辅助、不证明运行态',yes.stdout);self.assertIn('missing V2 metric string',no.stdout)
            print('PASS: real compiled binary with V2 strings accepted; compiled binary without V2 rejected (auxiliary only)',flush=True)

    def test_rollback_has_no_mutating_docker_calls(self):
        with tempfile.TemporaryDirectory(prefix='w6-rollback-') as temp:
            directory=Path(temp);capture=directory/'docker-calls.jsonl'
            docker=directory/'docker'
            docker.write_text('''#!/usr/bin/env python3
import json,os,sys
with open(os.environ['W6_DOCKER_CAPTURE'],'a') as f:f.write(json.dumps(sys.argv[1:])+'\\n')
if sys.argv[1:3] != ['image','inspect']:raise SystemExit('unexpected mutating Docker call')
print(json.dumps({'id':'sha256:'+'a'*64,'repo_tags':['fixture/app:before'],'repo_digests':['fixture/app@sha256:'+'b'*64]}))
''');docker.chmod(0o755)
            env=dict(os.environ,PATH=str(directory)+os.pathsep+os.environ['PATH'],W6_DOCKER_CAPTURE=str(capture))
            record=directory/'record.json'
            args=['python3',ROLLBACK,'--image','fixture/app:before','--record',record,'--dry-run']
            result=execute(*args,env=env);self.assertEqual(result.returncode,0,result.stderr)
            data=json.loads(record.read_text());self.assertFalse(data['tag_created']);self.assertTrue(data['dry_run'])
            self.assertIn(':rollback-',data['rollback_tag']);self.assertIn('sha256:'+'b'*64,data['repo_digests'][0])
            calls=[json.loads(line) for line in capture.read_text().splitlines()]
            self.assertEqual(len(calls),1);self.assertEqual(calls[0][:2],['image','inspect'])
            self.assertFalse(Path(data['override_path']).exists())
            self.assertIn('MONITORING_OPS_SAMPLES_ENABLED',result.stdout)
            original=record.read_bytes();again=execute(*args,env=env)
            self.assertNotEqual(again.returncode,0);self.assertEqual(original,record.read_bytes())
            print('PASS: rollback records tag/image ID/registry digest; only docker image inspect executed; planned tag/config/up never executed; immutable record not overwritten',flush=True)


if __name__=='__main__':unittest.main(verbosity=2)
