// Temporary, deliberate release block. Replace this guard only in the reviewed
// integration commit that supplies the supported Core upgrade/recovery contract,
// exact compatible release, user procedure and real compatibility evidence.
// There is no environment variable or workflow input that bypasses the gate.
process.stderr.write("NODE_UPGRADE_COMPATIBILITY_NOT_READY: enrolled-Node version upgrade and rollback are not yet delivered; tagged releases are blocked.\n");
process.exitCode = 1;
