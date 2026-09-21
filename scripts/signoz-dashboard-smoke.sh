#!/usr/bin/env bash
#
# Smoke check for the SigNoz dashboards managed by modules/signoz-dashboards.
#
# For each dashboard's principal metric series, query the SigNoz v5 query_range
# API and fail (non-zero exit) if the series returns no data. This catches
# dashboards that were created but are backed by missing telemetry.
#
# Requires:
#   SIGNOZ_ENDPOINT       e.g. https://signoz.cowlab.org
#   SIGNOZ_ACCESS_TOKEN   a SigNoz service-account API key
set -euo pipefail

: "${SIGNOZ_ENDPOINT:?SIGNOZ_ENDPOINT is required}"
: "${SIGNOZ_ACCESS_TOKEN:?SIGNOZ_ACCESS_TOKEN is required}"

for bin in curl jq; do
  command -v "$bin" >/dev/null 2>&1 || { echo "FAIL: '$bin' is required" >&2; exit 1; }
done

end_ms="$(date +%s)000"
start_ms="$(( end_ms - 3600000 ))"

# "<dashboard-name>:<principal-metric>"
dashboards=(
  "homelab-overview:k8s.node.cpu.usage"
  "kubernetes-nodes:k8s.node.memory.usage"
  "pi5-thermal:node_thermal_zone_temp"
)

fail=0
for entry in "${dashboards[@]}"; do
  name="${entry%%:*}"
  metric="${entry#*:}"

  payload="$(jq -n \
    --arg m "$metric" \
    --argjson s "$start_ms" \
    --argjson e "$end_ms" \
    '{
       start: $s,
       end: $e,
       requestType: "time_series",
       schemaVersion: "v1",
       compositeQuery: {
         queries: [
           {
             type: "builder_query",
             spec: {
               name: "A",
               signal: "metrics",
               stepInterval: "60s",
               aggregations: [
                 { metricName: $m, timeAggregation: "avg", spaceAggregation: "avg" }
               ]
             }
           }
         ]
       }
     }')"

  response="$(curl -sS -w $'\n%{http_code}' --max-time 30 \
    -X POST "${SIGNOZ_ENDPOINT%/}/api/v5/query_range" \
    -H "SIGNOZ-API-KEY: ${SIGNOZ_ACCESS_TOKEN}" \
    -H "Content-Type: application/json" \
    -d "$payload")" || { echo "FAIL ${name}: request error for ${metric}"; fail=1; continue; }

  http_code="$(printf '%s' "$response" | tail -n1)"
  body="$(printf '%s' "$response" | sed '$d')"

  if [ "$http_code" != "200" ]; then
    echo "FAIL ${name}: HTTP ${http_code} for ${metric}"
    fail=1
    continue
  fi

  if printf '%s' "$body" | jq -e \
    '[.. | objects | select(has("series")) | .series | length] | any(. > 0)' \
    >/dev/null 2>&1; then
    echo "PASS ${name}: ${metric} returned data"
  else
    echo "FAIL ${name}: no data returned for ${metric}"
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  echo "Dashboard smoke check FAILED"
  exit 1
fi

echo "Dashboard smoke check PASSED"
