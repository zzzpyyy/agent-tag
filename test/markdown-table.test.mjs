import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const source = await readFile(new URL("../web/markdown.js", import.meta.url), "utf8");
const moduleURL = `data:text/javascript;base64,${Buffer.from(source).toString("base64")}`;
const { renderMarkdown } = await import(moduleURL);

test("renders a Markdown table with alignment and inline formatting", () => {
  const html = renderMarkdown([
    "| 争议 | codex | pi |",
    "| :--- | :---: | ---: |",
    "| **tools** 是否属于 workspace | `agent` 的能力 | 是 |",
  ].join("\n"));

  assert.match(html, /<table>/);
  assert.match(html, /<thead>/);
  assert.match(html, /<tbody>/);
  assert.match(html, /<th class="align-left">争议<\/th>/);
  assert.match(html, /<th class="align-center">codex<\/th>/);
  assert.match(html, /<th class="align-right">pi<\/th>/);
  assert.match(html, /<strong>tools<\/strong>/);
  assert.match(html, /<code>agent<\/code>/);
});
