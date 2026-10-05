// Tag policy is only the first gate. The release workflow additionally executes
// the root-owned old/new binary matrix on the exact packaged Linux artifact.
// No environment variable can authorize an untested stable version.
const tags = process.argv.slice(2);
const tag = tags[0];
const testPrerelease = /^v0\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)-(?:alpha|beta|rc)\.(?:0|[1-9][0-9]*)$/;

if (tags.length === 1 && tag === "v0.2.0") {
  process.stdout.write("NODE_CONTROLLED_RELEASE_CANDIDATE: v0.2.0 packaging requires the exact artifact controlled-upgrade gate before publication.\n");
} else if (tags.length === 1 && typeof tag === "string" && tag === tag.trim() && testPrerelease.test(tag)) {
  process.stdout.write("NODE_TEST_PRERELEASE_ALLOWED: test-only fresh enrollment; no in-place version upgrade or automatic rollback is supported.\n");
} else {
  process.stderr.write("NODE_SUPPORTED_RELEASE_REQUIRED: supply v0.2.0 or one canonical v0.x.y-alpha.N, v0.x.y-beta.N or v0.x.y-rc.N tag; every other stable or v1+ version remains blocked.\n");
  process.exitCode = 1;
}
