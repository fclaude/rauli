#!/usr/bin/env bash
# Builds the site for a local address, serves it, and runs the accessibility
# checks against it: axe-core on every page in light and dark at 1280 and 320
# pixels, then keyboard navigation and reflow, after measuring every contrast pair. Needs zola, python3, node, and
# Chromium for Playwright (npx playwright install chromium).
#
#   bash site/audit/run.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
site="$(dirname "$here")"
port="${PORT:-8099}"
out="$(mktemp -d)"
# Some other server on this port would answer instead of ours, and the audit
# would report on the wrong pages. Better to stop and say so.
if python3 -c "import socket,sys; s=socket.socket(); s.settimeout(0.5); sys.exit(0 if s.connect_ex(('127.0.0.1', int(sys.argv[1]))) == 0 else 1)" "$port"; then
  echo "run.sh: something is already listening on port $port; set PORT to a free one" >&2
  exit 2
fi
trap 'kill "$server" 2>/dev/null || true; rm -rf "$out"' EXIT

python3 "$site/build.py"
(cd "$site" && zola build --base-url "http://127.0.0.1:$port" -o "$out/site" >/dev/null && python3 finish.py "$out/site")
python3 "$here/contrast.py" "$out/site"
(cd "$here" && npm install --no-audit --no-fund >/dev/null)
python3 -m http.server "$port" --bind 127.0.0.1 --directory "$out/site" >/dev/null 2>&1 &
server=$!
sleep 1
export BASE="http://127.0.0.1:$port"
(cd "$here" && node axe.js && node keyboard.js)
