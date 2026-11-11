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
      const Native = WebSocket; window.WebSocket = class extends Native {
        constructor(...args) { super(...args); window.terminalSocket = this; }
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
  const action = (id, name) => `[...${row(id)}.querySelectorAll("button")].find(b=>b.textContent===${JSON.stringify(name)})`;
  await a.click(select(first.id)); await a.connected();
  if (!await a.evaluate('window.top === window && !document.querySelector("iframe")')) throw new Error("Terminal is not a top-level page");
  // B's initial snapshot is deliberately stale until the user refreshes it.
  if (await b.evaluate(select(first.id)+".disabled")) throw new Error("Occupancy was pushed to another browser");
  await b.click(select(first.id));
  await wait(() => b.evaluate('document.getElementById("status")?.textContent.startsWith("Disconnected")'), "stale attach rejected");
  await b.click('document.getElementById("refresh")');
  await wait(() => b.evaluate(`[...${row(first.id)}.querySelectorAll("button")].every(b=>b.disabled)`), "occupied controls disabled");
  await a.text("printf first > a.input\n");
  await wait(async () => (await fs.readFile(path.join(dir,"a.input"),"utf8").catch(()=>"")) === "first", "first controller retained input");
  await b.click('document.getElementById("new")'); await b.connected();
  const secondID = await b.evaluate("document.body.dataset.pty");
  if (secondID === first.id) throw new Error("New terminal reused occupied PTY");
  await b.text("printf second > b.input\n");
  await wait(async () => (await fs.readFile(path.join(dir,"b.input"),"utf8").catch(()=>"")) === "second", "independent PTY input");
  if (await a.evaluate(`!!${row(secondID)}`)) throw new Error("New PTY list update was pushed");
  // Hiding releases the attachment, persists only in A, and keeps the shell alive.
  await a.click(action(first.id,"Hide"));
  await wait(() => a.evaluate(`location.href===${JSON.stringify(address)} && !${row(first.id)}`), "hide detaches and persists");
  await wait(async () => !(await api()).find(p=>p.id===first.id).occupied, "hide releases lease");
  await b.click('document.getElementById("refresh")');
  await wait(() => b.evaluate(`!${select(first.id)}.disabled`), "hidden terminal remains available to B");
  await a.click('document.getElementById("show-hidden")');
  await wait(() => a.evaluate(`!!${row(first.id)}`), "show hidden list");
  await a.click(action(first.id,"Show")); await a.click(select(first.id)); await a.connected();
  // PDF.js, range/media fetches and reconnect all stay under the prefix.
  const pdf = path.join(root,"TEST-ASSETS/neukirch.pdf");
  if (await fs.access(pdf).then(()=>true,()=>false)) {
    await a.text(`printf '\\033Pregion split 50%%; region secondary open "${pdf}"\\033\\\\'\n`);
    await wait(() => a.evaluate('!!document.querySelector(".media-preview .pdf-page canvas")'), "prefixed PDF canvas");
  }
  await a.evaluate("window.beforeReload = true");
  await a.command("Page.reload");
  await wait(() => a.evaluate("!window.beforeReload"), "new document");
  await a.connected();
  await a.text("printf reconnected > a.reconnected\n");
  await wait(async () => (await fs.readFile(path.join(dir,"a.reconnected"),"utf8").catch(()=>"")) === "reconnected", "reload reacquires original PTY");
  await a.click(action(first.id,"Close"));
  await wait(async () => !(await api()).some(p=>p.id===first.id), "owner closes PTY");
  await b.click(action(secondID,"Close"));
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
  console.log("Service browser smoke passed: exclusive control, independent PTYs, manual refresh, hide/show, prefixed rendering, reload, zero-PTY recovery, application tabs");
} finally {
  socket?.close(); browser?.kill("SIGTERM"); server?.kill("SIGTERM");
  await fs.writeFile(path.join(dir,"server.log"),serverLog);
  await fs.writeFile(path.join(dir,"browser.log"),browserLog);
  console.log("Artifacts: " + dir);
}
