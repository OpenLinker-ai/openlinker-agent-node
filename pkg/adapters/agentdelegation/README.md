# Attempt-scoped delegation transport

The broker exposes only the active Runtime Attempt's `CallAgent` and
`ReadDelegatedRun` callbacks. The SDK owns Core transport and authorization;
the broker owns local MCP framing and request-key tracking. It exits with the
Attempt and does not run as a resident daemon.

Same-UID CLI and Agent Node installations retain private temporary directories
and sockets (0700/0600). Official Provider containers use
`OPENLINKER_AGENT_DELEGATION_BROKER_ROOT=/delegation-tool`, an isolated tmpfs
owned by Runtime UID 10001 and Provider GID 10002 with mode 2710. The broker
validates that explicit group boundary, then creates per-Attempt 0710 directories
and 0660 sockets. The Provider can connect to its supplied socket path but cannot
list or modify the broker root, read Runtime credentials, or choose a Core URL.
Unrelated UIDs have no access. The supplied Compose profiles mount this tmpfs
and leave delegation disabled until `OPENLINKER_AGENT_DELEGATION_TARGETS` is set.

CI runs a Linux test as root that starts the broker as UID 10001, completes the
MCP delegation/result exchange as UID 10002, and rejects UID 10003. SDK tests
separately exercise negotiated reads through Worker transport/policy wrappers,
Attempt cancellation, and request proofs. Real Provider/Core acceptance is
still required to verify the complete model-to-child-to-parent flow.
