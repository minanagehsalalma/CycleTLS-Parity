/**
 * Tests for pid undefined safety checks in cleanExit and cleanup methods.
 *
 * Bug: Non-null assertion `this.child.pid!` panics if pid is undefined
 * (process failed to spawn). Should check pid !== undefined first and
 * fall back to direct kill.
 */

describe("pid undefined safety", () => {
  test("cleanExit should check pid !== undefined before process.kill", () => {
    const fs = require("fs");
    const source = fs.readFileSync(
      require.resolve("../dist/index.js"),
      "utf8"
    );

    // Extract ONLY the cleanExit method body
    const methodStart = source.indexOf("async cleanExit(");
    const methodEnd = source.indexOf("async cleanup()", methodStart);
    const methodSource = source.substring(methodStart, methodEnd);

    // cleanExit should have pid !== undefined check (not blind pid! access)
    expect(methodSource).toContain("pid !== undefined");
    // Should also have a fallback for when pid is undefined
    expect(methodSource).toContain(".kill(");
  });

  test("cleanup should check pid !== undefined before process.kill", () => {
    const fs = require("fs");
    const source = fs.readFileSync(
      require.resolve("../dist/index.js"),
      "utf8"
    );

    // Extract ONLY the cleanup method body
    const methodStart = source.indexOf("async cleanup()");
    const methodEnd = source.indexOf("Force close the WebSocket");
    const methodSource = source.substring(methodStart, methodEnd);

    // cleanup should have pid !== undefined check
    expect(methodSource).toContain("pid !== undefined");
  });

  test("no non-null pid assertions should remain in process.kill calls", () => {
    const fs = require("fs");
    const path = require("path");
    const tsSource = fs.readFileSync(
      path.resolve(__dirname, "..", "src", "index.ts"),
      "utf8"
    );

    // After the fix, there should be NO occurrences of .pid! in the TypeScript source
    // (the ! is the TypeScript non-null assertion that we're removing)
    const pidBangMatches = tsSource.match(/\.pid!/g) || [];
    expect(pidBangMatches.length).toBe(0);
  });
});
