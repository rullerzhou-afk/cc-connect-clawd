# Clawd Sidecar Release

This fork builds the Clawd Telegram approval sidecar from `./cmd/cc-connect-clawd`.
The output layout must match Clawd's resolver and packaging checks:

```text
dist/clawd-sidecar/
  windows-x64/cc-connect-clawd.exe
  windows-arm64/cc-connect-clawd.exe
  darwin-x64/cc-connect-clawd
  darwin-arm64/cc-connect-clawd
  linux-x64/cc-connect-clawd
  cc-connect-clawd-windows-x64.zip
  cc-connect-clawd-windows-arm64.zip
  cc-connect-clawd-darwin-x64.tar.gz
  cc-connect-clawd-darwin-arm64.tar.gz
  cc-connect-clawd-linux-x64.tar.gz
  checksums.txt
```

Use the checked-in helper to keep Go target names separate from Clawd directory
names. In particular, Go `amd64` maps to Clawd `x64`. The JSON manifest uses
slash-normalized paths so it can be consumed across Windows and Unix runners.

```bash
go run ./cmd/clawd-sidecar-release --dry-run
go run ./cmd/clawd-sidecar-release --target windows-x64
go run ./cmd/clawd-sidecar-release
```

PowerShell uses the same `go run` commands:

```powershell
go run ./cmd/clawd-sidecar-release --dry-run
go run ./cmd/clawd-sidecar-release --target windows-x64
go run ./cmd/clawd-sidecar-release --go "C:\Program Files\Go\bin\go.exe" --target windows-x64
```

The Makefile wrappers call the same helper when GNU make is available:

```bash
make release-clawd-sidecar-manifest
make release-clawd-sidecar CLAWD_SIDECAR_TARGET=windows-x64
```

`CGO_ENABLED=0` is set by the helper for every build target. The build command
uses a minimal environment allowlist plus `GOOS` / `GOARCH`, so user shell
values such as `GOFLAGS`, `GOPROXY`, and `GOPRIVATE` are not inherited.

Each sidecar binary supports:

```bash
cc-connect-clawd --version
```

The release helper injects version, commit, and build time. Override them from
CI with `--build-version`, `--build-commit`, and `--build-time` when building
from a detached checkout or a fixed release tag.

## Release Flow

Short term, publish sidecar archives from the `cc-connect-clawd` fork release or
from CI build artifacts for a fixed fork commit/tag. Clawd CI should verify
`checksums.txt`, extract or copy that fixed version, and place the binaries into:

```text
bin/cc-connect-clawd/<platform-arch>/cc-connect-clawd(.exe)
```

Do not consume upstream `cc-connect` latest artifacts for Clawd builds. A future
upstream pull request can be considered after the sidecar interface stabilizes,
but that is outside this packaging step.
