#!/usr/bin/env bash
# Configures OpenSearch and OpenSearch Dashboards for the service logs. Safe to run again:
# every step creates or replaces its object.
set -euo pipefail

OS=${OPENSEARCH_URL:-http://opensearch:9200}
OSD=${DASHBOARDS_URL:-http://opensearch-dashboards:5601}
DIR=$(dirname "$0")

# call METHOD URL [BODY_FILE] prints the response body and fails on an HTTP error.
call() {
  local args=(-sS -X "$1" -H 'Content-Type: application/json' -H 'osd-xsrf: true' -w '\n%{http_code}')
  [ $# -ge 3 ] && args+=(--data-binary "@$3")
  local out code
  out=$(curl "${args[@]}" "$2")
  code=${out##*$'\n'}
  out=${out%$'\n'*}
  if [ "$code" -ge 400 ]; then
    echo "$1 $2 -> $code: $out" >&2
    return 1
  fi
  printf '%s' "$out"
}

until curl -sf "$OS/_cluster/health?wait_for_status=yellow&timeout=5s" >/dev/null; do sleep 2; done

call PUT "$OS/_index_template/gophprofile-logs" "$DIR/index-template.json" >/dev/null
echo "index template: ok"

# ISM updates are optimistic: the current _seq_no and _primary_term must be passed back.
if policy=$(curl -sf "$OS/_plugins/_ism/policies/gophprofile-logs-retention"); then
  seq=$(printf '%s' "$policy" | grep -o '"_seq_no":[0-9]*' | cut -d: -f2)
  term=$(printf '%s' "$policy" | grep -o '"_primary_term":[0-9]*' | cut -d: -f2)
  call PUT "$OS/_plugins/_ism/policies/gophprofile-logs-retention?if_seq_no=$seq&if_primary_term=$term" \
    "$DIR/ism-policy.json" >/dev/null
  echo "retention policy: updated"
else
  call PUT "$OS/_plugins/_ism/policies/gophprofile-logs-retention" "$DIR/ism-policy.json" >/dev/null
  echo "retention policy: created"
fi

# The monitor's webhook action refers to this channel by its fixed id; recreate it so the
# file stays the source of truth.
curl -sS -o /dev/null -X DELETE "$OS/_plugins/_notifications/configs/gophprofile-alert-receiver"
call POST "$OS/_plugins/_notifications/configs" "$DIR/alert-channel.json" >/dev/null
echo "alert channel: ok"

found=$(printf '{"query":{"term":{"monitor.name.keyword":"gophprofile-error-logs"}}}' |
  curl -sS -X POST -H 'Content-Type: application/json' --data-binary @- "$OS/_plugins/_alerting/monitors/_search" |
  grep -o '"_id":"[^"]*"' | head -n1 | cut -d'"' -f4 || true)
if [ -n "$found" ]; then
  call PUT "$OS/_plugins/_alerting/monitors/$found" "$DIR/monitor.json" >/dev/null
  echo "error monitor: updated"
else
  call POST "$OS/_plugins/_alerting/monitors" "$DIR/monitor.json" >/dev/null
  echo "error monitor: created"
fi

until curl -sf "$OSD/api/status" >/dev/null; do sleep 2; done

call POST "$OSD/api/saved_objects/index-pattern/gophprofile-logs?overwrite=true" "$DIR/index-pattern.json" >/dev/null
call POST "$OSD/api/saved_objects/search/gophprofile-errors?overwrite=true" "$DIR/search-errors.json" >/dev/null
call POST "$OSD/api/saved_objects/search/gophprofile-by-trace?overwrite=true" "$DIR/search-trace.json" >/dev/null
printf '{"changes":{"defaultIndex":"gophprofile-logs"}}' >/tmp/settings.json
call POST "$OSD/api/opensearch-dashboards/settings" /tmp/settings.json >/dev/null
echo "dashboards objects: ok"
