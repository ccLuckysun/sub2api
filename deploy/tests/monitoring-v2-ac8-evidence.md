# W8 / AC-8 final-image rerun — 2026-11-15

**Drill 1: PASS; Drill 2: NOT EXECUTED; full AC-8: PARTIAL.**
The final-image command exited **0**, including automatic cleanup. The older
run and its failures are retained below as historical evidence.

## Final image and command

```sh
docker image inspect sub2api-v2smoke:worktree-final-1794723477 \
  --format '{{.Id}} {{.Architecture}}'
python3 deploy/tests/monitoring-v2-ac8-drill.py \
  --image sub2api-v2smoke:worktree-final-1794723477 \
  > /tmp/ac8-final-image-run.log 2>&1
```

```text
sha256:388802b295a69a8cee6c50cac65b1e0e9bdfdb1b74743e2299d4ef5f74f96053 amd64
```

Both recreated application containers were inspected and matched that exact
image ID. No rebuild or push was performed. Project:
`sub2api-v2smoke-598a172c`.

After the initial fixture smoke check, the drill began with
`MONITORING_METRICS_ENABLED=true`, `MONITORING_OPS_METRICS_ENABLED=false` and
`MONITORING_OPS_SAMPLES_ENABLED=false`. Samples must be disabled with V2 for this
image's configuration validation. Fresh Prometheus storage was started only
after the disabled application became ready. Restoration enabled both V2 and
samples while preserving the image and Prometheus TSDB.

The copied alert rule was compared to the repository source and matched all
fields exactly, including expression, labels, annotations and `for: 1m`.
The original 3m windows and 15s scrape/evaluation intervals were retained.
Gateway requests traversed the application to an isolated mock upstream and
returned HTTP 200. No Alertmanager or notification receiver was configured;
no production service, mail or webhook was contacted.

## Final-image timeline (UTC)

| Time | Observation |
| --- | --- |
| 06:21:38.395 | Exact final image ready; V2 disabled, V1 enabled |
| 06:21:38.845 | Pre-traffic idle observation begins |
| 06:23:23.643 | Idle passed, no pending/firing; controlled gateway traffic begins |
| 06:24:08.441 | First observed pending; V1 increase >0, phase absent |
| 06:25:09.232 | First observed firing: **105.588s** after traffic begins, <300s |
| 06:25:09.232 | V2 restoration begins |
| 06:25:11.629 | Same image ready with V2 enabled |
| 06:25:50.800 | Resolved: ALERTS/expression empty, phase increase >0; **41.568s** after restoration begins |
| 06:25:50.800 | Stop gateway requests; recovered idle observation begins |
| 06:29:20.500 | Final idle snapshot: V1 increase=0, phase increase=0, ALERTS/expression empty |

Pre-traffic idle lasted **104.798s**. Recovered idle lasted **209.700s**,
exceeding the full 3m window; every idle snapshot had no pending/firing alert.
The firing/recovery durations are polling observations, not exact Prometheus
evaluation timestamps. Startup and pre-traffic idle are outside the firing
measurement, which begins at controlled disabled gateway flow.

## Final-image raw Prometheus ALERTS

Query: `ALERTS{alertname="Sub2apiInstrumentationStalled"}`.
The following bodies are copied unchanged from the recorded HTTP responses.

Firing, `ac8-117-disabled-flow-ALERTS.json`:

```json
{"status":"success","data":{"resultType":"vector","result":[{"metric":{"__name__":"ALERTS","alertname":"Sub2apiInstrumentationStalled","alertstate":"firing","instance":"sub2api:9091","severity":"critical"},"value":[1794723908.954,"1"]}]}}
```

Recovery, `ac8-149-recovery-flow-ALERTS.json`:

```json
{"status":"success","data":{"resultType":"vector","result":[]}}
```

Both pre-traffic idle (`ac8-001-idle-ALERTS.json`) and final idle
(`ac8-233-idle-window-expired-ALERTS.json`) returned:

```json
{"status":"success","data":{"resultType":"vector","result":[]}}
```

Resolved means the previously firing series disappeared, the expression became
empty and phase growth resumed. Prometheus ALERTS does not emit a `resolved`
row. Notification delivery was not exercised.

## Final-image output and cleanup

```text
CHECKS PASS: HTTP/mock/usage; concrete phase/account/result labels; exact PG sample=1; M9 request>0
RESULT: PASS TEMP_CONFIG_REMOVED: True
EVIDENCE: /tmp/sub2api-v2smoke-598a172c-evidence
process exit: 0
report.status: PASS
report.cleanup: true
report.temporary_config_removed: true
```

After script exit, independent Docker label queries for this project returned
zero containers, volumes and networks:

```json
{"project":"sub2api-v2smoke-598a172c","status":"PASS","resources":{"containers":"","volumes":"","networks":""}}
```

Raw responses, full timeline, copied rule/config, report and cleanup evidence:
`/tmp/sub2api-v2smoke-598a172c-evidence/`. In particular,
`ac8-prom-cleanup.log` records successful removal of the optional Prometheus
container and volume, and `cleanup-verification.json` records the independent
zero-resource check. Console output: `/tmp/ac8-final-image-run.log`.
These are local ephemeral paths; the raw ALERTS bodies are also preserved above.
Python AST parsing and the targeted `git diff --check` passed.

**Drill 2 remains NOT EXECUTED:** the unchanged image has no stable mechanism
to keep phase series present while freezing their growth. V2 disablement
removes series and only establishes Drill 1; it does not establish Drill 2.
No fake counters or scrape manipulation were substituted. Full AC-8 therefore
remains PARTIAL.

---

# Historical earlier-image run — 2026-11-15

Drill 1 alert/restore/idle checks: **PASS**. Drill 2: **NOT EXECUTED**.
No production connections, Alertmanager, SMTP, mail, or webhooks in this stack.
Only deploy/tests files were changed. No commit.

## Command and scope

```sh
python3 deploy/tests/monitoring-v2-ac8-drill.py \
  --image sub2api-v2smoke:worktree-1794716252
```

Same image ID before/after toggling:

```text
sha256:8794fde5080d079221edadf68d422f1d1d6b5e328ae070b35ce730c6a444fd0a
```

The independent script imports the existing smoke fixture unchanged. Initial
enabled smoke verifies a real gateway request, mock upstream, usage and exact PG
sample. It then recreates the same image with:

```text
MONITORING_METRICS_ENABLED=true
MONITORING_OPS_METRICS_ENABLED=false
MONITORING_OPS_SAMPLES_ENABLED=false
```

The image requires samples disabled with V2; V1 metrics stay enabled. A fresh
isolated Prometheus starts only after the disabled application is ready, so
previous enabled phase samples cannot mask the missing-series exercise. All
gateway requests go through the application to the local mock upstream.
No counters are synthesized and the application binary is not changed.

Only `Sub2apiInstrumentationStalled` is copied from the repository's chain.yml.
The expression, `for: 1m`, 3m increase windows and annotations are unchanged.
Scrape and evaluation are both 15s. Prometheus has no alerting receiver config.
All services use a fresh internal Docker network, no published host ports or
host data volumes.

## Timeline (UTC, observed via Prometheus API)

| Time | Event / observation |
| --- | --- |
| 05:22:32.088 | Same image ready, V2 disabled, V1 metrics enabled |
| 05:22:32.370 | Pre-traffic idle observation starts |
| 05:24:17.021 | Idle window complete without pending/firing; disabled gateway traffic starts |
| 05:24:39.806 | First observed pending |
| 05:25:35.082 | First observed firing, **78.060s** after traffic start, ≤300s |
| 05:25:35.082 | Begin restoring V2 and samples |
| 05:25:36.824 | Same image ID ready with V2 enabled |
| 05:26:04.891 | ALERTS empty, rule expression empty, phase increase >0: **29.809s** after restore begins |
| 05:26:04.891 | Stop gateway traffic; recovered idle observation starts |
| 05:29:34.142 | >200s idle: V1 increase=0, phase increase=0, expression/ALERTS empty |

Pre-traffic idle observation lasted approximately 104.65s (>for plus two
evaluation ticks); post-recovery idle lasted approximately 209.25s (>3m window).
Snapshots were taken about every 10s during idle and about every 5s during flow.
The 78.060s and 29.809s values include API polling, not exact rule evaluation
timestamps. Startup and pre-traffic idle are not subtracted from a delayed
firing result; firing is measured from the first controlled disabled flow.

## Raw Prometheus ALERTS responses

Query:

```promql
ALERTS{alertname="Sub2apiInstrumentationStalled"}
```

Firing (`ac8-097-disabled-flow-ALERTS.json`, unchanged raw response):

```json
{"status":"success","data":{"resultType":"vector","result":[{"metric":{"__name__":"ALERTS","alertname":"Sub2apiInstrumentationStalled","alertstate":"firing","instance":"sub2api:9091","severity":"critical"},"value":[1794720334.805,"1"]}]}}
```

Recovery (`ac8-121-recovery-flow-ALERTS.json`, unchanged raw response):

```json
{"status":"success","data":{"resultType":"vector","result":[]}}
```

Pre-traffic idle (`ac8-001-idle-ALERTS.json`) and final idle
(`ac8-205-idle-window-expired-ALERTS.json`) have the same empty response.
Prometheus ALERTS has pending/firing states, not a `resolved` row. Here resolved
means the previously firing alert is absent, its expression is empty and real
phase growth has resumed. No notification-delivery claim is made.

Full raw responses, copied rule/config, timeline and application evidence:
`/tmp/sub2api-v2smoke-681f272c-evidence/`.
Console output: `/tmp/ac8-drill-run.log`.
These are local ephemeral paths; the script retains them after container cleanup.

## Execution failure history and cleanup

The first attempt retained samples=true when disabling V2; the app rejected the
invalid combination before the drill began. It is not counted as an alert
result. Evidence: `/tmp/sub2api-v2smoke-fc5445a4-evidence/` and
`/tmp/ac8-drill-invalid-config.log`; that project was cleaned by the base script.

The measured run above completed all Drill 1 assertions, but its **process exit
was 1 / base report status FAIL** because the base smoke cleanup did not enable
the optional Prom profile. Its original report is preserved unchanged. A
follow-up removed only resources with this project's Docker label; final checks
found zero containers, volumes and networks:

```text
CLEANUP_FOLLOWUP_PASS {"containers":"","volumes":"","networks":""}
```

Follow-up evidence:
`/tmp/sub2api-v2smoke-681f272c-evidence/cleanup-followup.json`.
The standalone script now removes its Prom container/volume in `finally` before
the base cleanup. This corrected cleanup path was exercised separately with a
fresh isolated profiled Prom container and real volume:

```text
PROFILE_CLEANUP_PATH_PASS; zero containers/volumes/networks
```

Cleanup-path output: `/tmp/ac8-cleanup-check-ccbc28/ac8-prom-cleanup.log`.
At that historical stage, the complete several-minute drill had not been rerun after this cleanup-only fix;
do not relabel the original process exit as 0. Alert/restore/idle observations
remain valid, and the measured project's final resources are verified removed.

## Drill 2 boundary

**NOT EXECUTED**: there is no stable phase-only freeze mechanism in the unchanged
image. Disabling V2 removes phase series; it is not evidence that existing phase
counters remain present and stop growing. No relabel/drop operation or invented
metric was substituted for this required scenario. Full AC-8 therefore remains
incomplete despite Drill 1 passing its runtime assertions.
