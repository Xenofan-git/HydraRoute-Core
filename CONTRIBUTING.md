# Contributing

## Required local checks

```sh
gofmt -w $(find cmd internal -type f -name '*.go')
go test -count=1 ./...
go vet ./...
```

The GitHub Actions workflow runs the formatting check, full test suite and `go vet`, then cross-builds static Linux AMD64 and ARM64 binaries for `hrapi`, `hrcore` and `hrparse`. The workflow is committed in the source tree but has not yet run because no dedicated HydraRoute Core remote repository is configured.

## Safety invariants

- Keep HydraRoute Core isolated from existing router, 3x-ui/Xray and other proxy services.
- Never add global routes, policy rules, firewall, DNS or interface mutation to this project.
- Keep all SOCKS inbounds loopback-only.
- Never log or persist subscription URLs, tokens, raw share links or plaintext profile source material.
- Do not enable or install the systemd deployment template on a user's device without explicit approval.
- Any runtime refresh must preserve the last-known-good config and report source failures per profile.


## API origin boundary

Every API read and write endpoint must enforce loopback client/Host checks. If an `Origin` header is present, compare the complete authority (including port) against the request Host; do not accept merely matching `localhost` or loopback hostname. Keep regression tests for DNS rebinding and same-host/different-port requests.
