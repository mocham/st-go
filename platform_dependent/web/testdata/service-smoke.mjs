// Real multi-browser service regression; Node >=22 and Chromium, no npm.
import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../../../", import.meta.url));
const tmp = await fs.access("/tmp/opencode").then(() => "/tmp/opencode", () => tmpdir());
const dir = await fs.mkdtemp(path.join(tmp, "st-service-browser-"));
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function wait(check, label) {
  for (let end = Date.now() + 15000; Date.now() < end;) {
    if (await check()) return;
    await delay(50);
  }
  throw new Error("Timed out: " + label);
}
let server, browser, socket, serverLog = "", browserLog = "";
try {
  server = spawn(path.join(root, "BINARIES/st-web"), ["-service", "-port", "0", "-base-path", "/tools/terminal/", "-dir", dir, "-e", "/bin/sh"]);
  server.stdout.on("data", data => { serverLog += data; });
  server.stderr.on("data", data => { serverLog += data; });
  await wait(() => /Web terminal service: (http:\/\/\S+)/.test(serverLog), "service startup");
  const address = serverLog.match(/Web terminal service: (http:\/\/\S+)/)[1];
  const origin = new URL(address).origin;
  const api = async (suffix = "", method = "GET", body) => {
    const response = await fetch(address + "api/ptys" + suffix, {method, headers: {"Origin": origin, "Content-Type": "application/json"}, body: body === undefined ? undefined : JSON.stringify(body)});
    if (!response.ok) throw new Error(await response.text());
    return response.status === 204 ? null : response.json();
  };
  browser = spawn(process.env.CHROMIUM || "chromium", ["--headless", "--no-sandbox", "--disable-gpu", "--disable-background-networking", "--disable-extensions", "--no-first-run", "--remote-debugging-port=0", "--user-data-dir=" + path.join(dir, "profile"), "about:blank"]);
  browser.stderr.on("data", data => { browserLog += data; });
  await wait(() => /DevTools listening on (ws:\/\/\S+)/.test(browserLog), "Chromium startup");
  socket = new WebSocket(browserLog.match(/DevTools listening on (ws:\/\/\S+)/)[1]);
  await new Promise((resolve, reject) => { socket.addEventListener("open", resolve, {once:true}); socket.addEventListener("error", reject, {once:true}); });
  let sequence = 0;
  const pending = new Map(), errors = [], requests = [];
  socket.onmessage = event => {
    const data = JSON.parse(event.data);
    if (data.id) {
      const request = pending.get(data.id); pending.delete(data.id);
      if (request) data.error ? request.reject(new Error(JSON.stringify(data.error))) : request.resolve(data.result);
    }
    if (data.method === "Runtime.exceptionThrown") errors.push(data.params.exceptionDetails);
    if (data.method === "Network.requestWillBeSent") requests.push(data.params.request.url);
    if (data.method === "Network.webSocketCreated") requests.push(data.params.url);
  };
  const call = (method, params = {}, sessionId) => new Promise((resolve, reject) => {
    const id = ++sequence; pending.set(id, {resolve, reject}); socket.send(JSON.stringify({id, method, params, sessionId}));
  });
  async function page() {
    const {browserContextId} = await call("Target.createBrowserContext");
    const {targetId} = await call("Target.createTarget", {url:"about:blank", browserContextId});
    const {sessionId} = await call("Target.attachToTarget", {targetId, flatten:true});
    const command = (method, params) => call(method, params, sessionId);
    await command("Runtime.enable"); await command("Page.enable"); await command("Network.enable");
    await command("Emulation.setDeviceMetricsOverride", {width:1000, height:720, deviceScaleFactor:1, mobile:false});
    await command("Page.addScriptToEvaluateOnNewDocument", {source:`
      window.resizeLog = [];
      const Native = WebSocket; window.WebSocket = class extends Native {
        constructor(...args) { super(...args); window.terminalSocket = this; }
        send(data) {
          const message = JSON.parse(data);
          if (message.type === "resize") window.resizeLog.push([message.width, message.height]);
          super.send(data);
        }
      };
    `});
    const evaluate = async expression => {
      const response = await command("Runtime.evaluate", {expression, returnByValue:true, awaitPromise:true});
      if (response.exceptionDetails) throw new Error(JSON.stringify(response.exceptionDetails));
      return response.result.value;
    };
    const navigate = async url => { await command("Page.navigate", {url}); await wait(() => evaluate('!!document.querySelector(".pty")'), "terminal list"); };
    const click = expression => evaluate(expression + ".click(); true");
    const connected = () => wait(() => evaluate('document.getElementById("status")?.textContent === "Connected" && Number(document.getElementById("screen")?.dataset.frames) > 0'), "attached Canvas");
    const text = value => evaluate(`window.terminalSocket.send(JSON.stringify({type:"text",text:${JSON.stringify(value)}})); true`);
    return {command, evaluate, navigate, click, connected, text};
  }
  const a = await page(), b = await page();
  await a.navigate(address); await b.navigate(address);
  const first = (await api())[0];
  const row = id => `document.querySelector('[data-id="${id}"]')`;
  const select = id => row(id) + '.querySelector(".select")';
  async function serviceAction(page, id) {
    if (!await page.evaluate('document.getElementById("service-menu").matches(":popover-open")')) await page.click('document.getElementById("service-menu-toggle")');
    await wait(() => page.evaluate('document.getElementById("service-menu").matches(":popover-open")'), "service dropdown");
    await page.click(`document.getElementById(${JSON.stringify(id)})`);
  }
  async function tabAction(page, id, name) {
    await page.click(row(id) + '.querySelector(".tab-actions")');
    await wait(() => page.evaluate('document.getElementById("pty-menu").matches(":popover-open")'), "tab actions dropdown");
    await page.click(`[...document.getElementById("pty-menu").querySelectorAll("button")].find(b=>b.textContent===${JSON.stringify(name)})`);
  }
  await a.click(select(first.id)); await a.connected();
  const viewportState = `(() => {
    const viewport = document.getElementById("viewport"), screen = document.getElementById("screen");
    const bounds = viewport.getBoundingClientRect(), canvas = screen.getBoundingClientRect(), bar = document.getElementById("tab-bar").getBoundingClientRect();
    return {resizes:window.resizeLog, viewport:[bounds.width,bounds.height,viewport.clientWidth,viewport.clientHeight], top:bounds.top, bar:[bar.top,bar.bottom], headers:document.querySelectorAll("header").length, tabTops:[...document.querySelectorAll(".pty")].map(t=>t.getBoundingClientRect().top), screen:[screen.width,screen.height], canvas:[canvas.width,canvas.height]};
  })()`;
  async function stableViewport(page, width, height, deviceScaleFactor) {
    await page.command("Emulation.setDeviceMetricsOverride", {width,height,deviceScaleFactor,mobile:false});
    await delay(1000);
    const before = await page.evaluate(viewportState);
    await delay(1000);
    const after = await page.evaluate(viewportState);
    if (after.resizes.length !== before.resizes.length) throw new Error("Terminal resize did not settle: " + JSON.stringify(after));
    if (after.canvas.some((size, i) => size > after.viewport[i] + .05)) throw new Error("Canvas exceeds its viewport: " + JSON.stringify(after));
    if (after.headers !== 1 || Math.abs(after.top-after.bar[1]) > .05 || new Set(after.tabTops).size > 1) throw new Error("Terminal controls are not in one tab bar: " + JSON.stringify(after));
    for (const menu of ["service", "tools"]) {
      await page.evaluate(`document.getElementById("${menu}-menu-toggle").focus(); document.getElementById("${menu}-menu-toggle").click(); true`);
      await wait(() => page.evaluate(`document.getElementById("${menu}-menu").matches(":popover-open") && document.getElementById("${menu}-menu-toggle").getAttribute("aria-expanded") === "true"`), "menu opens");
      await delay(100);
      const inside = await page.evaluate(`(() => { const r = document.getElementById("${menu}-menu").getBoundingClientRect(); return r.left >= 0 && r.right <= innerWidth+.1 && r.top >= document.getElementById("tab-bar").getBoundingClientRect().bottom && r.bottom <= innerHeight+.1; })()`);
      if (!inside) throw new Error("Dropdown extends outside viewport: " + JSON.stringify(await page.evaluate(`({menu:${JSON.stringify(menu)}, bounds:document.getElementById("${menu}-menu").getBoundingClientRect().toJSON(), bar:document.getElementById("tab-bar").getBoundingClientRect().toJSON(), width:innerWidth, height:innerHeight, zoom:document.body.style.zoom})`)));
      if (menu === "tools" && width === 375) {
        const shot = await page.command("Page.captureScreenshot", {format:"png"});
        await fs.writeFile(path.join(dir,"compact-tabs.png"), Buffer.from(shot.data,"base64"));
      }
      await page.command("Input.dispatchKeyEvent", {type:"keyDown", key:"Escape", code:"Escape", windowsVirtualKeyCode:27});
      await page.command("Input.dispatchKeyEvent", {type:"keyUp", key:"Escape", code:"Escape", windowsVirtualKeyCode:27});
      await wait(() => page.evaluate(`!document.getElementById("${menu}-menu").matches(":popover-open") && document.getElementById("${menu}-menu-toggle").getAttribute("aria-expanded") === "false"`), "Escape dismisses menu");
    }
    const closed = await page.evaluate(viewportState);
    if (closed.resizes.length !== after.resizes.length || closed.top !== after.top || closed.viewport.join() !== after.viewport.join()) throw new Error("Dropdown opening resized the terminal");
  }
  // Scaling creates fractional toolbar/viewport dimensions. The bar stays on
  // one row at narrow widths, and dropdowns never cause PTY resize feedback.
  for (const zoom of ["1", "1.1"]) {
    await a.evaluate(`document.body.style.zoom = ${JSON.stringify(zoom)}`);
    for (const size of [[1000,720,1],[375,667,1],[800,601,1.25]]) await stableViewport(a,...size);
  }
  await a.evaluate('document.body.style.zoom = "1"');
  await stableViewport(a,1000,720,1);
  if (!await a.evaluate('window.top === window && !document.querySelector("iframe")')) throw new Error("Terminal is not a top-level page");
  // B's initial snapshot is deliberately stale until the user refreshes it.
  if (await b.evaluate(select(first.id)+".disabled")) throw new Error("Occupancy was pushed to another browser");
  await b.click(select(first.id));
  await wait(() => b.evaluate('document.getElementById("status")?.textContent.startsWith("Disconnected")'), "stale attach rejected");
  await serviceAction(b,"refresh");
  await wait(() => b.evaluate(`[...${row(first.id)}.querySelectorAll("button")].every(b=>b.disabled)`), "occupied controls disabled");
  await a.text("printf first > a.input\n");
  await wait(async () => (await fs.readFile(path.join(dir,"a.input"),"utf8").catch(()=>"")) === "first", "first controller retained input");
  await b.click('document.getElementById("new")'); await b.connected();
  const secondID = await b.evaluate("document.body.dataset.pty");
  if (secondID === first.id) throw new Error("New terminal reused occupied PTY");
  await b.evaluate('document.body.style.zoom = "1.1"');
  await stableViewport(b,375,667,1.25); // horizontally scrolling terminal tabs
  await b.evaluate('document.body.style.zoom = "1"');
  await stableViewport(b,1000,720,1);
  await b.text("printf second > b.input\n");
  await wait(async () => (await fs.readFile(path.join(dir,"b.input"),"utf8").catch(()=>"")) === "second", "independent PTY input");
  if (await a.evaluate(`!!${row(secondID)}`)) throw new Error("New PTY list update was pushed");
  // Hiding releases the attachment, persists only in A, and keeps the shell alive.
  await tabAction(a,first.id,"Hide");
  await wait(() => a.evaluate(`location.href===${JSON.stringify(address)} && !${row(first.id)}`), "hide detaches and persists");
  await wait(async () => !(await api()).find(p=>p.id===first.id).occupied, "hide releases lease");
  await serviceAction(b,"refresh");
  await wait(() => b.evaluate(`!${select(first.id)}.disabled`), "hidden terminal remains available to B");
  await serviceAction(a,"show-hidden");
  await wait(() => a.evaluate(`!!${row(first.id)}`), "show hidden list");
  await tabAction(a,first.id,"Show"); await a.click(select(first.id)); await a.connected();
  // PDF.js, range/media fetches and reconnect all stay under the prefix.
  const pdf = path.join(root,"TEST-ASSETS/neukirch.pdf");
  if (await fs.access(pdf).then(()=>true,()=>false)) {
    await a.text(`printf '\\033Pregion split 50%%; region secondary open "${pdf}"\\033\\\\'\n`);
    await wait(() => a.evaluate('!!document.querySelector(".media-preview .pdf-page canvas")'), "prefixed PDF canvas");
    await stableViewport(a,1000,720,1);
  }
  await a.evaluate("window.beforeReload = true");
  await a.command("Page.reload");
  await wait(() => a.evaluate("!window.beforeReload"), "new document");
  await a.connected();
  await a.text("printf reconnected > a.reconnected\n");
  await wait(async () => (await fs.readFile(path.join(dir,"a.reconnected"),"utf8").catch(()=>"")) === "reconnected", "reload reacquires original PTY");
  await tabAction(a,first.id,"Close");
  await wait(async () => !(await api()).some(p=>p.id===first.id), "owner closes PTY");
  await tabAction(b,secondID,"Close");
  await wait(async () => (await api()).length === 0, "last terminal closed");
  await wait(() => b.evaluate('document.getElementById("empty")?.textContent.includes("No terminals")'), "zero-PTY UI");
  await b.click('document.getElementById("new")'); await b.connected();
  const parentID = await b.evaluate("document.body.dataset.pty");
  const payload = Buffer.from(JSON.stringify({command:["/bin/sh"], directory:dir, title:"application tab"})).toString("base64").replace(/=+$/, "");
  await b.text(`printf '\\033Ptab-spawn auth %s %s %s\\033\\\\' "$ST_GO_TAB_TOKEN" 0123456789abcdef0123456789abcdef ${payload}\n`);
  await wait(() => b.evaluate(`document.body.dataset.pty && document.body.dataset.pty !== ${JSON.stringify(parentID)}`), "application tab opens in originating browser");
  await b.connected();
  const spawned = await api();
  if (spawned.length !== 2 || spawned.find(p=>p.id===parentID)?.occupied) throw new Error("Application tab did not preserve/detach its parent PTY");
  const invalid = requests.filter(raw => { const u = new URL(raw); return ["http:","ws:"].includes(u.protocol) && u.host === new URL(address).host && !u.pathname.startsWith("/tools/terminal/"); });
  if (invalid.length) throw new Error("Unprefixed requests: " + invalid.join(", "));
  if (errors.length) throw new Error(JSON.stringify(errors));
  console.log("Service browser smoke passed: single-row tabs, dropdown placement/dismissal without resize, exclusive control, independent PTYs, manual refresh, hide/show, prefixed rendering, reload, zero-PTY recovery, application tabs");
} finally {
  socket?.close(); browser?.kill("SIGTERM"); server?.kill("SIGTERM");
  await fs.writeFile(path.join(dir,"server.log"),serverLog);
  await fs.writeFile(path.join(dir,"browser.log"),browserLog);
  console.log("Artifacts: " + dir);
}
