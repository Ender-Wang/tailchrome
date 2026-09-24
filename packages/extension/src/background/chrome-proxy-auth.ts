import { ProxySession } from "@tailchrome/shared/background/proxy-session";
import { TAILSCALE_SERVICE_IP } from "@tailchrome/shared/constants";
import type { ProxySessionCredentials } from "@tailchrome/shared/types";

const PROXY_AUTH_PROBE_URL =
  `http://${TAILSCALE_SERVICE_IP}/.well-known/tailchrome-proxy-auth`;

export class ChromeProxyAuth {
  private readonly session = new ProxySession();
  private readonly attemptedRequests = new Set<string>();
  private sessionGeneration = 0;
  private primedGeneration = -1;
  private hasActiveSession = false;

  constructor(
    private readonly fetcher: typeof fetch = globalThis.fetch.bind(globalThis),
  ) {
    chrome.webRequest.onAuthRequired.addListener(
      this.listener,
      { urls: ["<all_urls>"] },
      ["asyncBlocking"],
    );
  }

  hasSession(port: number): boolean {
    return this.session.credentialsFor(port) !== undefined;
  }

  set(session: ProxySessionCredentials | null): void {
    this.session.set(session);
    this.attemptedRequests.clear();
    this.sessionGeneration += 1;
    this.primedGeneration = -1;
    this.hasActiveSession = session !== null;
  }

  /**
   * Populate Chromium's shared proxy auth cache while this extension's
   * onAuthRequired listener is allowed to see the challenge. Chromium hides
   * requests initiated by other extensions from this listener, but those
   * requests can reuse a cached proxy credential after this handshake.
   */
  prime(): void {
    if (
      !this.hasActiveSession ||
      this.primedGeneration === this.sessionGeneration
    ) {
      return;
    }
    this.primedGeneration = this.sessionGeneration;
    void this.fetcher(PROXY_AUTH_PROBE_URL, {
      method: "HEAD",
      cache: "no-store",
      mode: "no-cors",
      redirect: "manual",
    }).catch(() => {
      // The response is irrelevant: a 404/503 still completes the proxy auth
      // exchange. Routing health remains authoritative for helper failures.
    });
  }

  readonly listener = (
    details: chrome.webRequest.OnAuthRequiredDetails,
    callback?: (response: chrome.webRequest.BlockingResponse) => void,
  ): undefined => {
    if (!details.isProxy || details.challenger.host !== "127.0.0.1") {
      callback?.({});
      return;
    }
    const credentials = this.session.credentialsFor(details.challenger.port);
    if (!credentials) {
      callback?.({});
      return;
    }
    // Do not open an endless challenge loop if a process no longer accepts its credential.
    if (this.attemptedRequests.has(details.requestId)) {
      callback?.({ cancel: true });
      return;
    }
    if (this.attemptedRequests.size >= 1024) {
      this.attemptedRequests.delete(this.attemptedRequests.values().next().value!);
    }
    this.attemptedRequests.add(details.requestId);
    callback?.({ authCredentials: credentials });
  };
}
