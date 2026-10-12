const gate = document.querySelector("#gate");
const app = document.querySelector("#app");
const stage = document.querySelector("#stage");
const projectsNav = document.querySelector("#projects");
const finder = document.querySelector("#finder");

async function api(path, opts = {}) {
  const res = await fetch(path, {
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", ...(opts.headers || {}) },
    ...opts,
  });
  if (res.status === 401 && location.pathname !== "/") {
    location.href = "/";
    throw new Error("auth");
  }
  const text = await res.text();
  const body = text ? JSON.parse(text) : {};
  if (!res.ok) throw new Error(body.error?.message || res.statusText);
  return body;
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

const statusName = { active: "vigente", proposed: "proposta", superseded: "substituída", rejected: "rejeitada", archived: "arquivada", expired: "expirada", deprecated: "aposentada" };
function pill(status) {
  return `<span class="pill ${esc(status)}">${esc(statusName[status] || status)}</span>`;
}

async function me() {
  const res = await fetch("/auth/me", { credentials: "same-origin" });
  if (!res.ok) return null;
  return res.json();
}

function showApp(on) {
  app.hidden = !on;
  gate.hidden = on;
  gate.className = on ? "" : "gate";
}

document.querySelector("#logout").onclick = async () => {
  await fetch("/auth/logout", { method: "POST", credentials: "same-origin" });
  location.href = "/";
};

document.querySelector("#new-project").onclick = () => {
  history.pushState({}, "", "/projetos");
  route();
};

let finderTimer;
finder.addEventListener("input", () => {
  clearTimeout(finderTimer);
  finderTimer = setTimeout(() => {
    const q = finder.value.trim();
    if (q.length < 2) return route();
    search(q).catch((e) => fail(e));
  }, 180);
});

function withScope(path, scope) {
  return scope ? `${path}?scope=${encodeURIComponent(scope)}` : path;
}

async function sidebar(active) {
  const data = await api("/api/scopes");
  projectsNav.innerHTML = data.scopes.map((s) => `<a class="${s.slug === active ? "active" : ""}" href="/memorias?scope=${esc(s.slug)}"><span>${esc(s.name)}</span><small>${s.counts?.active || 0}</small></a>`).join("");
  document.querySelectorAll(".tools a").forEach((a) => {
    const base = a.pathname;
    if (base === "/mapa") a.href = "/mapa?scope=all";
    else if (["/sessoes", "/projeto", "/admin", "/cofre"].includes(base)) a.href = withScope(base, active || "global");
    a.classList.toggle("active", a.pathname === location.pathname);
  });
  return data.scopes;
}

async function route() {
  const user = await me();
  showApp(!!user);
  if (!user) return login();
  const path = location.pathname;
  const scope = new URLSearchParams(location.search).get("scope") || new URLSearchParams(location.search).get("project") || "";
  await sidebar(scope);
  if (path.startsWith("/memorias/")) return memory(decodeURIComponent(path.slice("/memorias/".length)));
  if (path.startsWith("/sessoes/")) return sessionDetail(decodeURIComponent(path.slice("/sessoes/".length)), scope);
  switch (path) {
    case "/memorias": return memories(scope);
    case "/mapa": return neuralMap(scope);
    case "/sessoes": return sessions(scope);
    case "/projeto": return projectPage(scope);
    case "/admin": return adminPage(scope);
    case "/cofre": return vaultPage(scope);
    case "/revisao": return review();
    case "/tokens": return tokens();
    case "/saude": return health();
    case "/conectar": return connect();
    case "/projetos": return newProject();
    default: return memories(scope || "global");
  }
}

async function vaultPage(scope) {
  const slug = scope || "global";
  const q = `?project=${encodeURIComponent(slug)}&scope=${encodeURIComponent(slug === "global" ? "global" : slug)}`;
  const data = await api("/api/vault" + q);
  const rows = (data.entries || []).map((e) => `<tr>
    <td><code>${esc(e.key)}</code></td>
    <td>${esc(e.label || "—")}</td>
    <td class="muted">${esc(e.hint || "")}</td>
    <td><code class="muted">${esc(e.ref)}</code></td>
    <td class="row">
      <button class="btn quiet" type="button" data-reveal="${esc(e.key)}">Revelar</button>
      <button class="btn danger" type="button" data-del="${esc(e.key)}">Apagar</button>
    </td>
  </tr>`).join("") || `<tr><td colspan="5" class="muted">Nenhuma credencial neste escopo.</td></tr>`;
  stage.innerHTML = `<header class="top"><div><h1>Cofre</h1><p>Credenciais criptografadas (AES-GCM). A IA consulta com <code>memory_vault_get</code>; nas memórias use só a referência <code>vault:…</code>.</p></div>
    <div class="actions"><label class="field inline">Escopo<select id="vault-scope">${(await api("/api/scopes")).scopes.map((s) => `<option value="${esc(s.slug)}" ${s.slug === slug ? "selected" : ""}>${esc(s.name)}</option>`).join("")}</select></label></div></header>
    <form class="sheet" id="vault-form">
      <div class="row" style="gap:0.6rem;flex-wrap:wrap">
        <label class="field" style="flex:1;min-width:8rem;margin:0"><span class="muted">Chave</span><input name="key" required pattern="[a-z0-9][a-z0-9._/-]{0,127}" placeholder="docker-hub"></label>
        <label class="field" style="flex:1;min-width:8rem;margin:0"><span class="muted">Rótulo</span><input name="label" placeholder="Docker Hub"></label>
        <label class="field" style="flex:1;min-width:8rem;margin:0"><span class="muted">Dica (sem segredo)</span><input name="hint" placeholder="org impa365"></label>
      </div>
      <label class="field"><span class="muted">Valor</span><input name="value" type="password" required autocomplete="new-password" placeholder="cole o token/senha"></label>
      <div class="actions"><button class="btn" type="submit">Salvar no cofre</button><span class="muted" id="vault-msg"></span></div>
      <p class="error" id="vault-err"></p>
    </form>
    <section class="sheet"><h2>Entradas · ${esc(slug)}</h2>
      <table><thead><tr><th>Chave</th><th>Rótulo</th><th>Dica</th><th>Ref</th><th></th></tr></thead><tbody>${rows}</tbody></table>
      <pre class="sheet" id="vault-reveal" hidden></pre>
    </section>`;
  stage.querySelector("#vault-scope").onchange = (ev) => {
    history.pushState({}, "", "/cofre?scope=" + encodeURIComponent(ev.target.value));
    route().catch(fail);
  };
  stage.querySelector("#vault-form").onsubmit = async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const key = String(f.get("key") || "").trim().toLowerCase();
    try {
      const out = await api("/api/vault/" + encodeURIComponent(key) + q, {
        method: "PUT",
        body: JSON.stringify({ key, label: f.get("label") || "", hint: f.get("hint") || "", value: f.get("value") }),
      });
      stage.querySelector("#vault-msg").textContent = "salvo · " + out.ref;
      route();
    } catch (e) { stage.querySelector("#vault-err").textContent = e.message; }
  };
  stage.querySelectorAll("[data-reveal]").forEach((btn) => btn.onclick = async () => {
    const box = stage.querySelector("#vault-reveal");
    try {
      const out = await api("/api/vault/" + encodeURIComponent(btn.dataset.reveal) + q);
      box.hidden = false;
      box.textContent = out.value;
    } catch (e) { stage.querySelector("#vault-err").textContent = e.message; }
  });
  stage.querySelectorAll("[data-del]").forEach((btn) => btn.onclick = async () => {
    if (!confirm("Apagar " + btn.dataset.del + " do cofre?")) return;
    try {
      await api("/api/vault/" + encodeURIComponent(btn.dataset.del) + q, { method: "DELETE", body: "{}" });
      route();
    } catch (e) { stage.querySelector("#vault-err").textContent = e.message; }
  });
}

function fail(e) { stage.innerHTML = `<p class="error">${esc(e.message)}</p>`; }

function login() {
  gate.innerHTML = `<form autocomplete="current-password">
    <div class="brand"><span class="mark" aria-hidden="true"></span><div><strong>impamind</strong><small>memória do trabalho</small></div></div>
    <h1>Entrar</h1>
    <label class="field">Senha de administração<input type="password" name="password" required autofocus placeholder="••••••••"></label>
    <p class="error" id="err" aria-live="polite"></p>
    <button class="btn" type="submit">Entrar</button>
  </form>`;
  gate.querySelector("form").onsubmit = async (ev) => {
    ev.preventDefault();
    const password = new FormData(ev.target).get("password");
    const res = await fetch("/auth/login", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ password }) });
    if (!res.ok) { gate.querySelector("#err").textContent = "Senha recusada."; return; }
    location.href = "/memorias?scope=global";
  };
}

function memCard(m, scope) {
  return `<a class="mem" href="/memorias/${esc(m.id)}?project=${esc(scope || m.scope || "")}"><div class="row">${pill(m.status)} <span>${esc(m.kind)}</span></div><strong>${esc(m.title)}</strong><span>${esc(m.content).slice(0, 160)}</span></a>`;
}

async function memories(scope) {
  const slug = scope || "global";
  const q = `?project=${encodeURIComponent(slug)}&scope=${slug === "global" ? "global" : "project"}`;
  const data = await api("/api/memories" + q);
  const groups = { 1: [], 2: [], 3: [] };
  for (const m of data.items) groups[m.tier] ? groups[m.tier].push(m) : groups[2].push(m);
  const section = (tier, label) => `<section class="tier" data-tier="${tier}"><h2>${label}</h2>${groups[tier].map((m) => memCard(m, slug)).join("") || `<p class="empty">Nada neste nível.</p>`}</section>`;
  stage.innerHTML = `<header class="top"><div><h1>${esc(slug)}</h1><p>${data.total} memórias</p></div>
    <div class="actions">${slug !== "global" ? `<a class="btn quiet" href="/projeto?scope=${esc(slug)}">Projeto</a>` : ""}<button class="btn" id="add" type="button">Nova memória</button></div></header>
    ${section(1, "T1 · núcleo")}${section(2, "T2 · trabalho")}${section(3, "T3 · contexto")}`;
  stage.querySelector("#add").onclick = () => composer(slug);
}

function composer(scope) {
  stage.insertAdjacentHTML("beforeend", `<form class="sheet" id="composer"><h2>Nova memória</h2>
    <label class="field">Tipo<select name="kind"><option>note</option><option>decision</option><option>preference</option><option>knowledge</option><option>architecture</option><option>todo</option></select></label>
    <label class="field">Título<input name="title" required></label>
    <label class="field">Conteúdo<textarea name="content" required></textarea></label>
    <label class="field">Motivo<input name="rationale" placeholder="obrigatório para uma decisão"></label>
    <label class="field"><span><input type="checkbox" name="approve"> Aprovar agora, quando a política pedir</span></label>
    <div class="actions"><button class="btn" type="submit">Salvar</button><button class="btn quiet" type="button" id="cancel">Cancelar</button></div>
    <p class="error" id="err"></p></form>`);
  stage.querySelector("#cancel").onclick = () => stage.querySelector("#composer").remove();
  stage.querySelector("#composer").onsubmit = async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    try {
      await api("/api/memories", { method: "POST", body: JSON.stringify({
        project: scope === "global" ? "" : scope,
        scope: scope === "global" ? "global" : "project",
        kind: f.get("kind"), title: f.get("title"), content: f.get("content"), rationale: f.get("rationale") || "",
        approve: f.get("approve") === "on", user_stated: true,
      }) });
      route();
    } catch (e) { stage.querySelector("#err").textContent = e.message; }
  };
}

async function memory(id) {
  const project = new URLSearchParams(location.search).get("project") || "";
  const q = project ? `?project=${encodeURIComponent(project)}` : "";
  const data = await api("/api/memories/" + encodeURIComponent(id) + "/history" + q);
  const m = data.memory;
  const links = await api("/api/links?memory_ids=" + encodeURIComponent(id) + (project ? "&project=" + encodeURIComponent(project) : "")).catch(() => ({ links: [] }));
  const peers = await api(`/api/memories?project=${encodeURIComponent(project || "global")}&scope=${project && project !== "global" ? "project" : "global"}&limit=200`).catch(() => ({ items: [] }));
  const line = (data.lineage || []).map((x) => `<article><div class="row">${pill(x.status)} <strong>${esc(x.title)}</strong></div><p>${esc(x.content)}</p></article>`).join("");
  const versions = (data.versions || []).map((v) => `<li>${esc(v.change_type)} · v${v.version} · ${esc(v.created_at)}</li>`).join("");
  const linkRows = (links.links || []).map((l) => `<li><code>${esc(l.label || "link")}</code> · ${esc(l.source_id === id ? l.target_id : l.source_id)}
    <button class="text-btn" type="button" data-unlink-s="${esc(l.source_id)}" data-unlink-t="${esc(l.target_id)}">remover</button></li>`).join("") || `<li class="muted">Nenhuma ligação.</li>`;
  const peerOpts = (peers.items || []).filter((x) => x.id !== id).map((x) => `<option value="${esc(x.id)}">${esc(x.title)}</option>`).join("");
  const active = m.status === "active" || m.status === "proposed";
  stage.innerHTML = `<header class="top"><div><a class="muted" href="/memorias?scope=${esc(project)}">← ${esc(project || "memórias")}</a><h1>${esc(m.title)}</h1><p>${pill(m.status)} ${esc(m.kind)} · T${m.tier} · ${esc(m.source_type)}</p></div>
    <div class="actions">
      ${m.status === "proposed" ? `<button class="btn" id="approve" type="button">Aprovar</button><button class="btn danger" id="reject" type="button">Rejeitar</button>` : ""}
      ${active ? `<button class="btn quiet" id="edit" type="button">Editar</button>` : ""}
      ${m.status === "active" ? `<button class="btn quiet" id="supersede" type="button">Substituir</button>` : ""}
      ${m.status === "active" && project && project !== "global" ? `<button class="btn quiet" id="promote" type="button">Promover</button>` : ""}
      ${m.status === "active" ? `<button class="btn quiet" id="archive" type="button">Arquivar</button><button class="btn quiet" id="deprecate" type="button">Aposentar</button>` : ""}
      ${active ? `<button class="btn danger" id="del" type="button">Apagar</button>` : ""}
    </div></header>
    <article class="sheet" id="body"><p>${esc(m.content)}</p><p class="muted">${esc(m.rationale || "")}</p></article>
    <section class="sheet"><h2>Ligações</h2><ul id="link-list">${linkRows}</ul>
      <form id="link-form" class="row" style="margin-top:0.6rem">
        <label class="field" style="flex:1;margin:0"><span class="muted">Ligar a</span><select name="target" required>${peerOpts || `<option value="">—</option>`}</select></label>
        <label class="field" style="width:8rem;margin:0"><span class="muted">Rótulo</span><input name="label" placeholder="relaciona"></label>
        <button class="btn" type="submit">Ligar</button>
      </form>
      <div class="actions" style="margin-top:0.55rem">
        <button class="btn quiet" type="button" id="suggest-links">Sugerir ligações</button>
        <span class="muted" id="suggest-msg"></span>
      </div>
      <p class="error" id="link-err"></p>
    </section>
    <section class="sheet"><h2>Linhagem</h2><div class="timeline">${line}</div></section>
    <section class="sheet"><h2>Versões</h2><ul>${versions}</ul></section>
    <p class="error" id="err"></p>`;
  const err = () => stage.querySelector("#err");
  const go = async (path, body, method = "POST") => {
    try { await api(path + q, { method, body: JSON.stringify(body ?? {}) }); route(); }
    catch (e) { err().textContent = e.message; }
  };
  stage.querySelector("#approve")?.addEventListener("click", () => go("/api/memories/" + id + "/approve", { note: "aprovado na interface" }));
  stage.querySelector("#reject")?.addEventListener("click", () => go("/api/memories/" + id + "/reject", { note: "rejeitado na interface" }));
  stage.querySelector("#promote")?.addEventListener("click", () => go("/api/memories/" + id + "/promote", { approve: false }));
  stage.querySelector("#archive")?.addEventListener("click", () => go("/api/memories/" + id + "/transition", { to: "archived", reason: "arquivado na interface" }));
  stage.querySelector("#deprecate")?.addEventListener("click", () => go("/api/memories/" + id + "/transition", { to: "deprecated", reason: "aposentado na interface" }));
  stage.querySelector("#del")?.addEventListener("click", async () => {
    if (!confirm("Apagar esta memória de forma permanente?")) return;
    try {
      await api("/api/memories/" + id + q, { method: "DELETE", body: JSON.stringify({ reason: "apagado na interface" }) });
      history.pushState({}, "", "/memorias?scope=" + encodeURIComponent(project || "global"));
      route();
    } catch (e) { err().textContent = e.message; }
  });
  stage.querySelector("#edit")?.addEventListener("click", () => {
    stage.querySelector("#body").innerHTML = `<form id="edit-form">
      <label class="field">Título<input name="title" value="${esc(m.title)}" required></label>
      <label class="field">Conteúdo<textarea name="content" required>${esc(m.content)}</textarea></label>
      <label class="field">Motivo<input name="rationale" value="${esc(m.rationale || "")}"></label>
      <label class="field">Nível<select name="tier"><option value="1" ${m.tier === 1 ? "selected" : ""}>T1</option><option value="2" ${m.tier === 2 ? "selected" : ""}>T2</option><option value="3" ${m.tier === 3 ? "selected" : ""}>T3</option></select></label>
      <label class="field"><span><input type="checkbox" name="pinned" ${m.pinned ? "checked" : ""}> Fixar</span></label>
      <div class="actions"><button class="btn" type="submit">Salvar</button><button class="btn quiet" type="button" id="edit-cancel">Cancelar</button></div></form>`;
    stage.querySelector("#edit-cancel").onclick = () => route();
    stage.querySelector("#edit-form").onsubmit = async (ev) => {
      ev.preventDefault();
      const f = new FormData(ev.target);
      try {
        await api("/api/memories/" + id + q, { method: "PATCH", body: JSON.stringify({
          title: f.get("title"), content: f.get("content"), rationale: f.get("rationale") || "",
          tier: Number(f.get("tier")), pinned: f.get("pinned") === "on", reason: "editado na interface",
        }) });
        route();
      } catch (e) { err().textContent = e.message; }
    };
  });
  stage.querySelector("#supersede")?.addEventListener("click", () => {
    stage.querySelector("#body").insertAdjacentHTML("afterend", `<form class="sheet" id="sup-form"><h2>Substituir</h2>
      <label class="field">Novo título<input name="title" value="${esc(m.title)}" required></label>
      <label class="field">Novo conteúdo<textarea name="content" required>${esc(m.content)}</textarea></label>
      <label class="field">Motivo<input name="reason" required placeholder="por que a versão antiga deixa de valer"></label>
      <div class="actions"><button class="btn" type="submit">Substituir</button><button class="btn quiet" type="button" id="sup-cancel">Cancelar</button></div>
      <p class="error" id="sup-err"></p></form>`);
    stage.querySelector("#sup-cancel").onclick = () => stage.querySelector("#sup-form").remove();
    stage.querySelector("#sup-form").onsubmit = async (ev) => {
      ev.preventDefault();
      const f = new FormData(ev.target);
      try {
        const out = await api("/api/memories/" + id + "/supersede" + q, { method: "POST", body: JSON.stringify({
          project: project === "global" ? "" : project,
          reason: f.get("reason"),
          new: { kind: m.kind, title: f.get("title"), content: f.get("content"), rationale: f.get("reason") },
        }) });
        const nid = out.memory?.id || id;
        history.pushState({}, "", `/memorias/${encodeURIComponent(nid)}?project=${encodeURIComponent(project)}`);
        route();
      } catch (e) { stage.querySelector("#sup-err").textContent = e.message; }
    };
  });
  stage.querySelector("#link-form").onsubmit = async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    try {
      await api("/api/links" + q, { method: "POST", body: JSON.stringify({ source_id: id, target_id: f.get("target"), label: f.get("label") || "relaciona" }) });
      route();
    } catch (e) { stage.querySelector("#link-err").textContent = e.message; }
  };
  stage.querySelector("#suggest-links")?.addEventListener("click", async () => {
    const msg = stage.querySelector("#suggest-msg");
    try {
      if (msg) msg.textContent = "a sugerir…";
      const out = await api("/api/memories/" + encodeURIComponent(id) + "/suggest-links" + q, { method: "POST", body: "{}" });
      if (msg) msg.textContent = `${out.created || 0} novas · ${out.candidates || 0} candidatas`;
      route();
    } catch (e) {
      if (msg) msg.textContent = e.message;
      else stage.querySelector("#link-err").textContent = e.message;
    }
  });
  stage.querySelectorAll("[data-unlink-s]").forEach((btn) => btn.onclick = async () => {
    try {
      await api(`/api/links?source_id=${encodeURIComponent(btn.dataset.unlinkS)}&target_id=${encodeURIComponent(btn.dataset.unlinkT)}${project ? "&project=" + encodeURIComponent(project) : ""}`, { method: "DELETE", body: "{}" });
      route();
    } catch (e) { stage.querySelector("#link-err").textContent = e.message; }
  });
}

async function search(q) {
  const scope = new URLSearchParams(location.search).get("scope") || "";
  const data = await api("/api/search", { method: "POST", body: JSON.stringify({ query: q, project: scope && scope !== "global" ? scope : "", scope: scope === "global" ? "global" : "" }) });
  stage.innerHTML = `<header class="top"><div><h1>Busca</h1><p>${esc(q)}</p></div></header>${(data.items || []).map((m) => memCard(m, m.scope)).join("") || `<p class="empty">Nada encontrado.</p>`}`;
}

function graphQuery(scope) {
  if (!scope || scope === "both" || scope === "all" || scope === "todos") return "?scope=all&limit=2000";
  if (scope === "global") return "?scope=global&limit=2000";
  return `?project=${encodeURIComponent(scope)}&scope=project&limit=2000`;
}

async function neuralMap(scope) {
  const slug = !scope || scope === "both" || scope === "todos" ? "all" : scope;
  const data = await api("/api/graph" + graphQuery(slug));
  const scopes = await api("/api/scopes");
  const byID = Object.fromEntries((scopes.scopes || []).map((s) => [s.id, s.slug]));
  const present = (scopes.scopes || []).filter((s) => (data.nodes || []).some((n) => n.scope_id === s.id));
  const scopeNames = Object.fromEntries(present.map((s) => [s.id, s.name]));
  // Soft Obsidian-like accents (low saturation) — not Atom One neon.
  const obsPalette = ["#b8a4a8", "#8fa4b8", "#b8b09a", "#9aaf9e", "#a898b0", "#8fadb0", "#b0a090", "#9a9aa3"];
  const scopeColors = Object.fromEntries(present.map((s, i) => [s.id, obsPalette[i % obsPalette.length]]));
  const mapMode = localStorage.getItem("impamind-map-mode") || "graph";
  const edgeCounts = { link: 0, similar: 0, supersedes: 0 };
  for (const e of data.edges || []) {
    if ((e.label || "").startsWith("topic:")) continue;
    edgeCounts[e.type] = (edgeCounts[e.type] || 0) + 1;
  }
  const scopeOpts = (scopes.scopes || []).map((s) => `<option value="${esc(s.slug)}" ${s.slug === slug ? "selected" : ""}>${esc(s.name)}</option>`).join("");
  stage.innerHTML = `<header class="top"><div><h1>Mapa</h1><p id="nmap-stats">${data.nodes.length} memórias · ${data.edges.length} ligações · ${esc(slug)}</p><p class="muted" style="font-size:12px;margin:0.2rem 0 0">Grafo estilo Obsidian — densifica sozinho se faltar ligação; depois congela. Arraste um ponto; <strong>Ctrl+arrastar</strong> move o conjunto conectado; <strong>Padrão</strong> reinicia.</p></div>
    <div class="actions"><label class="field inline">Escopo<select id="nmap-scope"><option value="all" ${slug === "all" ? "selected" : ""}>Todos</option>${scopeOpts}</select></label></div></header>
    <div class="nmap-wrap constellation-wrap obsidian-wrap" id="nmap-wrap">
      <div class="nmap-toolbar" id="nmap-toolbar">
        <button class="btn ${mapMode === "graph" ? "on" : ""}" type="button" data-act="mode-graph" title="Grafo force (Obsidian)">Grafo</button>
        <button class="btn ${mapMode === "stars" ? "on" : ""}" type="button" data-act="mode-stars" title="Campo estelar (Hindsight)">Estrelas</button>
        <button class="btn" type="button" data-act="zoom-out" title="Afastar">−</button>
        <button class="btn" type="button" data-act="zoom-in" title="Aproximar">+</button>
        <button class="btn" type="button" data-act="fit" title="Enquadrar">Fit</button>
        <button class="btn" type="button" data-act="reset" title="Layout automático (apaga posições fixadas)">Padrão</button>
        <button class="btn ${mapMode === "stars" ? "on" : ""}" type="button" data-act="motion" title="Só no modo Estrelas" ${mapMode === "graph" ? "disabled" : ""}>Motion</button>
        <button class="btn" type="button" data-act="suggest" title="Gerar ligações similares">Ligações</button>
        <button class="btn" type="button" data-act="png" title="Exportar PNG">PNG</button>
        <button class="btn" type="button" data-act="full" title="Tela cheia">Full</button>
      </div>
      <canvas id="nmap" aria-label="Constelação de memórias"></canvas>
      <div class="nmap-tip" id="nmap-tip" role="tooltip"></div>
      <aside class="nmap-hud hidden" id="nmap-hud" aria-hidden="true"></aside>
    </div>`;
  stage.querySelector("#nmap-scope").onchange = (ev) => {
    history.pushState({}, "", "/mapa?scope=" + encodeURIComponent(ev.target.value));
    route().catch(fail);
  };
  runNeuralMap(stage.querySelector("#nmap"), stage.querySelector("#nmap-hud"), stage.querySelector("#nmap-wrap"), stage.querySelector("#nmap-tip"), data, byID, slug, scopeNames, scopeColors, edgeCounts, mapMode);
}

function runNeuralMap(canvas, hud, wrap, tipEl, data, byID, slug, scopeNames, scopeColors, edgeCounts, mapMode) {
  const rawEdges = (data.edges || []).filter((e) => !(e.label || "").startsWith("topic:"));
  const nodeWeights = new Map();
  for (const e of rawEdges) {
    const w = typeof e.weight === "number" && e.weight > 0 ? e.weight : 1;
    nodeWeights.set(e.source, (nodeWeights.get(e.source) || 0) + w);
    nodeWeights.set(e.target, (nodeWeights.get(e.target) || 0) + w);
  }
  let maxWeight = 1;
  for (const v of nodeWeights.values()) if (v > maxWeight) maxWeight = v;

  const graphNodes = (data.nodes || []).map((n) => {
    const scopeLabel = scopeNames[n.scope_id] || byID[n.scope_id] || slug;
    return {
      id: n.id,
      label: n.title || n.id.slice(0, 12),
      metadata: {
        title: n.title,
        kind: n.kind,
        tier: n.tier,
        status: n.status,
        scope_id: n.scope_id,
        scopeLabel,
        degree: n.degree,
      },
    };
  });
  const idSet = new Set(graphNodes.map((n) => n.id));
  const graphLinks = rawEdges.filter((e) => idSet.has(e.source) && idSet.has(e.target)).map((e) => ({
    source: e.source,
    target: e.target,
    type: e.type || "similar",
    weight: e.weight,
  }));

  let view = null;
  let mode = mapMode || "graph";
  let hudToken = 0;
  const titleByID = Object.fromEntries(graphNodes.map((n) => [n.id, n.label]));

  function nodeColor(n) {
    const meta = n.metadata || n;
    // Soft project tint when viewing Todos; otherwise near-neutral dots.
    if (slug === "all" && meta.scope_id && scopeColors[meta.scope_id]) {
      return scopeColors[meta.scope_id];
    }
    const tier = meta.tier || 2;
    if (tier === 1) return "#b4aeb8";
    if (tier === 3) return "#9aa8b0";
    return "#9a9aa3";
  }

  function openMemory(node) {
    const sc = byID[node.metadata?.scope_id] || slug;
    history.pushState({}, "", `/memorias/${encodeURIComponent(node.id)}?project=${encodeURIComponent(sc)}`);
    route().catch(fail);
  }

  function kv(label, value) {
    if (value == null || value === "" || value === false) return "";
    return `<div class="nmap-k">${esc(label)}</div><div class="nmap-v">${esc(String(value))}</div>`;
  }
  function when(iso) {
    if (!iso) return "";
    return String(iso).replace("T", " ").slice(0, 16);
  }

  function showHUD(node) {
    const token = ++hudToken;
    if (!node) {
      hud.innerHTML = "";
      hud.classList.add("hidden");
      hud.setAttribute("aria-hidden", "true");
      return;
    }
    hud.classList.remove("hidden");
    hud.setAttribute("aria-hidden", "false");
    const meta = node.metadata || {};
    hud.innerHTML = `<div class="nmap-hud-head"><strong>${esc(meta.title || node.label || "Memória")}</strong><button class="text-btn" type="button" id="nmap-close">fechar</button></div>
      <p class="muted">Carregando ficha…</p>`;
    hud.querySelector("#nmap-close").onclick = () => showHUD(null);

    const sc = byID[meta.scope_id] || slug;
    const q = sc && sc !== "all" ? `?project=${encodeURIComponent(sc)}` : "";
    Promise.all([
      api("/api/memories/" + encodeURIComponent(node.id) + "/history" + q),
      api("/api/links?memory_ids=" + encodeURIComponent(node.id) + (q ? "&" + q.slice(1) : "")).catch(() => ({ links: [] })),
    ]).then(([hist, linkRes]) => {
      if (token !== hudToken) return;
      const m = hist.memory || meta;
      const tags = (m.tags || []).map((t) => `<span class="pill">#${esc(t)}</span>`).join(" ");
      const linkRows = (linkRes.links || []).map((l) => {
        const other = l.source_id === m.id ? l.target_id : l.source_id;
        const name = titleByID[other] || other.slice(0, 8);
        return `<li><code>${esc(l.label || "link")}</code> ${esc(name)}</li>`;
      }).join("") || `<li class="muted">Nenhuma ligação.</li>`;
      const versions = (hist.versions || []).slice(0, 6).map((v) => `<li>${esc(v.change_type)} · v${v.version} · ${esc(when(v.created_at))}</li>`).join("") || `<li class="muted">Sem histórico.</li>`;
      const lineage = (hist.lineage || []).filter((x) => x.id !== m.id).slice(0, 4).map((x) => `<li>${pill(x.status)} ${esc(x.title)}</li>`).join("");
      hud.innerHTML = `<div class="nmap-hud-head"><div class="row">${pill(m.status)} <span class="pill">T${m.tier}</span><span class="pill">${esc(m.kind)}</span></div>
        <button class="text-btn" type="button" id="nmap-close">fechar</button></div>
        <strong>${esc(m.title)}</strong>
        <p class="muted nmap-scope">${esc(meta.scopeLabel || sc)} · grau ${meta.degree || 0} · v${m.version || 1}</p>
        <p class="nmap-body">${esc(m.content || "")}</p>
        ${m.rationale ? `<p class="nmap-why"><span class="muted">Por quê</span> ${esc(m.rationale)}</p>` : ""}
        ${tags ? `<div class="nmap-tags">${tags}</div>` : ""}
        <div class="nmap-kv">
          ${kv("origem", m.source_type)}
          ${kv("agente", m.created_by_agent)}
          ${kv("modelo", m.created_by_model)}
          ${kv("cliente", m.created_by_client)}
          ${kv("confiança", m.confidence != null ? Number(m.confidence).toFixed(2) : "")}
          ${kv("acessos", m.access_count)}
          ${kv("criada", when(m.created_at))}
          ${kv("atualizada", when(m.updated_at))}
          ${kv("válida de", when(m.valid_from))}
          ${kv("válida até", when(m.valid_until))}
          ${kv("revisar", when(m.review_after))}
          ${kv("revisão", m.review_state)}
          ${m.pinned ? kv("fixada", "sim") : ""}
          ${m.user_confirmed_claim ? kv("confirmada", "sim") : ""}
          ${kv("substitui", m.supersedes_id ? (titleByID[m.supersedes_id] || m.supersedes_id.slice(0, 8)) : "")}
          ${kv("motivo", m.supersede_reason)}
        </div>
        <h3>Ligações</h3>
        <ul class="nmap-list">${linkRows}</ul>
        ${lineage ? `<h3>Linhagem</h3><ul class="nmap-list">${lineage}</ul>` : ""}
        <h3>Versões</h3>
        <ul class="nmap-list">${versions}</ul>
        <div class="actions nmap-hud-actions"><button class="btn" type="button" id="nmap-open">Abrir ficha completa</button></div>`;
      hud.querySelector("#nmap-close").onclick = () => showHUD(null);
      hud.querySelector("#nmap-open").onclick = (ev) => { ev.preventDefault(); openMemory(node); };
    }).catch((e) => {
      if (token !== hudToken) return;
      hud.innerHTML = `<p class="error">${esc(e.message)}</p><button class="text-btn" type="button" id="nmap-close">fechar</button>`;
      hud.querySelector("#nmap-close").onclick = () => showHUD(null);
    });
  }

  function mountView() {
    view?.destroy();
    const common = {
      data: { nodes: graphNodes, links: graphLinks },
      tooltipEl: tipEl,
      nodeSizeFn: (node) => {
        const deg = nodeWeights.get(node.id) || node.metadata?.degree || 0;
        return 1.55 + Math.min(2.6, Math.sqrt(deg) * 0.38);
      },
      onNodeClick: (node) => { showHUD(node); },
      onFrameStats: ({ zoom, pinned, frozen }) => {
        const stats = stage.querySelector("#nmap-stats");
        if (!stats) return;
        const pin = pinned != null ? ` · ${pinned} fixados` : "";
        const fr = frozen ? " · congelado" : " · organizando";
        stats.textContent = `${graphNodes.length} memórias · ${graphLinks.length} ligações · zoom ${Math.round(zoom * 100)}%${pin}${fr} · ${slug}`;
      },
    };
    if (mode === "stars") {
      view = ImpamindConstellation.mount(canvas, common);
    } else {
      view = ImpamindGraph.mount(canvas, {
        ...common,
        scopeKey: slug,
        nodeColorFn: nodeColor,
      });
    }
    wrap.querySelector('[data-act="mode-graph"]')?.classList.toggle("on", mode === "graph");
    wrap.querySelector('[data-act="mode-stars"]')?.classList.toggle("on", mode === "stars");
    const motionBtn = wrap.querySelector('[data-act="motion"]');
    if (motionBtn) {
      motionBtn.disabled = mode === "graph";
      motionBtn.classList.toggle("on", mode === "stars" && view.getMotion?.());
    }
  }

  mountView();
  showHUD(null);

  // Auto-densify once per session when the graph is too sparse for an Obsidian look.
  const densifyKey = "impamind-map-autodensify:" + slug;
  const edgeRatio = graphLinks.length / Math.max(graphNodes.length, 1);
  if (mode === "graph" && edgeRatio < 0.5 && !sessionStorage.getItem(densifyKey)) {
    sessionStorage.setItem(densifyKey, "1");
    const stats = stage.querySelector("#nmap-stats");
    if (stats) stats.textContent = `${graphNodes.length} memórias · gerando ligações… · ${slug}`;
    (async () => {
      try {
        await api("/api/graph/suggest-links?scope=" + encodeURIComponent(slug), { method: "POST", body: "{}" });
        view?.destroy();
        await neuralMap(slug);
      } catch (_) {
        if (stats) stats.textContent = `${graphNodes.length} memórias · ${graphLinks.length} ligações · ${slug}`;
      }
    })();
  }

  const toolbar = wrap.querySelector("#nmap-toolbar");
  toolbar.onclick = (ev) => {
    const btn = ev.target.closest("[data-act]");
    if (!btn || !view) return;
    switch (btn.dataset.act) {
      case "mode-graph":
        mode = "graph";
        localStorage.setItem("impamind-map-mode", mode);
        mountView();
        break;
      case "mode-stars":
        mode = "stars";
        localStorage.setItem("impamind-map-mode", mode);
        mountView();
        break;
      case "reset":
        if (mode === "graph" && view.resetLayout) {
          if (confirm("Voltar ao layout automático? Isso apaga as posições que você fixou arrastando.")) view.resetLayout();
        } else if (view.fitView) view.fitView();
        break;
      case "zoom-in": view.zoomBy(1.12); break;
      case "zoom-out": view.zoomBy(1 / 1.12); break;
      case "fit": view.fitView(); break;
      case "motion": {
        if (!view.getMotion) break;
        const on = !view.getMotion();
        view.setMotion(on);
        btn.classList.toggle("on", on);
        break;
      }
      case "png": view.exportPNG(`impamind-constellation-${slug}.png`); break;
      case "full": {
        (async () => {
          try {
            if (document.fullscreenElement === wrap) await document.exitFullscreen();
            else if (wrap.requestFullscreen) await wrap.requestFullscreen();
          } catch (_) { /* ignore */ }
        })();
        break;
      }
      case "suggest": {
        (async () => {
          btn.disabled = true;
          btn.textContent = "…";
          try {
            const out = await api("/api/graph/suggest-links?scope=" + encodeURIComponent(slug), { method: "POST", body: "{}" });
            view.destroy();
            await neuralMap(slug);
            if (out?.created != null) alert(`Pronto: ${out.created} ligação(ões) similar(es) criadas (${out.scanned || "?"} memórias analisadas).`);
          } catch (e) {
            btn.textContent = "Ligações";
            btn.disabled = false;
            alert(e.message);
          }
        })();
        break;
      }
    }
  };

  const stop = () => { view?.destroy(); };
  const obs = new MutationObserver(() => { if (!document.body.contains(canvas)) { stop(); obs.disconnect(); } });
  obs.observe(document.body, { childList: true, subtree: true });
}

function newProject() {
  stage.innerHTML = `<header class="top"><div><h1>Novo projeto</h1><p>Um projeto isola decisões, notas e sessões.</p></div></header>
    <form class="sheet" id="np"><label class="field">Slug<input name="slug" required pattern="[a-z0-9][a-z0-9._-]{0,62}"></label>
    <label class="field">Nome<input name="name"></label>
    <label class="field">Política<select name="review_policy"><option>balanced</option><option>strict</option><option>open</option></select></label>
    <button class="btn" type="submit">Criar</button><p class="error" id="err"></p></form>`;
  stage.querySelector("#np").onsubmit = async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    try {
      const sc = await api("/api/projects", { method: "POST", body: JSON.stringify({ slug: f.get("slug"), name: f.get("name"), review_policy: f.get("review_policy") }) });
      location.href = "/memorias?scope=" + encodeURIComponent(sc.slug);
    } catch (e) { stage.querySelector("#err").textContent = e.message; }
  };
}

async function review() {
  const data = await api("/api/review?all_projects=true");
  stage.innerHTML = `<header class="top"><div><h1>Revisão</h1><p>Propostas, conflitos e itens que pedem um humano.</p></div></header>` +
    ((data.items || []).map((it) => `<article class="sheet"><div class="row">${pill(it.type)} <span class="muted">${esc(it.scope)}</span></div><ul>${(it.memories || []).map((m) => `<li><a href="/memorias/${esc(m.id)}?project=${esc(it.scope)}">${esc(m.title)}</a></li>`).join("")}</ul><div class="actions"><button class="btn" data-id="${esc(it.id)}" data-action="approve" type="button">Aprovar</button><button class="btn quiet" data-id="${esc(it.id)}" data-action="reject" type="button">Rejeitar</button><button class="btn quiet" data-id="${esc(it.id)}" data-action="dismiss" type="button">Dispensar</button></div></article>`).join("") || `<p class="empty">Caixa vazia.</p>`);
  stage.querySelectorAll("button[data-id]").forEach((btn) => btn.onclick = async () => {
    await api("/api/review/" + btn.dataset.id + "/resolve", { method: "POST", body: JSON.stringify({ action: btn.dataset.action, note: "resolvido na interface" }) });
    route();
  });
}

async function tokens() {
  const data = await api("/api/tokens");
  stage.innerHTML = `<header class="top"><div><h1>Tokens</h1><p>Cada cliente entra com o seu. O segredo aparece uma vez.</p></div></header>` +
    data.tokens.map((t) => `<article class="sheet"><strong>${esc(t.name)}</strong> <span class="muted">${esc(t.class)}${t.revoked_at ? " · revogado" : ""}</span><p class="muted">${(t.grants || []).map((g) => esc(g.scope || g.scope_id)).join(", ")}</p>${t.revoked_at ? "" : `<button class="btn danger" data-revoke="${esc(t.id)}" type="button">Revogar</button>`}</article>`).join("") +
    `<form class="sheet" id="nt"><h2>Novo token</h2><label class="field">Nome<input name="name" required></label><label class="field">Classe<select name="class"><option>agent</option><option>human</option></select></label><label class="field">Projeto (slug ou *)<input name="scope" value="*"></label><button class="btn" type="submit">Criar</button><div id="once"></div></form>`;
  stage.querySelector("#nt").onsubmit = async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const scope = f.get("scope");
    const scopes = await api("/api/scopes");
    const match = scopes.scopes.find((s) => s.slug === scope);
    const created = await api("/api/tokens", { method: "POST", body: JSON.stringify({ name: f.get("name"), class: f.get("class"), client_type: "ui", grants: [{ scope_id: scope === "*" ? "*" : match?.id, can_read: true, can_write: true, can_propose: true, can_approve: f.get("class") === "human" }] }) });
    stage.querySelector("#once").innerHTML = `<p>Copie agora.</p><pre>${esc(created.token)}</pre>`;
  };
  stage.querySelectorAll("[data-revoke]").forEach((btn) => btn.onclick = async () => { await api("/api/tokens/" + btn.dataset.revoke, { method: "DELETE" }); route(); });
}

async function health() {
  const [status, proto] = await Promise.all([api("/api/status"), api("/api/health/protocol?all_projects=true")]);
  const rows = (proto.health || []).map((h) => `<tr><td>${esc(h.client)}</td><td>${esc(h.agent)}</td><td>${h.sessions}</td><td>${Math.round((h.recall_rate || 0) * 100)}%</td><td>${h.violations}</td></tr>`).join("");
  stage.innerHTML = `<header class="top"><div><h1>Saúde</h1><p>Versão ${esc(status.status.version)} · SQLite ${esc(status.status.sqlite.sqlite_version)} · no ar há ${esc(status.status.uptime)}</p></div></header>
    <article class="sheet"><table><tr><th>Cliente</th><th>Agente</th><th>Sessões</th><th>Recall</th><th>Violações</th></tr>${rows || `<tr><td colspan="5" class="muted">Ainda sem sessões.</td></tr>`}</table></article>`;
}

async function sessions(scope) {
  const slug = scope || "global";
  if (slug === "global") {
    stage.innerHTML = `<header class="top"><div><h1>Sessões</h1><p>Escolha um projeto na sidebar — sessões são por projeto.</p></div></header>`;
    return;
  }
  const data = await api(`/api/sessions?project=${encodeURIComponent(slug)}&limit=100`);
  const rows = (data.sessions || []).map((s) => `<tr>
    <td><a href="/sessoes/${esc(s.id)}?scope=${esc(slug)}">${esc(s.title || s.task || s.id.slice(0, 8))}</a></td>
    <td>${pill(s.status)}</td>
    <td class="muted">${esc(s.client || "—")} · ${esc(s.agent || "—")}</td>
    <td class="muted">${esc(s.last_activity_at || s.started_at)}</td>
    <td class="muted">${s.checkpoints || 0} cp · ${s.writes || 0} writes</td>
  </tr>`).join("");
  stage.innerHTML = `<header class="top"><div><h1>Sessões</h1><p>Trabalho dos agentes em ${esc(slug)}</p></div></header>
    <article class="sheet"><table><tr><th>Sessão</th><th>Status</th><th>Cliente</th><th>Atividade</th><th>Contagens</th></tr>
    ${rows || `<tr><td colspan="5" class="muted">Nenhuma sessão ainda.</td></tr>`}</table></article>`;
}

async function sessionDetail(id, scope) {
  const slug = scope || "global";
  const q = slug && slug !== "global" ? `?project=${encodeURIComponent(slug)}` : "";
  const data = await api("/api/sessions/" + encodeURIComponent(id) + q);
  const s = data.session;
  const cp = data.checkpoint;
  stage.innerHTML = `<header class="top"><div><a class="muted" href="/sessoes?scope=${esc(slug)}">← sessões</a>
    <h1>${esc(s.title || s.task || s.id)}</h1>
    <p>${pill(s.status)} ${esc(s.client || "")} · ${esc(s.agent || "")} · ${esc(s.model || "")}</p></div></header>
    <article class="sheet">
      <p><strong>Tarefa</strong> ${esc(s.task || "—")}</p>
      <p class="muted">Início ${esc(s.started_at)} · última ${esc(s.last_activity_at)}${s.closed_at ? " · fechada " + esc(s.closed_at) : ""}</p>
      <p class="muted">recall ${s.recalled ? "sim" : "não"} · obs ${s.observations} · writes ${s.writes} · cp ${s.checkpoints} · violações ${s.violations}</p>
      ${(s.files || []).length ? `<p>Arquivos: ${(s.files || []).map((f) => `<code>${esc(f)}</code>`).join(" ")}</p>` : ""}
    </article>
    <article class="sheet"><h2>Checkpoint</h2>${cp ? `<pre>${esc(JSON.stringify(cp, null, 2))}</pre>` : `<p class="muted">Sem checkpoint.</p>`}</article>`;
}

async function projectPage(scope) {
  const slug = scope || "global";
  if (slug === "global") {
    stage.innerHTML = `<header class="top"><div><h1>Projeto</h1><p>O escopo global não tem aliases nem exclusão.</p></div>
      <a class="btn" href="/projetos">Novo projeto</a></header>`;
    return;
  }
  const [sc, al] = await Promise.all([
    api("/api/projects/" + encodeURIComponent(slug)),
    api("/api/projects/" + encodeURIComponent(slug) + "/aliases"),
  ]);
  const aliases = (al.aliases || []).map((a) => `<li><code>${esc(a.kind)}</code> ${esc(a.alias)}
    <button class="text-btn" type="button" data-alias="${esc(a.alias)}">remover</button></li>`).join("") || `<li class="muted">Nenhum alias.</li>`;
  stage.innerHTML = `<header class="top"><div><h1>${esc(sc.name || slug)}</h1><p>slug <code>${esc(slug)}</code> · política ${esc(sc.review_policy || "—")}</p></div>
    <div class="actions"><a class="btn quiet" href="/memorias?scope=${esc(slug)}">Memórias</a>
    <button class="btn danger" id="del-proj" type="button">Apagar projeto</button></div></header>
    <form class="sheet" id="proj-form">
      <label class="field">Nome<input name="name" value="${esc(sc.name || "")}" required></label>
      <label class="field">Descrição<textarea name="description">${esc(sc.description || "")}</textarea></label>
      <label class="field">Política<select name="review_policy">
        ${["balanced", "strict", "open"].map((p) => `<option ${sc.review_policy === p ? "selected" : ""}>${p}</option>`).join("")}
      </select></label>
      <button class="btn" type="submit">Salvar</button>
      <p class="error" id="err"></p>
    </form>
    <section class="sheet"><h2>Aliases</h2><ul id="aliases">${aliases}</ul>
      <form id="alias-form" class="row" style="margin-top:0.6rem">
        <label class="field" style="width:7rem;margin:0"><span class="muted">Tipo</span><select name="kind"><option>path</option><option>repo</option><option>name</option></select></label>
        <label class="field" style="flex:1;margin:0"><span class="muted">Valor</span><input name="value" required placeholder="caminho ou nome"></label>
        <button class="btn" type="submit">Adicionar</button>
      </form>
      <p class="error" id="alias-err"></p>
    </section>`;
  stage.querySelector("#proj-form").onsubmit = async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    try {
      await api("/api/projects/" + encodeURIComponent(slug), { method: "PATCH", body: JSON.stringify({
        name: f.get("name"), description: f.get("description"), review_policy: f.get("review_policy"),
      }) });
      route();
    } catch (e) { stage.querySelector("#err").textContent = e.message; }
  };
  stage.querySelector("#alias-form").onsubmit = async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    try {
      await api("/api/projects/" + encodeURIComponent(slug) + "/aliases", { method: "POST", body: JSON.stringify({ kind: f.get("kind"), value: f.get("value") }) });
      route();
    } catch (e) { stage.querySelector("#alias-err").textContent = e.message; }
  };
  stage.querySelectorAll("[data-alias]").forEach((btn) => btn.onclick = async () => {
    await api("/api/projects/" + encodeURIComponent(slug) + "/aliases/" + encodeURIComponent(btn.dataset.alias), { method: "DELETE", body: "{}" });
    route();
  });
  stage.querySelector("#del-proj").onclick = async () => {
    if (!confirm("Apagar o projeto " + slug + "? Há backup automático.")) return;
    try {
      const out = await api("/api/projects/" + encodeURIComponent(slug), { method: "DELETE", body: "{}" });
      stage.innerHTML = `<p class="sheet">Projeto apagado. Backup: <code>${esc(out.backup || "—")}</code></p>`;
    } catch (e) { stage.querySelector("#err").textContent = e.message; }
  };
}

async function adminPage(scope) {
  const slug = scope || "global";
  const logs = await api("/api/logs?limit=80").catch(() => ({ logs: [] }));
  const logRows = (logs.logs || []).map((l) => `<tr>
    <td class="muted">${esc(l.ts || "")}</td>
    <td>${esc(l.level || "")}</td>
    <td>${esc(l.source || "")}</td>
    <td>${esc(l.operation || "")}</td>
    <td>${esc(l.detail || "")}</td>
  </tr>`).join("");
  const syncOk = slug && slug !== "global";
  stage.innerHTML = `<header class="top"><div><h1>Admin</h1><p>Backup, clone .impamind, sync e logs · escopo ${esc(slug)}</p></div>
    <div class="actions"><button class="btn" id="backup" type="button">Backup agora</button></div></header>
    <section class="sheet"><h2>Clone .impamind</h2>
      <p class="muted">Arquivo portátil com projetos, memórias, links e cofre (cifrado com passphrase). Use para migrar entre local e VPS.</p>
      <div class="row" style="gap:0.5rem;flex-wrap:wrap;align-items:end">
        <label>Passphrase<br><input id="bundle-pass" type="password" autocomplete="new-password" minlength="8" placeholder="mín. 8 caracteres"></label>
        <label>Confirmar<br><input id="bundle-pass2" type="password" autocomplete="new-password" minlength="8"></label>
        <label class="check"><input id="bundle-global" type="checkbox" checked> Incluir global</label>
        <label class="check"><input id="bundle-all" type="checkbox" ${syncOk ? "" : "checked"}> Toda a instância</label>
        <label class="check"><input id="bundle-overwrite" type="checkbox" checked> Sobrescrever vault no import</label>
        <label class="check"><input id="bundle-skip-links" type="checkbox" checked> Pular links no import (mais rápido / evita timeout)</label>
      </div>
      <p class="muted" style="margin-top:0.5rem">A passphrase do <strong>export</strong> tem que ser a mesma no <strong>import</strong>. Confirmação só no export.</p>
      <div class="actions" style="margin-top:0.75rem">
        <button class="btn" id="bundle-export" type="button">Exportar .impamind</button>
        <label class="btn quiet">Importar .impamind<input id="bundle-import" type="file" accept=".impamind,application/zip" hidden></label>
      </div>
      <p class="muted" id="bundle-msg"></p>
      <pre id="bundle-out" hidden></pre>
    </section>
    <section class="sheet"><h2>Sync JSON (leve)</h2>
      <div class="actions">
        <button class="btn quiet" id="export" type="button" ${syncOk ? "" : "disabled"}>Exportar JSON</button>
        <label class="btn quiet">Importar JSON<input id="import" type="file" accept="application/json,.json" hidden ${syncOk ? "" : "disabled"}></label>
      </div>
      <p class="muted" id="sync-msg">${syncOk ? "Só memórias ativas do projeto (sem cofre)." : "Abra Admin a partir de um projeto para sync JSON."}</p>
      <pre id="sync-out" hidden></pre>
    </section>
    <section class="sheet"><h2>Logs recentes</h2>
      <table><tr><th>Quando</th><th>Nível</th><th>Fonte</th><th>Op</th><th>Detalhe</th></tr>
      ${logRows || `<tr><td colspan="5" class="muted">Sem logs.</td></tr>`}</table>
    </section>
    <p class="error" id="err"></p>`;
  stage.querySelector("#backup").onclick = async () => {
    try {
      const out = await api("/api/backup", { method: "POST", body: "{}" });
      stage.querySelector("#sync-msg").textContent = "Backup em " + out.backup;
    } catch (e) { stage.querySelector("#err").textContent = e.message; }
  };
  const bundlePassExport = () => {
    const a = stage.querySelector("#bundle-pass").value || "";
    const b = stage.querySelector("#bundle-pass2").value || "";
    if (a.length < 8) throw new Error("Passphrase com pelo menos 8 caracteres");
    if (a !== b) throw new Error("Passphrases não conferem — preencha os dois campos iguais");
    return a;
  };
  const bundlePassImport = () => {
    const a = stage.querySelector("#bundle-pass").value || "";
    if (a.length < 8) throw new Error("Passphrase do export (mín. 8 caracteres) no primeiro campo");
    return a;
  };
  const errMsg = (out, fallback) =>
    out?.error?.message || out?.error?.msg || out?.message || out?.msg || fallback;
  stage.querySelector("#bundle-export").onclick = async () => {
    try {
      stage.querySelector("#err").textContent = "";
      stage.querySelector("#bundle-msg").textContent = "Exportando…";
      const passphrase = bundlePassExport();
      const allInst = stage.querySelector("#bundle-all").checked || !syncOk;
      const body = JSON.stringify({
        passphrase,
        include_global: stage.querySelector("#bundle-global").checked,
        project: allInst ? "" : slug,
      });
      const res = await fetch("/api/bundle/export", {
        method: "POST", credentials: "same-origin",
        headers: { "Content-Type": "application/json", Accept: "application/zip" },
        body,
      });
      if (!res.ok) {
        const t = await res.text();
        let out = {};
        try { out = JSON.parse(t); } catch (_) {}
        throw new Error(errMsg(out, t || res.statusText));
      }
      const blob = await res.blob();
      const cd = res.headers.get("Content-Disposition") || "";
      const m = /filename="([^"]+)"/.exec(cd);
      const fname = m?.[1] || "impamind-bundle.impamind";
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = fname;
      a.click();
      URL.revokeObjectURL(a.href);
      stage.querySelector("#bundle-msg").textContent = "Exportado: " + fname + " — guarde a passphrase; sem ela o import falha.";
    } catch (e) { stage.querySelector("#err").textContent = e.message; stage.querySelector("#bundle-msg").textContent = ""; }
  };
  stage.querySelector("#bundle-import").onchange = async (ev) => {
    const file = ev.target.files?.[0];
    if (!file) return;
    try {
      stage.querySelector("#err").textContent = "";
      stage.querySelector("#bundle-msg").textContent = "Importando " + file.name + "… (pode levar 1–2 min)";
      const passphrase = bundlePassImport();
      const fd = new FormData();
      fd.append("file", file);
      fd.append("passphrase", passphrase);
      if (stage.querySelector("#bundle-overwrite").checked) fd.append("overwrite_vault", "1");
      if (stage.querySelector("#bundle-skip-links").checked) fd.append("skip_links", "1");
      const res = await fetch("/api/bundle/import", { method: "POST", credentials: "same-origin", body: fd });
      const out = await res.json().catch(() => ({}));
      if (!res.ok) {
        const hint = (out.error?.message || "").includes("passphrase")
          ? " — use a mesma passphrase do export (não a senha de login)."
          : "";
        throw new Error(errMsg(out, res.statusText) + hint);
      }
      const pre = stage.querySelector("#bundle-out");
      pre.hidden = false;
      pre.textContent = JSON.stringify(out, null, 2);
      const errs = (out.errors || []).length;
      stage.querySelector("#bundle-msg").textContent =
        `Import: +${out.memories_created || 0} memórias (skip ${out.memories_skipped || 0}), +${out.projects_created || 0} projetos, vault +${(out.vault_created || 0) + (out.vault_updated || 0)}` +
        (errs ? `, ${errs} avisos` : "");
    } catch (e) { stage.querySelector("#err").textContent = e.message; stage.querySelector("#bundle-msg").textContent = ""; }
    ev.target.value = "";
  };
  stage.querySelector("#export").onclick = async () => {
    if (!syncOk) return;
    try {
      const out = await api(`/api/sync/export?project=${encodeURIComponent(slug)}&include_global=true`);
      const payload = JSON.stringify(out, null, 2);
      const pre = stage.querySelector("#sync-out");
      pre.hidden = false;
      pre.textContent = payload;
      const blob = new Blob([payload], { type: "application/json" });
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = `impamind-${slug}.json`;
      a.click();
      URL.revokeObjectURL(a.href);
    } catch (e) { stage.querySelector("#err").textContent = e.message; }
  };
  stage.querySelector("#import").onchange = async (ev) => {
    if (!syncOk) return;
    const file = ev.target.files?.[0];
    if (!file) return;
    try {
      const parsed = JSON.parse(await file.text());
      const mems = Array.isArray(parsed) ? parsed : (parsed.memories || parsed.items || []);
      const list = mems.map((m, i) => ({
        id: m.id, kind: m.kind || "note", title: m.title, content: m.content,
        tags: m.tags || [], tier: m.tier || 2,
        file: m.file || `import-${i}.md`, hash: m.hash || ("ui-" + i),
      }));
      const out = await api("/api/sync/import", { method: "POST", body: JSON.stringify({ project: slug, items: list }) });
      stage.querySelector("#sync-msg").textContent = `Import: ${(out.results || []).length} itens`;
    } catch (e) { stage.querySelector("#err").textContent = e.message; }
  };
}

function connect() {
  const origin = location.origin;
  const snippet = `{
  "mcpServers": {
    "impamind": {
      "url": "${origin}/mcp",
      "headers": {
        "Authorization": "Bearer \${env:IMPAMIND_TOKEN}",
        "X-Impamind-Project": "seu-projeto"
      }
    }
  }
}`;
  stage.innerHTML = `<header class="top"><div><h1>Conectar</h1><p>Qualquer cliente MCP entra com um Bearer e o projeto.</p></div></header>
    <article class="sheet"><p>No Cursor, grave isto em <code>.cursor/mcp.json</code>. O token fica em <code>IMPAMIND_TOKEN</code>.</p><pre>${esc(snippet)}</pre>
      <p class="muted">A mesma URL <code>${esc(origin)}/mcp</code> serve Claude, Codex e scripts. OAuth usa <code>${esc(origin)}/.well-known/oauth-protected-resource</code>. O perfil administrativo é <code>/mcp/admin</code>.</p></article>
    <article class="sheet"><h2>Cliente local</h2>
      <p>Hooks e sync markdown. Baixe o binário da sua plataforma:</p>
      <div class="actions">
        <a class="btn" href="/downloads/impamind-windows-amd64.exe" download>Windows amd64</a>
        <a class="btn quiet" href="/downloads/impamind-linux-amd64" download>Linux amd64</a>
      </div>
      <pre class="muted" style="margin-top:0.8rem">IMPAMIND_URL=${esc(origin)}
IMPAMIND_TOKEN=seu-token
IMPAMIND_PROJECT=seu-projeto
impamind hook   # stdin: payload do host
impamind sync pull ./memories
impamind sync push ./memories</pre>
    </article>`;
}

window.addEventListener("popstate", () => route().catch(fail));
document.addEventListener("click", (ev) => {
  const a = ev.target.closest("a");
  if (!a || a.origin !== location.origin || a.target || a.hasAttribute("download") || a.pathname.startsWith("/downloads/")) return;
  ev.preventDefault();
  history.pushState({}, "", a.pathname + a.search);
  route().catch(fail);
});
route().catch((e) => { gate.innerHTML = `<p class="error">${esc(e.message)}</p>`; });
