# Cutting a release

Releases are tag-driven: the version bump is merged to `main`, and the tag is
pushed onto that commit.

## Cut a release

Merge the version bump first. `scripts/version-check.sh` compares the three
values below and reports every disagreement:

- `VERSION` at the repository root, which the composite action reads to decide
  which binary it downloads
  ([issue #94](https://github.com/elecnix/cite/issues/94));
- `const version` in `cmd/cite/main.go`, which is compiled into the binary and
  printed by `cite version`;
- the tag, added to the comparison when the release workflow runs.

Refresh the documented `uses: elecnix/cite@<full-sha>  # vX.Y.Z` pins in the
same commit or the next one. `scripts/action-pins_test.sh` only warns about a
lagging pin, because a pin can only specify a tag that already exists.

Tag the bump commit itself:

```sh
git tag vX.Y.Z
git push origin vX.Y.Z
```

The [`release` workflow](../.github/workflows/release.yml) then runs on its own:

1. **Verify**: `scripts/version-check.sh` compares the tag, `VERSION`, and
   `cmd/cite/main.go`. A tag pushed against a tree where they disagree stops
   the workflow before any binary is built.
2. **Build**: a matrix job builds static binaries for `linux/amd64`,
   `linux/arm64`, and `darwin/arm64` with
   `CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o cite-<goos>-<goarch> ./cmd/cite`.
   The flags make the builds static, path-independent, and reproducible.
3. **Attest**: each binary gets a signed SLSA build provenance attestation
   (via `actions/attest-build-provenance`) published to the repository's
   attestation store.
4. **Publish**: a final job downloads the build artifacts and attaches every
   binary to the GitHub release for the tag, with generated release notes.

Every action step in the workflow is pinned to a full-length commit SHA.

## Which release a consumer runs

The `version:` input decides, and an unset input means the release this action
belongs to:

| `with: version:` | binary downloaded |
| --- | --- |
| unset (the default) | the release this action belongs to, read from the `VERSION` file checked out with the action |
| `vX.Y.Z` | that release |
| `latest` | the newest published release, deliberately floating |

An unset input resolves to a tag, so `uses: elecnix/cite@<full-sha>` pins the
action and the binary together. The pinning advice below asks for that pair,
and it is why the default is not `latest`. A full-SHA pin used to download
whatever release was published last, so the reviewed code and the pin could
disagree without a word in the log (issue #94). To track the newest release
without editing the workflow, set `version: latest` and take the floating
behaviour on purpose.

A pin on a branch rather than a tag (`elecnix/cite@main`) reads `VERSION` on the
branch, which specifies the last released version, or the upcoming one once its
bump commit is merged. Between that commit and the tag push, the download has no
asset to fetch and the run fails with the URL in the log, so tag the bump commit
promptly.

## Verify a release as a consumer

Download the binary for your platform from the release page, then verify its
provenance against this repository:

```sh
gh attestation verify cite-linux-amd64 -R elecnix/cite
```

A successful verification proves the binary was built by this repository's
release workflow from the tagged commit, not substituted or rebuilt somewhere
else.

## Why SHA pinning matters

Consumers are expected to pin the cite Action to a release's full commit SHA
(`uses: elecnix/cite@<full-sha>`) rather than to a moving tag. A mutable tag
is one compromised release away from fleet-wide code execution in every
consumer's privileged CI context. A full SHA is not. Each release's SHA and
build provenance make that pin verifiable end to end.

The binary follows the same pin. `action.yml` hands the download to
`scripts/download-cite.sh`, which resolves an unset `version:` from the `VERSION`
file checked out next to the action, so a SHA pin and the binary it runs come
from one commit.
