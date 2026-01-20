# Local Changes Summary

This document summarizes the current local changes in this working tree, pointing to where the code lives and the practical impact of each improvement.

## Runtime and protocol (Go)
- WebSocket backend now supports full lifecycle events and bidirectional commands (send/close/ping/pong), with request tracking for cleanup. Files: `cycletls/index.go`, `cycletls/state/websocket_tracker.go`. Impact: `cycleTLS.ws()` no longer hangs and behaves like a real WebSocket connection.
- Safe channel writer, panic recovery, and request cancellation tracking harden the dispatcher path. Files: `cycletls/index.go`, `cycletls/state/request_tracker.go`. Impact: prevents send-on-closed-channel crashes and allows canceling in-flight work without killing the server.
- Shared buffer pooling extracted into state utilities and reused across dispatchers. Files: `cycletls/state/globals.go`, `cycletls/index.go`. Impact: lower allocations and reduced GC pressure under load.
- Request/response types now include binary bodies and new options (BodyBytes, enableConnectionReuse, protocol). Files: `cycletls/types.go`, `cycletls/index.go`. Impact: binary payloads are preserved and protocol choices are explicit.
- Proxy TLS now respects the user-provided verification setting instead of always skipping validation. Files: `cycletls/connect.go`. Impact: safer proxy TLS behavior and fewer silent security gaps.
- Connection reuse now has LRU eviction, HTTP/3 transport caching, and per-address locking. Files: `cycletls/roundtripper.go`, `cycletls/roundtripper_lru_test.go`, `cycletls/roundtripper_concurrency_test.go`. Impact: prevents unbounded cache growth, reduces connection churn, and avoids race conditions.
- Client pooling uses an FNV-1a key with metadata and cleanup helpers. Files: `cycletls/client.go`, `cycletls/client_test.go`. Impact: faster deterministic reuse and easier pool hygiene.
- New protocol encoder and orchestration helpers added as scaffolding for future refactors. Files: `cycletls/server/protocol/encoder.go`, `cycletls/orchestration/browser.go`, `cycletls/tests/unit/protocol_encoder_test.go`. Impact: isolates binary framing logic and reduces duplication when wiring new paths.

## TypeScript client (Node)
- WebSocket API now matches ws-style EventEmitter behavior with readyState, send/close/ping/pong, and lifecycle events. Files: `src/index.ts`. Impact: the JS API is drop-in compatible with the ws library patterns.
- Response handling adds stream/arraybuffer/blob support and request timeouts. Files: `src/index.ts`. Impact: avoids hanging promises and enables streaming or large-payload workflows.
- PacketBuffer reads 64-bit lengths with BigInt. Files: `src/index.ts`. Impact: prevents data corruption when frame sizes exceed 32-bit ranges.
- InstanceManager now guards against duplicate process creation and cleans up shared instances. Files: `src/index.ts`. Impact: fewer port races and more reliable process lifecycle management.

## Tests, CI, and docs
- New unit tests cover state tracking, protocol encoding, and client key behavior. Files: `cycletls/tests/unit/state_globals_test.go`, `cycletls/tests/unit/state_request_tracker_test.go`, `cycletls/tests/unit/state_websocket_tracker_test.go`, `cycletls/tests/unit/protocol_encoder_test.go`, `cycletls/client_test.go`.
- QUIC/HTTP3 integration tests expanded with clearer diagnostics. Files: `cycletls/tests/integration/quic_test.go`.
- WebSocket manual test scripts added for local validation. Files: `test_websocket_new.js`, `test_websocket_debug.js`.
- CI workflows updated to newer action versions, Go 1.24, Node 20, and security scanning. Files: `.github/workflows/golang.yml`, `.github/workflows/test_golang.yml`, `.github/workflows/test_npm.yml`, `.github/workflows/test_npm_publish.yml`, `.github/workflows/publish.yml`, `.github/workflows/manual_publish.yml`, `.github/workflows/security.yml`, `src/go.mod`.
- New documentation artifacts summarize the changes and planned work. Files: `RELEASE_NOTES_2.0.6.md`, `WEBSOCKET_IMPLEMENTATION_SUMMARY.md`, `IMPROVEMENT_PLAN.md`.
