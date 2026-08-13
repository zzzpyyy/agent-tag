import assert from "node:assert/strict";
import test from "node:test";

import { shouldSubmitMessage } from "../web/composer.js";

test("IME candidate confirmation never submits the message", () => {
  assert.equal(shouldSubmitMessage({ key:"Enter", shiftKey:false, isComposing:true, keyCode:13 }, true), false);
  assert.equal(shouldSubmitMessage({ key:"Enter", shiftKey:false, isComposing:false, keyCode:229 }, false), false);
});

test("plain Enter submits while Shift+Enter keeps a newline", () => {
  assert.equal(shouldSubmitMessage({ key:"Enter", shiftKey:false, isComposing:false, keyCode:13 }, false), true);
  assert.equal(shouldSubmitMessage({ key:"Enter", shiftKey:true, isComposing:false, keyCode:13 }, false), false);
});
