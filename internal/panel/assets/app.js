// ThothDock Web Panel. Every value from the engine is inserted as text, never
// as markup. The page refreshes on engine events; it does not poll.
"use strict";

const state = { csrf: "", tab: "containers", events: [], source: null, timer: 0, logsId: "", loaded: false };
const $ = (id) => document.getElementById(id);

function el(tag, props, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (k === "class") node.className = v;
    else if (k === "text") node.textContent = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v);
  }
  for (const c of children) if (c) node.append(c);
  return node;
}

class Unauthorized extends Error {}

async function api(method, path, body) {
  const headers = {};
  if (method !== "GET") headers["X-ThothDock-CSRF"] = state.csrf;
  if (body !== undefined) headers["Content-Type"] = "application/json";
  let res;
  try {
    res = await fetch(path, { method, headers, credentials: "same-origin", body: body === undefined ? undefined : JSON.stringify(body) });
  } catch (_) {
    throw new Error("Cannot reach the phone. Check that the panel is still running and you are on the same network.");
  }
  const data = await res.json().catch(() => ({}));
  if (res.status === 401) throw new Unauthorized(data.error || "pairing required");
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function toast(text, kind) {
  const t = $("toast");
  t.textContent = text;
  t.className = "toast" + (kind ? " " + kind : "");
  t.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => { t.hidden = true; }, 3800);
}

function banner(text, info, action) {
  $("banner").className = "banner" + (info ? " info" : "");
  $("banner-text").textContent = text;
  const b = $("banner-action");
  b.hidden = !action;
  b.onclick = action || null;
  $("banner").hidden = false;
}
function clearBanner() { $("banner").hidden = true; }

function bytes(n) {
  if (!n && n !== 0) return "–";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(1) : n) + " " + u[i];
}

function ago(seconds) {
  const s = Math.max(0, Date.now() / 1000 - seconds);
  if (s < 60) return "just now";
  if (s < 3600) return Math.floor(s / 60) + " min ago";
  if (s < 86400) return Math.floor(s / 3600) + " h ago";
  return Math.floor(s / 86400) + " days ago";
}

function chip(id, text, kind) {
  const c = $(id);
  c.textContent = text;
  c.className = "chip" + (kind ? " " + kind : "");
  c.hidden = false;
}

// ------------------------------------------------------------- pairing

function showPair(message, expired) {
  if (state.source) { state.source.close(); state.source = null; }
  $("app").hidden = true;
  $("logout").hidden = true;
  $("session-state").hidden = true;
  $("engine-version").hidden = true;
  $("pair").hidden = false;
  $("pair-error").textContent = message || "";
  if (expired) banner("Your session ended. Pair this browser again with a new code from the phone.", true);
  else clearBanner();
  $("code").focus();
}

$("pair-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const btn = e.submitter;
  btn.disabled = true;
  try {
    const r = await api("POST", "/pair", { code: $("code").value });
    state.csrf = r.csrf;
    $("code").value = "";
    clearBanner();
    await start();
  } catch (err) {
    $("pair-error").textContent = err.message;
  } finally {
    btn.disabled = false;
  }
});

$("logout").addEventListener("click", async () => {
  try { await api("POST", "/api/logout"); } catch (_) { /* ends the session either way */ }
  state.csrf = "";
  showPair("Signed out.");
});

// ------------------------------------------------------------- app

async function start() {
  const s = await api("GET", "/api/session");
  state.csrf = s.csrf;
  $("fingerprint").textContent = "ThothDock " + s.version + " · certificate SHA-256 " + s.fingerprint;
  $("pair").hidden = true;
  $("app").hidden = false;
  $("logout").hidden = false;
  chip("session-state", "Paired", "ok");
  listen();
  await refresh();
}

function listen() {
  if (state.source) state.source.close();
  const src = new EventSource("/api/events");
  src.onmessage = (m) => {
    let ev;
    try { ev = JSON.parse(m.data); } catch (_) { return; }
    state.events.unshift(ev);
    state.events.length = Math.min(state.events.length, 200);
    clearTimeout(state.timer);
    state.timer = setTimeout(refresh, 300);
  };
  src.onerror = () => chip("session-state", "Reconnecting…", "bad");
  src.onopen = () => chip("session-state", "Paired", "ok");
  state.source = src;
}

async function refresh() {
  try {
    await Promise.all([summary(), render()]);
    clearBanner();
  } catch (err) {
    if (err instanceof Unauthorized) return showPair("", true);
    banner(err.message, false, refresh);
  }
}

async function summary() {
  const s = await api("GET", "/api/summary");
  chip("engine-version", "Engine " + s.engine.version, "");
  const tile = (n, label, cls) => el("div", { class: "tile" }, el("div", { class: "n " + (cls || ""), text: String(n) }), el("div", { class: "l", text: label }));
  $("tiles").replaceChildren(
    tile(s.containers.running, "Running", "running"), tile(s.containers.stopped, "Stopped"),
    tile(s.images, "Images", "cyan"), tile(s.volumes, "Volumes", "blue"),
    tile(s.stacks, "Stacks"), tile(s.networks, "Networks"));
  const h = s.host || {};
  const parts = [];
  const fig = (label, value) => el("span", null, label + " ", el("b", { text: value }));
  if (h.memTotal) parts.push(fig("Memory", bytes(h.memAvailable) + " free of " + bytes(h.memTotal)));
  if (h.storageTotal) parts.push(fig("Storage", bytes(h.storageFree) + " free of " + bytes(h.storageTotal)));
  if (h.load) parts.push(fig("Load", h.load.join(" ")));
  parts.push(fig("Engine", s.engine.version + " · " + s.engine.arch + " · " + s.engine.cpus + " CPUs"));
  $("host").replaceChildren(...parts);
}

document.querySelectorAll(".tabs button").forEach((b) => b.addEventListener("click", () => {
  document.querySelectorAll(".tabs button").forEach((x) => x.classList.toggle("active", x === b));
  state.tab = b.dataset.tab;
  state.loaded = false;
  render().catch((err) => toast(err.message, "bad"));
}));

async function render() {
  const view = $("view");
  if (!state.loaded) view.replaceChildren(el("div", { class: "list" }, el("div", { class: "skeleton" }), el("div", { class: "skeleton" })));
  const views = { containers: containersView, stacks: stacksView, images: imagesView, volumes: volumesView, networks: networksView, events: eventsView };
  const content = await views[state.tab]();
  state.loaded = true;
  view.replaceChildren(content);
}

function empty(title, text) { return el("div", { class: "empty" }, el("b", { text: title }), el("span", { text })); }

async function act(button, fn, done) {
  button.disabled = true;
  try {
    await fn();
    if (done) toast(done, "good");
  } catch (err) {
    if (err instanceof Unauthorized) return showPair("", true);
    toast(err.message, "bad");
  } finally {
    button.disabled = false;
    refresh();
  }
}

function containerRow(c) {
  const id = encodeURIComponent(c.id);
  const verb = (label, v, cls) => el("button", { class: "btn " + (cls || ""), text: label, onclick: (e) => act(e.target, () => api("POST", "/api/containers/" + id + "/" + v), label + " · " + c.name) });
  const actions = el("div", { class: "actions" });
  if (c.state === "running" || c.state === "restarting") actions.append(verb("Stop", "stop", "go"), verb("Restart", "restart", "go"));
  else actions.append(verb("Start", "start", "go"));
  actions.append(
    el("button", { class: "btn", text: "Logs", onclick: () => openLogs(c) }),
    el("button", { class: "btn", text: "Shell", onclick: () => openShell(c) }),
    el("button", { class: "btn del", text: "Delete", onclick: (e) => confirmDelete(c, e.target) }));
  const ports = c.ports.length ? el("div", { class: "ports" }, ...c.ports.map((p) => el("span", { class: "port", text: p }))) : null;
  return el("div", { class: "row" },
    el("div", { class: "head" }, el("span", { class: "dot " + c.state }), el("span", { class: "name", text: c.name }), el("span", { class: "state " + c.state, text: c.state })),
    el("div", { class: "meta", text: c.image }),
    el("div", { class: "meta dim", text: c.status + (c.project ? " · stack " + c.project + " / " + c.service : "") }),
    ports, actions);
}

async function containersView() {
  const cs = await api("GET", "/api/containers");
  if (!cs.length) return empty("No containers yet", "Run one in the ThothDock terminal, for example: docker run -d --name hello --restart unless-stopped alpine sleep 100000");
  return el("div", { class: "list" }, ...cs.map(containerRow));
}

async function stacksView() {
  const stacks = await api("GET", "/api/stacks");
  if (!stacks.length) return empty("No Compose stacks", "Start one in the terminal with docker compose up -d.");
  return el("div", { class: "list" }, ...stacks.map((st) => {
    const name = encodeURIComponent(st.name);
    const verb = (label, v) => el("button", { class: "btn go", text: label, onclick: (e) => act(e.target, () => api("POST", "/api/stacks/" + name + "/" + v), label + " · stack " + st.name) });
    return el("div", { class: "row" },
      el("div", { class: "head" }, el("span", { class: "dot " + (st.running ? "running" : "") }), el("span", { class: "name", text: st.name }),
        el("span", { class: "state" + (st.running ? " running" : ""), text: st.running + " / " + st.services.length + " running" })),
      ...st.services.map((s) => el("div", { class: "svc" }, el("span", { class: "dot " + s.state }), el("span", { text: s.service + " — " + s.status }))),
      el("div", { class: "actions" }, verb("Start", "start"), verb("Stop", "stop"), verb("Restart", "restart")));
  }));
}

async function imagesView() {
  const imgs = await api("GET", "/api/images");
  if (!imgs.length) return empty("No images", "Pull one with docker pull.");
  return el("div", { class: "list" }, ...imgs.map((i) => el("div", { class: "row" },
    el("div", { class: "head" }, el("span", { class: "name", text: i.tags.length ? i.tags.join(", ") : "<untagged>" }), el("span", { class: "state", text: bytes(i.size) })),
    el("div", { class: "meta dim", text: i.id.slice(0, 12) + " · created " + ago(i.created) }))));
}

async function volumesView() {
  const vols = await api("GET", "/api/volumes");
  if (!vols.length) return empty("No volumes", "Create one with docker volume create, or use -v name:/path.");
  return el("div", { class: "list" }, ...vols.map((v) => el("div", { class: "row" },
    el("div", { class: "head" }, el("span", { class: "name", text: v.name }), el("span", { class: "state", text: v.driver })),
    el("div", { class: "meta dim", text: (v.project ? "stack " + v.project + " · " : "") + "created " + v.created }))));
}

async function networksView() {
  const nets = await api("GET", "/api/networks");
  return el("div", { class: "list" }, ...nets.map((n) => el("div", { class: "row" },
    el("div", { class: "head" }, el("span", { class: "name", text: n.name }), el("span", { class: "state", text: n.builtin ? "device network" : n.subnet })),
    el("div", { class: "meta dim", text: n.builtin
      ? "Built in: containers here share the phone's network"
      : "Own loopback address per container; names resolve inside the network" + (n.project ? " · stack " + n.project : "") }))));
}

async function eventsView() {
  if (!state.events.length) return empty("Waiting for activity", "Engine events appear here as they happen.");
  return el("div", { class: "events" }, ...state.events.map((e) => el("div", null,
    el("span", { class: "t", text: new Date(e.time * 1000).toLocaleTimeString() + "  " }),
    el("span", { text: e.type + " " }), el("span", { class: "a", text: e.action + " " }), el("span", { text: e.name || e.id.slice(0, 12) }))));
}

// ------------------------------------------------------------- dialogs

async function loadLogs() {
  const r = await api("GET", "/api/containers/" + encodeURIComponent(state.logsId) + "/logs?tail=500");
  $("logs-text").textContent = r.text || "(no output)";
  $("logs-text").scrollTop = $("logs-text").scrollHeight;
}

async function openLogs(c) {
  state.logsId = c.id;
  $("logs-title").textContent = "Logs · " + c.name;
  $("logs-text").textContent = "Loading…";
  $("logs").showModal();
  try { await loadLogs(); } catch (err) { $("logs-text").textContent = err.message; }
}
$("logs-refresh").addEventListener("click", () => loadLogs().catch((err) => toast(err.message, "bad")));
$("logs-close").addEventListener("click", () => $("logs").close());

function openShell(c) {
  const safe = /^[a-zA-Z0-9][a-zA-Z0-9_.-]*$/.test(c.name) ? c.name : c.id.slice(0, 12);
  $("shell-cmd").textContent = "docker exec -it " + safe + " sh";
  $("shell").showModal();
}
$("shell-close").addEventListener("click", () => $("shell").close());
$("shell-copy").addEventListener("click", async () => {
  try { await navigator.clipboard.writeText($("shell-cmd").textContent); toast("Command copied", "good"); }
  catch (_) { toast("Select the command and copy it", "bad"); }
});

function confirmDelete(c, button) {
  $("confirm-text").textContent = "Delete container " + c.name + "? It is stopped first if it runs. Its data outside named volumes is lost.";
  const dlg = $("confirm");
  const yes = $("confirm-yes"), no = $("confirm-no");
  const close = () => { yes.onclick = null; no.onclick = null; dlg.close(); };
  yes.onclick = () => { close(); act(button, () => api("DELETE", "/api/containers/" + encodeURIComponent(c.id)), "Deleted " + c.name); };
  no.onclick = close;
  dlg.showModal();
}

// ------------------------------------------------------------- boot

start().catch((err) => showPair(err instanceof Unauthorized ? "" : err.message));
