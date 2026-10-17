#!/bin/sh
# Локальная демонстрация консоли: временная папка данных, учётка с
# устройствами и висящим кодом, сервер на свободном порту.
set -eu
cd "$(dirname "$0")/.."
DATA=$(mktemp -d "${TMPDIR:-/tmp}/synk-demo.XXXXXX")
trap 'kill $SRV 2>/dev/null || true; rm -rf "$DATA"' EXIT
LISTEN=${LISTEN:-127.0.0.1:18090}
METRICS=${METRICS:-127.0.0.1:18091}
export SYNK_METRICS_LISTEN=$METRICS
export SYNK_PUBLIC_URL=https://mtrack-sync.example.test

./bin/synk account add --data "$DATA" --name Alex >/dev/null
./bin/synk account add --data "$DATA" --name Family >/dev/null
./bin/synk serve --data "$DATA" --listen $LISTEN --metrics-listen $METRICS >"$DATA/server.log" 2>&1 &
SRV=$!
sleep 0.5
# Подключаем два устройства через реальный API, одно потом отзываем.
register() { # account role name
  code=$(./bin/synk invite --data "$DATA" --account "$1" $2 | awk '/^Код:/{print $2}')
  curl -s -X POST "http://$LISTEN/v1/register" -H 'Content-Type: application/json' \
    -d "{\"invite_code\":\"$code\",\"device_name\":\"$3\"}"
}
T1=$(register Alex --admin "iPhone 15" | sed 's/.*"token":"\([^"]*\)".*/\1/')
register Alex "" "Pixel 8" >/dev/null
register Family "" "iPad" >/dev/null
curl -s -X POST "http://$LISTEN/v1/ops" -H "Authorization: Bearer $T1" -H 'Content-Type: application/json' \
  -d '{"ops":[{"op_id":"op1","device_id":"x","entity":"transaction","entity_uuid":"u1","hlc":"0001","payload":"{}"},{"op_id":"op2","device_id":"x","entity":"category","entity_uuid":"u2","hlc":"0002","payload":"{}"}]}' >/dev/null
PIXEL=$(./bin/synk device list --data "$DATA" | awk '/Pixel/{print $1}')
./bin/synk device revoke --data "$DATA" --id "$PIXEL" >/dev/null
./bin/synk invite --data "$DATA" --account Family >/dev/null

exec ./bin/synk --data "$DATA" --listen $LISTEN --tui
