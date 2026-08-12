import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const html = await readFile(new URL("../web/index.html", import.meta.url), "utf8");
const css = await readFile(new URL("../web/styles.css", import.meta.url), "utf8");

test("login and registration are required before the tenant workspace", () => {
  assert.match(html, /id="auth-view"/);
  assert.match(html, /id="auth-login-tab"/);
  assert.match(html, /id="auth-register-tab"/);
  assert.match(html, /id="app-shell" hidden/);
  assert.match(html, /id="logout"/);
});

test("global defaults and current-conversation settings have distinct entries", () => {
  assert.match(html, /id="global-settings"/);
  assert.match(html, /id="conversation-settings"/);
  assert.match(html, /id="settings-scope"/);
  assert.match(html, /id="settings-global-scope"/);
  assert.match(html, /id="settings-conversation-scope"/);
});

test("skills have a management tab, editor, and per-agent assignment surface", () => {
  assert.match(html, /id="tab-skills"/);
  assert.match(html, /id="skills-view"/);
  assert.match(html, /id="skill-form"/);
  assert.match(html, /id="skill-assignments"/);
  assert.match(html, /id="conversation-skills"/);
});

test("agent editor exposes local installation status before adding a provider", () => {
  assert.match(html, /id="agent-provider"/);
  assert.match(html, /id="agent-provider-status"/);
  assert.match(html, /id="provider-health-list"/);
});

test("new conversation is one click and default agents live in global settings", () => {
  assert.doesNotMatch(html, /id="new-dialog"/);
  assert.match(html, /id="setting-default-agent-options"/);
  assert.match(css, /\.setting-agent-options/);
});

test("dialog cancel buttons bypass required-field validation", () => {
  const cancelButtons = [...html.matchAll(/<button[^>]*value="cancel"[^>]*>/g)].map((match) => match[0]);
  assert.ok(cancelButtons.length > 0);
  assert.ok(cancelButtons.every((button) => /formnovalidate/.test(button)), cancelButtons.join("\n"));
});

test("conversation title is renamed inline instead of in the management dialog", () => {
  assert.match(html, /id="conversation-title"[^>]*tabindex="0"/);
  assert.doesNotMatch(html, /id="manage-conversation-title"/);
  assert.match(css, /#conversation-title\.editing/);
});
