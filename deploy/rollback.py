#!/usr/bin/env python3
"""Record local image identity and print a rollback plan. Dry-run only.
Never tags, pushes, starts/stops services or connects to a remote host.
"""
import argparse
from datetime import datetime,timezone
import json
from pathlib import Path
import re
import shlex
import subprocess
import sys

ROOT=Path(__file__).resolve().parent


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image',required=True,help='local image to preserve BEFORE deployment')
    parser.add_argument('--record',type=Path,required=True,help='new immutable JSON record path')
    parser.add_argument('--compose-file',type=Path,action='append',help='base/overlay files in deployment order')
    parser.add_argument('--project-name',help='existing Compose project name')
    parser.add_argument('--env-file',type=Path,action='append',help='environment files in deployment order')
    parser.add_argument('--dry-run',action='store_true',required=True)
    args=parser.parse_args()
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._/:@-]*',args.image):
        raise ValueError('invalid local image reference')
    template='{"id":{{json .Id}},"repo_tags":{{json .RepoTags}},"repo_digests":{{json .RepoDigests}}}'
    inspected=subprocess.run(['docker','image','inspect','--format',template,args.image],check=True,text=True,capture_output=True)
    image=json.loads(inspected.stdout)
    if not re.fullmatch(r'sha256:[0-9a-f]{64}',image['id']):
        raise ValueError('unexpected local image ID')
    repo=args.image.split('@',1)[0]
    if ':' in repo.rsplit('/',1)[-1]:repo=repo.rsplit(':',1)[0]
    timestamp=datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S%fZ')
    rollback_tag=repo+':rollback-'+timestamp
    record_path=args.record.resolve()
    override=record_path.with_name(record_path.stem+'.override.json')
    compose=['docker','compose']
    if args.project_name:compose+=['-p',args.project_name]
    for path in args.env_file or []:compose+=['--env-file',str(path.resolve())]
    for path in args.compose_file or [ROOT/'docker-compose.yml',ROOT/'docker-compose.monitoring.yml']:
        if not path.is_file():raise ValueError('compose file missing: '+str(path))
        compose+=['-f',str(path.resolve())]
    compose+=['-f',str(override)]
    override_content={'services':{'sub2api':{'image':rollback_tag,'environment':{
        'MONITORING_METRICS_ENABLED':'true','MONITORING_OPS_METRICS_ENABLED':'false','MONITORING_OPS_SAMPLES_ENABLED':'false'}}}}
    commands=[['docker','image','tag',image['id'],rollback_tag],compose+['config','--quiet'],compose+['up','-d','--no-deps','sub2api']]
    record={'format_version':1,'recorded_at_utc':timestamp,'dry_run':True,'source_reference':args.image,
            'image_id':image['id'],'repo_tags':image.get('repo_tags') or [],'repo_digests':image.get('repo_digests') or [],
            'rollback_tag':rollback_tag,'tag_created':False,'override_path':str(override),
            'override_content':override_content,'planned_commands':commands}
    # Exclusive creation preserves prior release evidence instead of overwriting it.
    with record_path.open('x',encoding='utf-8') as stream:
        json.dump(record,stream,ensure_ascii=False,indent=2);stream.write('\n')
    print('DRY-RUN ONLY: no tag/push/deploy commands executed')
    print('RECORDED: '+str(record_path))
    print('CURRENT REFERENCE: '+args.image)
    print('LOCAL IMAGE ID: '+image['id'])
    print('REGISTRY DIGESTS: '+(', '.join(record['repo_digests']) or 'unavailable (local image; image ID is not a registry digest)'))
    print('PLANNED ROLLBACK TAG: '+rollback_tag+' (not created)')
    print('PLANNED OVERRIDE FILE (not written): '+str(override))
    print(json.dumps(override_content,ensure_ascii=False,indent=2))
    for command in commands:print('PLAN: '+shlex.join(command))
    print('CHECK AFTER A SEPARATELY AUTHORIZED ROLLBACK: V1 /metrics works; sample rows stop advancing; V2 dashboards show instrumentation disabled/uncollected.')
    return 0


if __name__=='__main__':
    try:sys.exit(main())
    except (OSError,ValueError,KeyError,subprocess.CalledProcessError) as error:
        # Never echo captured Docker output/environment on an error.
        print(f'FAIL: {error}',file=sys.stderr)
        sys.exit(1)
