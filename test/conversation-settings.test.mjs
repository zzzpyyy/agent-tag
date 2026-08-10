import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const html = await readFile(new URL("../web/index.html", import.meta.url), "utf8");

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
