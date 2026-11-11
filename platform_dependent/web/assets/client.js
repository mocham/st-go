"use strict";
(() => {
  const screen = document.getElementById("screen");
  const viewport = document.getElementById("viewport");
  const input = document.getElementById("input");
  const status = document.getElementById("status");
  const copy = document.getElementById("copy");
  const sync = document.getElementById("sync");
  const ctx = screen.getContext("2d", { alpha: false });
  const htmlPanes = new Map();
  const managed = document.body.dataset.managed === "true";
  if (managed && !document.body.dataset.pty) return;
  const notifyService = detail => { if (managed) window.dispatchEvent(new CustomEvent("st-terminal", { detail })); };
  let htmlState;
  let socket, metrics, composing = false, clipboard = "", reconnect, screenRatio = 0, syncEnabled = false;
  let resizeTimer, heartbeat, viewportSize, requestedWidth = 0, requestedHeight = 0, wheel = 0;
  const utf8 = new TextEncoder();
  const message = text => { status.textContent = text; };
  const syncState = data => {
    syncEnabled = !!data.syncEnabled;
    sync.disabled = !data.syncAvailable;
    sync.textContent = !data.syncAvailable ? "Sync unavailable" : syncEnabled ? (data.syncReady ? "Sync on" : "Sync starting") : "Sync off";
  };
  const send = value => {
    if (socket && socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify(value));
  };
  const focus = () => input.focus({ preventScroll: true });
  const modifiers = e => (e.shiftKey ? 1 : 0) | (e.ctrlKey ? 4 : 0) | (e.altKey ? 8 : 0) | (e.metaKey ? 64 : 0);
  const paste = text => {
    if (utf8.encode(text).length > 8 * 1024 * 1024) { message("Paste exceeds 8 MiB"); return; }
    send({ type: "paste", text });
    focus();
  };
  const copyFromGesture = text => {
    const transfer = document.createElement("textarea");
    transfer.value = text;
    transfer.readOnly = true;
    transfer.setAttribute("aria-hidden", "true");
    transfer.style.cssText = "position:fixed;left:-10000px;top:0;width:1px;height:1px;opacity:0";
    document.body.appendChild(transfer);
    transfer.select();
    transfer.setSelectionRange(0, transfer.value.length);
    try { return document.execCommand("copy"); }
    catch (_) { return false; }
    finally { transfer.remove(); }
  };
  const copyClipboard = () => {
    // execCommand is deprecated but remains the only broadly supported copy
    // path on plain HTTP and runs synchronously inside this click/key gesture.
    if (copyFromGesture(clipboard)) { message("Copied"); focus(); return; }
    if (!navigator.clipboard || !navigator.clipboard.writeText) {
      message("Clipboard write denied by browser"); focus(); return;
    }
    navigator.clipboard.writeText(clipboard)
      .then(() => message("Copied"), () => message("Clipboard write denied by browser"))
      .finally(focus);
  };
  const sizeScreen = (width, height) => {
    if (!width || !height) return;
    if (screen.width !== width || screen.height !== height) {
      screen.width = width; screen.height = height;
    }
    const ratio = Number.isFinite(window.devicePixelRatio) && window.devicePixelRatio > 0 ? window.devicePixelRatio : 1;
    // Keep one server-rendered pixel per browser device pixel. CSS pixels can
    // differ under display scaling or browser zoom, so compensate only the
    // presentation size without resizing/rerasterizing the terminal buffer.
    screen.style.width = width / ratio + "px"; screen.style.height = height / ratio + "px";
    screenRatio = ratio;
    placeHTML();
  };
  const resize = () => {
    if (!metrics || !viewportSize) return;
    const ratio = Number.isFinite(window.devicePixelRatio) && window.devicePixelRatio > 0 ? window.devicePixelRatio : 1;
    const width = Math.max(2 * metrics.border + metrics.cellWidth, Math.floor(viewportSize.width * ratio));
    const height = Math.max(2 * metrics.border + metrics.cellHeight, Math.floor(viewportSize.height * ratio));
    if (width === requestedWidth && height === requestedHeight) return;
    if (width * height * 4 > 64 * 1024 * 1024) { message("Viewport exceeds pixel budget"); return; }
    requestedWidth = width; requestedHeight = height;
    placeHTML();
    send({ type: "resize", width, height });
  };
  new ResizeObserver(([entry]) => {
    // clientWidth/Height round fractional CSS pixels before device scaling.
    // Use the content box so wrapping toolbars and zoom cannot oversize Canvas.
    viewportSize = entry.contentRect;
    clearTimeout(resizeTimer); resizeTimer = setTimeout(resize, 60);
  }).observe(viewport);
  window.addEventListener("resize", resize, { passive: true });
  setInterval(() => {
    const ratio = Number.isFinite(window.devicePixelRatio) && window.devicePixelRatio > 0 ? window.devicePixelRatio : 1;
    if (ratio !== screenRatio) { screenRatio = ratio; resize(); }
  }, 250);

  function placeHTML() {
    if (!htmlState) return;
    const ratio = window.devicePixelRatio || 1;
    for (const pane of htmlPanes.values()) {
      const r = pane.data;
      pane.element.hidden = requestedWidth !== htmlState.width || requestedHeight !== htmlState.height;
      Object.assign(pane.element.style, { left: r.x / ratio + "px", top: r.y / ratio + "px", width: r.width / ratio + "px", height: r.height / ratio + "px" });
    }
  }
  function scrollHTML(pane) {
    if (!pane.loaded) return;
    const scroll = pane.element.contentDocument.scrollingElement, view = pane.data.view;
    if (pane.positionID !== view.positionID) {
      if (view.positionID) scroll.scrollTop = Math.max(0, scroll.scrollHeight - scroll.clientHeight) * view.position / 1000000;
      pane.positionID = view.positionID; pane.scroll = 0;
    }
    scroll.scrollTop += (view.scroll - pane.scroll) * metrics.cellHeight / (window.devicePixelRatio || 1);
    pane.scroll = view.scroll;
  }
  function renderHTML(data) {
    htmlState = data;
    const live = new Set();
    for (const overlay of data.overlays || []) {
      live.add(overlay.id);
      let pane = htmlPanes.get(overlay.id);
      if (!pane) {
        const element = document.createElement("iframe");
        element.className = "html-preview";
        element.title = "Document preview";
        // Styles and selectable HTML are local to this pane. Scripts, forms,
        // popups and top-level navigation from preview documents are disabled.
        element.setAttribute("sandbox", "allow-same-origin");
        pane = { element, loaded: false, positionID: -1, scroll: 0 };
        htmlPanes.set(overlay.id, pane); viewport.appendChild(element);
      }
      const doc = overlay.view.document, previous = pane.data?.view.document;
      pane.data = overlay;
      if (!previous || previous.html !== doc.html || previous.css !== doc.css) {
        const oldScroll = pane.loaded ? pane.element.contentDocument.scrollingElement.scrollTop : 0;
        pane.loaded = false;
        pane.element.onload = () => {
          pane.loaded = true; pane.positionID = -1; pane.scroll = 0;
          const content = pane.element.contentDocument;
          content.scrollingElement.scrollTop = oldScroll;
          content.addEventListener("click", e => {
            const link = e.target.closest?.("a");
            if (link && !link.getAttribute("href")?.startsWith("#")) e.preventDefault();
          });
          content.addEventListener("keydown", e => {
            if (e.key === "Escape") { e.preventDefault(); focus(); }
            else if (e.ctrlKey && !e.altKey && !e.metaKey && (e.key.toLowerCase() === "u" || e.key.toLowerCase() === "d")) {
              e.preventDefault();
              const scroll = content.scrollingElement;
              scroll.scrollTop += (e.key.toLowerCase() === "u" ? -1 : 1) * scroll.clientHeight;
            }
            else if (e.key === "Tab" && pane.data.view.captureTab) {
              e.preventDefault(); focus();
              send({ type: "key", key: "Tab", modifiers: modifiers(e) });
            }
          });
          scrollHTML(pane);
        };
        const css = (doc.css || "").replace(/<\/style/gi, "<\\/style");
        pane.element.srcdoc = '<!doctype html><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src \'none\'; style-src \'unsafe-inline\'; img-src data:; font-src data:; form-action \'none\'; base-uri \'none\'"><style>html{color-scheme:light}body{margin:16px;overflow-wrap:anywhere}img,svg{max-width:100%}pre{white-space:pre-wrap}' + css + '</style>' + doc.html;
      } else scrollHTML(pane);
    }
    for (const [id, pane] of htmlPanes) if (!live.has(id)) { pane.element.remove(); htmlPanes.delete(id); }
    placeHTML();
  }

  const clearGlyphs = state => {
    for (const bitmap of state.atlases) bitmap.close();
    state.atlases.clear(); state.glyphs.clear(); state.glyphBytes = 0;
  };
  async function paint(data, state) {
    const active = () => socket === state.conn && state.conn.readyState === WebSocket.OPEN;
    if (!active()) return;
    if (data.byteLength < 12) throw new Error("Short paint frame");
    const header = new DataView(data);
    const magic = header.getUint32(0, false);
    const width = header.getUint32(4, true), height = header.getUint32(8, true);
    if (!width || !height || width > 65535 || height > 65535 || width * height > 16777216) throw new Error("Invalid frame dimensions");
    // Recheck after asynchronous decoding as well: a resize or reconnect may
    // have overtaken it. Cache definitions still apply at obsolete sizes.
    const visible = () => active() && (!requestedWidth || width === requestedWidth && height === requestedHeight);
    const bounds = (x, y, w, h) => {
      if (!w || !h || x + w > width || y + h > height) throw new Error("Invalid patch dimensions");
    };
    let painted = false, glyphDefinitions = 0, glyphDraws = 0;
    if (magic === 0x53545731) { // STW1: ordinary PNG damage.
      if (data.byteLength < 28) throw new Error("Short pixel frame");
      const [x, y, w, h] = Array.from({ length: 4 }, (_, i) => header.getUint32(12 + i * 4, true));
      bounds(x, y, w, h);
      if (!visible()) return;
      const bitmap = await createImageBitmap(new Blob([data.slice(28)], { type: "image/png" }));
      try {
        if (bitmap.width !== w || bitmap.height !== h) throw new Error("Pixel dimensions differ");
        if (visible()) { sizeScreen(width, height); ctx.drawImage(bitmap, x, y); painted = true; }
      } finally { bitmap.close(); }
    } else if (magic === 0x53544731) { // STG1: ordered glyph definitions and ID draws.
      let offset = 12;
      const word = () => {
        if (offset + 4 > data.byteLength) throw new Error("Short glyph frame");
        const value = header.getUint32(offset, true); offset += 4; return value;
      };
      const count = word();
      if (count > (data.byteLength - offset) / 4) throw new Error("Invalid glyph operation count");
      for (let i = 0; i < count; i++) {
        const op = word();
        if (op === 0) clearGlyphs(state);
        else if (op === 3) {
          const id = word(), count = word(), w = word(), h = word(), columns = word(), length = word();
          const aw = columns * w, ah = Math.ceil(count / columns) * h;
          if (!id || !count || !w || !h || !columns || columns > count || id + count > 0x100000000 ||
              aw > 65535 || ah > 65535 || state.glyphBytes + aw * ah * 4 > 32 * 1024 * 1024 ||
              state.glyphs.size + count > 16384 || !length || offset + length > data.byteLength) throw new Error("Invalid glyph atlas");
          for (let n = 0; n < count; n++) if (state.glyphs.has(id + n)) throw new Error("Duplicate glyph ID");
          const bitmap = await createImageBitmap(new Blob([data.slice(offset, offset + length)], { type: "image/png" }));
          offset += length;
          if (!active()) { bitmap.close(); return; }
          if (bitmap.width !== aw || bitmap.height !== ah) { bitmap.close(); throw new Error("Glyph atlas dimensions differ"); }
          state.atlases.add(bitmap); state.glyphBytes += aw * ah * 4;
          for (let n = 0; n < count; n++) state.glyphs.set(id + n, { bitmap, x: n % columns * w, y: Math.floor(n / columns) * h, w, h });
          glyphDefinitions += count;
        } else if (op === 2) {
          const id = word(), x = word(), y = word(), repeat = word();
          const glyph = state.glyphs.get(id);
          if (!glyph || !repeat) throw new Error("Unknown glyph ID or empty run");
          bounds(x, y, glyph.w * repeat, glyph.h);
          if (visible()) {
            if (!painted) sizeScreen(width, height);
            for (let n = 0; n < repeat; n++) ctx.drawImage(glyph.bitmap, glyph.x, glyph.y, glyph.w, glyph.h, x + n * glyph.w, y, glyph.w, glyph.h);
            painted = true;
            glyphDraws += repeat;
          }
        } else throw new Error("Unknown glyph operation");
      }
      if (offset !== data.byteLength) throw new Error("Trailing glyph data");
    } else throw new Error("Unknown paint frame");
    if (glyphDefinitions) screen.dataset.glyphDefinitions = String(Number(screen.dataset.glyphDefinitions || 0) + glyphDefinitions);
    if (glyphDraws) screen.dataset.glyphDraws = String(Number(screen.dataset.glyphDraws || 0) + glyphDraws);
    if (painted) screen.dataset.frames = String(Number(screen.dataset.frames || 0) + 1);
  }

  function connect() {
    clearTimeout(reconnect);
    const url = new URL(managed ? "../attach" : "ws", location.href);
    url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
    socket = new WebSocket(url);
    socket.binaryType = "arraybuffer";
    const state = { conn: socket, glyphs: new Map(), atlases: new Set(), glyphBytes: 0, queue: Promise.resolve() };
    state.scene = new SceneClient(screen, viewport, send, focus);
    socket.onmessage = event => {
      if (typeof event.data !== "string") {
        // Image decoding is asynchronous; commit damage frames in wire order.
        state.queue = state.queue.then(() => paint(event.data, state)).catch(error => {
          if (socket === state.conn) { message(error.message); state.conn.close(); }
        });
        return;
      }
      const data = JSON.parse(event.data);
      if (data.type === "hello") {
        clearInterval(heartbeat);
        heartbeat = setInterval(() => send({type: "ping"}), data.heartbeatMS || 20000);
        notifyService({type: "connected", attachment: data.attachment});
        metrics = data; requestedWidth = data.width; requestedHeight = data.height;
        message("Connected"); syncState(data); resize();
        send({ type: "paint_ack", id: 0 }); // Opt into one presentation in flight.
        if (requestedWidth === data.width && requestedHeight === data.height) sizeScreen(data.width, data.height);
        focus(); send({ type: "focus", focused: true });
      }
      else if (data.type === "paint") {
        // ACK only after every PNG/atlas and draw in this presentation has
        // committed. While waiting, the server coalesces damage into its latest
        // retained pixels instead of queuing obsolete frames in this browser.
        state.queue = state.queue.then(() => {
          if (socket === state.conn && state.conn.readyState === WebSocket.OPEN) state.conn.send(JSON.stringify({ type: "paint_ack", id: data.id }));
        });
      }
      else if (data.type === "html") {
        state.queue = state.queue.then(() => {
          if (socket === state.conn && state.conn.readyState === WebSocket.OPEN) { renderHTML(data); state.scene.previewsApply(data); }
        }).catch(error => { if (socket === state.conn) { message(error.message); state.conn.close(); } });
      }
      else if (data.type === "scene") {
        state.queue = state.queue.then(async () => {
          if (socket !== state.conn || state.conn.readyState !== WebSocket.OPEN) return;
          if (requestedWidth !== data.width || requestedHeight !== data.height) return;
          sizeScreen(data.width,data.height); await state.scene.apply(data);
        }).catch(error => { if (socket === state.conn) { message(error.message); state.conn.close(); } });
      }
      else if (data.type === "open_pty") notifyService({type: "open", pty: data.openPTY});
      else if (data.type === "document") state.scene.documents(data.documents, data.cancelled);
      else if (data.type === "clipboard_sync") syncState(data);
      else if (data.type === "clipboard") { clipboard = data.text || ""; copy.disabled = false; message("Selection ready to copy"); }
      else if (data.type === "clipboard_read") {
        const replySocket = socket;
        (async () => {
          let text = "", ok = false;
          try { if (data.selection === "c") { text = await navigator.clipboard.readText(); ok = utf8.encode(text).length <= 8 * 1024 * 1024; } } catch (_) { /* Browser permission/user gesture is required. */ }
          if (replySocket.readyState === WebSocket.OPEN) replySocket.send(JSON.stringify({ type: "clipboard_reply", id: data.id, text: ok ? text : "", ok }));
        })();
      }
    };
    socket.onclose = () => {
      clearGlyphs(state);
      state.scene.close();
      if (socket === state.conn) {
        clearInterval(heartbeat);
        for (const pane of htmlPanes.values()) pane.element.remove();
        htmlPanes.clear(); htmlState = null;
        if (managed) {
          metrics = null;
          message("Disconnected — refresh the terminal list to attach again");
          copy.disabled = true; sync.disabled = true;
          notifyService({type: "disconnected"});
        } else {
          message("Disconnected — reconnecting (only one browser may attach)"); reconnect = setTimeout(connect, 1500);
        }
      }
    };
    socket.onerror = () => message("Connection unavailable");
  }

  window.addEventListener("pagehide", () => { clearTimeout(reconnect); if (socket) socket.close(); });
  window.addEventListener("pageshow", event => { if (event.persisted) location.reload(); });

  const specials = new Set(["Backspace", "Tab", "Enter", "Escape", "Delete", "Insert", "Home", "End", "PageUp", "PageDown", "ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"]);
  input.addEventListener("keydown", e => {
    if (e.isComposing || composing || e.key === "Process" || e.key === "Dead") return;
    if (e.getModifierState("AltGraph")) return; // Let the input event commit composed text.
    const lower = e.key.toLowerCase();
    if ((e.ctrlKey && e.shiftKey || e.metaKey) && lower === "v") return; // Native paste event supplies text.
    if ((e.ctrlKey && e.shiftKey || e.metaKey) && lower === "c") { e.preventDefault(); if (!copy.disabled) copyClipboard(); return; }
    if (e.metaKey && !e.ctrlKey) return; // Leave browser/OS command shortcuts alone.
    if (specials.has(e.key) || /^F([1-9]|[12][0-9]|3[0-5])$/.test(e.key) || e.key.length === 1 && e.key.charCodeAt(0) >= 32 && e.key.charCodeAt(0) <= 126) {
      e.preventDefault(); send({ type: "key", key: e.key, modifiers: modifiers(e) });
    }
  });
  const flushText = () => {
    if (composing || !input.value) return;
    const text = input.value; input.value = "";
    // Composition/autocorrect commits bypass legacy Latin-1 keysym encoding.
    if (utf8.encode(text).length <= 32768) send({ type: "text", text });
  };
  input.addEventListener("compositionstart", () => { composing = true; });
  input.addEventListener("compositionend", () => { composing = false; setTimeout(flushText, 0); });
  input.addEventListener("input", flushText);
  input.addEventListener("paste", e => { e.preventDefault(); paste(e.clipboardData.getData("text/plain")); input.value = ""; });
  input.addEventListener("focus", () => send({ type: "focus", focused: true }));
  input.addEventListener("blur", () => send({ type: "focus", focused: false }));

  const point = e => {
    const r = screen.getBoundingClientRect();
    return { x: Math.round((e.clientX - r.left) * screen.width / Math.max(1, r.width)), y: Math.round((e.clientY - r.top) * screen.height / Math.max(1, r.height)) };
  };
  const button = e => [1, 2, 3, 8, 9][e.button] || 0;
  screen.addEventListener("contextmenu", e => e.preventDefault());
  screen.addEventListener("pointerdown", e => { e.preventDefault(); focus(); screen.setPointerCapture(e.pointerId); send({ type: "press", button: button(e), modifiers: modifiers(e), ...point(e) }); });
  screen.addEventListener("pointerup", e => { e.preventDefault(); send({ type: "release", button: button(e), modifiers: modifiers(e), ...point(e) }); });
  screen.addEventListener("pointercancel", e => send({ type: "release", button: button(e) || 1, modifiers: modifiers(e), ...point(e) }));
  screen.addEventListener("pointermove", e => send({ type: "motion", modifiers: modifiers(e), ...point(e) }));
  screen.addEventListener("wheel", e => {
    e.preventDefault(); wheel += e.deltaY * (e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? viewport.clientHeight : 1);
    const steps = Math.min(8, Math.floor(Math.abs(wheel) / 40));
    if (!steps) return;
    const b = wheel < 0 ? 4 : 5; wheel -= Math.sign(wheel) * steps * 40;
    for (let i = 0; i < steps; i++) {
      const data = { button: b, modifiers: modifiers(e), ...point(e) };
      send({ type: "press", ...data }); send({ type: "release", ...data });
    }
  }, { passive: false });

  copy.addEventListener("click", copyClipboard);
  sync.addEventListener("click", () => { send({ type: "clipboard_sync", enabled: !syncEnabled }); focus(); });
  document.getElementById("paste").addEventListener("click", async () => { try { paste(await navigator.clipboard.readText()); } catch (_) { message("Use Ctrl+Shift+V or the browser paste menu"); } });
  document.getElementById("escape").addEventListener("click", () => { send({ type: "key", key: "Escape" }); focus(); });
  document.getElementById("tab").addEventListener("click", () => { send({ type: "key", key: "Tab" }); focus(); });
  connect();
})();
