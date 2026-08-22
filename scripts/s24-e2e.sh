#!/usr/bin/env bash
# S24 end-to-end acceptance (plan §S24): build the binary, then drive the full fixture
# stack — real Docker container, scripted `claude` baked into a derived agent image, a local
# fake GitHub serving both the REST API and git smart-HTTP (`git http-backend`), and the
# real MCP server. `run` (default) executes the scripted acceptance and exits; `hold` leaves
# the instance running for a browser walkthrough.
set -euo pipefail
cd "$(dirname "$0")/.."
make build
exec go run ./e2e/s24 -mode "${1:-run}"
