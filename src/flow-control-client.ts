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

import {
  buildInitPacket,
  buildCreditPacket,
  parseFrame,
  parseResponsePayload,
  parseDataPayload,
  parseErrorPayload,
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
 * Request options for CycleTLS requests.
 */
export interface RequestOptions {
  url: string;
  method?: string;
  headers?: Record<string, string>;
  body?: string;
  ja3?: string;
  ja4r?: string;
  userAgent?: string;
  proxy?: string;
  timeout?: number;
  disableRedirect?: boolean;
  insecureSkipVerify?: boolean;
  forceHTTP1?: boolean;
  forceHTTP3?: boolean;
}

/**
 * Response from a CycleTLS request.
 */
export interface Response {
  requestId: string;
  statusCode: number;
  finalUrl: string;
  headers: Record<string, string[]>;
  /** Readable stream for the response body */
  body: Readable;
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

      // Timeout handling
      const timeoutId = setTimeout(() => {
        if (!resolved) {
          ws.close();
          reject(new CycleTLSError("Request timeout", 408, requestId));
        }
      }, this.timeout);

      ws.on("open", () => {
        if (this.debug) {
          console.log(`[CycleTLS] WebSocket connected for ${requestId}`);
        }

        // Build and send init packet
        const requestOptions = {
          url: options.url,
          method: options.method ?? "GET",
          headers: options.headers ?? {},
          body: options.body ?? "",
          ja3: options.ja3,
          ja4r: options.ja4r,
          userAgent: options.userAgent,
          proxy: options.proxy,
          timeout: options.timeout,
          disableRedirect: options.disableRedirect,
          insecureSkipVerify: options.insecureSkipVerify,
          forceHTTP1: options.forceHTTP1,
          forceHTTP3: options.forceHTTP3,
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
              clearTimeout(timeoutId);
              resolve({
                requestId,
                statusCode: response.statusCode,
                finalUrl: response.finalUrl,
                headers: response.headers,
                body: bodyStream,
              });
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
              bodyStream.destroy(new Error(error.message));
              ws.close();
              break;
            }

            default:
              console.warn(`Unknown frame method: ${frame.method}`);
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
      });

      ws.on("close", () => {
        if (this.debug) {
          console.log(`[CycleTLS] WebSocket closed for ${requestId}`);
        }
        if (!resolved) {
          resolved = true;
          clearTimeout(timeoutId);
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
