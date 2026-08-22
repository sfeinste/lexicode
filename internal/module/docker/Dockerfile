# The Lexicode agent base image (decision D-7). This file is embedded in the lexicode binary;
# the image is built on demand and tagged lexicode/agent-base:<sha256 of this file, first 12>,
# so upgrading the binary rebuilds exactly when this file changed.
#
# Node is installed from the exact pinned tarball on nodejs.org rather than nodesource or nvm:
# a fixed version per architecture keeps the build deterministic (same Dockerfile, same Node),
# with no third-party apt repository and no shell-profile tricks for non-interactive shells.
# @anthropic-ai/claude-code itself is deliberately unpinned — a rebuild (triggered by editing
# this file) should pick up the current CLI.
FROM debian:bookworm-slim

ARG NODE_VERSION=22.14.0

RUN apt-get update && apt-get install -y --no-install-recommends \
        git curl jq ripgrep build-essential ca-certificates xz-utils procps \
    && rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
        amd64) narch=x64 ;; \
        arm64) narch=arm64 ;; \
        *) echo "unsupported architecture: $arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${narch}.tar.xz" \
        -o /tmp/node.tar.xz; \
    tar -xJf /tmp/node.tar.xz -C /usr/local --strip-components=1; \
    rm /tmp/node.tar.xz; \
    node --version; npm --version

RUN npm install -g @anthropic-ai/claude-code && claude --version

# Non-root user with a fixed uid/gid so files materialized into the workspace can be owned
# correctly from the host side. /workspace is the run's git worktree; at run time it is an
# anonymous volume (the rootfs is read-only) initialized from this directory's ownership.
RUN useradd -m -u 1000 -s /bin/bash agent \
    && mkdir -p /workspace \
    && chown agent:agent /workspace

USER agent
WORKDIR /workspace

# The container's main process is a placeholder; the agent CLI runs via exec. `--init` (set by
# the adapter) reaps zombies. Writes to /proc/1/fd/1 land in the container log stream, which is
# what Reattach resumes from.
CMD ["sleep", "infinity"]
