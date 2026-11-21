#!/usr/bin/env bash
# Generates traffic for the dashboards and alerts: uploads, reads of originals, thumbnails and
# metadata, an occasional rejected upload and deletion. The pace keeps one IP under the default
# limit of 10 uploads per minute.
#
#   scripts/load.sh                 run until Ctrl+C
#   DURATION=300 scripts/load.sh    run for five minutes
set -euo pipefail

BASE=${BASE_URL:-http://localhost:8080}
IMAGE=${IMAGE:-internal/handlers/assets/placeholder.png}
PAUSE=${PAUSE:-8}
DURATION=${DURATION:-0}
USERS=${USERS:-5}

code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

end=$((DURATION > 0 ? SECONDS + DURATION : 0))
i=0
while [ "$end" -eq 0 ] || [ "$SECONDS" -lt "$end" ]; do
  i=$((i + 1))
  user="load-user-$((i % USERS))"
  resp=$(curl -s -w '\n%{http_code}' -H "X-User-ID: $user" -F "file=@$IMAGE" "$BASE/api/v1/avatars")
  status=${resp##*$'\n'}
  id=$(printf '%s' "${resp%$'\n'*}" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  line="upload=$status"
  if [ -n "$id" ]; then
    line+=" get=$(code "$BASE/api/v1/avatars/$id")"
    line+=" meta=$(code "$BASE/api/v1/avatars/$id/metadata")"
    line+=" thumb=$(code "$BASE/api/v1/users/$user/avatar?size=100x100")"
    line+=" list=$(code "$BASE/api/v1/users/$user/avatars")"
    if [ $((i % 4)) -eq 0 ]; then
      line+=" delete=$(code -X DELETE -H "X-User-ID: $user" "$BASE/api/v1/avatars/$id")"
    fi
  fi
  if [ $((i % 5)) -eq 0 ]; then
    line+=" not_image=$(code -H "X-User-ID: $user" -F "file=@$0;type=image/png" "$BASE/api/v1/avatars")"
    line+=" missing=$(code "$BASE/api/v1/avatars/00000000-0000-0000-0000-000000000000")"
  fi
  echo "$(date +%T) $user $line"
  sleep "$PAUSE"
done
