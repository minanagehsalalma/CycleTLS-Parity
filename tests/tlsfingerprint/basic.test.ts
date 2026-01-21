/**
 * Basic TLS fingerprint tests against tlsfingerprint.com
 *
 * Tests basic HTTP methods (GET, POST, PUT, PATCH, DELETE) and
 * validates that TLS fingerprint fields are present in responses.
 *
 * Mirrors Go tests in cycletls/tests/integration/tlsfingerprint/echo_test.go
 *
 */

import CycleTLS from "../../dist/index.js";
import {
  TEST_SERVER_URL,
  getDefaultOptions,
  assertTLSFieldsPresent,
  consumeBodyAsJson,
  EchoResponse,
  isServiceAvailable,
} from "./helpers";

// Longer timeout for network requests
jest.setTimeout(90000);

// Check service availability and conditionally run tests
let serviceAvailable = false;

beforeAll(async () => {
  serviceAvailable = await isServiceAvailable();
  if (!serviceAvailable) {
    console.warn('SKIPPING tlsfingerprint tests: Service unavailable (received 521 or timeout)');
  }
});

// Helper to conditionally run test
const conditionalTest = (name: string, fn: () => Promise<void>) => {
  it(name, async () => {
    if (!serviceAvailable) {
      console.log(`Skipped: ${name} (service unavailable)`);
      return;
    }
    await fn();
  });
};

describe("TLS Fingerprint - Basic HTTP Methods", () => {
  let client: CycleTLS;

  beforeEach(() => {
    if (!serviceAvailable) return;
    client = new CycleTLS({
      port: 9119,
      debug: false,
      timeout: 30000,
      autoSpawn: true,
    });
  });

  afterEach(async () => {
    if (client) {
      await client.close();
    }
  });

  describe("GET Requests", () => {
    conditionalTest("should make a GET request and return TLS fingerprint fields", async () => {
      const options = getDefaultOptions();
      const response = await client.get(`${TEST_SERVER_URL}/get?foo=bar`, options);

      expect(response.statusCode).toBe(200);

      const body = await consumeBodyAsJson<EchoResponse>(response.body);

      // Verify TLS fingerprint fields are present
      assertTLSFieldsPresent(body as unknown as Record<string, unknown>);

      // Verify query args are present
      expect(body.args).toBeDefined();
      expect(body.args?.foo).toBe("bar");
    });

    conditionalTest("should include ja3 hash in response", async () => {
      const options = getDefaultOptions();
      const response = await client.get(`${TEST_SERVER_URL}/get`, options);

      expect(response.statusCode).toBe(200);

      const body = await consumeBodyAsJson<EchoResponse>(response.body);

      expect(body.ja3).toBeDefined();
      expect(body.ja3.length).toBeGreaterThan(0);
      expect(body.ja3_hash).toBeDefined();
      expect(body.ja3_hash.length).toBeGreaterThan(0);
    });

    conditionalTest("should include ja4 fingerprint in response", async () => {
      const options = getDefaultOptions();
      const response = await client.get(`${TEST_SERVER_URL}/get`, options);

      expect(response.statusCode).toBe(200);

      const body = await consumeBodyAsJson<EchoResponse>(response.body);

      expect(body.ja4).toBeDefined();
      expect(body.ja4.length).toBeGreaterThan(0);
    });

    conditionalTest("should include peetprint in response", async () => {
      const options = getDefaultOptions();
      const response = await client.get(`${TEST_SERVER_URL}/get`, options);

      expect(response.statusCode).toBe(200);

      const body = await consumeBodyAsJson<EchoResponse>(response.body);

      expect(body.peetprint).toBeDefined();
      expect(body.peetprint.length).toBeGreaterThan(0);
      expect(body.peetprint_hash).toBeDefined();
      expect(body.peetprint_hash.length).toBeGreaterThan(0);
    });
  });

  describe("POST Requests", () => {
    conditionalTest("should make a POST request with JSON body", async () => {
      const options = getDefaultOptions();
      const response = await client.post(
        `${TEST_SERVER_URL}/post`,
        JSON.stringify({ message: "hello" }),
        {
          ...options,
          headers: { "Content-Type": "application/json" },
        }
      );

      expect(response.statusCode).toBe(200);

      const body = await consumeBodyAsJson<EchoResponse>(response.body);

      assertTLSFieldsPresent(body as unknown as Record<string, unknown>);

      // Verify the data was received (either in data or json field)
      expect(body.data || body.json).toBeTruthy();
    });
  });

  describe("PUT Requests", () => {
    conditionalTest("should make a PUT request with JSON body", async () => {
      const options = getDefaultOptions();
      const response = await client.request({
        url: `${TEST_SERVER_URL}/put`,
        method: "PUT",
        body: JSON.stringify({ update: "data" }),
        headers: { "Content-Type": "application/json" },
        ...options,
      });

      expect(response.statusCode).toBe(200);

      const body = await consumeBodyAsJson<EchoResponse>(response.body);

      assertTLSFieldsPresent(body as unknown as Record<string, unknown>);
    });
  });

  describe("PATCH Requests", () => {
    conditionalTest("should make a PATCH request with JSON body", async () => {
      const options = getDefaultOptions();
      const response = await client.request({
        url: `${TEST_SERVER_URL}/patch`,
        method: "PATCH",
        body: JSON.stringify({ patch: "value" }),
        headers: { "Content-Type": "application/json" },
        ...options,
      });

      expect(response.statusCode).toBe(200);

      const body = await consumeBodyAsJson<EchoResponse>(response.body);

      assertTLSFieldsPresent(body as unknown as Record<string, unknown>);
    });
  });

  describe("DELETE Requests", () => {
    conditionalTest("should make a DELETE request", async () => {
      const options = getDefaultOptions();
      const response = await client.request({
        url: `${TEST_SERVER_URL}/delete`,
        method: "DELETE",
        ...options,
      });

      expect(response.statusCode).toBe(200);

      const body = await consumeBodyAsJson<EchoResponse>(response.body);

      assertTLSFieldsPresent(body as unknown as Record<string, unknown>);
    });
  });

  describe("Anything Endpoint", () => {
    conditionalTest("should echo request to /anything endpoint", async () => {
      const options = getDefaultOptions();
      const response = await client.get(`${TEST_SERVER_URL}/anything`, options);

      expect(response.statusCode).toBe(200);

      const body = await consumeBodyAsJson<EchoResponse>(response.body);

      assertTLSFieldsPresent(body as unknown as Record<string, unknown>);

      // Verify method is captured
      expect(body.method).toBe("GET");
    });

    conditionalTest("should capture POST method in /anything endpoint", async () => {
      const options = getDefaultOptions();
      const response = await client.post(
        `${TEST_SERVER_URL}/anything`,
        JSON.stringify({ test: "data" }),
        {
          ...options,
          headers: { "Content-Type": "application/json" },
        }
      );

      expect(response.statusCode).toBe(200);

      const body = await consumeBodyAsJson<EchoResponse>(response.body);

      assertTLSFieldsPresent(body as unknown as Record<string, unknown>);
      expect(body.method).toBe("POST");
    });
  });

  describe("Headers Endpoint", () => {
    conditionalTest("should echo custom headers", async () => {
      const options = getDefaultOptions();
      const response = await client.request({
        url: `${TEST_SERVER_URL}/headers`,
        ...options,
        headers: {
          "X-Custom-Header": "TestValue123",
          Accept: "application/json",
        },
      });

      expect(response.statusCode).toBe(200);

      const body = await consumeBodyAsJson<Record<string, unknown>>(response.body);

      assertTLSFieldsPresent(body);

      // Headers should be echoed back
      const headers = body.headers as Record<string, string> | undefined;
      expect(headers).toBeDefined();
      if (headers) {
        // Header names may be normalized (case-insensitive)
        const customHeader =
          headers["X-Custom-Header"] || headers["x-custom-header"];
        expect(customHeader).toBe("TestValue123");
      }
    });
  });
});
