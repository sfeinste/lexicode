#!/usr/bin/env bash
# S39 acceptance: the brief §3 chain, end to end, automated (plan §S39).
#
# Builds the binary, then drives the full fixture stack — real Docker containers, a scripted
# `claude` baked into a derived agent image, a local fake GitHub serving both the REST API and
# git smart-HTTP, the real MCP server, the real poller, the real trigger engine and the real
# loop guard — through all eight steps of the canonical chain, and prints the two brief §10
# timings.
#
# Requirements: Docker running, git, and a few minutes. The first run also builds the agent
# base image (several minutes); later runs reuse it.
set -euo pipefail
cd "$(dirname "$0")/.."
make build
exec go run ./e2e/s39 "$@"
