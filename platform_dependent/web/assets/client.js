"use strict";
(() => {
  const screen = document.getElementById("screen");
  const viewport = document.getElementById("viewport");
  const input = document.getElementById("input");
  const status = document.getElementById("status");
  const copy = document.getElementById("copy");
  const ctx = screen.getContext("2d", { alpha: false });
  let socket, composing = false, clipboard = "", reconnect, screenRatio = 0;
  let paintQueue = Promise.resolve(), wheel = 0;
  const utf8 = new TextEncoder();
  const message = text => { status.textContent = text; };
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
  const copyClipboard = async () => {
    try { await navigator.clipboard.writeText(clipboard); message("Copied"); }
    catch (_) { message("Clipboard write denied by browser"); }
    focus();
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
  };
  window.addEventListener("resize", () => sizeScreen(screen.width, screen.height), { passive: true });
  setInterval(() => {
    const ratio = Number.isFinite(window.devicePixelRatio) && window.devicePixelRatio > 0 ? window.devicePixelRatio : 1;
    if (ratio !== screenRatio) sizeScreen(screen.width, screen.height);
  }, 250);

  async function paint(data) {
    if (data.byteLength < 28) throw new Error("Short pixel frame");
    const header = new DataView(data);
    if (header.getUint32(0, false) !== 0x53545731) throw new Error("Unknown pixel frame");
    const values = Array.from({ length: 6 }, (_, i) => header.getUint32(4 + i * 4, true));
    const [width, height, x, y, w, h] = values;
    if (!w || !h || !width || !height || width * height > 16777216 || x + w > width || y + h > height) throw new Error("Invalid frame dimensions");
    const bitmap = await createImageBitmap(new Blob([data.slice(28)], { type: "image/png" }));
    try {
      if (bitmap.width !== w || bitmap.height !== h) throw new Error("Pixel dimensions differ");
      sizeScreen(width, height);
      ctx.drawImage(bitmap, x, y);
      screen.dataset.frames = String(Number(screen.dataset.frames || 0) + 1);
    } finally { bitmap.close(); }
  }

  function connect() {
    clearTimeout(reconnect);
    const url = new URL("ws", location.href);
    url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
    socket = new WebSocket(url);
    socket.binaryType = "arraybuffer";
    socket.onmessage = event => {
      if (typeof event.data !== "string") {
        // Image decoding is asynchronous; commit damage frames in wire order.
        paintQueue = paintQueue.then(() => paint(event.data)).catch(error => { message(error.message); socket.close(); });
        return;
      }
      const data = JSON.parse(event.data);
      if (data.type === "hello") { sizeScreen(data.width, data.height); message("Connected"); focus(); send({ type: "focus", focused: true }); }
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
    socket.onclose = () => { message("Disconnected — reconnecting (only one browser may attach)"); reconnect = setTimeout(connect, 1500); };
    socket.onerror = () => message("Connection unavailable");
  }

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
  document.getElementById("paste").addEventListener("click", async () => { try { paste(await navigator.clipboard.readText()); } catch (_) { message("Use Ctrl+Shift+V or the browser paste menu"); } });
  document.getElementById("escape").addEventListener("click", () => { send({ type: "key", key: "Escape" }); focus(); });
  document.getElementById("tab").addEventListener("click", () => { send({ type: "key", key: "Tab" }); focus(); });
  setInterval(() => send({ type: "ping" }), 20000);
  connect();
})();
