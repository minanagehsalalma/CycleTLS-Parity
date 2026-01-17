# CycleTLS Streaming Protocol

## Overview

CycleTLS v2.0.6+ uses a modern streaming protocol by default that provides credit-based backpressure for HTTP requests. This prevents memory explosion when downloading large files or handling high-throughput workloads.

## The Problem

The legacy protocol uses a single multiplexed WebSocket with no flow control:

```
Client ──── 1 WebSocket ────→ Server
            JSON messages       reads at max speed
            multiplexed         buffers fill up
                               → OOM crash
```

When downloading large files, the server reads and sends data as fast as possible. If the client can't keep up (slow disk, network bottleneck, processing delay), data buffers grow unbounded until the process runs out of memory.

## The Solution: Credit-Based Flow Control

The modern CycleTLS protocol introduces TCP-like flow control at the application layer:

```
Client ──── 1 WebSocket/Request ────→ Server
            Binary protocol            waits for credits
            Credit flow control        before sending
                                      → bounded memory
```

**How it works:**

1. Client opens WebSocket with initial credit window (e.g., 64KB)
2. Server sends data up to the credit limit, then **blocks**
3. Client consumes data and sends `credit` packets to replenish
4. Server resumes sending when credits are available

This ensures the server never sends more data than the client can handle.

## Usage

### TypeScript / JavaScript

```typescript
import CycleTLS from 'cycletls';

// Create a CycleTLS client
const client = new CycleTLS({
  port: 9119,              // Server port (default: 9119)
  initialWindow: 65536,    // Initial credit window in bytes (default: 64KB)
  creditThreshold: 32768,  // Replenish credits when this many bytes consumed
  autoSpawn: true,         // Auto-start server if not running
  debug: false,            // Enable debug logging
  timeout: 30000,          // Request timeout in ms
});

// Make a request with flow control
const response = await client.request({
  url: 'https://example.com/large-file.zip',
  method: 'GET',
  headers: { 'User-Agent': 'MyApp/1.0' },
  ja3: '771,4865-4866-4867...',  // Optional JA3 fingerprint
});

console.log(`Status: ${response.statusCode}`);
console.log(`Final URL: ${response.finalUrl}`);

// Stream the response body with backpressure
for await (const chunk of response.body) {
  await processChunk(chunk);  // Slow processing is OK!
  // Credits are automatically replenished as you consume data
}

// Clean up when done
await client.close();
```

### Convenience Methods

```typescript
// GET request
const response = await client.get('https://example.com/data');

// POST request
const response = await client.post('https://api.example.com/upload', jsonBody, {
  headers: { 'Content-Type': 'application/json' }
});
```

### Request Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `url` | string | required | Target URL |
| `method` | string | `"GET"` | HTTP method |
| `headers` | object | `{}` | Request headers |
| `body` | string | `""` | Request body |
| `ja3` | string | - | JA3 fingerprint |
| `ja4r` | string | - | JA4R raw fingerprint |
| `userAgent` | string | - | User-Agent header |
| `proxy` | string | - | Proxy URL |
| `timeout` | number | - | Request timeout (ms) |
| `disableRedirect` | boolean | `false` | Don't follow redirects |
| `insecureSkipVerify` | boolean | `false` | Skip TLS verification |
| `forceHTTP1` | boolean | `false` | Force HTTP/1.1 |
| `forceHTTP3` | boolean | `false` | Force HTTP/3 (QUIC) |

### Client Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `port` | number | `9119` | Server port |
| `initialWindow` | number | `65536` | Initial credit window (bytes) |
| `creditThreshold` | number | `initialWindow/2` | When to replenish credits |
| `autoSpawn` | boolean | `true` | Auto-start server |
| `executablePath` | string | - | Custom server executable path |
| `debug` | boolean | `false` | Enable debug logging |
| `timeout` | number | `30000` | Default request timeout (ms) |

## Protocol Details

### Version Routing

The Go server uses a query parameter for version routing:

| URL | Protocol | Description |
|-----|----------|-------------|
| `ws://localhost:9119` | Modern (default) | Binary, flow control, 1 WS/request |
| `ws://localhost:9119?v=2` | Modern | Explicit modern selection |
| `ws://localhost:9119?v=1` | Legacy | JSON, multiplexed, no flow control |

### Binary Frame Format

The modern protocol uses a length-prefixed binary format:

**Init Packet (Client → Server):**
```
[2B: requestId.length][requestId string]
[2B: "init".length]["init"]
[4B: initial credit window]
[2B: options.length][JSON options]
```

**Credit Packet (Client → Server):**
```
[2B: requestId.length][requestId string]
[2B: "credit".length]["credit"]
[4B: credit amount]
```

**Response Frame (Server → Client):**
```
[2B: requestId.length][requestId string]
[2B: "response".length]["response"]
[4B: payload.length][JSON: {statusCode, finalUrl, headers}]
```

**Data Frame (Server → Client):**
```
[2B: requestId.length][requestId string]
[2B: "data".length]["data"]
[4B: chunk.length][chunk bytes]
```

**End Frame (Server → Client):**
```
[2B: requestId.length][requestId string]
[2B: "end".length]["end"]
```

**Error Frame (Server → Client):**
```
[2B: requestId.length][requestId string]
[2B: "error".length]["error"]
[4B: payload.length][JSON: {statusCode, message}]
```

## Backward Compatibility

### For Existing Users

The legacy API is available via the `Legacy` export:

```typescript
import { Legacy } from 'cycletls';

const cycleTLS = await Legacy();
const response = await cycleTLS('https://example.com', {});
// Works exactly as before - internally uses ?v=1 legacy protocol
```

### Migrating from Legacy

If you're downloading large files or experiencing memory issues:

```typescript
// Before (Legacy - may OOM on large files)
import { Legacy } from 'cycletls';
const cycleTLS = await Legacy();
const response = await cycleTLS('https://example.com/huge.zip', {});
const body = response.body; // Entire body buffered in memory!

// After (Modern - bounded memory)
import CycleTLS from 'cycletls';
const client = new CycleTLS();
const response = await client.request({ url: 'https://example.com/huge.zip' });
for await (const chunk of response.body) {
  // Process chunks as they arrive
  // Memory stays bounded regardless of file size
}
```

## Architecture

### Legacy
```
TypeScript Legacy()
    │
    ├── WebSocket (multiplexed)
    │       │
    │       ▼
    │   Go Server (readSocket)
    │       │
    │       ├── goroutine per request
    │       │
    │       └── chanWrite (shared channel)
```

### Modern (CycleTLS)
```
TypeScript new CycleTLS()
    │
    └── WebSocket (per request)
            │
            ▼
        Go Server (handleWSRequestV2)
            │
            ├── errgroup coordination
            │
            ├── creditWindow semaphore
            │
            └── context cancellation
```

## Performance Considerations

- **WebSocket overhead**: ~1-3ms per request for localhost connections
- **Credit window size**: Larger = higher throughput, smaller = less memory
- **Credit threshold**: Lower = more frequent credit packets, smoother flow

**Recommended settings:**
- Standard downloads: `initialWindow: 65536` (64KB)
- Large file streaming: `initialWindow: 262144` (256KB)
- Memory-constrained: `initialWindow: 16384` (16KB)

## Troubleshooting

### Server not responding
```typescript
const client = new CycleTLS({ debug: true });
// Check console for connection logs
```

### Request timeout
```typescript
const client = new CycleTLS({ timeout: 60000 }); // 60 seconds
```

### Memory still growing
- Ensure you're consuming the response body stream
- Use smaller `initialWindow` if client processing is slow
- Check that `creditThreshold` triggers replenishment

## Files Reference

### Go Server
| File | Purpose |
|------|---------|
| `cycletls/flow_control.go` | Credit window semaphore with context support |
| `cycletls/packet_builder.go` | Binary frame encoding |
| `cycletls/packet_reader.go` | Binary frame decoding |
| `cycletls/ws_handler_v2.go` | V2 request handler with errgroup |
| `cycletls/index.go:1480-1499` | Version routing logic |

### TypeScript Client
| File | Purpose |
|------|---------|
| `src/flow-control-client.ts` | CycleTLS class |
| `src/protocol.ts` | Binary protocol helpers |
| `src/credit-manager.ts` | Credit replenishment logic |

### Tests
| File | Coverage |
|------|----------|
| `cycletls/flow_control_test.go` | Credit window unit tests |
| `cycletls/packet_test.go` | Protocol encoding/decoding |
| `tests/flow-control.test.ts` | TypeScript integration tests |
