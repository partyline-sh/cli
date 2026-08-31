// `ptln review` — markup surface for a planning artifact.
//
// This page owns the drawing; the artifact owns nothing but its own pixels. The two are separated by
// a sandbox with no allow-same-origin, so every fact this page learns about the artifact's DOM
// arrives as a postMessage from sdk.js and is treated as untrusted data: authenticated by
// event.source (an opaque origin has no usable event.origin), bounded, and never inserted as markup.
(function () {
  "use strict";

  // The INK the overlay draws with. These are the app's *-foreground state tokens, and unlike the
  // page chrome they do NOT follow the viewer's theme: they are drawn ON the artifact, which has its
  // own background and does not know what theme we are in. A casing stroke (drawShape) keeps them
  // legible whether the artifact is light or dark. Chrome colours live in viewer.html's CSS.
  // WHERE THIS PAGE IS MOUNTED. A standalone `ptln review` serves one artifact at "/", while the
  // daemon hosts many at "/w/<work-item-id>/". Every request below is relative to whichever it is,
  // so the same viewer serves both without knowing which one it is in.
  var BASE = location.pathname.replace(/\/+$/, "");

  var KIND_INK = { scope: "#9e1a58", behaviour: "#14532d", constraint: "#7c2d12", question: "#713f12" };
  var VP_WIDTH = { mobile: 390, tablet: 834, desktop: 1280 };
  var SVG_NS = "http://www.w3.org/2000/svg";

  var frame = document.getElementById("frame");
  var stage = document.getElementById("stage");
  var overlay = document.getElementById("overlay");
  var composer = document.getElementById("composer");
  var composerText = document.getElementById("composer-text");
  var composerHint = document.getElementById("composer-hint");
  var composerKind = document.getElementById("composer-kind");
  var marksEl = document.getElementById("marks");
  var emptyEl = document.getElementById("empty");
  var countEl = document.getElementById("count");
  var doneBtn = document.getElementById("done");

  var state = { kind: "constraint", tool: "none", viewport: "desktop", height: 800, marks: [], pending: null };

  // ---- toolbar -----------------------------------------------------------

  function group(id, attr, onPick) {
    var root = document.getElementById(id);
    root.addEventListener("click", function (e) {
      var btn = e.target.closest("button[" + attr + "]");
      if (!btn) return;
      Array.prototype.forEach.call(root.querySelectorAll("button[" + attr + "]"), function (b) {
        b.setAttribute("aria-pressed", String(b === btn));
      });
      onPick(btn.getAttribute(attr));
    });
  }

  group("kinds", "data-kind", function (v) { state.kind = v; });
  group("tools", "data-tool", function (v) {
    state.tool = v;
    document.body.setAttribute("data-armed", String(v !== "none"));
  });
  group("viewports", "data-vp", function (v) { state.viewport = v; sizeStage(); });

  function sizeStage() {
    var w = VP_WIDTH[state.viewport] || 1280;
    stage.style.width = w + "px";
    frame.style.height = state.height + "px";
    overlay.setAttribute("width", String(w));
    overlay.setAttribute("height", String(state.height));
    overlay.setAttribute("viewBox", "0 0 " + w + " " + state.height);
    redraw();
  }

  // ---- courier to the artifact ------------------------------------------

  var probes = {};
  var probeSeq = 0;

  window.addEventListener("message", function (e) {
    // The artifact is in an opaque origin, so e.origin is "null" and useless as a check. The frame's
    // contentWindow identity is the real gate — anything from another window is ignored outright.
    if (e.source !== frame.contentWindow) return;
    var d = e.data;
    if (!d || d.__pl !== 1) return;

    if (d.type === "measure:result") {
      var h = Math.max(200, Math.min(60000, +d.height || 0));
      if (h !== state.height) { state.height = h; sizeStage(); }
      return;
    }
    if (d.type === "probe:result") {
      var fn = probes[d.id];
      if (!fn) return;
      delete probes[d.id];
      fn({
        selector: typeof d.selector === "string" ? d.selector.slice(0, 512) : "",
        text: typeof d.text === "string" ? d.text.slice(0, 300) : "",
        rect: d.rect && typeof d.rect.x === "number" ? d.rect : null,
        viewport: state.viewport,
      });
    }
  });

  // Resolves to an anchor, or to a bare viewport-only anchor if the artifact never answers — a mark
  // must never be lost because the courier was slow or the artifact's script died.
  function probe(x, y) {
    return new Promise(function (resolve) {
      var id = ++probeSeq;
      var settled = false;
      probes[id] = function (a) { if (!settled) { settled = true; resolve(a); } };
      try {
        frame.contentWindow.postMessage({ __pl: 1, type: "probe", id: id, x: x, y: y }, "*");
      } catch (err) { /* fall through to the timeout */ }
      setTimeout(function () {
        if (settled) return;
        settled = true;
        delete probes[id];
        resolve({ selector: "", text: "", rect: null, viewport: state.viewport });
      }, 700);
    });
  }

  // ---- drawing -----------------------------------------------------------

  var draft = null;

  function pointIn(e) {
    var r = overlay.getBoundingClientRect();
    return [Math.round(e.clientX - r.left), Math.round(e.clientY - r.top)];
  }

  overlay.addEventListener("pointerdown", function (e) {
    if (state.tool === "none") return;
    e.preventDefault();
    overlay.setPointerCapture(e.pointerId);
    var p = pointIn(e);
    draft = { type: state.tool, points: [p, p], color: KIND_INK[state.kind] };
    if (state.tool === "pin") { draft.points = [p]; finish(); return; }
    redraw();
  });

  overlay.addEventListener("pointermove", function (e) {
    if (!draft) return;
    var p = pointIn(e);
    if (draft.type === "freehand") {
      var last = draft.points[draft.points.length - 1];
      // Thin the stream: a point every ~3px keeps the stored path small without a visible change.
      if (Math.abs(p[0] - last[0]) + Math.abs(p[1] - last[1]) >= 3) draft.points.push(p);
    } else {
      draft.points[1] = p;
    }
    redraw();
  });

  overlay.addEventListener("pointerup", function (e) {
    if (!draft) return;
    try { overlay.releasePointerCapture(e.pointerId); } catch (err) { /* already released */ }
    // A click that never moved is a pin, whatever tool is armed — a zero-area box is not a mark.
    if (draft.type !== "freehand" && draft.type !== "pin") {
      var a = draft.points[0], b = draft.points[1];
      if (Math.abs(a[0] - b[0]) < 4 && Math.abs(a[1] - b[1]) < 4) { draft.type = "pin"; draft.points = [a]; }
    }
    finish();
  });

  // Where the mark is "about": an arrow points AT its target, a box or scribble is about its middle.
  function anchorPoint(shape) {
    var pts = shape.points;
    if (shape.type === "pin") return pts[0];
    if (shape.type === "arrow") return pts[pts.length - 1];
    var sx = 0, sy = 0;
    for (var i = 0; i < pts.length; i++) { sx += pts[i][0]; sy += pts[i][1]; }
    return [Math.round(sx / pts.length), Math.round(sy / pts.length)];
  }

  function finish() {
    var shape = draft;
    draft = null;
    redraw();
    var ap = anchorPoint(shape);
    probe(ap[0], ap[1]).then(function (anchor) {
      state.pending = { shape: shape, anchor: anchor, kind: state.kind };
      openComposer(ap, anchor);
    });
  }

  // ---- composer ----------------------------------------------------------

  function openComposer(at, anchor) {
    var wrap = document.getElementById("stagewrap");
    var sr = stage.getBoundingClientRect();
    var wr = wrap.getBoundingClientRect();
    var left = sr.left - wr.left + at[0] + 14;
    var top = sr.top - wr.top + at[1] + 8;
    // Keep it on screen when the mark is near the right or bottom edge.
    left = Math.min(left, wr.width - 296);
    composer.style.left = Math.max(8, left) + "px";
    composer.style.top = Math.max(8, top) + "px";
    composerKind.textContent = state.pending.kind;
    composerKind.setAttribute("data-kind", state.pending.kind);
    // textContent, never innerHTML: `selector` and `text` came out of untrusted artifact HTML.
    composerHint.textContent = anchor.selector ? anchor.selector : "unanchored — no element at that point";
    composerText.value = "";
    composer.hidden = false;
    composerText.focus();
  }

  function closeComposer() {
    composer.hidden = true;
    state.pending = null;
    redraw();
  }

  function saveMark() {
    if (!state.pending) return;
    var body = composerText.value.trim();
    if (!body) { composerText.focus(); return; }
    state.marks.push({
      kind: state.pending.kind,
      body: body,
      anchor: state.pending.anchor,
      shape: state.pending.shape,
    });
    composer.hidden = true;
    state.pending = null;
    renderMarks();
    redraw();
  }

  document.getElementById("composer-save").addEventListener("click", saveMark);
  document.getElementById("composer-cancel").addEventListener("click", closeComposer);
  composerText.addEventListener("keydown", function (e) {
    if (e.key === "Escape") { e.preventDefault(); closeComposer(); }
    // Enter saves; Shift+Enter is a newline. A one-line remark is the common case.
    if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); saveMark(); }
  });

  // ---- rendering ---------------------------------------------------------

  function el(name, attrs) {
    var n = document.createElementNS(SVG_NS, name);
    for (var k in attrs) n.setAttribute(k, String(attrs[k]));
    return n;
  }

  function drawShape(shape, color, label) {
    var pts = shape.points;
    var g = el("g", {});
    var stroke = { stroke: color, "stroke-width": 2.5, fill: "none", "stroke-linecap": "round", "stroke-linejoin": "round" };
    // Casing: the same geometry drawn wider in translucent white, underneath. Annotation ink has to
    // sit on top of someone else's design, and a mockup can be any colour — without this, a dark
    // mark on a dark hero is invisible and the human silently loses the mark they thought they left.
    var casing = { stroke: "rgba(255,255,255,.9)", "stroke-width": 6, fill: "none", "stroke-linecap": "round", "stroke-linejoin": "round" };

    if (shape.type === "pin") {
      g.appendChild(el("circle", { cx: pts[0][0], cy: pts[0][1], r: 9, fill: "none", stroke: "rgba(255,255,255,.9)", "stroke-width": 5 }));
      g.appendChild(el("circle", { cx: pts[0][0], cy: pts[0][1], r: 9, fill: color, "fill-opacity": .22, stroke: color, "stroke-width": 2 }));
    } else if (shape.type === "rect" || shape.type === "highlight") {
      var x = Math.min(pts[0][0], pts[1][0]), y = Math.min(pts[0][1], pts[1][1]);
      var w = Math.abs(pts[1][0] - pts[0][0]), h = Math.abs(pts[1][1] - pts[0][1]);
      // radius 3px ~= the app's --radius (0.3rem): boxy, deliberately not pill-shaped.
      g.appendChild(el("rect", { x: x, y: y, width: w, height: h, rx: 3, fill: "none", stroke: "rgba(255,255,255,.9)", "stroke-width": 6 }));
      var box = el("rect", { x: x, y: y, width: w, height: h, rx: 3, stroke: color, "stroke-width": 2.5, fill: color });
      box.setAttribute("fill-opacity", shape.type === "highlight" ? ".28" : ".08");
      g.appendChild(box);
    } else if (shape.type === "arrow") {
      var a = pts[0], b = pts[1];
      g.appendChild(el("line", Object.assign({ x1: a[0], y1: a[1], x2: b[0], y2: b[1] }, casing)));
      g.appendChild(el("line", Object.assign({ x1: a[0], y1: a[1], x2: b[0], y2: b[1] }, stroke)));
      var ang = Math.atan2(b[1] - a[1], b[0] - a[0]);
      var head = 11;
      g.appendChild(el("polygon", {
        points: [
          b[0] + "," + b[1],
          (b[0] - head * Math.cos(ang - 0.4)) + "," + (b[1] - head * Math.sin(ang - 0.4)),
          (b[0] - head * Math.cos(ang + 0.4)) + "," + (b[1] - head * Math.sin(ang + 0.4)),
        ].join(" "),
        fill: color,
      }));
    } else {
      var d = "M " + pts.map(function (p) { return p[0] + " " + p[1]; }).join(" L ");
      g.appendChild(el("path", Object.assign({ d: d }, casing)));
      g.appendChild(el("path", Object.assign({ d: d }, stroke)));
    }

    if (label) {
      var ap = anchorPoint(shape);
      g.appendChild(el("circle", { cx: ap[0], cy: ap[1] - 16, r: 10, fill: "rgba(255,255,255,.9)" }));
      g.appendChild(el("circle", { cx: ap[0], cy: ap[1] - 16, r: 8.5, fill: color }));
      var t = el("text", { x: ap[0], y: ap[1] - 12.5, "text-anchor": "middle", "font-size": 11, "font-weight": 700, fill: "#fff" });
      t.textContent = String(label);
      g.appendChild(t);
    }
    return g;
  }

  function redraw() {
    while (overlay.firstChild) overlay.removeChild(overlay.firstChild);
    state.marks.forEach(function (m, i) {
      overlay.appendChild(drawShape(m.shape, KIND_INK[m.kind], i + 1));
    });
    if (state.pending) overlay.appendChild(drawShape(state.pending.shape, KIND_INK[state.pending.kind], null));
    if (draft) overlay.appendChild(drawShape(draft, KIND_INK[state.kind], null));
  }

  function renderMarks() {
    marksEl.textContent = "";
    state.marks.forEach(function (m, i) {
      var li = document.createElement("li");
      li.setAttribute("data-kind", m.kind);
      var num = document.createElement("span");
      num.className = "num";
      num.textContent = String(i + 1);
      var body = document.createElement("div");
      body.className = "body";
      var k = document.createElement("div");
      k.className = "k";
      k.textContent = m.kind;
      var t = document.createElement("div");
      t.className = "t";
      t.textContent = m.body;
      body.appendChild(k);
      body.appendChild(t);
      if (m.anchor.selector) {
        var s = document.createElement("div");
        s.className = "sel";
        s.textContent = m.anchor.selector;
        body.appendChild(s);
      }
      var del = document.createElement("button");
      del.className = "del";
      del.title = "Remove";
      del.textContent = "×";
      del.addEventListener("click", function () { state.marks.splice(i, 1); renderMarks(); redraw(); });
      li.appendChild(num); li.appendChild(body); li.appendChild(del);
      marksEl.appendChild(li);
    });
    countEl.textContent = String(state.marks.length);
    emptyEl.style.display = state.marks.length ? "none" : "";
    doneBtn.disabled = state.marks.length === 0;
  }

  // ---- exit --------------------------------------------------------------

  // Replaces the whole surface with a single closing line, in the page's own tokens.
  function farewell(text) {
    var p = document.createElement("p");
    p.textContent = text;
    p.style.cssText = "margin:auto;padding:40px;color:var(--muted-foreground)";
    document.body.textContent = "";
    document.body.appendChild(p);
  }

  function post(path, body) {
    return fetch(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
  }

  var activity = document.getElementById("activity");
  var actLog = document.getElementById("act-log");
  var actStatus = document.getElementById("act-status");
  var versionEl = document.getElementById("version");

  // The button label owns the count element, so restoring it has to re-adopt that node rather than
  // assign a string — an innerHTML rebuild would orphan the span renderMarks() writes into.
  function resetSendLabel() {
    doneBtn.textContent = "Send ";
    doneBtn.appendChild(countEl);
    doneBtn.append(" marks");
  }

  function setBusy(on) {
    document.body.setAttribute("data-busy", String(on));
  }

  function logLine(text, isErr) {
    var li = document.createElement("li");
    if (isErr) li.className = "err";
    li.textContent = text;
    actLog.appendChild(li);
    // Keep the newest line in view, but only ~200 lines: a long turn otherwise grows the DOM
    // without bound behind a panel nobody scrolls back through.
    while (actLog.children.length > 200) actLog.removeChild(actLog.firstChild);
    actLog.scrollTop = actLog.scrollHeight;
  }

  // The model's progress, in the place the human is already looking. The same stream prints in the
  // terminal — neither is the "real" one, because which window is in front is not ours to assume.
  var events = new EventSource(BASE + "/events");
  events.onmessage = function (e) {
    var d;
    try { d = JSON.parse(e.data); } catch (err) { return; }
    if (!d || typeof d.type !== "string") return;

    if (d.type === "status") {
      activity.hidden = false;
      activity.setAttribute("data-state", "working");
      actStatus.textContent = String(d.text || "working…");
      logLine(String(d.text || ""), false);
    } else if (d.type === "activity") {
      activity.hidden = false;
      logLine(String(d.text || ""), false);
    } else if (d.type === "error") {
      activity.setAttribute("data-state", "error");
      actStatus.textContent = "failed";
      logLine(String(d.text || "failed"), true);
      setBusy(false);
      resetSendLabel();
      renderMarks();
    } else if (d.type === "reload") {
      activity.setAttribute("data-state", "ok");
      actStatus.textContent = "v" + d.version + " ready";
      logLine("new version loaded", false);
      reloadArtifact(d.version);
    }
  };

  // Swap the frame to the new version. The cache-buster is belt-and-braces next to the server's
  // no-store: a frame showing the version you just changed is indistinguishable from "nothing
  // happened", and that is the single most confusing thing this loop could do.
  function reloadArtifact(version) {
    frame.src = BASE + "/artifact?v=" + encodeURIComponent(version) + "&t=" + Date.now();
    if (versionEl) versionEl.textContent = "Marks · v" + version;
    state.marks = [];
    state.pending = null;
    composer.hidden = true;
    setBusy(false);
    resetSendLabel();
    renderMarks();
    redraw();
  }

  doneBtn.addEventListener("click", function () {
    if (!state.marks.length) return;
    setBusy(true);
    doneBtn.textContent = "Sending…";
    activity.hidden = false;
    activity.setAttribute("data-state", "working");
    actStatus.textContent = "sending…";
    post(BASE + "/marks", { marks: state.marks }).then(function (r) {
      if (r.status === 409) throw new Error("a revision is already running");
      if (!r.ok) throw new Error("send failed");
      // The marks are the server's now; the page clears when the new version lands.
      logLine("sent " + state.marks.length + " mark(s)", false);
    }).catch(function (err) {
      activity.setAttribute("data-state", "error");
      actStatus.textContent = "failed";
      logLine(String(err && err.message ? err.message : "send failed"), true);
      setBusy(false);
      doneBtn.textContent = "Retry send";
    });
  });

  document.getElementById("finish").addEventListener("click", function () {
    post(BASE + "/finish", {}).finally(function () { farewell("Done. You can close this tab."); });
  });

  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !composer.hidden) closeComposer();
  });

  // The artifact reports its own height on load; ask once in case it loaded before we bound.
  frame.addEventListener("load", function () {
    try { frame.contentWindow.postMessage({ __pl: 1, type: "measure" }, "*"); } catch (err) { /* not ready */ }
  });

  frame.src = BASE + "/artifact";
  sizeStage();
  renderMarks();
})();
