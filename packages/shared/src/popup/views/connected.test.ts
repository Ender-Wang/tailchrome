// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from "vitest";
import { baseState, makePeer } from "../../__test__/fixtures";
import type { RoutingHealth } from "../../types";
import { sendMessage } from "../popup";
import { renderConnected, setExternalProxyDetails, updateConnected } from "./connected";

vi.mock("../popup", () => ({
  sendMessage: vi.fn(),
  enterSubView: vi.fn(),
  leaveSubView: vi.fn(),
  getLatestState: vi.fn(),
}));

describe("connected view", () => {
  beforeEach(() => {
    vi.mocked(sendMessage).mockClear();
    document.body.textContent = "";
  });

  it.each<RoutingHealth["status"]>(["active", "blocked", "conflicted", "unavailable", "inactive"])(
    "reflects browser routing health (%s) in the connection indicator",
    (status) => {
      const root = document.createElement("div");
      renderConnected(root, baseState({ routingHealth: { status: "active", message: "" } }));
      updateConnected(root, baseState({ routingHealth: { status, message: "Routing detail" } }));

      const expected = {
        active: ["active", "Browser routing active"],
        blocked: ["blocked", "Protected traffic blocked"],
        conflicted: ["warning", "Browser proxy controlled elsewhere"],
        unavailable: ["warning", "Browser routing unavailable"],
        inactive: ["inactive", "Browser routing off"],
      }[status];
      expect(root.querySelector<HTMLElement>(".status-bar")?.dataset.routing).toBe(expected[0]);
      expect(root.querySelector(".status-bar-routing")?.textContent).toBe(expected[1]);
      expect(root.querySelector(".status-bar-routing")?.getAttribute("title")).toBe("Routing detail");
    },
  );

  it("does not show working routing until browser confirmation arrives", () => {
    const root = document.createElement("div");
    renderConnected(root, baseState());
    expect(root.querySelector<HTMLElement>(".status-bar")?.dataset.routing).toBe("warning");
    expect(root.textContent).toContain("Checking browser routing");
  });

  it("explains when an unavailable exit node is blocking traffic", () => {
    const root = document.createElement("div");
    renderConnected(root, baseState({
      selectedExitNodeID: "missing-exit",
      routingHealth: { status: "blocked", message: "" },
    }));
    expect(root.querySelector(".status-bar-routing")?.textContent)
      .toBe("Exit node unavailable · traffic blocked");
  });

  it("counts only actual devices when deciding whether to show search", () => {
    const root = document.createElement("div");
    const peers = Array.from({ length: 8 }, (_, i) => makePeer({
      id: `mullvad-${i}`, tags: ["tag:mullvad-exit-node"],
    }));
    renderConnected(root, baseState({ peers }));
    expect(root.querySelector(".peer-search")).toBeNull();
    expect(root.querySelector(".peer-list-toggle")).toBeNull();
  });

  const devicePeers = Array.from({ length: 8 }, (_, i) => makePeer({
    id: `device-${i}`,
    dnsName: `device-${i}.example.ts.net.`,
  }));

  it("keeps View all selected when profile data triggers a full redraw", () => {
    const root = document.createElement("div");
    const state = baseState({ peers: devicePeers });
    renderConnected(root, state);
    root.querySelector<HTMLButtonElement>(".peer-list-toggle")!.click();
    const originalContainer = root.querySelector(".peer-container");

    updateConnected(root, {
      ...state,
      currentProfile: { id: "work", name: "Work" },
      profiles: [{ id: "work", name: "Work" }],
    });

    expect(root.querySelector(".peer-container")).not.toBe(originalContainer);
    expect(root.querySelectorAll(".peer-item")).toHaveLength(8);
    expect(root.querySelector(".peer-list-toggle")?.getAttribute("aria-expanded")).toBe("true");
  });

  it("retains the expansion preference across search-threshold redraws", () => {
    const root = document.createElement("div");
    const state = baseState({ peers: devicePeers });
    renderConnected(root, state);
    root.querySelector<HTMLButtonElement>(".peer-list-toggle")!.click();

    updateConnected(root, { ...state, peers: devicePeers.slice(0, 5) });
    expect(root.querySelector(".peer-search")).toBeNull();
    updateConnected(root, state);
    expect(root.querySelectorAll(".peer-item")).toHaveLength(8);

    root.querySelector<HTMLButtonElement>(".peer-list-toggle")!.click();
    renderConnected(root, state);
    expect(root.querySelectorAll(".peer-item")).toHaveLength(5);
  });

  it("keeps View all when returning to the dashboard without sharing it between roots", () => {
    const root = document.createElement("div");
    const state = baseState({ peers: devicePeers });
    renderConnected(root, state);
    root.querySelector<HTMLButtonElement>(".peer-list-toggle")!.click();
    root.textContent = "Another view";

    const otherRoot = document.createElement("div");
    renderConnected(otherRoot, state);
    expect(otherRoot.querySelectorAll(".peer-item")).toHaveLength(5);

    renderConnected(root, state);
    expect(root.querySelectorAll(".peer-item")).toHaveLength(8);
  });

  it("renders Exit Node and Profile navigation as native buttons", () => {
    const root = document.createElement("div");
    renderConnected(
      root,
      baseState({
        currentProfile: { id: "work", name: "Work" },
        profiles: [{ id: "work", name: "Work" }],
      }),
    );

    const buttons = Array.from(root.querySelectorAll("button.setting-row"));
    expect(buttons.some((button) => button.textContent?.includes("Exit Node"))).toBe(
      true,
    );
    expect(buttons.some((button) => button.textContent?.includes("Profile"))).toBe(
      true,
    );
  });

  it("replaces an empty pre-login profile with the authenticated account", () => {
    const root = document.createElement("div");
    const pending = baseState({
      currentProfile: { id: "", name: "" },
      profiles: [{ id: "personal", name: "Personal" }],
    });
    renderConnected(root, pending);

    expect(
      root.querySelector(".setting-value-profile")?.textContent,
    ).toContain("Default");

    updateConnected(root, {
      ...pending,
      currentProfile: { id: "work", name: "Work" },
      profiles: [
        { id: "personal", name: "Personal" },
        { id: "work", name: "Work" },
      ],
    });

    expect(
      root.querySelector(".setting-value-profile")?.textContent,
    ).toContain("Work");
  });

  it("preserves focused split-tunneling edits across status updates", () => {
    const root = document.createElement("div");
    const state = baseState({
      domainSplit: { mode: "bypass", domains: ["saved.example.com"] },
    });
    renderConnected(root, state);
    const input = root.querySelector<HTMLTextAreaElement>(
      ".split-tunneling-input",
    )!;
    input.value = "unsaved.example.com";
    input.dispatchEvent(new Event("input"));
    input.focus();

    updateConnected(root, {
      ...state,
      stateVersion: 1,
      domainSplit: { mode: "only", domains: ["server.example.com"] },
    });

    expect(input.value).toBe("unsaved.example.com");
  });

  it("summarises saved split-tunneling rules on the collapsed row", () => {
    const root = document.createElement("div");
    renderConnected(
      root,
      baseState({
        domainSplit: { mode: "only", domains: ["a.example.com", "b.example.com"] },
      }),
    );

    const header = root.querySelector<HTMLButtonElement>(
      ".split-tunneling-header",
    )!;
    const editor = root.querySelector<HTMLElement>(".split-tunneling-editor")!;

    // Collapsed by default, but the row still reports the live rule state.
    expect(editor.classList.contains("hidden")).toBe(true);
    expect(header.getAttribute("aria-expanded")).toBe("false");
    expect(
      header.querySelector(".setting-value-split-tunneling")!.firstChild!
        .textContent,
    ).toBe("Only · 2 domains");
  });

  it("reports no rules as Off, but never reports empty Only mode as Off", () => {
    const offRoot = document.createElement("div");
    renderConnected(
      offRoot,
      baseState({ domainSplit: { mode: "bypass", domains: [] } }),
    );
    expect(
      offRoot.querySelector(".setting-value-split-tunneling")!.firstChild!
        .textContent,
    ).toBe("Off");

    // "only" with an empty list routes nothing through the exit node — the
    // opposite of off — so it must not read as "Off".
    const onlyRoot = document.createElement("div");
    renderConnected(
      onlyRoot,
      baseState({ domainSplit: { mode: "only", domains: [] } }),
    );
    expect(
      onlyRoot.querySelector(".setting-value-split-tunneling")!.firstChild!
        .textContent,
    ).toBe("Only · no domains");
  });

  it("expands the split-tunneling editor when the row is clicked", () => {
    const root = document.createElement("div");
    renderConnected(root, baseState({}));

    const header = root.querySelector<HTMLButtonElement>(
      ".split-tunneling-header",
    )!;
    const editor = root.querySelector<HTMLElement>(".split-tunneling-editor")!;
    expect(header.getAttribute("aria-controls")).toBe(editor.id);

    header.click();
    expect(editor.classList.contains("hidden")).toBe(false);
    expect(header.getAttribute("aria-expanded")).toBe("true");

    header.click();
    expect(editor.classList.contains("hidden")).toBe(true);
    expect(header.getAttribute("aria-expanded")).toBe("false");
  });

  it("refreshes the split-tunneling summary on save and on state updates", () => {
    const root = document.createElement("div");
    const state = baseState({ domainSplit: { mode: "bypass", domains: [] } });
    renderConnected(root, state);

    const summary = () =>
      root.querySelector(".setting-value-split-tunneling")!.firstChild!
        .textContent;
    expect(summary()).toBe("Off");

    const input = root.querySelector<HTMLTextAreaElement>(
      ".split-tunneling-input",
    )!;
    input.value = "saved.example.com";
    input.dispatchEvent(new Event("input"));
    root.querySelector<HTMLButtonElement>(".split-tunneling-save")!.click();

    // Optimistic: the summary updates without waiting for the background.
    expect(summary()).toBe("Bypass · 1 domain");

    updateConnected(root, {
      ...state,
      stateVersion: 1,
      domainSplit: { mode: "only", domains: ["a.example.com", "b.example.com"] },
    });
    expect(summary()).toBe("Only · 2 domains");
  });

  it("commits unsaved split-tunneling domains when the mode changes", () => {
    const root = document.createElement("div");
    renderConnected(
      root,
      baseState({ domainSplit: { mode: "bypass", domains: [] } }),
    );
    const input = root.querySelector<HTMLTextAreaElement>(
      ".split-tunneling-input",
    )!;
    input.value = "internal.example.com\n***";
    input.dispatchEvent(new Event("input"));

    root
      .querySelector<HTMLButtonElement>(
        '.split-tunneling-mode-btn[data-mode="only"]',
      )!
      .click();

    expect(sendMessage).toHaveBeenCalledWith({
      type: "set-domain-split",
      config: { mode: "only", domains: ["internal.example.com"] },
    });
  });

  it("gates LAN access, sends its command and never uses a wildcard as a client URL", () => {
    const root = document.createElement("div");
    document.body.appendChild(root);
    const proxy = {
      enabled: true, running: true, host: "127.0.0.1", port: 12345,
      protocols: ["http", "socks5"], authRequired: true,
    };
    renderConnected(root, baseState({ supportsExternalProxy: true, externalProxy: proxy }));
    expect(root.querySelector("[data-external-proxy-lan]")).toBeNull();
    renderConnected(root, baseState({ supportsExternalProxy: true, supportsExternalProxyLAN: true, externalProxy: proxy }));
    const toggle = root.querySelector<HTMLInputElement>("[data-external-proxy-lan] input")!;
    toggle.checked = true;
    toggle.dispatchEvent(new Event("change"));
    expect(sendMessage).toHaveBeenCalledWith({ type: "set-external-proxy-lan", allowLAN: true });
    const next = baseState({ supportsExternalProxy: true, supportsExternalProxyLAN: true,
      externalProxy: { ...proxy, allowLAN: true, host: "0.0.0.0", lanAddresses: ["192.168.1.20"], pacPath: "/proxy.pac" } });
    updateConnected(root, next);
    setExternalProxyDetails({ ...next.externalProxy!, username: "tailchrome", password: "secret" });
    const details = root.querySelector<HTMLElement>("[data-external-proxy-details]")!;
    const address = details.querySelector<HTMLSelectElement>("select")!;
    expect(Array.from(address.options, option => option.value)).toEqual(["127.0.0.1", "192.168.1.20"]);
    address.value = "192.168.1.20";
    address.dispatchEvent(new Event("change"));
    expect(details.querySelector<HTMLInputElement>('[aria-label="Proxy server"]')!.value).toBe("192.168.1.20");
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    details.querySelector<HTMLButtonElement>('[aria-label="Copy HTTP URL"]')!.click();
    expect(writeText).toHaveBeenLastCalledWith("http://tailchrome:secret@192.168.1.20:12345");
    details.querySelector<HTMLButtonElement>('[aria-label="Copy SOCKS5 URL"]')!.click();
    expect(writeText).toHaveBeenLastCalledWith("socks5://tailchrome:secret@192.168.1.20:12345");
    expect(details.querySelector<HTMLInputElement>('[aria-label="Automatic proxy URL"]')!.value).toBe("http://192.168.1.20:12345/proxy.pac");
    details.querySelector<HTMLButtonElement>('[aria-label="Copy PAC URL"]')!.click();
    expect(writeText).toHaveBeenLastCalledWith("http://192.168.1.20:12345/proxy.pac");
    expect(details.textContent).toContain("Proxy authentication is still required");
    expect(details.textContent).toContain("If authentication does not work, use Manual");
    expect(details.textContent).not.toContain("secret");
    expect(document.querySelector(".toast")!.textContent).toBe("Copied PAC URL");
    root.remove();
  });

  it("capability-gates the macOS local app proxy and sends toggle commands", () => {
    const unsupported = document.createElement("div");
    renderConnected(unsupported, baseState({ supportsExternalProxy: false }));
    expect(unsupported.textContent).toContain("Helper update required");

    const root = document.createElement("div");
    renderConnected(
      root,
      baseState({
        supportsExternalProxy: true,
        externalProxy: {
          enabled: false,
          running: false,
          host: "127.0.0.1",
          protocols: ["http", "socks5"],
          authRequired: true,
        },
      }),
    );
    const toggle = root.querySelector<HTMLInputElement>(
      "[data-external-proxy-row] input",
    )!;
    toggle.checked = true;
    toggle.dispatchEvent(new Event("change"));
    expect(sendMessage).toHaveBeenCalledWith({
      type: "set-external-proxy",
      enabled: true,
    });
  });

  it("reveals copyable app proxy details without placing them in state", () => {
    const root = document.createElement("div");
    document.body.appendChild(root);
    renderConnected(
      root,
      baseState({
        supportsExternalProxy: true,
        externalProxy: {
          enabled: true,
          running: true,
          host: "127.0.0.1",
          port: 12345,
          protocols: ["http", "socks5"],
          authRequired: true,
          username: "tailchrome",
        },
      }),
    );
    setExternalProxyDetails({
      enabled: true,
      running: true,
      host: "127.0.0.1",
      port: 12345,
      protocols: ["http", "socks5"],
      authRequired: true,
      username: "tailchrome",
      password: "secret",
    });
    const details = root.querySelector<HTMLElement>("[data-external-proxy-details]")!;
    const password = details.querySelector<HTMLInputElement>('[aria-label="Proxy password"]')!;
    expect(password.type).toBe("password");
    expect(password.value).toBe("secret");
    expect(details.textContent).not.toContain("secret");
    details.querySelector<HTMLButtonElement>('[aria-label="Show proxy password"]')!.click();
    expect(password.type).toBe("text");
    details.querySelector<HTMLButtonElement>('[aria-label="Hide proxy password"]')!.click();
    expect(password.type).toBe("password");
    expect(details.querySelector<HTMLInputElement>('[aria-label="Proxy server"]')!.value).toBe("127.0.0.1");
  });
});
