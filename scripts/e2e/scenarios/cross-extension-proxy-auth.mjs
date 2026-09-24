import assert from "node:assert/strict";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { waitForPopup } from "../assertions.mjs";
import {
  expectedHostVersion,
  makeControl,
  makeExitNodePeer,
  makeRunningState,
} from "../fixtures.mjs";
import {
  createRoutingNetwork,
  proxyCredentials,
  routingTestHost,
} from "../network-fixture.mjs";

const fixtureExtensionId = "pmaemddieijoflbdigobhhlglobhaoep";
const fixtureFirefoxId = "cross-extension-request@tailchrome.test";
const fixtureFirefoxUuid = "7e06f5d8-cf9c-46d3-99b6-a296382e8395";
const fixtureDir = resolve(
  dirname(fileURLToPath(import.meta.url)),
  "../fixtures/cross-extension-request",
);

export const suite = "smoke";
export const browsers = ["chrome", "firefox"];
export const launchOptions = {
  additionalExtensionDirs: [fixtureDir],
  additionalFirefoxExtensionUuids: {
    [fixtureFirefoxId]: fixtureFirefoxUuid,
  },
  chromeArgs: [`--host-resolver-rules=MAP ${routingTestHost} 127.0.0.1`],
  firefoxPrefs: { "network.dns.localDomains": routingTestHost },
};

export const control = () => {
  const exitNode = makeExitNodePeer({ exitNode: true });
  const running = makeRunningState();
  return makeControl({
    allowRuntimeUpdates: true,
    enableRealProxyAuthProbe: true,
    proxyAuth: proxyCredentials,
    status: makeRunningState({
      exitNode,
      prefs: { ...running.prefs, exitNodeID: exitNode.id },
    }),
  });
};

async function nativeUpdate(page, update) {
  await page.evaluate(
    (value) => chrome.runtime.sendMessage({ tailchromeE2ENative: value }),
    update,
  );
}

async function fetchFromExtension(page, url) {
  return page.evaluate(async (target) => {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 4_000);
    try {
      await fetch(target, { cache: "no-store", signal: controller.signal });
      return "ok";
    } catch (error) {
      return String(error);
    } finally {
      clearTimeout(timeout);
    }
  }, url);
}

export async function run({ browser, browserName, openPopup }) {
  const network = await createRoutingNetwork();
  let popup;
  let fixture;
  try {
    popup = await openPopup();
    await waitForPopup(popup);
    await nativeUpdate(popup, {
      control: { proxyPort: network.proxyPort },
      reply: {
        procRunning: {
          port: network.proxyPort,
          pid: 1,
          version: expectedHostVersion,
          proxyAuth: proxyCredentials,
        },
      },
    });
    if (browserName === "chrome") {
      await popup.waitForFunction(
        (port) => new Promise((resolve, reject) => {
          chrome.proxy.settings.get({ incognito: false }, (details) => {
            const error = chrome.runtime.lastError;
            if (error) reject(new Error(error.message));
            else resolve(details.value.pacScript?.data.includes(`PROXY 127.0.0.1:${port}`));
          });
        }),
        { timeout: 5_000 },
        network.proxyPort,
      );
    }

    fixture = await browser.newPage();
    const fixtureOrigin = browserName === "chrome"
      ? `chrome-extension://${fixtureExtensionId}`
      : `moz-extension://${fixtureFirefoxUuid}`;
    try {
      await fixture.goto(
        `${fixtureOrigin}/runner.html`,
        { waitUntil: "domcontentloaded", timeout: 5_000 },
      );
    } catch (error) {
      if (browserName !== "firefox" || error?.name !== "TimeoutError") throw error;
      const title = await fixture.evaluate(() => document.title).catch(() => "");
      if (title !== "Cross-extension request fixture") throw error;
    }
    const fetchResult = await fetchFromExtension(
      fixture,
      network.baseURL + "/cross-extension",
    );
    assert.equal(
      fetchResult,
      "ok",
      JSON.stringify({ proxyAttempts: network.proxyAttempts, hits: network.hits }),
    );
    assert.ok(
      network.hits.some((hit) => hit.path === "/cross-extension" && hit.proxied),
      "The second extension's request did not traverse the authenticated proxy",
    );
  } finally {
    await fixture?.close();
    await popup?.close();
    await network.close();
  }
}
