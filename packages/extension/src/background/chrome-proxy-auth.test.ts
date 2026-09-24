import { describe, expect, it, vi } from "vitest";
import { ChromeProxyAuth } from "./chrome-proxy-auth";

function challenge(overrides: Record<string, unknown> = {}) {
  return { isProxy: true, challenger: { host: "127.0.0.1", port: 1055 }, requestId: "1", ...overrides } as chrome.webRequest.OnAuthRequiredDetails;
}

describe("Chrome proxy authentication", () => {
  it("responds only to proxy challenges at the current loopback port", () => {
    const auth = new ChromeProxyAuth();
    auth.set({ port: 1055, username: "user", password: "secret" });
    const callback = vi.fn();
    for (const details of [challenge({ isProxy: false }), challenge({ challenger: { host: "evil.example", port: 1055 } }), challenge({ challenger: { host: "127.0.0.1", port: 9999 } })]) {
      auth.listener(details, callback);
      expect(callback).toHaveBeenLastCalledWith({});
    }
    auth.listener(challenge(), callback);
    expect(callback).toHaveBeenLastCalledWith({ authCredentials: { username: "user", password: "secret" } });
    auth.listener(challenge(), callback);
    expect(callback).toHaveBeenLastCalledWith({ cancel: true });
    auth.set(null);
    auth.listener(challenge({ requestId: "2" }), callback);
    expect(callback).toHaveBeenLastCalledWith({});
  });

  it("primes Chromium's proxy auth cache once for each helper session", async () => {
    const fetcher = vi.fn(async () => new Response(null, { status: 204 }));
    const auth = new ChromeProxyAuth(fetcher as typeof fetch);

    auth.prime();
    expect(fetcher).not.toHaveBeenCalled();

    auth.set({ port: 1055, username: "user", password: "secret" });
    auth.prime();
    auth.prime();
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(fetcher).toHaveBeenCalledWith(
      "http://100.100.100.100/.well-known/tailchrome-proxy-auth",
      expect.objectContaining({
        method: "HEAD",
        mode: "no-cors",
        redirect: "manual",
      }),
    );

    auth.set({ port: 1056, username: "user", password: "new-secret" });
    auth.prime();
    expect(fetcher).toHaveBeenCalledTimes(2);
  });
});
