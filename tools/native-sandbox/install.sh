#!/usr/bin/env sh
set -eu
bundle_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
node -e 'const [a,b]=process.versions.node.split(".").map(Number); if(a<20 || a===20&&b<11) throw new Error("Node.js 20.11+ is required")'
# Install only when the operator explicitly runs this installer. Both direct
# and transitive packages come from the adjacent lock; no lifecycle scripts.
npm ci --prefix "$bundle_dir" --ignore-scripts --no-audit --no-fund
printf 'Sandbox runtime installed from the bundled lock. Set OPENLINKER_AGENT_NODE_SESSION_SANDBOX_BIN to %s/node_modules/.bin/srt\n' "$bundle_dir"
