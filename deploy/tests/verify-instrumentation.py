#!/usr/bin/env python3
"""Auxiliary binary string check only: does NOT prove runtime instrumentation."""
import argparse
import mmap
from pathlib import Path
import sys

METRICS = ('sub2api_request_phase_seconds','sub2api_gateway_requests_total','sub2api_upstream_ttft_seconds')


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary',type=Path)
    args=parser.parse_args()
    print('仅辅助、不证明运行态：字符串存在不代表开关、埋点路径或样本落库正常。')
    with args.binary.open('rb') as stream:
        if args.binary.stat().st_size == 0:
            missing=list(METRICS)
        else:
            with mmap.mmap(stream.fileno(),0,access=mmap.ACCESS_READ) as binary:
                missing=[name for name in METRICS if binary.find(name.encode()) < 0]
    if missing:
        for name in missing:print('FAIL: missing V2 metric string: '+name)
        return 1
    print('PASS: '+str(args.binary)+' contains all '+str(len(METRICS))+' V2 metric strings (auxiliary only)')
    return 0


if __name__=='__main__':
    try:sys.exit(main())
    except OSError as error:
        print(f'FAIL: {error}',file=sys.stderr)
        sys.exit(1)
