#!/bin/bash
# demo-passive-node.sh
# Streams validator-2 (passive) logs during a failover scenario for VHS recording.
# Shows the passive node detecting a leaderless cluster and promoting itself to active.
#
# The integration docker compose environment must already be running.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
MOCK_URL="http://localhost:8899"
COMPOSE="docker compose -f $PROJECT_ROOT/integration/docker-compose.yml"

# Reset: validator-1 active
curl -sf -X POST -H "Content-Type: application/json" \
    -d '{"action":"reset","target":"validator-1"}' \
    "$MOCK_URL/action" >/dev/null

# Disconnect validator-1 after 10 seconds (background — does not block the log stream)
(sleep 10 && \
 curl -sf -X POST -H "Content-Type: application/json" \
     -d '{"action":"disconnect","target":"validator-1"}' \
     "$MOCK_URL/action" >/dev/null) &
disown $!

# Wait one poll cycle for the reset to be reflected in logs, then start streaming.
# --tail=3 shows a few steady-state context lines before following live output.
sleep 3
timeout 25 $COMPOSE logs --no-log-prefix --tail=3 -f validator-2 2>/dev/null || true
