#!/usr/bin/env bash
# E4 against the built image: the cutover runbook in the README, with kuma-import from the image, against a MariaDB
# source made from internal/importer/testdata/uptime-kuma-schema.sql. It checks that settings, API keys of both
# kinds, alert state and a skipped monitor's history arrive, that -verify exits 0 after the import and 3 after a
# change, and that imported API keys work on the REST API.
#
# Usage: cutover-e4.sh <image> <mariadb container> <mariadb root password> <timescaledb container> <timescaledb URL from the host>
#
# Both databases are made and dropped by the script. The service listens on 127.0.0.1:3716. It needs python3 with
# bcrypt, curl and docker.
set -euo pipefail
image=$1 maria=$2 mariapw=$3 pg=$4 pgurl=$5
here=$(cd "$(dirname "$0")" && pwd)
db=kuma_e4_$$
dsn="root:${mariapw}@tcp(127.0.0.1:$(docker port "$maria" 3306 | head -1 | cut -d: -f2))/${db}"
target="${pgurl%/*}/${db}"
token=e4-admin-token-0123456789abcdef-0123456789
pass=0 fail=0
out=$(mktemp)
step() { if eval "$2"; then echo "PASS $1"; pass=$((pass + 1)); else echo "FAIL $1"; fail=$((fail + 1)); fi; }
maria_sql() { docker exec -i "$maria" mariadb -uroot -p"$mariapw" --default-character-set=utf8mb4 "$@"; }
pg_sql() { docker exec -i "$pg" psql -qAt -U postgres -d "$db" "$@"; }
import() { docker run --rm --network host -e SOURCE_MARIADB_DSN="$dsn" -e DATABASE_URL="$target" --entrypoint /kuma-import "$image" "$@"; }
cleanup() {
	docker rm -f kuma-e4 >/dev/null 2>&1 || true
	echo "DROP DATABASE IF EXISTS ${db}" | maria_sql
	docker exec "$pg" psql -q -U postgres -c "DROP DATABASE IF EXISTS ${db} WITH (FORCE)" >/dev/null
}
trap cleanup EXIT

# The source: the schema, a monitor with two days of beats and Uptime Kuma's statistics for them, a monitor that
# can't be imported, settings, an API key of each kind, and alert state.
echo "CREATE DATABASE ${db}" | maria_sql
grep -v '^--' "$here/../importer/testdata/uptime-kuma-schema.sql" | maria_sql "$db"
better_key=$(python3 -c "import secrets,string; a=string.ascii_letters; print(''.join(secrets.choice(a) for _ in range(64)))")
better_hash=$(python3 -c "import hashlib,base64,sys; print(base64.urlsafe_b64encode(hashlib.sha256(sys.argv[1].encode()).digest()).decode().rstrip('='))" "$better_key")
legacy_hash=$(python3 -c "import bcrypt; print(bcrypt.hashpw(b'legacy-secret', bcrypt.gensalt(10)).decode().replace('\$2b\$', '\$2a\$', 1))")
maria_sql "$db" <<SQL
SET time_zone = '+00:00';
INSERT INTO monitor (id, name, type, url, \`interval\`, active, expiry_notification) VALUES
  (1, 'Site', 'http', 'https://example.com', 60, 1, 1),
  (2, 'Broken', 'http', 'https://example.com', 60, 1, 0);
UPDATE monitor SET headers = 'not json' WHERE id = 2;
INSERT INTO heartbeat (monitor_id, status, time, msg)
  SELECT m, IF(MOD(seq, 37) = 0, 0, 1), UTC_TIMESTAMP() - INTERVAL seq * 10 MINUTE, ''
  FROM seq_1_to_288 JOIN (SELECT 1 AS m UNION SELECT 2) AS ms;
INSERT INTO stat_hourly (monitor_id, timestamp, ping, ping_min, ping_max, up, down)
  SELECT monitor_id, UNIX_TIMESTAMP(DATE_FORMAT(time, '%Y-%m-%d %H:00:00')), 1, 1, 1, SUM(status = 1), SUM(status = 0)
  FROM heartbeat GROUP BY monitor_id, 2;
INSERT INTO stat_daily (monitor_id, timestamp, ping, ping_min, ping_max, up, down)
  SELECT monitor_id, UNIX_TIMESTAMP(DATE(time)), 1, 1, 1, SUM(status = 1), SUM(status = 0)
  FROM heartbeat GROUP BY monitor_id, 2;
INSERT INTO setting (\`key\`, value) VALUES ('primaryBaseURL', '"https://kuma.example.com"'), ('keepDataPeriodDays', '90'),
  ('tlsExpiryNotifyDays', '[7,14,21]');
INSERT INTO better_auth_apikey (id, name, start, \`key\`, referenceId, enabled) VALUES ('k1', 'CI', LEFT('${better_key}', 6), '${better_hash}', 'u1', 1);
INSERT INTO api_key (id, \`key\`, name, user_id, active) VALUES (3, '${legacy_hash}', 'Old script', 1, 1);
INSERT INTO notification_sent_history (type, monitor_id, days) VALUES ('certificate', 1, 21), ('certificate', 1, 14);
INSERT INTO monitor_tls_info (monitor_id, info_json) VALUES (1, '{"valid":true,"certInfo":{"subject":{"CN":"example.com"},"fingerprint256":"AB:CD","daysRemaining":10,"validTo":"2026-12-01T00:00:00Z"}}');
INSERT INTO domain_expiry (domain, expiry, last_check, last_expiry_notification_sent) VALUES ('example.com', '2027-03-01 00:00:00', UTC_TIMESTAMP(), 21);
SQL
docker exec "$pg" psql -q -U postgres -c "CREATE DATABASE ${db}" >/dev/null

# 1. Dry run.
dry=$(import 2>/dev/null)
step "the dry run reports settings, keys and alert state, and no values" \
	'echo "$dry" | grep -q "\"key\": \"primaryBaseURL\"" && echo "$dry" | grep -q "\"certificateAlerts\": 2" && ! echo "$dry" | grep -q kuma.example.com'
step "the dry run wrote nothing" '[ "$(pg_sql -c "SELECT count(*) FROM pg_tables WHERE tablename = '\''monitor'\''")" = 0 ]'

# 2. The service runs, and the import comes in while Uptime Kuma would still run.
docker run -d --name kuma-e4 --network host -e DATABASE_URL="$target" -e ANALYTICS_API_TOKEN=$token \
	-e LISTEN_ADDR=127.0.0.1:3716 -e KUMA_DISABLE_LEAK_PASSWORD_CHECK=true "$image" >/dev/null
for _ in $(seq 1 30); do curl -sf http://127.0.0.1:3716/healthz >/dev/null && break; sleep 1; done
report=$(import -apply 2>/dev/null)
step "the import copied the settings" 'echo "$report" | grep -A1 "\"key\": \"primaryBaseURL\"" | grep -q "\"result\": \"copied\""'
step "a default retention was replaced" 'echo "$report" | grep -A1 "\"key\": \"keepDataPeriodDays\"" | grep -q "\"result\": \"copied\""'
step "the skipped monitor's history was kept" 'echo "$report" | grep -q "\"newBeats\": 288" && [ "$(pg_sql -c "SELECT count(*) FROM skipped_heartbeat WHERE source_id = 2")" = 288 ]'
step "certificate alerts, the certificate and the domain arrived" \
	'[ "$(pg_sql -c "SELECT count(*) FROM cert_alert_sent")" = 2 ] && [ "$(pg_sql -c "SELECT fingerprint FROM monitor_tls_info")" = "AB:CD" ] && [ "$(pg_sql -c "SELECT last_notified_days FROM domain_expiry")" = 21 ]'
api() { curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $1" http://127.0.0.1:3716/api/v1/monitors; }
step "better-auth's key works on the API" '[ "$(api "$better_key")" = 200 ]'
step "the older table's key works on the API" '[ "$(api uk3_legacy-secret)" = 200 ]'
step "a wrong key doesn't" '[ "$(api uk3_wrong)" = 401 ] && [ "$(api "${better_key%?}x")" = 401 ]'

# 3. Verify right after an import.
set +e
import -verify >"$out" 2>/dev/null
code=$?
set -e
step "-verify exits 0 after the import ($code)" '[ $code = 0 ] && grep -q "\"matching\": 1" "$out" && grep -q "\"notImported\": 1" "$out"'

# 4-5. Uptime Kuma stops; the last import activates; 6. restart; 7. verify again.
import -apply -activate >/dev/null 2>&1
docker restart kuma-e4 >/dev/null
for _ in $(seq 1 30); do curl -sf http://127.0.0.1:3716/healthz >/dev/null && break; sleep 1; done
step "the monitor is active after -activate" '[ "$(pg_sql -c "SELECT active FROM monitor")" = t ]'
step "the imported retention applies after the restart" 'docker logs kuma-e4 2>&1 | grep -q "\"retentionDays\":90"'
set +e
import -verify >/dev/null 2>&1
code=$?
set -e
step "-verify still exits 0 ($code)" '[ $code = 0 ]'

# A difference: a beat lost here.
pg_sql -c "DELETE FROM heartbeat WHERE time = (SELECT min(time) FROM heartbeat)"
set +e
import -verify >"$out" 2>/dev/null
code=$?
set -e
step "-verify exits 3 when a monitor differs ($code)" '[ $code = 3 ] && grep -q "beats: 288 in the source, 287 here" "$out"'
rm -f "$out"

echo "$pass/$((pass + fail)) passed"
[ $fail = 0 ]
