# Release Process

flowstate is distributed as a source build: clone the repository and run `make
build` to produce `bin/flowstate`. There is no hosted package channel in this
fork.

## Build from source

```bash
make build      # produces bin/flowstate
./bin/flowstate --version
```

CI requires a clean `gofmt -l .`, `make test`, and `make build` before changes
land.

## Optional: tagged release archives

The GoReleaser configuration (`.goreleaser.yml`) is retained for producing
tagged-release archives if you choose to publish binaries from your fork. It
builds `flowstate` for darwin/linux on amd64/arm64 and writes `tar.gz` archives
plus a checksums file. The Homebrew cask publishing step was removed with the
move to source-only distribution.

1. Land the release changes on your default branch.
2. Optional local check if GoReleaser is installed:

   ```bash
   goreleaser release --snapshot --clean --skip=publish
   ```

3. Create and push the annotated tag (replace `vX.Y.Z` with the next semver
   tag):

   ```bash
   git tag -a vX.Y.Z -m "vX.Y.Z"
   git push origin vX.Y.Z
   ```

4. Watch the `Release` workflow for the tag.

## Release verification checklist

If you publish tagged archives, confirm the following (substitute the release
version for `X.Y.Z`):

1. The release has these artifacts:
   - `flowstate_X.Y.Z_darwin_amd64.tar.gz`
   - `flowstate_X.Y.Z_darwin_arm64.tar.gz`
   - `flowstate_X.Y.Z_linux_amd64.tar.gz`
   - `flowstate_X.Y.Z_linux_arm64.tar.gz`
   - `flowstate_X.Y.Z_checksums.txt`
2. The release notes were generated from commits since the previous tag.
3. Each archive extracts a `flowstate` binary that reports the expected
   `flowstate --version`.
