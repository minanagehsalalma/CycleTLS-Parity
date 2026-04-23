# Signed HLS Case Study: From `403` to `200`

This fork documents a real-world debugging pass where a signed HLS playlist returned `200` in Chromium but `403` in `CycleTLS`, even after basic header spoofing.

## Goal

Make `CycleTLS` behave closely enough to a modern Chrome navigation request that a signed `.m3u8` playlist could be fetched successfully outside the browser path.

## What Changed

### TLS / ClientHello parity

- added a deterministic modern Chrome-style `ClientHelloSpec`
- aligned extension ordering with observed Chromium wire behavior
- preserved GREASE, ALPN, ALPS, ECH GREASE, Brotli cert compression, and modern key share ordering

### HTTP/2 parity

- matched Chromium-style HTTP/2 settings:
  - `1:65536;2:0;4:6291456;6:262144`
- matched Chromium-style connection flow increment:
  - `15663105`
- matched Chromium-style HEADERS priority:
  - `exclusive=true`
  - `streamDep=0`
  - `weight=255`

### Header ordering

- preserved explicit regular header ordering
- preserved lowercase `user-agent` handling on the HTTP/2 path
- removed remarshal behavior that could reorder headers after request construction

## What We Learned

TLS and HTTP/2 fingerprint parity alone was not enough.

The final blocker turned out to be egress routing:

- local Chrome was routed through a split-tunneled NordVPN path
- `CycleTLS` was initially routed through the default system path
- the signed HLS token was bound to the VPN egress range

Once the `CycleTLS` binary was added to NordVPN split tunneling, the same signed HLS flow started returning `200`.

## Verified Outcome

With the patched binary and matching VPN egress:

- `tls.peet.ws` reported the expected Chrome-like `h2` fingerprint
- the signed HLS playlist returned `200`
- response content-type was `application/vnd.apple.mpegurl`

## Why This Matters

This fork is a concrete example of the gap between:

- synthetic browser fingerprint spoofing
- and full real-world request equivalence

In practice, access decisions can depend on:

- TLS shape
- HTTP/2 framing
- header order
- and network path / egress identity

This case study demonstrates all four.
