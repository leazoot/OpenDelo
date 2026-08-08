# Changelog

All notable changes to OpenDelo are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Entries describe what changed **for someone using the product**. Internal
refactors appear only when they change a guarantee, a command, or a file on disk.

## [Unreleased]

## [0.1.0] — 2026-08-08

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
- **A request could arrive at an open console and never appear.** Opening the gate
  starts a fetch of the list; a request that arrived while that fetch was still out
  was written into the cache by the push and then overwritten when the fetch landed
  carrying the list as it had been. Nothing fetched again, so the seam kept saying no
  one was waiting while the gateway held the request. Slower machines lost it more
  often; on a fast one it was invisible.
- **"Allow until the task ends" granted exactly one call.** It inherited the default
  request count of one, which is what "allow just this once" grants, so the second
  call landed back at the seam. The approval now sets the count explicitly; the lease
  is still bound to the session and still expires on its own.
- **An approval no longer answered a later identical call.** The decision chain read
  only learned rules, never the authorisation it had just issued, so the same request
  asked again in the same session. High-risk operations are excluded: that they always
  need a person has no exception.
- **A request forwarded through the proxy left no trace of having been executed.**
  Only the MCP face recorded an execution; the proxy left an access log, and a log is
  not an audit. Both faces now record the same event, and the ledger carries the
  status the external service actually answered rather than the 200/502 the agent sees.
- **A request could take twenty seconds to appear at the seam on WebKit.** The gateway
  writes each event in a single call, and the WebKit build in our browser matrix hands
  the page only the beginning of a large write — a 2 897-byte arrival arrived as its
  first 123 bytes — withholding the rest until more traffic reaches the connection.
  On a quiet stream that is the twenty-second heartbeat. Until then the console held
  half a JSON document: nothing to parse, nothing to report, and a seam that said no
  one was waiting. Each event is now followed by a short comment frame that carries
  the tail out.
- **The ledger's spine ran through the agent names.** Browsers indent lists by 40px
  and the reset never cleared it, so every row sat 40px right of the spine drawn
  behind them.
- **Ctrl-C reported a failure whenever the console was open.** A subscribed event
  stream held the graceful shutdown for its full grace period and the process exited
  with status 1. Shutdown now closes the stream first, and a grace that runs out
  forces the remaining connections closed with a warning instead of failing.
- Windows database paths are now turned into valid file URIs.

### Security

- `react-router` 7.9.6 → 8.3.0 and Playwright 1.48.2 → 1.62.1, closing every known
  high advisory in both pnpm projects. The end-to-end project had never been audited
  at all, and had drifted fourteen minor versions behind while shipping an advisory
  about downloading browsers without verifying certificates.
- An agent asking for a credential is judged in one place again. The MCP face kept a
  second keyword list of its own, which missed `read_api_key`, `read_private_key`,
  `read_passphrase` and `read_keychain_item` entirely.

## [0.1.0-rc.3] — 2026-07-31

Release-pipeline rehearsal. No user-facing change beyond the Windows path fix above.

## [0.1.0-rc.2] — 2026-07-31

First rehearsal of the release pipeline.

[Unreleased]: https://github.com/leazoot/OpenDelo/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/leazoot/OpenDelo/compare/v0.1.0-rc.3...v0.1.0
[0.1.0-rc.3]: https://github.com/leazoot/OpenDelo/compare/v0.1.0-rc.2...v0.1.0-rc.3
[0.1.0-rc.2]: https://github.com/leazoot/OpenDelo/releases/tag/v0.1.0-rc.2
