# Cutting a release

Releases are tag-driven: pushing a `v*` tag is the only step a maintainer takes.

## Cut a release

Before tagging, bump the composite action's download default so the tag
and `action.yml` agree — the workflow fails the release if they drift:

```sh
# in the same change as the release:
#   action.yml: inputs.version.default → vX.Y.Z
```

```sh
git tag vX.Y.Z
git push origin vX.Y.Z
```

The [`release` workflow](../.github/workflows/release.yml) then runs automatically:

1. **Build** — a matrix job builds static binaries for `linux/amd64`,
   `linux/arm64`, and `darwin/arm64` with
   `CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o cite-<goos>-<goarch> ./cmd/cite`.
   The flags make the builds static, path-independent, and reproducible.
2. **Attest** — each binary gets a signed SLSA build provenance attestation
   (via `actions/attest-build-provenance`) published to the repository's
   attestation store.
3. **Publish** — a final job downloads the build artifacts and attaches every
   binary to the GitHub release for the tag, with generated release notes.

Every action step in the workflow is pinned to a full-length commit SHA.

## Verify a release as a consumer

Download the binary for your platform from the release page, then verify its
provenance against this repository:

```sh
gh attestation verify cite-linux-amd64 -R elecnix/cite
```

A successful verification proves the binary was built by this repository's
release workflow from the tagged commit — it was not substituted or rebuilt
somewhere else.

## Why SHA pinning matters

Consumers are expected to pin the cite Action to a release's full commit SHA
(`uses: elecnix/cite@<full-sha>`) rather than to a moving tag. A mutable tag
is one compromised release away from fleet-wide code execution in every
consumer's privileged CI context; a full SHA is not. Each release's SHA and
build provenance make that pin verifiable end to end.
