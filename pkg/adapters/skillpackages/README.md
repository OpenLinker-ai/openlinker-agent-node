# Managed skill package execution leaf

This package consumes Core's `_openlinker_skill_packages` v1 assignment extension.
It has only standard-library dependencies and never starts a Worker or Provider.
The embedding product obtains Agent ID and authority from the SDK trusted context,
passes the dedicated snapshot (never caller input), chooses the final readable
workspace/cache, and supplies the SDK durable event emitter.

`Load` verifies identity, provider, SHA-256, UTF-8 paths, sizes and dependencies;
materializes immutable files; and returns prompt data plus a stable session digest.
`Instructions` includes full instructions for a new session and a small file index
on resumed turns. Products bind `SessionMode` to their existing private session
selection; an empty selection removes the prior digest and forces a fresh session.

Default storage remains the workspace's Git-excluded private cache. A product
may provision an explicit cross-UID cache: the root must be owned by the Host with
the selected group and mode 02750. Directories inherit that group and use 02750;
files use 0440. The Provider joins only this read group. The Host must provision
readable paths through any tool sandbox. No credential/state directory is shared.

Tests cover the leaf's immutable/concurrent filesystem operations. Each consumer
also tests its actual Provider entrypoints; leaf tests are not execution acceptance.
