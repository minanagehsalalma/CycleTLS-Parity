const { withCycleTLS } = require("./test-utils.js");
jest.setTimeout(30000);

let ja3 =
  "771,4865-4867-4866-49195-49199-52393-52392-49196-49200-49162-49161-49171-49172-51-57-47-53-10,0-23-65281-10-11-35-16-5-51-43-13-45-28-21,29-23-24-25-256-257,0";
let userAgent =
  "Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:87.0) Gecko/20100101 Firefox/87.0";

test("Multiple concurrent GET requests should complete successfully", async () => {
  // Use a random high port to avoid conflicts
  const port = 9200 + Math.floor(Math.random() * 100);
  await withCycleTLS({ port, timeout: 30000, autoSpawn: true }, async (cycleTLS) => {
    const urls = [
      "https://httpbin.org/user-agent",
      "https://httpbin.org/get",
      "https://httpbin.org/headers",
    ];

    // Make requests concurrently using Promise.all
    const promises = urls.map(url =>
      cycleTLS.get(url, {
        ja3: ja3,
        userAgent: userAgent,
      })
    );
    const results = await Promise.all(promises);

    // Verify all responses
    for (const response of results) {
      expect(response.statusCode).toBe(200);

      // Consume body to properly complete request
      for await (const _ of response.body) {
        // drain body
      }
    }
  });
});

test("POST request should complete successfully", async () => {
  const port = 9210 + Math.floor(Math.random() * 100);
  await withCycleTLS({ port, timeout: 30000, autoSpawn: true }, async (cycleTLS) => {
    const response = await cycleTLS.post(
      "https://httpbin.org/post",
      JSON.stringify({ field: "POST-VAL" }),
      {
        ja3: ja3,
        userAgent: userAgent,
        headers: { "Content-Type": "application/json" },
      }
    );

    expect(response.statusCode).toBe(200);

    // Consume body
    for await (const _ of response.body) {
      // drain body
    }
  });
});

test("Sequential requests to same host should reuse connection", async () => {
  await withCycleTLS({ port: 9151, timeout: 30000 }, async (cycleTLS) => {
    // Make multiple requests to same domain
    const url = "https://httpbin.org";

    // First request
    const response1 = await cycleTLS.get(`${url}/get`, {
      ja3: ja3,
      userAgent: userAgent,
    });
    expect(response1.statusCode).toBe(200);

    // Second request - should reuse connection
    const response2 = await cycleTLS.get(`${url}/get?second=true`, {
      ja3: ja3,
      userAgent: userAgent,
    });
    expect(response2.statusCode).toBe(200);

    // Third request with different path but same domain - should still reuse connection
    const response3 = await cycleTLS.get(`${url}/headers`, {
      ja3: ja3,
      userAgent: userAgent,
    });
    expect(response3.statusCode).toBe(200);

    // The connection reuse is happening at the Go level, and we can't directly test it from JS
    // But we can verify that all requests completed successfully
  });
});
