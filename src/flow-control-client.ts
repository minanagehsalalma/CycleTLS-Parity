/**
 * CycleTLS - Modern HTTP Client with Flow Control
 *
 * Provides backpressure-aware HTTP requests using the V2 binary protocol.
 * Each request opens a dedicated WebSocket connection with credit-based
 * flow control for memory-efficient large file downloads.
 */

import { spawn, ChildProcess, SpawnOptionsWithoutStdio } from "child_process";
import WebSocket from "ws";
import path from "path";
import os from "os";
import fs from "fs";
import { Readable } from "stream";
import { EventEmitter } from "events";
import { Blob } from "buffer";

import {
  buildInitPacket,
  buildCreditPacket,
  parseFrame,
  parseResponsePayload,
  parseDataPayload,
  parseErrorPayload,
  parseWebSocketOpenPayload,
  parseWebSocketMessagePayload,
  parseWebSocketClosePayload,
  buildWebSocketSendPacket,
  buildWebSocketClosePacket,
} from "./protocol";
import { CreditManager } from "./credit-manager";

/**
 * Options for CycleTLS client configuration.
 */
export interface CycleTLSOptions {
  /** Port for the Go server (default: 9119) */
  port?: number;
  /** Enable debug logging */
  debug?: boolean;
  /** Request timeout in milliseconds */
  timeout?: number;
  /** Path to CycleTLS executable */
  executablePath?: string;
  /** Auto-spawn server if not running (default: true) */
  autoSpawn?: boolean;
  /** Initial credit window in bytes (default: 65536) */
  initialWindow?: number;
  /** Credit replenishment threshold in bytes (default: initialWindow/2) */
  creditThreshold?: number;
}

/**
 * Cookie to send with the request.
 */
export interface RequestCookie {
  name: string;
  value: string;
  path?: string;
  domain?: string;
}

/**
 * Request options for CycleTLS requests.
 */
export interface RequestOptions {
  /** Target URL */
  url: string;
  /** HTTP method (default: "GET") */
  method?: string;
  /** Request headers */
  headers?: Record<string, string>;
  /** Request body as string */
  body?: string;
  /** Request body as binary data */
  bodyBytes?: Uint8Array;
  /** JA3 fingerprint string */
  ja3?: string;
  /** JA4R fingerprint string */
  ja4r?: string;
  /** User-Agent header override */
  userAgent?: string;
  /** Proxy URL */
  proxy?: string;
  /** Request timeout in milliseconds (time until headers arrive) */
  timeout?: number;
  /**
   * Read timeout in milliseconds (time for body streaming after headers arrive).
   * If not specified, no read timeout is applied and streams can hang indefinitely
   * if the server stalls mid-body. Recommended for large file downloads.
   */
  readTimeout?: number;
  /** Disable automatic redirect following */
  disableRedirect?: boolean;
  /** Skip TLS certificate verification */
  insecureSkipVerify?: boolean;
  /** Force HTTP/1.1 instead of HTTP/2 */
  forceHTTP1?: boolean;
  /** Force HTTP/3 (QUIC) */
  forceHTTP3?: boolean;
  /** HTTP/2 fingerprint string (SETTINGS frame order, priorities, etc.) */
  http2Fingerprint?: string;
  /** QUIC fingerprint string */
  quicFingerprint?: string;
  /** Disable GREASE for exact JA4 matching */
  disableGrease?: boolean;
  /** Custom TLS SNI (Server Name Indication) override */
  serverName?: string;
  /** Custom header ordering for fingerprint accuracy */
  headerOrder?: string[];
  /** Use header order exactly as provided (preserves insertion order) */
  orderAsProvided?: boolean;
  /** Cookies to send with the request */
  cookies?: RequestCookie[];
  /** Auto retry with TLS 1.3 compatible curves on handshake failure (default: true) */
  tls13AutoRetry?: boolean;
  /** Enable connection reuse for subsequent requests (default: true) */
  enableConnectionReuse?: boolean;
}

/**
 * Response from a CycleTLS request.
 * Provides both raw stream access and convenience methods for common response parsing.
 *
 * @example
 * ```typescript
 * const response = await client.get('https://api.example.com/data');
 *
 * // Option 1: Use convenience methods (recommended for most cases)
 * const json = await response.json();
 * const text = await response.text();
 *
 * // Option 2: Use raw stream for large files (preserves backpressure)
 * for await (const chunk of response.body) {
 *   process.stdout.write(chunk);
 * }
 * ```
 */
export interface Response {
  requestId: string;
  statusCode: number;
  /** Alias for statusCode (for legacy API compatibility) */
  status: number;
  finalUrl: string;
  headers: Record<string, string[]>;
  /** Readable stream for the response body (use for large files to preserve backpressure) */
  body: Readable;

  /**
   * Parse response body as JSON.
   * Note: This buffers the entire response, so use body stream directly for large responses.
   */
  json<T = unknown>(): Promise<T>;

  /**
   * Get response body as text string.
   * Note: This buffers the entire response, so use body stream directly for large responses.
   */
  text(): Promise<string>;

  /**
   * Get response body as ArrayBuffer.
   * Note: This buffers the entire response, so use body stream directly for large responses.
   */
  arrayBuffer(): Promise<ArrayBuffer>;

  /**
   * Get response body as Blob.
   * Note: This buffers the entire response, so use body stream directly for large responses.
   */
  blob(): Promise<Blob>;

  /**
   * Get response body as Buffer (Node.js specific).
   * Note: This buffers the entire response, so use body stream directly for large responses.
   */
  buffer(): Promise<Buffer>;

  /** Alias for body (for legacy API compatibility) - same as body stream */
  data: Readable;
}

/**
 * Error thrown by CycleTLS.
 */
export class CycleTLSError extends Error {
  constructor(
    message: string,
    public readonly statusCode: number = 0,
    public readonly requestId?: string
  ) {
    super(message);
    this.name = "CycleTLSError";
  }
}

/**
 * Creates a lazy buffer getter that caches the body stream contents.
 * Used by convenience methods (json, text, buffer, etc.) to avoid re-reading the stream.
 */
function createBufferGetter(bodyStream: Readable): () => Promise<Buffer> {
  let cachedBuffer: Buffer | null = null;
  return async (): Promise<Buffer> => {
    if (cachedBuffer !== null) {
      return cachedBuffer;
    }
    const chunks: Buffer[] = [];
    for await (const chunk of bodyStream) {
      chunks.push(chunk as Buffer);
    }
    cachedBuffer = Buffer.concat(chunks);
    return cachedBuffer;
  };
}

// WebSocket ready states matching the ws library
const WS_CONNECTING = 0;
const WS_OPEN = 1;
const WS_CLOSING = 2;
const WS_CLOSED = 3;

/**
 * WebSocket connection for the streaming CycleTLS API.
 * Provides an interface matching the `ws` library for familiarity.
 *
 * @example
 * ```typescript
 * const client = new CycleTLS();
 * const ws = await client.ws('wss://echo.websocket.org');
 *
 * ws.on('open', () => console.log('Connected'));
 * ws.on('message', (data) => console.log('Received:', data));
 * ws.on('close', (code, reason) => console.log('Closed:', code, reason));
 *
 * ws.send('Hello, World!');
 * ws.close();
 * ```
 */
export class CycleTLSWebSocketV2 extends EventEmitter {
  private _readyState: number = WS_CONNECTING;
  private _protocol: string = "";
  private _extensions: string = "";
  private _binaryType: "nodebuffer" | "arraybuffer" = "nodebuffer";

  private wsConnection: WebSocket;
  private requestId: string;
  private debug: boolean;

  constructor(
    private readonly url: string,
    wsConnection: WebSocket,
    requestId: string,
    debug: boolean = false
  ) {
    super();
    this.wsConnection = wsConnection;
    this.requestId = requestId;
    this.debug = debug;
  }

  /** Get the WebSocket URL */
  get URL(): string {
    return this.url;
  }

  /** Get the current ready state */
  get readyState(): number {
    return this._readyState;
  }

  /** Get the negotiated subprotocol */
  get protocol(): string {
    return this._protocol;
  }

  /** Get the negotiated extensions */
  get extensions(): string {
    return this._extensions;
  }

  /** Get/set the binary type for received messages */
  get binaryType(): "nodebuffer" | "arraybuffer" {
    return this._binaryType;
  }

  set binaryType(value: "nodebuffer" | "arraybuffer") {
    this._binaryType = value;
  }

  /**
   * Send data through the WebSocket.
   * @param data - String or Buffer to send
   * @param callback - Optional callback called when send completes
   */
  send(data: string | Buffer, callback?: (err?: Error) => void): void {
    if (this._readyState !== WS_OPEN) {
      const err = new Error(
        `WebSocket is not open: readyState ${this._readyState}`
      );
      if (callback) {
        callback(err);
        return;
      }
      throw err;
    }

    try {
      const isBinary = Buffer.isBuffer(data);
      const buf = isBinary ? data : Buffer.from(data, "utf8");
      const packet = buildWebSocketSendPacket(this.requestId, buf, isBinary);
      this.wsConnection.send(packet);
      if (callback) callback();
    } catch (err) {
      const error = err instanceof Error ? err : new Error(String(err));
      if (callback) {
        callback(error);
      } else {
        this.emit("error", error);
      }
    }
  }

  /**
   * Close the WebSocket connection.
   * @param code - Close status code (default: 1000)
   * @param reason - Close reason string
   */
  close(code: number = 1000, reason: string = ""): void {
    if (this._readyState === WS_CLOSING || this._readyState === WS_CLOSED) {
      return;
    }

    this._readyState = WS_CLOSING;

    try {
      const packet = buildWebSocketClosePacket(this.requestId, code, reason);
      this.wsConnection.send(packet);
    } catch (err) {
      this.emit("error", err);
    }
  }

  /**
   * Send a ping frame.
   * @param data - Optional ping data
   */
  ping(data?: Buffer | string): void {
    if (this._readyState !== WS_OPEN) {
      throw new Error(`WebSocket is not open: readyState ${this._readyState}`);
    }
    // Ping is handled by the underlying connection
    if (this.debug) {
      console.log(`[CycleTLS WebSocket] Ping: ${data}`);
    }
  }

  /**
   * Send a pong frame.
   * @param data - Optional pong data
   */
  pong(data?: Buffer | string): void {
    if (this._readyState !== WS_OPEN) {
      throw new Error(`WebSocket is not open: readyState ${this._readyState}`);
    }
    // Pong is handled by the underlying connection
    if (this.debug) {
      console.log(`[CycleTLS WebSocket] Pong: ${data}`);
    }
  }

  /**
   * Immediately terminate the connection without sending a close frame.
   */
  terminate(): void {
    this._readyState = WS_CLOSED;
    this.wsConnection.close();
    this.removeAllListeners();
  }

  /** @internal Handle ws_open frame */
  _handleOpen(protocol: string, extensions: string): void {
    this._readyState = WS_OPEN;
    this._protocol = protocol;
    this._extensions = extensions;
    this.emit("open");
  }

  /** @internal Handle ws_message frame */
  _handleMessage(messageType: number, data: Buffer): void {
    // messageType: 1 = text, 2 = binary
    let result: Buffer | ArrayBuffer | string;

    if (messageType === 1) {
      // Text message - convert to string
      result = data.toString("utf8");
    } else if (this._binaryType === "arraybuffer") {
      result = data.buffer.slice(
        data.byteOffset,
        data.byteOffset + data.byteLength
      );
    } else {
      result = data;
    }

    // Emit in ws library format: (data, isBinary)
    this.emit("message", result, messageType === 2);
  }

  /** @internal Handle ws_close frame */
  _handleClose(code: number, reason: string): void {
    this._readyState = WS_CLOSED;
    this.emit("close", code, reason);
  }

  /** @internal Handle ws_error frame */
  _handleError(statusCode: number, message: string): void {
    const error = new CycleTLSError(message, statusCode, this.requestId);
    this.emit("error", error);
  }

  /** @internal Set ready state to closed */
  _setClosed(): void {
    this._readyState = WS_CLOSED;
  }
}

/**
 * Server-Sent Events (SSE) event.
 */
export interface SSEEvent {
  /** Optional event ID for resuming connections */
  id?: string;
  /** Event type/name (default: "message") */
  event?: string;
  /** Event data payload */
  data: string;
  /** Reconnection time in milliseconds */
  retry?: number;
}

/**
 * SSE response with event iteration support.
 */
export interface SSEResponse extends Response {
  /** Async iterator for consuming SSE events */
  events(): AsyncIterableIterator<SSEEvent>;
  /** Register a callback for incoming events */
  onEvent(callback: (event: SSEEvent) => void): void;
  /** Register a callback for error events */
  onError(callback: (error: Error) => void): void;
  /** Close the SSE connection */
  close(): Promise<void>;
}

/**
 * CycleTLS - Modern HTTP client with streaming and backpressure support.
 *
 * @example
 * ```typescript
 * import CycleTLS from 'cycletls';
 *
 * const client = new CycleTLS();
 * const response = await client.get('https://example.com');
 *
 * for await (const chunk of response.body) {
 *   console.log(chunk);
 * }
 *
 * await client.close();
 * ```
 */
export class CycleTLS extends EventEmitter {
  private port: number;
  private debug: boolean;
  private timeout: number;
  private executablePath?: string;
  private autoSpawn: boolean;
  private initialWindow: number;
  private creditThreshold: number;

  private serverProcess: ChildProcess | null = null;
  private requestCounter: number = 0;

  constructor(options: CycleTLSOptions = {}) {
    super();
    this.port = options.port ?? 9119;
    this.debug = options.debug ?? false;
    this.timeout = options.timeout ?? 30000;
    this.executablePath = options.executablePath;
    this.autoSpawn = options.autoSpawn ?? true;
    this.initialWindow = options.initialWindow ?? 65536;
    this.creditThreshold = options.creditThreshold ?? this.initialWindow / 2;
  }

  /**
   * Generate a unique request ID.
   */
  private generateRequestId(): string {
    return `req-${Date.now()}-${++this.requestCounter}`;
  }

  /**
   * Start the Go server if autoSpawn is enabled.
   */
  async ensureServerRunning(): Promise<void> {
    if (!this.autoSpawn) {
      return;
    }

    // Try to connect first
    try {
      await this.testConnection();
      return; // Server is already running
    } catch {
      // Need to start server
    }

    await this.startServer();
  }

  /**
   * Test if the server is reachable.
   */
  private testConnection(): Promise<void> {
    return new Promise((resolve, reject) => {
      const ws = new WebSocket(`ws://localhost:${this.port}?v=2`);
      const timeout = setTimeout(() => {
        ws.close();
        reject(new Error("Connection timeout"));
      }, 1000);

      ws.on("open", () => {
        clearTimeout(timeout);
        ws.close();
        resolve();
      });

      ws.on("error", (err) => {
        clearTimeout(timeout);
        reject(err);
      });
    });
  }

  /**
   * Resolve the path to the Go executable based on platform.
   */
  private resolveExecutablePath(): string | null {
    if (this.executablePath) {
      return this.executablePath;
    }

    const PLATFORM_BINARIES: Record<string, Record<string, string>> = {
      win32: { x64: "index.exe" },
      linux: { arm: "index-arm", arm64: "index-arm64", x64: "index" },
      darwin: {
        x64: "index-mac",
        arm: "index-mac-arm",
        arm64: "index-mac-arm64",
      },
      freebsd: { x64: "index-freebsd" },
    };

    const filename = PLATFORM_BINARIES[process.platform]?.[os.arch()];
    if (!filename) {
      return null;
    }
    return path.join(__dirname, filename);
  }

  /**
   * Start the Go server process.
   */
  private async startServer(): Promise<void> {
    const execPath = this.resolveExecutablePath();
    if (!execPath) {
      throw new Error(
        `Unsupported platform: ${process.platform}/${os.arch()}`
      );
    }

    if (!fs.existsSync(execPath)) {
      throw new Error(`Executable not found: ${execPath}`);
    }

    const spawnOptions: SpawnOptionsWithoutStdio = {
      env: { ...process.env, WS_PORT: this.port.toString() },
      shell: process.platform !== "win32",
      windowsHide: true,
      detached: process.platform !== "win32",
      cwd: path.dirname(execPath),
    };

    this.serverProcess = spawn(execPath, [], spawnOptions);

    // Wait for server to be ready
    for (let i = 0; i < 50; i++) {
      try {
        await this.testConnection();
        return;
      } catch {
        await new Promise((r) => setTimeout(r, 100));
      }
    }

    throw new Error("Server failed to start");
  }

  /**
   * Make an HTTP request with flow control.
   */
  async request(options: RequestOptions): Promise<Response> {
    await this.ensureServerRunning();

    const requestId = this.generateRequestId();

    return new Promise((resolve, reject) => {
      const ws = new WebSocket(`ws://localhost:${this.port}?v=2`);
      let resolved = false;

      // Create the response body stream
      const bodyStream = new Readable({
        read() {},
      });

      // Attach an error handler to prevent unhandled error events
      // The actual error is handled by rejecting the promise
      bodyStream.on("error", () => {
        // Error is already handled by promise rejection, this just prevents
        // Node.js from emitting an unhandled error event
      });

      // Credit manager for backpressure
      const creditManager = new CreditManager(
        this.creditThreshold,
        (credits) => {
          if (ws.readyState === WebSocket.OPEN) {
            const packet = buildCreditPacket(requestId, credits);
            ws.send(packet);
          }
        }
      );

      // Timeout handling:
      // - Use per-request timeout if provided, otherwise fall back to client-level timeout
      // - This timeout fires if headers don't arrive in time (initial connection timeout)
      const effectiveTimeout = options.timeout ?? this.timeout;
      let timeoutId: ReturnType<typeof setTimeout> | null = setTimeout(() => {
        if (!resolved) {
          ws.close();
          reject(new CycleTLSError("Request timeout", 408, requestId));
        }
      }, effectiveTimeout);

      // Read timeout handling:
      // - If readTimeout is specified, start a new timer after headers arrive
      // - This prevents streams from hanging indefinitely if server stalls mid-body
      let readTimeoutId: ReturnType<typeof setTimeout> | null = null;
      const clearReadTimeout = (): void => {
        if (readTimeoutId !== null) {
          clearTimeout(readTimeoutId);
          readTimeoutId = null;
        }
      };
      const resetReadTimeout = (): void => {
        clearReadTimeout();
        if (options.readTimeout !== undefined && options.readTimeout > 0) {
          readTimeoutId = setTimeout(() => {
            ws.close();
            bodyStream.destroy(new CycleTLSError("Read timeout", 408, requestId));
          }, options.readTimeout);
        }
      };

      ws.on("open", () => {
        if (this.debug) {
          console.log(`[CycleTLS] WebSocket connected for ${requestId}`);
        }

        // Build and send init packet
        // Convert bodyBytes to base64 if provided (for binary data transmission)
        const bodyBytesBase64 = options.bodyBytes
          ? Buffer.from(options.bodyBytes).toString("base64")
          : undefined;

        const requestOptions = {
          url: options.url,
          method: options.method ?? "GET",
          headers: options.headers ?? {},
          body: options.body ?? "",
          bodyBytes: bodyBytesBase64,
          ja3: options.ja3,
          ja4r: options.ja4r,
          userAgent: options.userAgent,
          proxy: options.proxy,
          timeout: effectiveTimeout,
          disableRedirect: options.disableRedirect,
          insecureSkipVerify: options.insecureSkipVerify,
          forceHTTP1: options.forceHTTP1,
          forceHTTP3: options.forceHTTP3,
          http2Fingerprint: options.http2Fingerprint,
          quicFingerprint: options.quicFingerprint,
          disableGrease: options.disableGrease,
          serverName: options.serverName,
          headerOrder: options.headerOrder,
          orderAsProvided: options.orderAsProvided,
          cookies: options.cookies,
          tls13AutoRetry: options.tls13AutoRetry,
          enableConnectionReuse: options.enableConnectionReuse,
        };

        const initPacket = buildInitPacket(
          requestId,
          requestOptions,
          this.initialWindow
        );
        ws.send(initPacket);
      });

      ws.on("message", (data: Buffer) => {
        try {
          const frame = parseFrame(data);

          if (frame.requestId !== requestId) {
            console.warn(`Unexpected request ID: ${frame.requestId}`);
            return;
          }

          switch (frame.method) {
            case "response": {
              const response = parseResponsePayload(frame.payload);
              resolved = true;
              // Clear the initial timeout (headers arrived)
              if (timeoutId !== null) {
                clearTimeout(timeoutId);
                timeoutId = null;
              }
              // Start the read timeout if configured (for body streaming)
              resetReadTimeout();

              // Create a lazy buffer getter for convenience methods
              const getBuffer = createBufferGetter(bodyStream);

              resolve({
                requestId,
                statusCode: response.statusCode,
                status: response.statusCode,  // alias for legacy compatibility
                finalUrl: response.finalUrl,
                headers: response.headers,
                body: bodyStream,

                async json<T = unknown>(): Promise<T> {
                  const buf = await getBuffer();
                  return JSON.parse(buf.toString("utf8")) as T;
                },

                async text(): Promise<string> {
                  const buf = await getBuffer();
                  return buf.toString("utf8");
                },

                async arrayBuffer(): Promise<ArrayBuffer> {
                  const buf = await getBuffer();
                  return buf.buffer.slice(
                    buf.byteOffset,
                    buf.byteOffset + buf.byteLength
                  );
                },

                async blob(): Promise<Blob> {
                  const buf = await getBuffer();
                  const contentType =
                    response.headers["content-type"]?.[0] ??
                    "application/octet-stream";
                  return new Blob([buf], { type: contentType });
                },

                async buffer(): Promise<Buffer> {
                  return getBuffer();
                },

                data: bodyStream,  // alias for legacy compatibility (same reference)
              });
              break;
            }

            case "data": {
              const chunk = parseDataPayload(frame.payload);
              bodyStream.push(chunk);
              creditManager.onDataReceived(chunk.length);
              // Reset read timeout on each chunk (server is still sending data)
              resetReadTimeout();
              break;
            }

            case "end": {
              // Clear read timeout - transfer complete
              clearReadTimeout();
              bodyStream.push(null);
              ws.close();
              break;
            }

            case "error": {
              const error = parseErrorPayload(frame.payload);
              clearReadTimeout();
              if (!resolved) {
                resolved = true;
                if (timeoutId !== null) {
                  clearTimeout(timeoutId);
                  timeoutId = null;
                }
                reject(
                  new CycleTLSError(error.message, error.statusCode, requestId)
                );
              }
              bodyStream.destroy(new Error(error.message));
              ws.close();
              break;
            }

            default:
              console.warn(`Unknown frame method: ${frame.method}`);
          }
        } catch (err) {
          clearReadTimeout();
          if (!resolved) {
            resolved = true;
            if (timeoutId !== null) {
              clearTimeout(timeoutId);
              timeoutId = null;
            }
            reject(err);
          }
        }
      });

      ws.on("error", (err) => {
        clearReadTimeout();
        if (!resolved) {
          resolved = true;
          if (timeoutId !== null) {
            clearTimeout(timeoutId);
            timeoutId = null;
          }
          reject(new CycleTLSError(err.message, 0, requestId));
        }
      });

      ws.on("close", () => {
        clearReadTimeout();
        if (this.debug) {
          console.log(`[CycleTLS] WebSocket closed for ${requestId}`);
        }
        if (!resolved) {
          resolved = true;
          if (timeoutId !== null) {
            clearTimeout(timeoutId);
            timeoutId = null;
          }
          reject(new CycleTLSError("Connection closed", 0, requestId));
        }
      });
    });
  }

  /**
   * Convenience method for GET requests.
   */
  async get(
    url: string,
    options: Omit<RequestOptions, "url" | "method"> = {}
  ): Promise<Response> {
    return this.request({ ...options, url, method: "GET" });
  }

  /**
   * Convenience method for POST requests.
   */
  async post(
    url: string,
    body: string,
    options: Omit<RequestOptions, "url" | "method" | "body"> = {}
  ): Promise<Response> {
    return this.request({ ...options, url, method: "POST", body });
  }

  /**
   * Convenience method for HEAD requests.
   */
  async head(
    url: string,
    options: Omit<RequestOptions, "url" | "method"> = {}
  ): Promise<Response> {
    return this.request({ ...options, url, method: "HEAD" });
  }

  /**
   * Convenience method for PUT requests.
   */
  async put(
    url: string,
    body: string = "",
    options: Omit<RequestOptions, "url" | "method" | "body"> = {}
  ): Promise<Response> {
    return this.request({ ...options, url, method: "PUT", body });
  }

  /**
   * Convenience method for DELETE requests.
   */
  async delete(
    url: string,
    options: Omit<RequestOptions, "url" | "method"> = {}
  ): Promise<Response> {
    return this.request({ ...options, url, method: "DELETE" });
  }

  /**
   * Convenience method for PATCH requests.
   */
  async patch(
    url: string,
    body: string = "",
    options: Omit<RequestOptions, "url" | "method" | "body"> = {}
  ): Promise<Response> {
    return this.request({ ...options, url, method: "PATCH", body });
  }

  /**
   * Convenience method for OPTIONS requests.
   */
  async options(
    url: string,
    options: Omit<RequestOptions, "url" | "method"> = {}
  ): Promise<Response> {
    return this.request({ ...options, url, method: "OPTIONS" });
  }

  /**
   * Convenience method for TRACE requests.
   */
  async trace(
    url: string,
    options: Omit<RequestOptions, "url" | "method"> = {}
  ): Promise<Response> {
    return this.request({ ...options, url, method: "TRACE" });
  }

  /**
   * Convenience method for CONNECT requests.
   */
  async connect(
    url: string,
    options: Omit<RequestOptions, "url" | "method"> = {}
  ): Promise<Response> {
    return this.request({ ...options, url, method: "CONNECT" });
  }

  /**
   * Open a WebSocket connection with TLS fingerprinting.
   *
   * @example
   * ```typescript
   * const ws = await client.ws('wss://echo.websocket.org');
   * ws.on('open', () => console.log('Connected'));
   * ws.on('message', (data) => console.log(data));
   * ws.send('Hello!');
   * ws.close();
   * ```
   */
  async ws(
    url: string,
    options: Omit<RequestOptions, "url"> = {}
  ): Promise<CycleTLSWebSocketV2> {
    await this.ensureServerRunning();

    const requestId = this.generateRequestId();
    const wsClient = new WebSocket(`ws://localhost:${this.port}?v=2`);

    const cycleTLSWs = new CycleTLSWebSocketV2(
      url,
      wsClient,
      requestId,
      this.debug
    );

    return new Promise((resolve, reject) => {
      let connected = false;

      // Use per-request timeout if provided, otherwise fall back to client-level timeout
      const effectiveTimeout = options.timeout ?? this.timeout;
      const timeoutId = setTimeout(() => {
        if (!connected) {
          wsClient.close();
          reject(new CycleTLSError("WebSocket connection timeout", 408, requestId));
        }
      }, effectiveTimeout);

      wsClient.on("open", () => {
        if (this.debug) {
          console.log(`[CycleTLS] WebSocket control connected for ${requestId}`);
        }

        // Build and send init packet with websocket protocol
        const requestOptions = {
          url,
          method: "GET",
          headers: options.headers ?? {},
          body: "",
          ja3: options.ja3,
          ja4r: options.ja4r,
          userAgent: options.userAgent,
          proxy: options.proxy,
          timeout: effectiveTimeout,
          disableRedirect: options.disableRedirect,
          insecureSkipVerify: options.insecureSkipVerify,
          forceHTTP1: options.forceHTTP1,
          forceHTTP3: options.forceHTTP3,
          protocol: "websocket",
        };

        const initPacket = buildInitPacket(
          requestId,
          requestOptions,
          this.initialWindow
        );
        wsClient.send(initPacket);
      });

      wsClient.on("message", (data: Buffer) => {
        try {
          const frame = parseFrame(data);

          if (frame.requestId !== requestId) {
            return;
          }

          switch (frame.method) {
            case "response": {
              // HTTP upgrade response received
              if (this.debug) {
                const response = parseResponsePayload(frame.payload);
                console.log(
                  `[CycleTLS] WebSocket upgrade response: ${response.statusCode}`
                );
              }
              break;
            }

            case "ws_open": {
              const wsOpen = parseWebSocketOpenPayload(frame.payload);
              connected = true;
              clearTimeout(timeoutId);
              // Store the open info but don't emit yet
              const protocol = wsOpen.protocol ?? "";
              const extensions = wsOpen.extensions ?? "";
              // Resolve the promise first, then emit 'open' after a short delay
              // This ensures event handlers can be registered before the event fires
              // We use setTimeout(0) because it goes to the macrotask queue,
              // which runs AFTER all microtasks (like promise .then callbacks)
              resolve(cycleTLSWs);
              setTimeout(() => {
                cycleTLSWs._handleOpen(protocol, extensions);
              }, 0);
              break;
            }

            case "ws_message": {
              const wsMsg = parseWebSocketMessagePayload(frame.payload);
              cycleTLSWs._handleMessage(wsMsg.messageType, wsMsg.data);
              break;
            }

            case "ws_close": {
              const wsClose = parseWebSocketClosePayload(frame.payload);
              cycleTLSWs._handleClose(wsClose.code, wsClose.reason);
              wsClient.close();
              break;
            }

            case "ws_error": {
              const error = parseErrorPayload(frame.payload);
              cycleTLSWs._handleError(error.statusCode, error.message);
              break;
            }

            case "error": {
              const error = parseErrorPayload(frame.payload);
              if (!connected) {
                connected = true;
                clearTimeout(timeoutId);
                reject(
                  new CycleTLSError(error.message, error.statusCode, requestId)
                );
              }
              cycleTLSWs._setClosed();
              wsClient.close();
              break;
            }

            case "end": {
              cycleTLSWs._setClosed();
              cycleTLSWs.emit("close", 1000, "Connection ended");
              wsClient.close();
              break;
            }
          }
        } catch (err) {
          if (!connected) {
            connected = true;
            clearTimeout(timeoutId);
            reject(err);
          }
        }
      });

      wsClient.on("error", (err) => {
        if (!connected) {
          connected = true;
          clearTimeout(timeoutId);
          reject(new CycleTLSError(err.message, 0, requestId));
        }
        cycleTLSWs.emit("error", err);
      });

      wsClient.on("close", () => {
        if (!connected) {
          connected = true;
          clearTimeout(timeoutId);
          reject(new CycleTLSError("Connection closed", 0, requestId));
        }
      });
    });
  }

  /**
   * Open a WebSocket connection (alias for ws).
   */
  webSocket(
    url: string,
    options: Omit<RequestOptions, "url"> = {}
  ): Promise<CycleTLSWebSocketV2> {
    return this.ws(url, options);
  }

  /**
   * Open a Server-Sent Events (SSE) connection with TLS fingerprinting.
   *
   * @example
   * ```typescript
   * const sse = await client.sse('https://api.example.com/events');
   * for await (const event of sse.events()) {
   *   console.log(event.data);
   * }
   * ```
   */
  async sse(
    url: string,
    options: Omit<RequestOptions, "url"> = {}
  ): Promise<SSEResponse> {
    await this.ensureServerRunning();

    const requestId = this.generateRequestId();

    return new Promise((resolve, reject) => {
      const ws = new WebSocket(`ws://localhost:${this.port}?v=2`);
      let resolved = false;

      const bodyStream = new Readable({
        read() {},
      });

      // Attach an error handler to prevent unhandled error events
      // The actual error is handled by rejecting the promise
      bodyStream.on("error", () => {
        // Error is already handled by promise rejection, this just prevents
        // Node.js from emitting an unhandled error event
      });

      // Event emitter for SSE events
      const eventEmitter = new EventEmitter();

      // Parse SSE events from stream
      let buffer = "";
      let currentEvent: Partial<SSEEvent> = {};

      const parseSSELine = (line: string): void => {
        if (line === "") {
          // Empty line = event complete
          if (currentEvent.data !== undefined) {
            const event: SSEEvent = {
              id: currentEvent.id,
              event: currentEvent.event,
              data: currentEvent.data,
              retry: currentEvent.retry,
            };
            eventEmitter.emit("event", event);
          }
          currentEvent = {};
          return;
        }

        if (line.startsWith(":")) {
          // Comment, ignore
          return;
        }

        const colonIndex = line.indexOf(":");
        let field: string;
        let value: string;

        if (colonIndex === -1) {
          field = line;
          value = "";
        } else {
          field = line.substring(0, colonIndex);
          value = line.substring(colonIndex + 1);
          if (value.startsWith(" ")) {
            value = value.substring(1);
          }
        }

        switch (field) {
          case "event":
            currentEvent.event = value;
            break;
          case "data":
            currentEvent.data =
              currentEvent.data !== undefined
                ? currentEvent.data + "\n" + value
                : value;
            break;
          case "id":
            currentEvent.id = value;
            break;
          case "retry":
            const retry = parseInt(value, 10);
            if (!isNaN(retry)) {
              currentEvent.retry = retry;
            }
            break;
        }
      };

      bodyStream.on("data", (chunk: Buffer) => {
        buffer += chunk.toString("utf8");
        const lines = buffer.split("\n");
        buffer = lines.pop() ?? "";
        for (const line of lines) {
          parseSSELine(line.replace(/\r$/, ""));
        }
      });

      bodyStream.on("end", () => {
        // Process any remaining buffer
        if (buffer) {
          parseSSELine(buffer);
        }
        // Emit final event if pending
        if (currentEvent.data !== undefined) {
          eventEmitter.emit("event", {
            id: currentEvent.id,
            event: currentEvent.event,
            data: currentEvent.data,
            retry: currentEvent.retry,
          });
        }
        eventEmitter.emit("end");
      });

      const creditManager = new CreditManager(
        this.creditThreshold,
        (credits) => {
          if (ws.readyState === WebSocket.OPEN) {
            const packet = buildCreditPacket(requestId, credits);
            ws.send(packet);
          }
        }
      );

      // Use per-request timeout if provided, otherwise fall back to client-level timeout
      const effectiveTimeout = options.timeout ?? this.timeout;
      const timeoutId = setTimeout(() => {
        if (!resolved) {
          ws.close();
          reject(new CycleTLSError("SSE connection timeout", 408, requestId));
        }
      }, effectiveTimeout);

      ws.on("open", () => {
        const requestOptions = {
          url,
          method: "GET",
          headers: {
            Accept: "text/event-stream",
            "Cache-Control": "no-cache",
            ...options.headers,
          },
          body: "",
          ja3: options.ja3,
          ja4r: options.ja4r,
          userAgent: options.userAgent,
          proxy: options.proxy,
          timeout: effectiveTimeout,
          disableRedirect: options.disableRedirect,
          insecureSkipVerify: options.insecureSkipVerify,
          forceHTTP1: options.forceHTTP1,
          forceHTTP3: options.forceHTTP3,
          protocol: "sse",
        };

        const initPacket = buildInitPacket(
          requestId,
          requestOptions,
          this.initialWindow
        );
        ws.send(initPacket);
      });

      ws.on("message", (data: Buffer) => {
        try {
          const frame = parseFrame(data);

          if (frame.requestId !== requestId) {
            return;
          }

          switch (frame.method) {
            case "response": {
              const response = parseResponsePayload(frame.payload);
              resolved = true;
              clearTimeout(timeoutId);

              // Create a lazy buffer getter for convenience methods
              const getBuffer = createBufferGetter(bodyStream);

              const sseResponse: SSEResponse = {
                requestId,
                statusCode: response.statusCode,
                status: response.statusCode,  // alias for legacy compatibility
                finalUrl: response.finalUrl,
                headers: response.headers,
                body: bodyStream,
                data: bodyStream,  // alias for legacy compatibility

                async json<T = unknown>(): Promise<T> {
                  const buf = await getBuffer();
                  return JSON.parse(buf.toString("utf8")) as T;
                },

                async text(): Promise<string> {
                  const buf = await getBuffer();
                  return buf.toString("utf8");
                },

                async arrayBuffer(): Promise<ArrayBuffer> {
                  const buf = await getBuffer();
                  return buf.buffer.slice(
                    buf.byteOffset,
                    buf.byteOffset + buf.byteLength
                  );
                },

                async blob(): Promise<Blob> {
                  const buf = await getBuffer();
                  const contentType =
                    response.headers["content-type"]?.[0] ??
                    "text/event-stream";
                  return new Blob([buf], { type: contentType });
                },

                async buffer(): Promise<Buffer> {
                  return getBuffer();
                },

                async *events(): AsyncIterableIterator<SSEEvent> {
                  const events: SSEEvent[] = [];
                  let resolveWait: (() => void) | null = null;
                  let ended = false;

                  eventEmitter.on("event", (event: SSEEvent) => {
                    events.push(event);
                    if (resolveWait) {
                      resolveWait();
                      resolveWait = null;
                    }
                  });

                  eventEmitter.on("end", () => {
                    ended = true;
                    if (resolveWait) {
                      resolveWait();
                      resolveWait = null;
                    }
                  });

                  while (true) {
                    if (events.length > 0) {
                      yield events.shift()!;
                    } else if (ended) {
                      break;
                    } else {
                      await new Promise<void>((r) => {
                        resolveWait = r;
                      });
                    }
                  }
                },

                onEvent(callback: (event: SSEEvent) => void): void {
                  eventEmitter.on("event", callback);
                },

                onError(callback: (error: Error) => void): void {
                  eventEmitter.on("error", callback);
                },

                async close(): Promise<void> {
                  ws.close();
                  bodyStream.destroy();
                },
              };

              resolve(sseResponse);
              break;
            }

            case "data": {
              const chunk = parseDataPayload(frame.payload);
              bodyStream.push(chunk);
              creditManager.onDataReceived(chunk.length);
              break;
            }

            case "end": {
              bodyStream.push(null);
              ws.close();
              break;
            }

            case "error": {
              const error = parseErrorPayload(frame.payload);
              if (!resolved) {
                resolved = true;
                clearTimeout(timeoutId);
                reject(
                  new CycleTLSError(error.message, error.statusCode, requestId)
                );
              }
              eventEmitter.emit("error", new Error(error.message));
              bodyStream.destroy(new Error(error.message));
              ws.close();
              break;
            }
          }
        } catch (err) {
          if (!resolved) {
            resolved = true;
            clearTimeout(timeoutId);
            reject(err);
          }
        }
      });

      ws.on("error", (err) => {
        if (!resolved) {
          resolved = true;
          clearTimeout(timeoutId);
          reject(new CycleTLSError(err.message, 0, requestId));
        }
        eventEmitter.emit("error", err);
      });

      ws.on("close", () => {
        if (!resolved) {
          resolved = true;
          clearTimeout(timeoutId);
          reject(new CycleTLSError("Connection closed", 0, requestId));
        }
      });
    });
  }

  /**
   * Open a Server-Sent Events connection (alias for sse).
   */
  eventSource(
    url: string,
    options: Omit<RequestOptions, "url"> = {}
  ): Promise<SSEResponse> {
    return this.sse(url, options);
  }

  /**
   * Stop the server if we spawned it.
   */
  async close(): Promise<void> {
    if (this.serverProcess) {
      this.serverProcess.kill();
      this.serverProcess = null;
    }
  }
}

export default CycleTLS;
