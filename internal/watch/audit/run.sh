#!/usr/bin/env bash
# Starts the demo, which serves the live view on a small fixture site with one
# approval waiting, and runs the accessibility checks against it: axe-core in
# light and dark at 1280 and 320 pixels, before and after taking control, then a
# keyboard-only walk and the reflow checks. Needs go, node, and Chromium for
# Playwright (npx playwright install chromium).
#
#   bash internal/watch/audit/run.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../../.." && pwd)"
out="$(mktemp -d)"
trap 'kill "$demo" 2>/dev/null || true; rm -rf "$out"' EXIT

(cd "$root" && go build -o "$out/demo" ./internal/watch/internal/demo)
(cd "$here" && npm install --no-audit --no-fund >/dev/null)

"$out/demo" >"$out/url" 2>"$out/err" &
demo=$!
for _ in $(seq 1 80); do
  [ -s "$out/url" ] && break
  sleep 0.25
done
export VIEW_URL="$(head -1 "$out/url")"
[ -n "$VIEW_URL" ] || { echo "the demo did not start:"; cat "$out/err"; exit 1; }

(cd "$here" && node axe.js && node keyboard.js)
