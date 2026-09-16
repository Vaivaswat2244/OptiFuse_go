#!/usr/bin/env bash
#
# Generates traffic against the deployed test applications so CloudWatch has
# something to report.
#
# The shape matters more than the volume. Lambda keeps an execution environment
# warm for roughly 5 to 15 minutes after an invocation, so a tight loop produces
# almost no cold starts and therefore no Init Duration records at all. Since
# cold start behaviour is the thing being measured, that would be useless.
#
# So each run does one request after a long idle gap (likely cold, giving an
# Init Duration line) followed by a short burst (warm, giving stable duration
# and memory figures). Run it from cron every 20 minutes and both populate.
#
# Cost: ~6 API calls per run, ~7 Lambda invocations each. At every 20 minutes
# that is roughly 90k invocations and 6k GB-seconds a month, against an Always
# Free allowance of 1M and 400k. Comfortably inside it.
#
#   chmod +x scripts/generate-traffic.sh
#   crontab -e
#   */20 * * * * /home/vaivaswat/Documents/projects/OptiFuse_go/scripts/generate-traffic.sh
#
# Watch it:      tail -f ~/optifuse-traffic.log
# Stop it:       crontab -e   and delete the line

set -uo pipefail

IMAGE_URL="${IMAGE_URL:-https://cggdh0wvsa.execute-api.ap-south-1.amazonaws.com/process}"
ORDER_URL="${ORDER_URL:-https://27epmgi5d7.execute-api.ap-south-1.amazonaws.com/order}"
WARM_CALLS="${WARM_CALLS:-4}"
LOG="${TRAFFIC_LOG:-$HOME/optifuse-traffic.log}"

hit() {
  local name="$1" url="$2" phase="$3"
  local out
  out=$(curl -s -X POST "$url" \
          -H 'Content-Type: application/json' \
          -d '{"source":"optifuse-traffic"}' \
          -o /dev/null \
          --max-time 30 \
          -w '%{http_code} %{time_total}' 2>/dev/null) || out="000 timeout"

  printf '%s  %-16s %-4s  status=%s  total=%ss\n' \
    "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$name" "$phase" \
    "${out%% *}" "${out##* }" >> "$LOG"
}

for pair in "image:$IMAGE_URL" "ecommerce:$ORDER_URL"; do
  name="${pair%%:*}"
  url="${pair#*:}"

  # First call after the idle gap. This is the one that should be cold.
  hit "$name" "$url" cold

  # Let the async downstream invocations finish before piling on more. The
  # handlers invoke each other with InvocationType Event, so the HTTP response
  # returns before the chain does.
  sleep 3

  for _ in $(seq 1 "$WARM_CALLS"); do
    hit "$name" "$url" warm
    sleep 1
  done
done
