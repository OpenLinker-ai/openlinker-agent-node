// Pre-1.0 test deployments use fresh enrollment, not an in-place Node upgrade.
// Only an explicit canonical prerelease tag authorizes tagged packaging. Local
// dev/SHA builds are not releases; no environment value can bypass this policy.
const tags = process.argv.slice(2);
const tag = tags[0];
const testPrerelease = /^v0\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)-(?:alpha|beta|rc)\.(?:0|[1-9][0-9]*)$/;

if (tags.length !== 1 || typeof tag !== "string" || tag !== tag.trim() || !testPrerelease.test(tag)) {
  process.stderr.write("NODE_TEST_PRERELEASE_REQUIRED: supply one canonical v0.x.y-alpha.N, v0.x.y-beta.N or v0.x.y-rc.N tag; stable and v1+ releases remain blocked.\n");
  process.exitCode = 1;
} else {
  process.stdout.write("NODE_TEST_PRERELEASE_ALLOWED: test-only fresh enrollment; no in-place version upgrade or automatic rollback is supported.\n");
}
