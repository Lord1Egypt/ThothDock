// ThothDock Web Panel. Every value from the engine is inserted as text,
// never as markup. The page refreshes on engine events, it does not poll.
"use strict";

const state = { csrf: "", tab: "containers", events: [], source: null, timer: 0, logsId: "" };
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
  const res = await fetch(path, { method, headers, credentials: "same-origin", body: body === undefined ? undefined : JSON.stringify(body) });
  const data = await res.json().catch(() => ({}));
  if (res.status === 401) throw new Unauthorized(data.error || "pairing required");
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function toast(text, bad) {
  const t = $("toast");
  t.textContent = text;
  t.className = bad ? "toast bad" : "toast";
  t.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => { t.hidden = true; }, 4000);
}

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

// ------------------------------------------------------------- pairing

function showPair(message) {
  if (state.source) { state.source.close(); state.source = null; }
  $("app").hidden = true;
  $("logout").hidden = true;
  $("engine-state").hidden = true;
  $("pair").hidden = false;
  $("pair-error").textContent = message || "";
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
  src.onerror = () => engineState(false);
  src.onopen = () => engineState(true);
  state.source = src;
}

function engineState(ok) {
  const p = $("engine-state");
  p.hidden = false;
  p.textContent = ok ? "Engine online" : "Engine unreachable";
  p.className = ok ? "pill ok" : "pill bad";
}

async function refresh() {
  try {
    await Promise.all([summary(), render()]);
    engineState(true);
  } catch (err) {
    if (err instanceof Unauthorized) return showPair("Your session ended. Pair again.");
    engineState(false);
    toast(err.message, true);
  }
}

async function summary() {
  const s = await api("GET", "/api/summary");
  const tile = (n, label, cls) => el("div", { class: "tile" }, el("div", { class: "n " + (cls || ""), text: String(n) }), el("div", { class: "l", text: label }));
  $("tiles").replaceChildren(
    tile(s.containers.running, "Running", "running"), tile(s.containers.stopped, "Stopped"),
    tile(s.stacks, "Stacks"), tile(s.images, "Images"), tile(s.volumes, "Volumes"), tile(s.networks, "Networks"));
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
  render().catch((err) => toast(err.message, true));
}));

async function render() {
  const view = $("view");
  const views = { containers: containersView, stacks: stacksView, images: imagesView, volumes: volumesView, networks: networksView, events: eventsView };
  const content = await views[state.tab]();
  view.replaceChildren(content);
}

function empty(text) { return el("div", { class: "empty", text }); }

async function act(button, fn, done) {
  button.disabled = true;
  try {
    await fn();
    if (done) toast(done);
  } catch (err) {
    if (err instanceof Unauthorized) return showPair("Your session ended. Pair again.");
    toast(err.message, true);
  } finally {
    button.disabled = false;
    refresh();
  }
}

function containerRow(c) {
  const id = encodeURIComponent(c.id);
  const action = (label, verb) => el("button", { class: "act", text: label, onclick: (e) => act(e.target, () => api("POST", "/api/containers/" + id + "/" + verb), label + ": " + c.name) });
  const actions = el("div", { class: "actions" });
  if (c.state === "running" || c.state === "restarting") actions.append(action("Stop", "stop"), action("Restart", "restart"));
  else actions.append(action("Start", "start"));
  actions.append(
    el("button", { class: "act", text: "Logs", onclick: () => openLogs(c) }),
    el("button", { class: "act del", text: "Delete", onclick: (e) => confirmDelete(c, e.target) }));
  return el("div", { class: "row" },
    el("div", { class: "head" }, el("span", { class: "dot " + c.state }), el("span", { class: "name", text: c.name }), el("span", { class: "state " + c.state, text: c.state })),
    el("div", { class: "meta", text: c.image }),
    el("div", { class: "meta dim", text: c.status + (c.project ? " · stack " + c.project + " / " + c.service : "") }),
    c.ports.length ? el("div", { class: "ports", text: c.ports.join("   ") }) : null,
    actions);
}

async function containersView() {
  const cs = await api("GET", "/api/containers");
  if (!cs.length) return empty("No containers yet. Run one with the Docker CLI in the ThothDock terminal.");
  return el("div", { class: "list" }, ...cs.map(containerRow));
}

async function stacksView() {
  const stacks = await api("GET", "/api/stacks");
  if (!stacks.length) return empty("No Compose stacks. Start one with docker compose up -d in the terminal.");
  return el("div", { class: "list" }, ...stacks.map((st) => {
    const name = encodeURIComponent(st.name);
    const action = (label, verb) => el("button", { class: "act", text: label, onclick: (e) => act(e.target, () => api("POST", "/api/stacks/" + name + "/" + verb), label + ": stack " + st.name) });
    return el("div", { class: "row" },
      el("div", { class: "head" }, el("span", { class: "dot " + (st.running ? "running" : "") }), el("span", { class: "name", text: st.name }),
        el("span", { class: "state" + (st.running ? " running" : ""), text: st.running + " / " + st.services.length + " running" })),
      ...st.services.map((s) => el("div", { class: "svc" }, el("span", { class: "dot " + s.state }), el("span", { text: s.service + " — " + s.status }))),
      el("div", { class: "actions" }, action("Start", "start"), action("Stop", "stop"), action("Restart", "restart")));
  }));
}

async function imagesView() {
  const imgs = await api("GET", "/api/images");
  if (!imgs.length) return empty("No images.");
  return el("div", { class: "list" }, ...imgs.map((i) => el("div", { class: "row" },
    el("div", { class: "head" }, el("span", { class: "name", text: i.tags.length ? i.tags.join(", ") : "<untagged>" }), el("span", { class: "state", text: bytes(i.size) })),
    el("div", { class: "meta dim", text: i.id.slice(0, 12) + " · created " + ago(i.created) }))));
}

async function volumesView() {
  const vols = await api("GET", "/api/volumes");
  if (!vols.length) return empty("No volumes.");
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
      : "Own loopback address per container, names resolve inside the network" + (n.project ? " · stack " + n.project : "") }))));
}

async function eventsView() {
  if (!state.events.length) return empty("Engine events appear here as they happen.");
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
$("logs-refresh").addEventListener("click", () => loadLogs().catch((err) => toast(err.message, true)));
$("logs-close").addEventListener("click", () => $("logs").close());

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
