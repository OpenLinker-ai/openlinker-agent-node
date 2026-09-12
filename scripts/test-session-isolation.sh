#!/bin/sh
# Uses a scratch protocol fixture, no model API or personal native credentials.
set -eu
cd "$(dirname "$0")/.."
test "$(id -u)" -ne 0 || { echo 'Run isolation acceptance as a non-root user.' >&2; exit 1; }
scratch=$(mktemp -d)
image_id=
cleanup() {
  if [ -n "$image_id" ]; then docker image rm "$image_id" >/dev/null 2>&1 || true; fi
  rm -rf "$scratch"
}
trap cleanup EXIT INT TERM
architecture=$(docker info --format '{{.Architecture}}')
case "$architecture" in
  aarch64|arm64) goarch=arm64 ;;
  x86_64|amd64) goarch=amd64 ;;
  *) echo 'Unsupported Docker architecture' >&2; exit 1 ;;
esac
GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" go build -o "$scratch/client" ./pkg/adapters/testdata/session-client
cat > "$scratch/Dockerfile" <<'DOCKERFILE'
FROM scratch
COPY client /codex
COPY client /claude
DOCKERFILE
docker build --quiet --network=none --iidfile "$scratch/image-id" "$scratch" >/dev/null
image_id=$(cat "$scratch/image-id")
OPENLINKER_TEST_SESSION_IMAGE="$image_id" GOWORK=off go test -race -count=1 -timeout=10m ./pkg/adapters ./pkg/adapters/sessionsandbox -run 'TestDockerSession' -v
