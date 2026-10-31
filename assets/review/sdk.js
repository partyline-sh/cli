// Injected into the artifact iframe by `ptln review`.
//
// The artifact runs in sandbox="allow-scripts" WITHOUT allow-same-origin, so it sits in an opaque
// origin: it cannot read the parent's DOM, cookies or storage, and the parent cannot read into it
// either. That isolation is the point — the artifact is agent-generated HTML and a prompt injection
// in the repo can steer what the generator emits.
//
// The cost of that wall is that the parent needs a courier to learn what element is under a point.
// This is it. It answers two questions and volunteers nothing:
//   probe   → what is at (x, y): a CSS path, the trimmed text, the bounding box
//   measure → how tall is the document, so the parent can size the frame to full content height
//
// Everything sent up is geometry and text. The parent treats all of it as untrusted data: it checks
// event.source, and it never inserts any of these strings as markup.
(function () {
  "use strict";

  var MAX_TEXT = 300;
  var MAX_DEPTH = 8;

  // A CSS path that is specific enough to find the element again, and general enough to survive a
  // regeneration. Prefers a stable id, then a non-generated class, then position among siblings.
  // Stops at MAX_DEPTH — a longer path is more brittle, not more useful.
  function cssPath(el) {
    var parts = [];
    var node = el;
    var depth = 0;
    while (node && node.nodeType === 1 && node !== document.documentElement && depth < MAX_DEPTH) {
      var tag = node.tagName.toLowerCase();
      // An id is only worth using if it looks author-written. Framework-generated ids (long hex,
      // ":r1:", "radix-…") change on every render and would anchor a mark to nothing.
      var id = node.getAttribute("id") || "";
      if (id && /^[A-Za-z][A-Za-z0-9_-]{0,40}$/.test(id) && !/^(radix|headless|r)[-:0-9]/.test(id)) {
        parts.unshift("#" + id);
        break;
      }
      var seg = tag;
      var cls = stableClass(node);
      if (cls) {
        seg += "." + cls;
      } else {
        var i = indexOfType(node);
        if (i > 1) seg += ":nth-of-type(" + i + ")";
      }
      parts.unshift(seg);
      node = node.parentNode;
      depth++;
    }
    return parts.join(">");
  }

  // Utility-framework class lists (tailwind and friends) are hundreds of tokens of styling noise and
  // change whenever the design does. A single semantic-looking class is a useful anchor; a wall of
  // "px-4 md:pt-2 hover:bg-…" is not, so anything that looks generated or utility-shaped is skipped.
  function stableClass(node) {
    var raw = (node.getAttribute("class") || "").trim();
    if (!raw) return "";
    var tokens = raw.split(/\s+/);
    if (tokens.length > 6) return "";
    for (var i = 0; i < tokens.length; i++) {
      var t = tokens[i];
      if (!/^[A-Za-z][A-Za-z0-9_-]{1,30}$/.test(t)) continue;
      if (t.indexOf(":") !== -1 || /^\d/.test(t)) continue;
      if (/^(px|py|pt|pb|pl|pr|mx|my|mt|mb|ml|mr|gap|text|bg|border|flex|grid|w|h|min|max|rounded|shadow|font|items|justify|space|absolute|relative|fixed|sticky|hidden|block|inline)$/.test(t.split("-")[0])) continue;
      return t;
    }
    return "";
  }

  function indexOfType(node) {
    var i = 1;
    var sib = node.previousElementSibling;
    while (sib) {
      if (sib.tagName === node.tagName) i++;
      sib = sib.previousElementSibling;
    }
    return i;
  }

  function docHeight() {
    var b = document.body;
    var d = document.documentElement;
    return Math.max(
      d ? d.scrollHeight : 0,
      d ? d.offsetHeight : 0,
      b ? b.scrollHeight : 0,
      b ? b.offsetHeight : 0,
    );
  }

  function send(msg) {
    msg.__pl = 1;
    // The parent's origin is unknowable from an opaque origin, so "*" is the only option. Safe here:
    // every field below is geometry or visible text, and the parent authenticates by event.source.
    try {
      parent.postMessage(msg, "*");
    } catch (e) {
      /* parent gone — nothing to do */
    }
  }

  function probe(id, x, y) {
    var el = null;
    try {
      el = document.elementFromPoint(x, y);
    } catch (e) {
      /* out of bounds */
    }
    if (!el) {
      send({ type: "probe:result", id: id, selector: "", text: "", rect: null });
      return;
    }
    var r = el.getBoundingClientRect();
    send({
      type: "probe:result",
      id: id,
      selector: cssPath(el),
      text: (el.textContent || "").replace(/\s+/g, " ").trim().slice(0, MAX_TEXT),
      // The frame is sized to full content height and never scrolls internally, so viewport
      // coordinates ARE artifact coordinates. No scroll offset to add.
      rect: { x: r.left, y: r.top, w: r.width, h: r.height },
    });
  }

  window.addEventListener("message", function (e) {
    var d = e.data;
    if (!d || d.__pl !== 1) return;
    if (d.type === "probe") probe(d.id, +d.x, +d.y);
    else if (d.type === "measure") send({ type: "measure:result", height: docHeight() });
  });

  function announce() {
    send({ type: "measure:result", height: docHeight() });
  }

  if (document.readyState === "complete") announce();
  window.addEventListener("load", announce);
  // Late layout (webfonts, images, a script that builds the page) changes the height after load, and
  // a frame sized to a stale height silently clips the bottom of the artifact.
  if (window.ResizeObserver && document.documentElement) {
    try {
      new ResizeObserver(announce).observe(document.documentElement);
    } catch (e) {
      /* older engine — load event is enough */
    }
  }
})();
