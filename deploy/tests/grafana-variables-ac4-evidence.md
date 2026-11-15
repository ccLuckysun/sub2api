# AC-4 local PG18 SQL evidence — 2026-11-15

Scope: SQL validity, numeric variable types, read-only role and filter results.
Result: **54/54 panel queries PASS**, 4 variable queries PASS. Browser/Grafana
variable switching and refresh-to-result timing remain **NOT EXECUTED**; this is
not a full AC-4 UI acceptance or a production-load performance result.

## Reproduction

```sh
python3 deploy/tests/grafana-variables-pg18-test.py
```

The script creates a uniquely named PostgreSQL 18 container, with `--network
none` and no published ports. It creates a minimal pre-V2 schema, applies the
actual repository migrations 251 and 252 in separate transactions, then inserts
2 users, 3 keys, 4 usage rows and 4 transfer samples. This is a query fixture,
not a full application migration-chain test. It does not modify dev/production
databases, backend code, existing smoke scripts or dashboards.

Migration SHA256 in this run:

```text
251_ops_v2_samples.sql
feac57dc6517b8e1573dae95a0a902314a1a61eb75cce5919dea6a2a07a5141c
252_add_usage_logs_v2_fields.sql
d47a162ab17b3718d0cc9ced7b6724e50b316d4e8a8403efe278895ee6e97ce8
```

The actual `deploy/monitoring/sql/grafana-ro.sql` provisions `grafana_ro`.
TCP/password login is verified as that role with read-only default enabled.
UPDATE and CREATE are denied even after disabling the transaction read-only
setting; the attempted statements run only in the disposable fixture and fail.
The subsequent query runner uses `grafana_ro` through the local socket, with
session read-only plus explicit `BEGIN READ ONLY`, statement timeout 14900ms
and a 15s client timeout. No data changes occur during the query matrix.

## Queries and variable contract

```sql
SELECT id AS __value, COALESCE(email, id::text) AS __text
FROM users ORDER BY 2 LIMIT 1000;

SELECT id AS __value, name AS __text FROM api_keys
WHERE ($user = -1 OR user_id = $user) ORDER BY 2 LIMIT 1000;
```

Both variables are `type=query`, `multi=false`, `includeAll=true`,
`allValue="-1"`, `refresh=2`, datasource `sub2api-pg`. Returned option values
and filter ID columns are bigint. No string quoting is introduced around the
numeric variable substitutions. All six panels in 04-users use both predicates:

```sql
($user = -1 OR user_id = $user)
AND ($api_key = -1 OR api_key_id = $api_key)
```

The exact SQL templates are read from the existing dashboard JSON and printed
by the runner. All other dashboards' Prom/system panels are checked to contain
no user/key variable filters. Grafana time macros are expanded by the test to
a fixed six-hour window and equivalent five-minute epoch buckets; actual
Grafana plugin interpolation has not been executed.

The six panel template strings, concatenated in panel order, have SHA256:

```text
741853094be53a24d642a1bb4940e4d77341396a2fe2b2f3f8df938bae616dc6
```

## Actual results and timing

The variable query returned 2 user options. Key options returned 3 for user All,
2 for user 1, and 0 for a verified absent ID. Existing key 11 belongs to user 1.
Panel inputs are the cross product of user `-1 / 1 / absent` and api_key
`-1 / 11 / absent`, giving 9 cases per panel. Missing IDs are checked absent
from all four seeded tables; no-match SQL is valid and returns the expected
empty shape. The aggregate panel returns one row with requests=0 rather than
zero rows.

| Query | Count/result | Elapsed, including docker/psql startup |
| --- | --- | --- |
| user options | 2, bigint | 31.517ms |
| key options, user All | 3, bigint | 29.967ms |
| key options, user 1 | 2, bigint | 30.924ms |
| key options, absent user | 0 | 29.812ms |
| panel 1: user requests | 9/9 PASS | 29.536–33.017ms |
| panel 2: tokens/cost | 9/9 PASS | 28.417–34.188ms |
| panel 3: key traffic | 9/9 PASS | 28.168–41.080ms |
| panel 4: slow requests | 9/9 PASS | 27.500–41.532ms |
| panel 5: latency | 9/9 PASS | 29.781–38.222ms |
| panel 6: error composition | 9/9 PASS | 29.641–36.171ms |

The wrapper additionally asserts actual seeded results: request totals All=4,
user 1=3, key 11=2; slow rows All=3, user 1=2, key 11=1; no-match=0. A legitimate
single selection is tested, unlike the earlier empty development database.

```text
PASS: variable queries=4; successful panel queries=54/54; failed=0
TIMING max=42.067ms; calls=62; includes docker/psql startup
FILTER_RESULTS PASS 54/54: All=4, user1=3, key11=2 requests; slow=3/2/1; no-match=0
ACL TCP password login grafana_ro/default_transaction_read_only=on: PASS
ACL UPDATE/CREATE denied even with transaction_read_only=off: PASS
CLEANUP disposable PG exit=0; no host ports/dev/production writes
script exit=0
```

Full local output: `/tmp/ac4-grafana-variables-pg18.log` (ephemeral local file).
The earlier dev result remains `/tmp/ac4-grafana-variables-dev-final.log`:
36/54 success, 18 failures due to missing `body_bytes` and sample table, no keys
or usage rows, no grafana_ro. Those failures are not erased or recast as passes;
the complete fixture above isolates the missing-schema/data cause.

Remaining: browser actual variable selections, dependent-panel automatic refresh
and end-to-end result <15s; actual Grafana macro/interpolation execution; large
or production-representative datasets. All measured SQL invocations here were
<15s, but these timings do not establish the missing UI/end-to-end requirement.
