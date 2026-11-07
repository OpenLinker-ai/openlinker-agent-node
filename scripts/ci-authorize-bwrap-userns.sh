#!/usr/bin/env bash
# Disposable Ubuntu CI runner only. Make the distro /usr/bin/bwrap able to create
# the user/network/PID namespaces Node's Linux preflight probes, without changing
# the global unprivileged-userns restriction.
#
# Ubuntu 24.04 restricts unprivileged user namespaces but enables no bubblewrap
# profile, so the runner loads the executable-specific scripts/ci-bwrap.apparmor.
# Ubuntu 26.04's apparmor package already enforces bwrap-userns-restrict for
# /usr/bin/bwrap (its sandboxed children keep no capabilities). Loading a second
# profile for the same path makes the kernel attach neither ("conflicting profile
# attachments"), and bwrap then fails with "loopback: Failed RTM_NEWADDR".
set -euo pipefail
cd "$(dirname "$0")/.."

probe() {
  bwrap --unshare-user --unshare-net --unshare-pid --ro-bind / / --proc /proc --dev /dev -- "$@"
}
restriction() {
  sysctl -n kernel.apparmor_restrict_unprivileged_userns 2>/dev/null || true
}

initial_restriction=$(restriction)
if probe /bin/true 2>/dev/null; then
  echo 'Installed policy already authorizes bwrap namespaces; no CI AppArmor profile loaded.'
elif [[ "$initial_restriction" == 1 ]]; then
  sudo apparmor_parser -r scripts/ci-bwrap.apparmor
  echo 'Loaded the CI-only AppArmor userns profile for /usr/bin/bwrap.'
else
  echo 'bwrap cannot create namespaces and the AppArmor userns restriction is not enabled.' >&2
fi
if [[ "$(restriction)" != "$initial_restriction" ]]; then
  echo 'The global unprivileged-userns restriction changed.' >&2
  exit 1
fi
probe /bin/true
echo "Sandboxed child confinement: $(probe /bin/cat /proc/self/attr/current 2>/dev/null || echo unavailable)"
