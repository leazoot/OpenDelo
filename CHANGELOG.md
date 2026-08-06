# Changelog

All notable changes to OpenDelo are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Entries describe what changed **for someone using the product**. Internal
refactors appear only when they change a guarantee, a command, or a file on disk.

## [Unreleased]

### Added

- **End-to-end suite.** Every one of the ten success criteria in the PRD runs against
  a real binary: real decision chain, real leases, real audit, real credential
  retrieval from a fake `op` on `PATH`. Outbound traffic only ever reaches local fake
  services — the release build has no way to point elsewhere.
- **Security acceptance.** Eight surfaces are scanned for credential sentinels in one
  pass (agent context, agent environment, MCP responses, logs, every file in the data
  directory, console DOM, approval text, ledger and its exports). Each of the five
  zero-metrics has its own case, and every negative assertion is preceded by proof
  that the thing being denied really happened.
- **Performance baselines.** Six budgets from REQ-NFR-001, each enforced rather than
  merely printed: decision chain P95, approval-to-console P95, Gate first paint P95,
  ledger query P95 over 100 000 records, lease countdown drift, and the gzipped
  initial bundle. `make bench` and `make bundle`.
- **Browser compatibility.** The five main pages, the core approve-from-the-keyboard
  flow, and the unsupported-browser path run on Chromium, Firefox, WebKit and Edge.
- **A message for browsers that cannot run the console.** Previously a blank page.
  Now a static notice that needs neither the bundle nor an inline script, so it
  survives both "no ES modules" and "JavaScript disabled".
- **Distributable binaries.** `make dist` cross-compiles macOS (arm64/amd64),
  Linux (amd64/arm64) and Windows (amd64) with the console embedded, plus
  `SHA256SUMS`. Nothing links against C, so any host can build all of them.
- **Documentation link check.** `make links` fails on internal links that no longer
  resolve, including section anchors.

### Fixed

- **Requests from agents never reached an open console.** The arrival event was
  published only by the Web API's own submit endpoint, while real agents arrive over
  MCP or the proxy. Since the Gate list deliberately does not poll, a request could
  sit at the seam and never appear until the page was reloaded. The notification now
  lives on the decision path itself, which all three faces share.
- **Cards at the seam could not be decided.** The arrival event carried a request
  view with no approval id and no available actions, so the card rendered but neither
  click nor keystroke did anything.
- **Every MCP tool call and every proxied request failed.** Both faces left
  `operation_id` empty, and the decision chain treats a request it cannot trace as
  invalid input — so nothing executed and nothing was recorded.
- **The first approval decision crashed the gateway.** One of the two assembly paths
  built the endpoints without an event broker; publishing the result dereferenced nil.
- **Learned authorisations stopped working after a second account was connected.**
  Trust memories were loaded after identity matching instead of before, so they never
  had a chance to say which identity this project had been using.
- **Ctrl-C reported a failure whenever the console was open.** A subscribed event
  stream held the graceful shutdown for its full grace period and the process exited
  with status 1. Shutdown now closes the stream first, and a grace that runs out
  forces the remaining connections closed with a warning instead of failing.
- Windows database paths are now turned into valid file URIs.

### Security

- `react-router` 7.9.6 → 7.18.2, closing seven high and six moderate advisories.
  One high advisory remains; it requires the 8.x major upgrade and is tracked
  separately.

## [0.1.0-rc.3] — 2026-07-31

Release-pipeline rehearsal. No user-facing change beyond the Windows path fix above.

## [0.1.0-rc.2] — 2026-07-31

First rehearsal of the release pipeline.

[Unreleased]: https://github.com/leazoot/OpenDelo/compare/v0.1.0-rc.3...HEAD
[0.1.0-rc.3]: https://github.com/leazoot/OpenDelo/compare/v0.1.0-rc.2...v0.1.0-rc.3
[0.1.0-rc.2]: https://github.com/leazoot/OpenDelo/releases/tag/v0.1.0-rc.2
