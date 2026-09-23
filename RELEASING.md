# Release and installed-version contract

A source commit, a passing test run and a working local binary are separate from
a released product. A deployment is complete only when its version, source
revision, published assets and installed bytes have all been verified.

## Prepare one version

1. Inspect the latest published release, existing tags and the clean main branch.
2. Add the new semantic version and its changes to CHANGELOG.md. Describe security
   compatibility requirements and the supported upgrade order explicitly.
3. Keep buildinfo.Version defaulting to dev for ordinary developer builds.
   Official builds receive the new version, full source revision and deterministic
   commit time through linker flags. Never reuse an old version for changed bytes.
4. Commit and push the reviewed changes. Record the implementation and publishing
   executor separately from the configured Git author.
5. Run the full Linux and Windows tests, race detection, vet, repeated shell /
   handler / MCP regressions and both installer suites. The installer suites must
   cover the user-mode default for new installs, automatic retention of one
   existing mode during updates, system/user isolation, ambiguous-mode refusal,
   user-mode install/upgrade/uninstall and rollback of replaced service
   definitions. Build all six targets from the exact clean commit with
   scripts/build-release.ps1 -Version vX.Y.Z.

## Publish and verify

1. Confirm main CI and local release qualification succeeded at the intended
   revision, then create a new annotated version tag. Never move a published tag.
2. Push that tag to run .github/workflows/release.yml. Wait for successful tests,
   deterministic builds, metadata smokes, provenance attestation and publication.
3. Read the non-draft, non-prerelease GitHub Release back. It must contain the six
   supported binaries, LICENSE, THIRD_PARTY_NOTICES.txt, SOURCE.txt and checksums.txt
   from the same tag. SOURCE.txt identifies the exact corresponding source archive.
   Keep license/source links with binary downloads; dependency notices retain their
   original terms. Refresh THIRD_PARTY_NOTICES.txt whenever dependencies change.
4. Independently download every published asset. Check its SHA-256 against the
   manifest, the GitHub asset digest and the local deterministic build; verify
   GitHub provenance. A successful workflow alone is not this verification.
5. Do not replace published release bytes to repair a defect. Publish a new version.

## Deploy the published bytes

1. Obtain authorization for the target machines. Preserve existing configuration,
   credential bytes, credential permissions, service identity and rollback assets.
2. Install only the verified downloaded release binaries. Local development builds,
   commit-suffixed builds and a manually supplied BinaryPath do not establish a
   completed release deployment.
3. Upgrade the server before its stricter client. A security-hardened client may
   require capabilities absent from older servers; never silently downgrade it.
4. Perform each server replacement through an independent controller. Keep all
   background processes invisible and preserve unrelated applications and jobs.
5. Check both installed server and MCP --version, /health version and revision,
   published versus installed hashes, service state and a real authenticated
   operation. Treat a dropped update connection as an unknown result; inspect the
   independent receipt before another mutation.
6. Record each machine separately, and retire only this deployment's temporary
   controller after successful verification. Keep rollback data until healthy.

Do not report the delivery as complete while any version bump, public release,
asset verification or authorized machine upgrade remains missing.
