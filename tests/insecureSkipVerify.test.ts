import CycleTLS from "../dist/index.js";
import { createSuiteInstance } from "./test-utils.js";

jest.setTimeout(30000);

describe("CycleTLS InsecureSkipVerify Test", () => {
  let client: CycleTLS;
  let cleanup: () => Promise<void>;
  let ja3 = "771,4865-4867-4866-49195-49199-52393-52392-49196-49200-49162-49161-49171-49172-51-57-47-53-10,0-23-65281-10-11-35-16-5-51-43-13-45-28-21,29-23-24-25-256-257,0";
  let userAgent = "Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:87.0) Gecko/20100101 Firefox/87.0";

  beforeAll(async () => {
    ({ instance: client, cleanup } = await createSuiteInstance({ port: 9152, timeout: 30000 }));
  });

  afterAll(async () => {
    await cleanup();
  });

  test("Should throw a handshake error for insecureSkipVerify", async () => {
    const url = "https://expired.badssl.com";
    // New API throws on certificate errors instead of returning status 495
    try {
      await client.get(url, {
        ja3: ja3,
        userAgent: userAgent,
        insecureSkipVerify: false,
      });
      fail("Expected error to be thrown");
    } catch (error: any) {
      expect(error.message).toMatch(/certificate/i);
    }
  });

  test("Should return a 200 response for insecureSkipVerify", async () => {
    const url = "https://expired.badssl.com";
    const response = await client.get(url, {
      ja3: ja3,
      userAgent: userAgent,
      insecureSkipVerify: true,
    });

    expect(response.status).toBe(200);
  });

  test("Should throw certificate error for self-signed certificate", async () => {
    const url = "https://self-signed.badssl.com";
    // New API throws on certificate errors instead of returning status 495
    try {
      await client.get(url, {
        ja3: ja3,
        userAgent: userAgent,
        insecureSkipVerify: false,
      });
      fail("Expected error to be thrown");
    } catch (error: any) {
      expect(error.message).toMatch(/certificate/i);
    }
  });

  test("Should properly handle connection refused error", async () => {
    // Test with a port that's likely not listening
    // New API throws on connection errors instead of returning status 502
    try {
      await client.get("https://localhost:9999", {
        ja3: ja3,
        userAgent: userAgent,
        timeout: 5000,
      });
      fail("Expected error to be thrown");
    } catch (error: any) {
      expect(error.message).toMatch(/connection refused|syscall error/i);
    }
  });
});
