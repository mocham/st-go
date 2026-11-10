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
  server = spawn(path.join(root, "BINARIES/st-web"), ["-port", "0", "-dir", dir, "-e", "/bin/sh"]);
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
  await command("Emulation.setDeviceMetricsOverride", { width: 800, height: 600, deviceScaleFactor: 1, mobile: false });
  await command("Page.navigate", { url: address });
  await wait(() => evaluate('Number(document.querySelector("canvas")?.dataset.frames || 0) > 0'), "canvas pixels");
  await wait(() => evaluate('document.querySelector("canvas").width === 800 && document.querySelector("canvas").height === 564'), "initial terminal size");
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

  // Exercise real browser clipboard gestures, including the native paste event.
  await call("Browser.grantPermissions", { origin: new URL(address).origin, permissions: ["clipboardReadWrite", "clipboardSanitizedWrite"] });
  await type(`printf '\\033]52;c;Q09QWS1NRQ==\\007'`);
  await wait(() => evaluate('!document.getElementById("copy").disabled'), "OSC 52 copy availability");
  await command("Runtime.evaluate", { expression: 'document.getElementById("copy").click()', userGesture: true });
  const browserClipboard = async () => (await command("Runtime.evaluate", { expression: "navigator.clipboard.readText()", awaitPromise: true, returnByValue: true, userGesture: true })).result.value;
  await wait(async () => await browserClipboard() === "COPY-ME", "browser clipboard copy");
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
  await wait(() => evaluate('document.querySelector("canvas").width === 1000 && document.querySelector("canvas").height === 664'), "live resize");
  await type("stty size > browser.size");
  await wait(fileEquals("browser.size", "23 62"), "PTY resize");
  await evaluate("window.__reloadMarker = true");
  await command("Page.reload");
  await wait(() => evaluate('!window.__reloadMarker && Number(document.querySelector("canvas")?.dataset.frames || 0) > 0'), "reconnect pixels");
  await type("printf '%s' reconnected > browser.reconnected");
  await wait(fileEquals("browser.reconnected", "reconnected"), "reconnected PTY input");
  if (pageErrors.length) throw new Error("JavaScript errors: " + pageErrors.join("\n"));
  console.log("PASS: Chromium canvas, keyboard/IME, clipboard/paste, SGR mouse, cwd, image, retained pane, ignored title, resize and reconnect.");
  console.log("Test artifacts: " + dir);
} catch (error) {
  console.error(serverLog, browserLog);
  throw error;
} finally {
  if (socket) socket.close();
  if (browser) browser.kill("SIGTERM");
  if (server) server.kill("SIGTERM");
}
