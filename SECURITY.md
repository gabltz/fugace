# Security policy

## Reporting a vulnerability

Please report vulnerabilities **privately** through GitHub: **Security → Report a vulnerability** on this repository (private vulnerability reporting). Do not open a public issue.

You can expect an acknowledgement within 7 days, and a fix or mitigation aimed for within 30 days, depending on severity. Please include the version (`fugace version`, or the image digest), your configuration (without secrets) and the steps to reproduce.

## Supported versions

Only the latest minor release receives security fixes.

## Scope

The threats fugace defends against, and the limits it accepts, are written down in [`docs/THREAT_MODEL.md`](docs/THREAT_MODEL.md), in French. Each threat maps to tests that run in CI. The following are documented limits, not vulnerabilities:

- an IRC client keeping local logs;
- plain-text traffic on port 6667;
- RAM being readable by root on the host while the container runs;
- users accepting a self-signed certificate without checking it.

## Verifying a release

Images are signed keylessly with cosign and ship with an SBOM and a provenance attestation:

```sh
cosign verify ghcr.io/gabltz/fugace:<version> \
  --certificate-identity-regexp 'https://github.com/gabltz/fugace/.github/workflows/release.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```
