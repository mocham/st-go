// Optional real-browser regression. Uses Node's built-in WebSocket/CDP, no npm.
import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { deflateSync } from "node:zlib";

const root = fileURLToPath(new URL("../../../", import.meta.url));
const temporary = await fs.access("/tmp/opencode").then(() => "/tmp/opencode", () => tmpdir());
const dir = await fs.mkdtemp(path.join(temporary, "st-web-browser-"));
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function wait(check, name) {
  const end = Date.now() + 10000;
  while (Date.now() < end) {
    if (await check()) return;
    await delay(50);
  }
  throw new Error("Timed out: " + name);
}

// Generate a small solid-color PNG without a third-party image package.
function fixture() {
  const crc = buffer => {
    let value = 0xffffffff;
    for (const byte of buffer) {
      value ^= byte;
      for (let i = 0; i < 8; i++) value = (value >>> 1) ^ ((value & 1) ? 0xedb88320 : 0);
    }
    return (value ^ 0xffffffff) >>> 0;
  };
  const chunk = (name, data) => {
    const type = Buffer.from(name), out = Buffer.alloc(data.length + 12);
    out.writeUInt32BE(data.length, 0); type.copy(out, 4); data.copy(out, 8);
    out.writeUInt32BE(crc(Buffer.concat([type, data])), 8 + data.length);
    return out;
  };
  const header = Buffer.alloc(13);
  header.writeUInt32BE(2, 0); header.writeUInt32BE(2, 4); header[8] = 8; header[9] = 6;
  const row = Buffer.from([0, 34, 187, 68, 255, 34, 187, 68, 255]);
  return Buffer.concat([
    Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk("IHDR", header),
    chunk("IDAT", deflateSync(Buffer.concat([row, row]))), chunk("IEND", Buffer.alloc(0)),
  ]);
}

let server, browser, socket;
let serverLog = "", browserLog = "";
try {
  server = spawn(path.join(root, "BINARIES/st-web"), ["-port", "0", "-config", path.join(root, "functionalities/config/config.json"), "-dir", dir, "-e", "/bin/sh"]);
  server.on("error", error => { serverLog += error.message; });
  server.stdout.on("data", data => { serverLog += data; });
  server.stderr.on("data", data => { serverLog += data; });
  await wait(() => /Web terminal: (http:\/\/\S+)/.test(serverLog), "server URL");
  const address = serverLog.match(/Web terminal: (http:\/\/\S+)/)[1];
  browser = spawn(process.env.CHROMIUM || "chromium", [
    "--headless", "--no-sandbox", "--disable-gpu", "--disable-background-networking",
    "--disable-extensions", "--no-first-run", "--remote-debugging-port=0",
    "--user-data-dir=" + path.join(dir, "profile"), "about:blank",
  ]);
  browser.on("error", error => { browserLog += error.message; });
  browser.stderr.on("data", data => { browserLog += data; });
  await wait(() => /DevTools listening on (ws:\/\/\S+)/.test(browserLog), "Chromium DevTools");
  socket = new WebSocket(browserLog.match(/DevTools listening on (ws:\/\/\S+)/)[1]);
  await new Promise((resolve, reject) => {
    socket.addEventListener("open", resolve, { once: true });
    socket.addEventListener("error", reject, { once: true });
  });
  let id = 0;
  const pending = new Map(), pageErrors = [];
  socket.addEventListener("message", event => {
    const data = JSON.parse(event.data);
    if (data.id) {
      const request = pending.get(data.id); pending.delete(data.id);
      if (request) data.error ? request.reject(new Error(JSON.stringify(data.error))) : request.resolve(data.result);
    }
    if (data.method === "Runtime.exceptionThrown") pageErrors.push(JSON.stringify(data.params.exceptionDetails));
  });
  const call = (method, params = {}, sessionId) => new Promise((resolve, reject) => {
    const request = { id: ++id, method, params };
    if (sessionId) request.sessionId = sessionId;
    pending.set(id, { resolve, reject }); socket.send(JSON.stringify(request));
  });
  const target = await call("Target.createTarget", { url: "about:blank" });
  const { sessionId } = await call("Target.attachToTarget", { targetId: target.targetId, flatten: true });
  const command = (method, params) => call(method, params, sessionId);
  const evaluate = async expression => (await command("Runtime.evaluate", { expression, returnByValue: true })).result.value;
  await command("Runtime.enable"); await command("Page.enable");
  // Observe the actual wire protocol before the embedded client attaches.
  await command("Page.addScriptToEvaluateOnNewDocument", { source: `
    window.paintLog = []; window.paintBatches = 0;
    const NativeSocket = window.WebSocket;
    window.WebSocket = class extends NativeSocket {
      constructor(...args) {
        super(...args);
        window.terminalSocket = this;
        this.addEventListener("message", event => {
          if (!(event.data instanceof ArrayBuffer)) {
            if (JSON.parse(event.data).type === "paint") window.paintBatches++;
            return;
          }
          const d = new DataView(event.data);
          if (d.getUint32(0) === 0x53545731) {
            window.paintLog.push({ type: "png", x: d.getUint32(12, true), y: d.getUint32(16, true), w: d.getUint32(20, true), h: d.getUint32(24, true) });
          } else if (d.getUint32(0) === 0x53544731) {
            let at = 12;
            const word = () => { const n = d.getUint32(at, true); at += 4; return n; };
            const count = word();
            for (let i = 0; i < count; i++) {
              const op = word();
              if (op === 3) { const id = word(), count = word(), w = word(), h = word(), columns = word(), n = word(); at += n; window.paintLog.push({ type: "atlas", id, count, w, h, columns }); }
              else if (op === 2) { const id = word(), x = word(), y = word(), repeat = word(); window.paintLog.push({ type: "draw", id, x, y, repeat }); }
            }
          }
          if (window.paintLog.length > 4096) window.paintLog.splice(0, window.paintLog.length - 4096);
        });
      }
    };
  ` });
  await command("Emulation.setDeviceMetricsOverride", { width: 800, height: 600, deviceScaleFactor: 1, mobile: false });
  await command("Page.navigate", { url: address });
  await wait(() => evaluate('Number(document.querySelector("canvas")?.dataset.frames || 0) > 0'), "canvas pixels");
  await wait(() => evaluate('document.querySelector("canvas").width === 800 && document.querySelector("canvas").height === 564'), "viewport device-pixel terminal size");
  if (!await evaluate(`(() => { const c = document.querySelector("canvas"), r = c.getBoundingClientRect(); return r.width === c.width && r.height === c.height; })()`)) {
    throw new Error("Canvas pixels were CSS-scaled");
  }
  await command("Emulation.setDeviceMetricsOverride", { width: 800, height: 600, deviceScaleFactor: 2, mobile: false });
  await wait(() => evaluate(`(() => { const c = document.querySelector("canvas"), r = c.getBoundingClientRect(); return devicePixelRatio === 2 && c.width === 1600 && c.height === 1128 && r.width === 800 && r.height === 564; })()`), "device-pixel canvas dimensions");
  await command("Emulation.setDeviceMetricsOverride", { width: 800, height: 600, deviceScaleFactor: 1, mobile: false });
  await wait(() => evaluate(`(() => { const c = document.querySelector("canvas"), r = c.getBoundingClientRect(); return devicePixelRatio === 1 && c.width === 800 && c.height === 564 && r.width === 800 && r.height === 564; })()`), "restored 1:1 canvas dimensions");
  async function type(text) {
    await command("Input.insertText", { text });
    await command("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 });
    await command("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 });
  }
  const fileEquals = (file, value) => async () => {
    try { return (await fs.readFile(path.join(dir, file), "utf8")).trim() === value; }
    catch { return false; }
  };
  await type('printf "%s" "$PWD" > browser.cwd');
  await wait(fileEquals("browser.cwd", dir), "PTY directory and keyboard");
  await type("printf '%s' 'café 世界' > unicode.txt");
  await wait(fileEquals("unicode.txt", "café 世界"), "Unicode input");
  await command("Input.insertText", { text: "printf '%s' '" });
  await command("Input.imeSetComposition", { text: "中文", selectionStart: 2, selectionEnd: 2 });
  await command("Input.insertText", { text: "中文" });
  await type("' > composition.txt");
  await wait(fileEquals("composition.txt", "中文"), "IME composition commit");

  // Repaint a colored FreeType glyph after erasing it. The second paint must
  // restore identical pixels through cached IDs, with no PNG for that row.
  await fs.writeFile(path.join(dir, "glyph.sh"), "printf '\\033[10;1H\\033[38;2;201;113;57mMMMMMMMM\\033[0m\\033[12;1H'\n");
  const glyphRaster = `(() => {
    const p = document.querySelector("canvas").getContext("2d").getImageData(1, 253, 128, 28).data;
    let hash = 2166136261, ink = false;
    for (let i = 0; i < p.length; i++) hash = Math.imul(hash ^ p[i], 16777619);
    for (let i = 0; i < p.length; i += 4) if (p[i] > p[i+1] && p[i+1] > p[i+2]) ink = true;
    return { hash, ink };
  })()`;
  await type("sh glyph.sh");
  await wait(async () => (await evaluate(glyphRaster)).ink, "colored FreeType glyph pixels");
  const originalGlyphs = await evaluate(glyphRaster);
  await type("printf '\\033[10;1H\\033[2K\\033[12;1H'");
  await wait(async () => !(await evaluate(glyphRaster)).ink, "glyph row erase");
  await evaluate("window.paintLog = []; true");
  await type("sh glyph.sh");
  await wait(async () => (await evaluate(glyphRaster)).hash === originalGlyphs.hash, "cached glyph pixels match original FreeType raster");
  const glyphWire = await evaluate("window.paintLog");
  if (!glyphWire.some(p => p.type === "draw" && p.y === 253) ||
      glyphWire.some(p => p.type === "png" && p.x < 129 && p.x + p.w > 1 && p.y < 281 && p.y + p.h > 253)) {
    throw new Error("Repeated FreeType glyph damage did not use cached IDs exclusively");
  }

  // Exercise real browser clipboard gestures, including the native paste event.
  await call("Browser.grantPermissions", { origin: new URL(address).origin, permissions: ["clipboardReadWrite", "clipboardSanitizedWrite"] });
  await type(`printf '\\033]52;c;Q09QWS1NRQ==\\007'`);
  await wait(() => evaluate('!document.getElementById("copy").disabled'), "OSC 52 copy availability");
  // The synchronous gesture fallback must work even when the modern API is
  // denied, as it commonly is on non-secure HTTP origins.
  await evaluate('window.nativeClipboardWrite = navigator.clipboard.writeText.bind(navigator.clipboard); navigator.clipboard.writeText = () => Promise.reject(new DOMException("Denied", "NotAllowedError")); true');
  await command("Runtime.evaluate", { expression: 'document.getElementById("copy").click()', userGesture: true });
  const browserClipboard = async () => (await command("Runtime.evaluate", { expression: "navigator.clipboard.readText()", awaitPromise: true, returnByValue: true, userGesture: true })).result.value;
  await wait(async () => await browserClipboard() === "COPY-ME", "browser clipboard copy");
  await evaluate('navigator.clipboard.writeText = window.nativeClipboardWrite; delete window.nativeClipboardWrite; true');
  await command("Runtime.evaluate", { expression: `navigator.clipboard.writeText("printf '%s' pasted > browser.paste")`, awaitPromise: true, userGesture: true });
  await command("Input.dispatchKeyEvent", { type: "keyDown", key: "V", code: "KeyV", windowsVirtualKeyCode: 86, modifiers: 10 });
  await command("Input.dispatchKeyEvent", { type: "keyUp", key: "V", code: "KeyV", windowsVirtualKeyCode: 86, modifiers: 10 });
  await type("");
  await wait(fileEquals("browser.paste", "pasted"), "native browser paste");

  // Receive a real SGR press/release through the PTY, not synthetic JS handlers.
  await type(`stty raw -echo; printf '\\033[?1000h\\033[?1006h'; touch mouse.ready; dd bs=1 count=18 of=mouse.input 2>/dev/null; printf '\\033[?1000l\\033[?1006l'; stty sane; touch mouse.done`);
  await wait(fileEquals("mouse.ready", ""), "mouse capture setup");
  await command("Input.dispatchMouseEvent", { type: "mousePressed", x: 40, y: 96, button: "left", clickCount: 1 });
  await command("Input.dispatchMouseEvent", { type: "mouseReleased", x: 40, y: 96, button: "left", clickCount: 1 });
  await wait(fileEquals("mouse.done", ""), "mouse capture completion");
  await wait(fileEquals("mouse.input", "\x1b[<0;3;3M\x1b[<0;3;3m"), "SGR mouse coordinates");

  await fs.writeFile(path.join(dir, "preview.png"), fixture());
  await type(`printf "\\033Popen 'preview.png' rect 1 3 2 2 fit-contain\\033\\\\"`);
  const greenPixels = right => `(() => {
    const c = document.querySelector("canvas");
    const x = ${right ? "Math.floor(c.width / 2) + 20" : "0"};
    const p = c.getContext("2d").getImageData(x, 0, c.width - x, c.height).data;
    for (let i = 0; i < p.length; i += 4) if (p[i] === 34 && p[i+1] === 187 && p[i+2] === 68) return true;
    return false;
  })()`;
  await wait(() => evaluate(greenPixels(false)), "DCS image pixels");
  await type(`printf '\\033]0;IGNORED_TITLE\\007'`);
  if (await evaluate("document.title") !== "st-go web terminal") throw new Error("Terminal title changed browser title");
  await type(`printf "\\033Pregion split 50%%; region secondary open 'preview.png'\\033\\\\"`);
  await wait(() => evaluate(greenPixels(true)), "retained preview pane");
  await type(`printf '\\033[2J'; touch erased`);
  await wait(fileEquals("erased", ""), "terminal erase command");
  await wait(() => evaluate(greenPixels(true)), "retained preview survives erase");
  await type(`printf '\\033Pregion reset\\033\\'`);
  await command("Emulation.setDeviceMetricsOverride", { width: 1000, height: 700, deviceScaleFactor: 1, mobile: false });
  await wait(() => evaluate('document.querySelector("canvas").width === 1000 && document.querySelector("canvas").height === 664'), "live device-pixel resize");
  await type("stty size > browser.size");
  await wait(fileEquals("browser.size", "23 62"), "PTY resize");
  await evaluate("window.__reloadMarker = true");
  await command("Page.reload");
  await wait(() => evaluate('!window.__reloadMarker && Number(document.querySelector("canvas")?.dataset.frames || 0) > 0'), "reconnect pixels");
  await type("printf '%s' reconnected > browser.reconnected");
  await wait(fileEquals("browser.reconnected", "reconnected"), "reconnected PTY input");
  // Also reconnect within the same page: its old ImageBitmaps and pending
  // decode queue must not leak into the new connection's glyph-ID namespace.
  await evaluate('window.previousFrames = Number(document.querySelector("canvas").dataset.frames); window.previousSocket = window.terminalSocket; window.terminalSocket.close(); true');
  await wait(() => evaluate('window.terminalSocket !== window.previousSocket && window.terminalSocket.readyState === WebSocket.OPEN && Number(document.querySelector("canvas").dataset.frames) > window.previousFrames'), "in-page reconnect with fresh glyph cache");
  await type("printf '%s' resumed > browser.resumed");
  await wait(fileEquals("browser.resumed", "resumed"), "in-page reconnected PTY input");

  // Stall a real asynchronous decode while the PTY produces many screenfuls.
  // Input must remain usable and the socket must not queue obsolete paints.
  let stress = "\x1b[2J";
  for (let row = 1; row <= 16; row++) {
    stress += `\x1b[${row};1H`;
    for (let col = 0; col < 48; col++) stress += `\x1b[38;2;${(row * 31 + col) % 256};${col * 5};${row * 11}m${String.fromCharCode(33 + (row * 48 + col) % 90)}`;
  }
  stress += "\x1b[0m\x1b[20;1H";
  await fs.writeFile(path.join(dir, "stress.ansi"), stress);
  await fs.writeFile(path.join(dir, "stress.sh"), "for n in $(seq 1 60); do cat stress.ansi; sleep 0.01; done\nprintf '\\033[2J\\033[1;1H\\033[48;2;31;191;79m                                        \\033[0m\\033[3;1H'\ntouch stress.done\n");
  await evaluate(`window.originalBitmap = window.createImageBitmap; window.paintStalled = false;
    window.createImageBitmap = async (...args) => {
      if (!window.paintStalled) { window.paintStalled = true; await new Promise(resolve => window.releasePaint = resolve); }
      return window.originalBitmap(...args);
    }; true`);
  await type("sh stress.sh");
  await wait(() => evaluate("window.paintStalled"), "stalled image decode");
  const stalledBatches = await evaluate("window.paintBatches");
  await wait(fileEquals("stress.done", ""), "sustained PTY output while browser is stalled");
  const inputStart = Date.now();
  await type("printf responsive > stress.input");
  await wait(fileEquals("stress.input", "responsive"), "input during stalled paint");
  if (Date.now() - inputStart > 1500) throw new Error("Stalled painting blocked keyboard input");
  if (await evaluate("window.paintBatches") - stalledBatches > 1) throw new Error("Unbounded browser paint backlog");
  await evaluate("window.createImageBitmap = window.originalBitmap; window.releasePaint(); true");
  await wait(() => evaluate(`(() => { const p = document.querySelector("canvas").getContext("2d").getImageData(10, 10, 1, 1).data; return p[0] === 31 && p[1] === 191 && p[2] === 79; })()`), "latest pixels after coalescing stalled output");

  // Real govim --web: no PDF preview, genuine HTML/CSS, save refresh, resize,
  // reconnect and teardown use the same retained secondary-pane lifecycle.
  await fs.writeFile(path.join(dir, "web.html"), '<h1 id="web-probe" style="color:rgb(20,80,140)">OLD web preview</h1><script>parent.previewScriptRan=true</script>');
  await fs.writeFile(path.join(dir, "web.md"), "# Markdown web preview\n\nSelectable **HTML** and $x^2$.\n");
  await type(path.join(root, "BINARIES/govim") + " --web --preview-gap 0s web.html");
  const preview = 'document.querySelector("iframe.html-preview")';
  await wait(() => evaluate(`${preview}?.contentDocument?.getElementById("web-probe")?.textContent === "OLD web preview"`), "govim HTML preview");
  if (!await evaluate(`(() => { const f = ${preview}, r = f.getBoundingClientRect(), c = document.querySelector("canvas").getBoundingClientRect(); return r.left > c.left + c.width * 0.35 && r.right <= c.right && f.contentWindow.getComputedStyle(f.contentDocument.getElementById("web-probe")).color === "rgb(20, 80, 140)" && !window.previewScriptRan; })()`)) {
    throw new Error("HTML overlay geometry, CSS or document isolation is incorrect");
  }
  await type(":%s/OLD/NEW/g");
  await type(":w");
  await wait(() => evaluate(`${preview}?.contentDocument?.getElementById("web-probe")?.textContent === "NEW web preview"`), "saved HTML preview refresh");
  await command("Emulation.setDeviceMetricsOverride", { width: 1200, height: 750, deviceScaleFactor: 1, mobile: false });
  await wait(() => evaluate(`(() => { const f = ${preview}; return f && document.querySelector("canvas").width === 1200 && f.getBoundingClientRect().width > 650; })()`), "HTML pane follows resize");
  await command("Page.reload");
  await wait(() => evaluate(`${preview}?.contentDocument?.getElementById("web-probe")?.textContent === "NEW web preview"`), "HTML pane restored on reconnect");
  await type(":e web.md");
  await wait(() => evaluate(`${preview}?.contentDocument?.body?.textContent.includes("Markdown web preview") && !!${preview}?.contentDocument?.querySelector("svg")`), "Markdown and SVG math rendered as HTML");
  await type(":q!");
  await wait(() => evaluate(`!${preview}`), "HTML preview removed on editor exit");
  if (pageErrors.length) throw new Error("JavaScript errors: " + pageErrors.join("\n"));
  console.log("PASS: Chromium glyph atlases, stalled-decode backpressure/input, govim HTML/CSS/Markdown preview, pixel fidelity, keyboard/IME, clipboard, mouse, resize and reconnect.");
  console.log("Test artifacts: " + dir);
} catch (error) {
  console.error(serverLog, browserLog);
  throw error;
} finally {
  if (socket) socket.close();
  if (browser) browser.kill("SIGTERM");
  if (server) server.kill("SIGTERM");
}
