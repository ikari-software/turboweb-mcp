# Security

TurboWeb MCP hands an AI agent the ability to drive your live browser tab —
clicks, typing, screenshots, DOM reads. That is a useful capability and a
worth-thinking-about attack surface. This document explains what the
project defends against, what it does not, and how to report a vulnerability.

## Threat model

The project assumes:

- The **user** runs the extension and the daemon on their own machine.
- The **agent** (Claude Code, Cursor, anything that speaks MCP) is a
  trusted-but-fallible client of the local MCP server.
- The **browser** loads arbitrary pages, some of which may be hostile.
- The **network** is hostile.

It does not assume:

- That every fork of the project on the internet is the real one.
- That every MCP client is well-behaved (an agent might be tricked by a
  page into trying something the user didn't intend).

## What's defended

| Vector | Defence |
|---|---|
| Page exfiltrating tool output to an attacker server | The MCP daemon binds only to `127.0.0.1`; tool calls do not traverse the network. The extension WebSocket only connects to `ws://127.0.0.1:18321/`. |
| Hostile page injecting code into the content script | Content script runs in Chrome's isolated world; the on-page overlay uses a closed Shadow DOM so the page can't reach it. |
| Hidden agent activity | Every tool call requires an `intent` argument. The extension popup shows the live activity log; the on-page overlay shows a toast + animated cursor at every action, click-through but unmissable. The agent cannot suppress the overlay. |
| Trojan extension impersonating the real one | The brand name "TurboWeb MCP", the icon set, and the cursor overlay are reserved (see `TRADEMARK.md`). Release binaries are signed via Sigstore — verification instructions below. |
| Replay attacks on the local WS | Localhost-only, no remote surface. |
| Stale daemon serving outdated code after a rebuild | The daemon exposes `/version` with a binary modtime; new MCP instances detect a stale daemon and respawn it. |

## What's NOT defended

| Vector | Why |
|---|---|
| **A user sideloads a malicious fork that calls itself something else.** | A malicious fork can copy the code under Apache 2.0 — that's a feature of OSS. The trademark policy stops it from calling itself "TurboWeb MCP", and signed releases let users verify they have the real binary. Beyond that the user has to read what they install. |
| **The user's MCP client is compromised.** | If the agent itself is malicious or compromised, it speaks the protocol legitimately; the extension dutifully clicks where it's told. The on-page overlay still surfaces every action, but a fast attacker could do real damage in the window between display and human reaction. |
| **A page that the agent visits attacks the extension.** | The content script reads page DOM; if the page intentionally returns malicious DOM content designed to confuse the agent, the agent may make bad decisions. This is an agent-side problem; we expose the data faithfully. |
| **The user explicitly disables the on-page overlay.** | The overlay is the safety signal. Killing it removes the user's visibility into what the agent is doing. |

## macOS Gatekeeper (automatic — no action needed)

The macOS (`darwin-arm64`) release binary is signed with a **Developer ID
Application** certificate and notarized with Apple's OCSP service by the
release CI workflow. Gatekeeper verifies the notarization ticket on first
run and accepts the binary automatically — no quarantine dialog, no
`xattr -d com.apple.quarantine`, no manual codesign check needed.

## Verifying a release (supply-chain assurance)

For users who want to confirm that a binary came from *this repo's CI
pipeline* rather than a third party, release assets are signed via
[Sigstore / cosign](https://www.sigstore.dev) using GitHub's keyless OIDC —
no long-lived signing keys, no private key material to leak. Each release
ships:

- `SHA256SUMS` — checksums for every asset.
- `SHA256SUMS.sig` + `SHA256SUMS.crt` — cosign signature + signing
  certificate (issued by Fulcio for this repo's release workflow).
- `SHA256SUMS.cosign.bundle` — combined verification material for
  offline use.

Verify a download:

```sh
# 1. Grab the binary you want + the three SHA256SUMS files.
gh release download v1.0.0 -p 'turboweb-mcp-by-ikari-darwin-arm64' \
                           -p 'SHA256SUMS*'

# 2. Confirm the signature is from this repo's release workflow.
cosign verify-blob \
  --certificate SHA256SUMS.crt \
  --signature SHA256SUMS.sig \
  --certificate-identity-regexp '^https://github.com/ikari-software/turboweb-mcp/\.github/workflows/release\.yml@' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS

# 3. Confirm your binary's hash matches what was signed.
#    macOS:
shasum -a 256 -c SHA256SUMS --ignore-missing
#    Linux:
sha256sum -c SHA256SUMS --ignore-missing
```

If step 2 fails with a non-matching certificate identity, the file did
not come from this repo's release workflow. If step 3 reports a checksum
mismatch, the binary has been tampered with after signing. In either
case, **do not run the binary** — open an issue.

## Reporting a vulnerability

Email the maintainer at the address in the GitHub profile, or open a
[private security advisory on GitHub](https://github.com/ikari-software/turboweb-mcp/security/advisories/new).
Please do not file public issues for security bugs.

We aim to acknowledge reports within 72 hours and ship a fix or
mitigation within 7 days for high-severity issues.
