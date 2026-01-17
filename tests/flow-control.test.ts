/**
 * Integration tests for CycleTLS (V2 Protocol)
 *
 * These tests verify the streaming and backpressure functionality
 * that provides memory-efficient large file downloads.
 */

import CycleTLS, { Legacy } from "../dist/index.js";

jest.setTimeout(60000);

describe("CycleTLS (V2 Protocol)", () => {
  let client: CycleTLS;

  beforeEach(() => {
    client = new CycleTLS({
      port: 9119,
      debug: false,
      timeout: 30000,
      initialWindow: 65536, // 64KB
      autoSpawn: true,
    });
  });

  afterEach(async () => {
    await client.close();
  });

  describe("Basic Request Functionality", () => {
    it("should make a successful GET request", async () => {
      const response = await client.get("https://httpbin.org/get");

      expect(response.statusCode).toBe(200);
      expect(response.requestId).toBeDefined();
      expect(response.finalUrl).toContain("httpbin.org");

      // Consume the body
      const chunks: Buffer[] = [];
      for await (const chunk of response.body) {
        chunks.push(chunk);
      }
      const body = Buffer.concat(chunks).toString("utf8");
      const json = JSON.parse(body);

      expect(json.url).toBe("https://httpbin.org/get");
    });

    it("should make a successful POST request", async () => {
      const postData = JSON.stringify({ test: "value", number: 42 });

      const response = await client.post("https://httpbin.org/post", postData, {
        headers: { "Content-Type": "application/json" },
      });

      expect(response.statusCode).toBe(200);

      const chunks: Buffer[] = [];
      for await (const chunk of response.body) {
        chunks.push(chunk);
      }
      const body = Buffer.concat(chunks).toString("utf8");
      const json = JSON.parse(body);

      expect(json.json).toEqual({ test: "value", number: 42 });
    });

    it("should include response headers", async () => {
      const response = await client.get("https://httpbin.org/response-headers?X-Custom=TestValue");

      expect(response.statusCode).toBe(200);
      expect(response.headers).toBeDefined();

      // Consume body
      for await (const _ of response.body) {
        // drain
      }
    });
  });

  describe("Streaming and Backpressure", () => {
    it("should stream response body in chunks", async () => {
      // Request 10KB of random bytes
      const response = await client.get("https://httpbin.org/bytes/10240");

      expect(response.statusCode).toBe(200);

      let totalBytes = 0;
      let chunkCount = 0;

      for await (const chunk of response.body) {
        totalBytes += chunk.length;
        chunkCount++;
      }

      expect(totalBytes).toBe(10240);
      expect(chunkCount).toBeGreaterThan(0);
    });

    it("should handle slow consumers with backpressure", async () => {
      // Request moderate amount of data
      const response = await client.get("https://httpbin.org/bytes/32768");

      expect(response.statusCode).toBe(200);

      let totalBytes = 0;
      const delays: number[] = [];

      for await (const chunk of response.body) {
        const start = Date.now();

        // Simulate slow consumer
        await new Promise((resolve) => setTimeout(resolve, 10));

        delays.push(Date.now() - start);
        totalBytes += chunk.length;
      }

      expect(totalBytes).toBe(32768);
      // Should have multiple chunks due to credit window
      expect(delays.length).toBeGreaterThan(1);
    });

    it("should complete large downloads without memory explosion", async () => {
      // Get initial memory usage
      const initialMemory = process.memoryUsage().heapUsed;

      // Download 1MB
      const response = await client.get("https://httpbin.org/bytes/1048576");

      expect(response.statusCode).toBe(200);

      let totalBytes = 0;
      let peakMemory = initialMemory;

      for await (const chunk of response.body) {
        totalBytes += chunk.length;
        const currentMemory = process.memoryUsage().heapUsed;
        peakMemory = Math.max(peakMemory, currentMemory);
      }

      expect(totalBytes).toBe(1048576);

      // Memory increase should be bounded (less than 5MB increase for 1MB download)
      // This verifies backpressure is working
      const memoryIncrease = peakMemory - initialMemory;
      expect(memoryIncrease).toBeLessThan(5 * 1024 * 1024);
    });
  });

  describe("Credit Window Behavior", () => {
    it("should work with small initial window", async () => {
      const smallWindowClient = new CycleTLS({
        port: 9119,
        initialWindow: 8192, // 8KB
        creditThreshold: 4096,
        autoSpawn: true,
      });

      try {
        const response = await smallWindowClient.get("https://httpbin.org/bytes/32768");

        expect(response.statusCode).toBe(200);

        let totalBytes = 0;
        for await (const chunk of response.body) {
          totalBytes += chunk.length;
        }

        expect(totalBytes).toBe(32768);
      } finally {
        await smallWindowClient.close();
      }
    });

    it("should work with large initial window", async () => {
      const largeWindowClient = new CycleTLS({
        port: 9119,
        initialWindow: 262144, // 256KB
        autoSpawn: true,
      });

      try {
        const response = await largeWindowClient.get("https://httpbin.org/bytes/131072");

        expect(response.statusCode).toBe(200);

        let totalBytes = 0;
        for await (const chunk of response.body) {
          totalBytes += chunk.length;
        }

        expect(totalBytes).toBe(131072);
      } finally {
        await largeWindowClient.close();
      }
    });
  });

  describe("Error Handling", () => {
    it("should handle 404 responses", async () => {
      const response = await client.get("https://httpbin.org/status/404");

      expect(response.statusCode).toBe(404);

      // Consume body even on error
      for await (const _ of response.body) {
        // drain
      }
    });

    it("should handle 500 responses", async () => {
      const response = await client.get("https://httpbin.org/status/500");

      expect(response.statusCode).toBe(500);

      for await (const _ of response.body) {
        // drain
      }
    });

    it("should handle connection errors gracefully", async () => {
      const badClient = new CycleTLS({
        port: 59999, // Non-existent server
        autoSpawn: false,
        timeout: 2000,
      });

      await expect(
        badClient.get("https://httpbin.org/get")
      ).rejects.toThrow();

      await badClient.close();
    });

    it("should handle request timeout", async () => {
      const timeoutClient = new CycleTLS({
        port: 9119,
        timeout: 1000, // 1 second timeout
        autoSpawn: true,
      });

      try {
        // Request a 5 second delay - should timeout
        await expect(
          timeoutClient.get("https://httpbin.org/delay/5")
        ).rejects.toThrow(/timeout/i);
      } finally {
        await timeoutClient.close();
      }
    });
  });

  describe("Request Options", () => {
    it("should support custom headers", async () => {
      const response = await client.request({
        url: "https://httpbin.org/headers",
        headers: {
          "X-Custom-Header": "TestValue123",
          "Accept": "application/json",
        },
      });

      expect(response.statusCode).toBe(200);

      const chunks: Buffer[] = [];
      for await (const chunk of response.body) {
        chunks.push(chunk);
      }
      const body = Buffer.concat(chunks).toString("utf8");
      const json = JSON.parse(body);

      expect(json.headers["X-Custom-Header"]).toBe("TestValue123");
    });

    it("should support custom user agent", async () => {
      const response = await client.request({
        url: "https://httpbin.org/user-agent",
        userAgent: "CycleTLS-Test/1.0",
      });

      expect(response.statusCode).toBe(200);

      const chunks: Buffer[] = [];
      for await (const chunk of response.body) {
        chunks.push(chunk);
      }
      const body = Buffer.concat(chunks).toString("utf8");
      const json = JSON.parse(body);

      expect(json["user-agent"]).toBe("CycleTLS-Test/1.0");
    });

    it("should handle redirects by default", async () => {
      const response = await client.get("https://httpbin.org/redirect/2");

      expect(response.statusCode).toBe(200);
      expect(response.finalUrl).toContain("/get");

      for await (const _ of response.body) {
        // drain
      }
    });

    it("should respect disableRedirect option", async () => {
      const response = await client.request({
        url: "https://httpbin.org/redirect/1",
        disableRedirect: true,
      });

      expect(response.statusCode).toBe(302);

      for await (const _ of response.body) {
        // drain
      }
    });
  });
});

describe("CycleTLS vs Legacy Comparison", () => {
  it("both protocols should return same data for simple requests", async () => {
    // V2 Modern CycleTLS
    const v2Client = new CycleTLS({ port: 9119, autoSpawn: true });

    // V1 Legacy
    const v1Client = await Legacy();

    try {
      // Make same request with both
      const [v2Response, v1Response] = await Promise.all([
        v2Client.get("https://httpbin.org/json"),
        v1Client("https://httpbin.org/json", {}),
      ]);

      // V2 response
      const v2Chunks: Buffer[] = [];
      for await (const chunk of v2Response.body) {
        v2Chunks.push(chunk);
      }
      const v2Body = JSON.parse(Buffer.concat(v2Chunks).toString("utf8"));

      // V1 response (body is a string on the response object)
      const v1Body = JSON.parse(v1Response.body as string);

      // Both should have same data structure
      expect(v2Response.statusCode).toBe(v1Response.status);
      expect(v2Body.slideshow).toEqual(v1Body.slideshow);
    } finally {
      await v2Client.close();
      await v1Client.exit();
    }
  });
});
