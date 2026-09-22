import assert from "node:assert/strict";
import test from "node:test";

import { pacHasExactHostRule, setInputValue } from "./assertions.mjs";

test("pacHasExactHostRule matches only an exact quoted host operand", () => {
  const pac = `function FindProxyForURL(url, host) {
    if (host === "outlook.office.com" || dnsDomainIs(host, ".outlook.office.com")) {
      return "DIRECT";
    }
  }`;

  assert.equal(pacHasExactHostRule(pac, "outlook.office.com"), true);
  assert.equal(pacHasExactHostRule(pac, "office.com"), false);
});

test("pacHasExactHostRule rejects lookalike domains and unrelated text", () => {
  const pac = `function FindProxyForURL(url, host) {
    // outlook.office.com
    if (host === "outlook.office.com.attacker.example") return "DIRECT";
    if (host === "prefix-outlook.office.com") return "DIRECT";
  }`;

  assert.equal(pacHasExactHostRule(pac, "outlook.office.com"), false);
});

const privilegedInputError =
  "Protocol error (input.performActions): unsupported operation The command does not support browsing contexts in privileged scope";

for (const { name, url, message } of [
  {
    name: "unrelated Firefox input errors",
    url: "moz-extension://fixture/popup.html",
    message: "Protocol error (input.performActions): browsing context closed",
  },
  {
    name: "Firefox navigation errors",
    url: "moz-extension://fixture/popup.html",
    message: "Protocol error (browsingContext.navigate): Navigation is not allowed in this context",
  },
  {
    name: "privileged input errors outside Firefox extension pages",
    url: "https://example.com/",
    message: privilegedInputError,
  },
]) {
  test(`setInputValue propagates ${name}`, async () => {
    const failure = new Error(message);
    let domInputUsed = false;
    const page = {
      waitForSelector: async () => {},
      focus: async () => {},
      keyboard: { down: async () => { throw failure; } },
      evaluate: async (_fn, input) => {
        if (input === undefined) return new URL(url).protocol;
        domInputUsed = true;
      },
    };

    await assert.rejects(setInputValue(page, "input", "query"), (error) => error === failure);
    assert.equal(domInputUsed, false);
  });
}

test("setInputValue propagates failures from the Firefox DOM fallback", async () => {
  const failure = new Error("input disappeared while entering its value");
  const page = {
    waitForSelector: async () => {},
    focus: async () => {},
    keyboard: { down: async () => { throw new Error(privilegedInputError); } },
    evaluate: async (_fn, input) => {
      if (input === undefined) return "moz-extension:";
      throw failure;
    },
  };

  await assert.rejects(setInputValue(page, "input", "query"), (error) => error === failure);
});

test("setInputValue checks the actual Firefox document when the cached URL is stale", async () => {
  let submitted;
  const page = {
    waitForSelector: async () => {},
    focus: async () => {},
    url: () => "about:blank",
    keyboard: { down: async () => { throw new Error(privilegedInputError); } },
    evaluate: async (_fn, input) => {
      if (input === undefined) return "moz-extension:";
      submitted = input;
    },
  };

  await setInputValue(page, "input", "query");
  assert.deepEqual(submitted, { selector: "input", value: "query" });
});
