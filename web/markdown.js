function escapeHtml(value) {
  return String(value).replace(/[&<>"']/g, (char) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  })[char]);
}

function safeHref(value) {
  const href = value.trim();
  if (href.startsWith("#") || href.startsWith("/")) return href;
  try {
    const parsed = new URL(href);
    return ["http:", "https:", "mailto:"].includes(parsed.protocol) ? href : null;
  } catch {
    return null;
  }
}

function inlineMarkdown(value) {
  const tokens = [];
  const stash = (html) => {
    const marker = `\u0000${tokens.length}\u0000`;
    tokens.push(html);
    return marker;
  };

  let source = String(value);
  source = source.replace(/`([^`\n]+)`/g, (_, code) => stash(`<code>${escapeHtml(code)}</code>`));
  source = source.replace(/\[([^\]\n]+)\]\(([^)\s]+)\)/g, (_, label, href) => {
    const safe = safeHref(href);
    if (!safe) return escapeHtml(label);
    return stash(`<a href="${escapeHtml(safe)}" target="_blank" rel="noopener noreferrer">${escapeHtml(label)}</a>`);
  });

  let html = escapeHtml(source)
    .replace(/\*\*([^*\n]+)\*\*/g, "<strong>$1</strong>")
    .replace(/__([^_\n]+)__/g, "<strong>$1</strong>")
    .replace(/(^|[^*])\*([^*\n]+)\*(?!\*)/g, "$1<em>$2</em>")
    .replace(/(^|[^_])_([^_\n]+)_(?!_)/g, "$1<em>$2</em>");
  html = html.replace(/\u0000(\d+)\u0000/g, (_, index) => tokens[Number(index)]);
  return html;
}

function isEscaped(value, index) {
  let slashes = 0;
  for (let cursor = index - 1; cursor >= 0 && value[cursor] === "\\"; cursor -= 1) slashes += 1;
  return slashes % 2 === 1;
}

function tableCells(line) {
  let source = String(line).trim();
  if (!source.includes("|")) return null;
  if (source.startsWith("|")) source = source.slice(1);
  if (source.endsWith("|") && !isEscaped(source, source.length - 1)) source = source.slice(0, -1);

  const cells = [];
  let cell = "";
  let inCode = false;
  let foundSeparator = false;
  for (let index = 0; index < source.length; index += 1) {
    const char = source[index];
    if (char === "`" && !isEscaped(source, index)) inCode = !inCode;
    if (char === "|" && !inCode && !isEscaped(source, index)) {
      cells.push(cell.trim());
      cell = "";
      foundSeparator = true;
      continue;
    }
    if (char === "|" && isEscaped(source, index) && cell.endsWith("\\")) cell = cell.slice(0, -1);
    cell += char;
  }
  cells.push(cell.trim());
  return foundSeparator || String(line).trim().startsWith("|") ? cells : null;
}

function tableDelimiter(line) {
  const cells = tableCells(line);
  return cells && cells.length > 0 && cells.every((cell) => /^:?-{3,}:?$/.test(cell));
}

function tableAlignment(delimiter) {
  if (delimiter.startsWith(":") && delimiter.endsWith(":")) return "center";
  if (delimiter.endsWith(":")) return "right";
  return "left";
}

export function renderMarkdown(markdown) {
  const lines = String(markdown ?? "").replace(/\r\n?/g, "\n").split("\n");
  const output = [];
  let paragraph = [];
  let listType = null;
  let listItems = [];
  let quoteLines = [];
  let code = null;

  const flushParagraph = () => {
    if (!paragraph.length) return;
    output.push(`<p>${paragraph.map(inlineMarkdown).join("<br>")}</p>`);
    paragraph = [];
  };
  const flushList = () => {
    if (!listType) return;
    output.push(`<${listType}>${listItems.map((item) => `<li>${inlineMarkdown(item)}</li>`).join("")}</${listType}>`);
    listType = null;
    listItems = [];
  };
  const flushQuote = () => {
    if (!quoteLines.length) return;
    output.push(`<blockquote>${quoteLines.map(inlineMarkdown).join("<br>")}</blockquote>`);
    quoteLines = [];
  };
  const flushText = () => { flushParagraph(); flushList(); flushQuote(); };

  for (let lineIndex = 0; lineIndex < lines.length; lineIndex += 1) {
    const line = lines[lineIndex];
    if (code) {
      if (/^\s*```/.test(line)) {
        output.push(`<pre><code${code.language ? ` data-language="${escapeHtml(code.language)}"` : ""}>${escapeHtml(code.lines.join("\n"))}</code></pre>`);
        code = null;
      } else {
        code.lines.push(line);
      }
      continue;
    }

    const fence = line.match(/^\s*```\s*([\w.+-]*)\s*$/);
    if (fence) {
      flushText();
      code = { language: fence[1], lines: [] };
      continue;
    }
    const headers = tableCells(line);
    const delimiters = lineIndex + 1 < lines.length ? tableCells(lines[lineIndex + 1]) : null;
    if (headers && delimiters && headers.length === delimiters.length && tableDelimiter(lines[lineIndex + 1])) {
      flushText();
      const alignments = delimiters.map(tableAlignment);
      const rows = [];
      let rowIndex = lineIndex + 2;
      while (rowIndex < lines.length && lines[rowIndex].trim()) {
        const cells = tableCells(lines[rowIndex]);
        if (!cells) break;
        rows.push(headers.map((_, index) => cells[index] ?? ""));
        rowIndex += 1;
      }
      const head = headers.map((cell, index) => `<th class="align-${alignments[index]}">${inlineMarkdown(cell)}</th>`).join("");
      const body = rows.map((cells) => `<tr>${cells.map((cell, index) => `<td class="align-${alignments[index]}">${inlineMarkdown(cell)}</td>`).join("")}</tr>`).join("");
      output.push(`<div class="table-wrap"><table><thead><tr>${head}</tr></thead><tbody>${body}</tbody></table></div>`);
      lineIndex = rowIndex - 1;
      continue;
    }
    if (!line.trim()) {
      flushText();
      continue;
    }
    const heading = line.match(/^(#{1,4})\s+(.+)$/);
    if (heading) {
      flushText();
      const level = heading[1].length;
      output.push(`<h${level}>${inlineMarkdown(heading[2])}</h${level}>`);
      continue;
    }
    if (/^\s*(?:---+|\*\*\*+)\s*$/.test(line)) {
      flushText();
      output.push("<hr>");
      continue;
    }
    const unordered = line.match(/^\s*[-*+]\s+(.+)$/);
    const ordered = line.match(/^\s*\d+[.)]\s+(.+)$/);
    if (unordered || ordered) {
      flushParagraph(); flushQuote();
      const nextType = unordered ? "ul" : "ol";
      if (listType && listType !== nextType) flushList();
      listType = nextType;
      listItems.push((unordered || ordered)[1]);
      continue;
    }
    const quote = line.match(/^\s*>\s?(.*)$/);
    if (quote) {
      flushParagraph(); flushList();
      quoteLines.push(quote[1]);
      continue;
    }
    flushList(); flushQuote();
    paragraph.push(line);
  }

  if (code) output.push(`<pre><code${code.language ? ` data-language="${escapeHtml(code.language)}"` : ""}>${escapeHtml(code.lines.join("\n"))}</code></pre>`);
  flushText();
  return output.join("");
}
