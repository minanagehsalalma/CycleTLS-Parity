/**
 * Binary Protocol Helpers for CycleTLS V2 Flow Control
 *
 * This module implements the binary protocol used for communication
 * between the TypeScript client and Go server in flow control mode.
 */

/**
 * BufferWriter simplifies building binary packets.
 */
class BufferWriter {
  private parts: Buffer[] = [];

  writeString(s: string): void {
    const len = Buffer.alloc(2);
    len.writeUInt16BE(s.length, 0);
    this.parts.push(len);
    this.parts.push(Buffer.from(s, "utf8"));
  }

  writeU32(v: number): void {
    const buf = Buffer.alloc(4);
    buf.writeUInt32BE(v, 0);
    this.parts.push(buf);
  }

  toBuffer(): Buffer {
    return Buffer.concat(this.parts);
  }
}

/**
 * Build an "init" packet to initialize a request.
 */
export function buildInitPacket(
  requestId: string,
  options: Record<string, unknown>,
  initialWindow: number
): Buffer {
  const w = new BufferWriter();
  w.writeString(requestId);
  w.writeString("init");
  w.writeU32(initialWindow);
  w.writeString(JSON.stringify(options));
  return w.toBuffer();
}

/**
 * Build a "credit" packet to replenish the server's credit window.
 */
export function buildCreditPacket(requestId: string, credits: number): Buffer {
  const w = new BufferWriter();
  w.writeString(requestId);
  w.writeString("credit");
  w.writeU32(credits);
  return w.toBuffer();
}

/**
 * BufferReader simplifies parsing binary packets.
 */
class BufferReader {
  private offset = 0;

  constructor(private data: Buffer) {}

  readU16(): number {
    const v = this.data.readUInt16BE(this.offset);
    this.offset += 2;
    return v;
  }

  readU32(): number {
    const v = this.data.readUInt32BE(this.offset);
    this.offset += 4;
    return v;
  }

  readString(): string {
    const len = this.readU16();
    const s = this.data.toString("utf8", this.offset, this.offset + len);
    this.offset += len;
    return s;
  }

  remaining(): Buffer {
    return this.data.subarray(this.offset);
  }
}

/**
 * Parsed frame from server.
 */
export interface ParsedFrame {
  requestId: string;
  method: string;
  payload: Buffer;
}

export function parseFrame(data: Buffer): ParsedFrame {
  const r = new BufferReader(data);
  const requestId = r.readString();
  const method = r.readString();
  const payload = r.remaining();
  return { requestId, method, payload };
}

/**
 * Parsed response payload.
 */
export interface ResponsePayload {
  statusCode: number;
  finalUrl: string;
  headers: Record<string, string[]>;
}

export function parseResponsePayload(payload: Buffer): ResponsePayload {
  const r = new BufferReader(payload);
  const statusCode = r.readU16();
  const finalUrl = r.readString();

  const headerCount = r.readU16();
  const headers: Record<string, string[]> = {};

  for (let i = 0; i < headerCount; i++) {
    const name = r.readString();
    const valueCount = r.readU16();
    const values: string[] = [];
    for (let j = 0; j < valueCount; j++) {
      values.push(r.readString());
    }
    headers[name] = values;
  }

  return { statusCode, finalUrl, headers };
}

/**
 * Parse a "data" frame payload.
 */
export function parseDataPayload(payload: Buffer): Buffer {
  const r = new BufferReader(payload);
  const length = r.readU32();
  return payload.subarray(4, 4 + length);
}

/**
 * Parsed error payload.
 */
export interface ErrorPayload {
  statusCode: number;
  message: string;
}

export function parseErrorPayload(payload: Buffer): ErrorPayload {
  const r = new BufferReader(payload);
  const statusCode = r.readU16();
  const message = r.readString();
  return { statusCode, message };
}
