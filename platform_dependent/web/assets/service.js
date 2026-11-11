"use strict";
(() => {
  const list = document.getElementById("terminals");
  const status = document.getElementById("service-status"), empty = document.getElementById("empty");
  const showHidden = document.getElementById("show-hidden");
  const prefix = document.body.dataset.prefix, selected = document.body.dataset.pty;
  const storageKey = "st-hidden:" + prefix;
  let hidden = new Set(), terminals = [], attachment = "", refreshSerial = 0;
  try { hidden = new Set(JSON.parse(localStorage.getItem(storageKey) || "[]")); } catch (_) {}
  if (selected) {
    hidden.delete(selected);
    empty.hidden = true;
    document.getElementById("terminal-tools").hidden = false;
    document.getElementById("viewport").hidden = false;
  }
  const saveHidden = () => { try { localStorage.setItem(storageKey, JSON.stringify([...hidden])); } catch (_) {} };
  const message = text => { status.textContent = text; };
  async function api(path, method = "GET", body) {
    const headers = { "X-ST-Service-Request": "1" };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    if (attachment && path === "/" + selected) headers["X-ST-Attachment"] = attachment;
    const response = await fetch(prefix + "api/ptys" + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), cache: "no-store" });
    if (!response.ok) throw new Error((await response.text()).trim() || "Request failed");
    return response.status === 204 ? null : response.json();
  }
  function detach() {
    location.assign(prefix);
  }
  function open(terminal) {
    if (selected === terminal.id && attachment) return;
    if (terminal.occupied) { message("Terminal is occupied. Refresh to check again."); return; }
    hidden.delete(terminal.id); saveHidden();
    location.assign(terminal.view);
  }
  function button(text, action, disabled = false) {
    const button = document.createElement("button");
    button.textContent = text; button.disabled = disabled;
    button.addEventListener("click", () => Promise.resolve().then(action).catch(error => message(error.message)));
    return button;
  }
  function render() {
    list.replaceChildren();
    for (const terminal of terminals) {
      const isHidden = hidden.has(terminal.id), own = selected === terminal.id && !!attachment;
      if (isHidden && !showHidden.checked) continue;
      const occupied = terminal.occupied && !own;
      const item = document.createElement("div");
      item.className = "pty" + (selected === terminal.id ? " active" : "") + (occupied ? " occupied" : "");
      item.dataset.id = terminal.id;
      const select = button(terminal.title + (occupied ? " · occupied" : ""), () => open(terminal), occupied || isHidden);
      select.className = "select";
      item.append(select);
      item.append(button(isHidden ? "Show" : "Hide", () => {
        if (isHidden) hidden.delete(terminal.id);
        else hidden.add(terminal.id);
        saveHidden(); render();
        if (!isHidden && selected === terminal.id) detach();
      }, occupied));
      item.append(button("Rename", async () => {
        const title = prompt("Terminal title", terminal.title);
        if (!title) return;
        Object.assign(terminal, await api("/" + terminal.id, "PATCH", {title})); render();
      }, occupied));
      item.append(button("Close", async () => {
        await api("/" + terminal.id, "DELETE");
        if (selected === terminal.id) detach();
        terminals = terminals.filter(t => t.id !== terminal.id);
        hidden.delete(terminal.id); saveHidden(); render(); message("Terminal closed");
      }, occupied));
      list.append(item);
    }
    empty.textContent = terminals.length ? "Select a terminal, show a hidden terminal, or create a new one." : "No terminals are running. Click New terminal to start one.";
  }
  async function refresh() {
    const serial = ++refreshSerial;
    const result = await api("");
    if (serial !== refreshSerial) return;
    terminals = result;
    const live = new Set(terminals.map(t => t.id));
    hidden = new Set([...hidden].filter(id => live.has(id))); saveHidden();
    if (selected && !live.has(selected)) detach();
    render(); message("Terminal list refreshed");
  }
  document.getElementById("refresh").onclick = () => refresh().catch(error => message(error.message));
  document.getElementById("new").onclick = async event => {
    event.target.disabled = true;
    try {
      const terminal = await api("", "POST", {});
      ++refreshSerial;
      terminals.push(terminal); open(terminal);
    } catch (error) { message(error.message); }
    finally { event.target.disabled = false; }
  };
  showHidden.onchange = render;
  window.addEventListener("st-terminal", event => {
    if (event.detail.type === "connected") {
      attachment = event.detail.attachment || ""; message("Connected"); render();
    } else if (event.detail.type === "disconnected") {
      attachment = "";
      message("Disconnected. Refresh terminals to check availability."); render();
    } else if (event.detail.type === "open") {
      const terminal = event.detail.pty;
      const expected = new URL(prefix + "api/ptys/" + terminal?.id + "/view/", location.href);
      if (!/^[a-f0-9]{32}$/.test(terminal?.id) || new URL(terminal.view, location.href).href !== expected.href) return;
      if (!terminals.some(t => t.id === terminal.id)) terminals.push(terminal);
      open(terminal);
    }
  });
  // Occupancy is intentionally fetched only at startup or on Refresh.
  refresh().catch(error => message(error.message));
})();
