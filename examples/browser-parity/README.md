# Browser Parity Demo

This example is for the forked build that tightens Chrome-style TLS, HTTP/2, and header-order behavior.

It does two things:

- probes `tls.peet.ws` to show the emitted fingerprint
- optionally requests a protected target URL from `TARGET_URL`

## Run

```bash
npm install
npm run build
node examples/browser-parity/demo.js
```

## Use The Patched Binary

Point the demo at the rebuilt fork binary:

```bash
set CYCLETLS_EXECUTABLE_PATH=C:\path\to\CycleTLS\dist\index.exe
node examples/browser-parity/demo.js
```

The demo prefers the repo's local `dist/index.js` build. If that file is not present, it falls back to the published `cycletls` package.

## Optional Protected Target

If you want to test a real gated endpoint:

```bash
set TARGET_URL=https://example.com/protected-resource
node examples/browser-parity/demo.js
```

## Expected Output Shape

```text
{
  "probe": {
    "ip": "198.51.100.x:port",
    "http_version": "h2",
    "ja3": "...",
    "ja4_r": "...",
    "akamai_fingerprint": "1:65536;2:0;4:6291456;6:262144|15663105|0|m,a,s,p"
  },
  "target": {
    "status": 200
  }
}
```

If the target is bound to a specific egress path, matching the TLS fingerprint may still not be enough. In that case, compare `probe.ip` with the network path used by the working browser.
