from pathlib import Path

p = Path(__file__).resolve().parents[1] / "internal" / "web" / "static" / "app.js"
lines = p.read_text(encoding="utf-8").splitlines(keepends=True)
head = lines[:369]

new = r'''async function neuralMap(scope) {
  const slug = !scope || scope === "both" || scope === "todos" ? "all" : scope;
  const data = await api("/api/graph" + graphQuery(slug));
  const scopes = await api("/api/scopes");
  const byID = Object.fromEntries((scopes.scopes || []).map((s) => [s.id, s.slug]));
  const palette = ["#0074d9", "#009296", "#f59e0b", "#8b5cf6", "#e07a72", "#7eb8b2", "#9bb4e0", "#c9a0d4"];
  const present = (scopes.scopes || []).filter((s) => (data.nodes || []).some((n) => n.scope_id === s.id));
  const scopeColors = Object.fromEntries(present.map((s, i) => [s.id, palette[i % palette.length]]));
  const scopeNames = Object.fromEntries(present.map((s) => [s.id, s.name]));
  const edgeCounts = { link: 0, similar: 0, supersedes: 0 };
  for (const e of data.edges || []) {
    if ((e.label || "").startsWith("topic:")) continue;
    edgeCounts[e.type] = (edgeCounts[e.type] || 0) + 1;
  }
  const scopeOpts = (scopes.scopes || []).map((s) => `<option value="${esc(s.slug)}" ${s.slug === slug ? "selected" : ""}>${esc(s.name)}</option>`).join("");
  stage.innerHTML = `<header class="top"><div><h1>Constelação</h1><p id="nmap-stats">${data.nodes.length} memórias · ${data.edges.length} ligações · ${esc(slug)}</p><p class="muted" style="font-size:12px;margin:0.25rem 0 0">Visual no estilo <a href="https://github.com/vectorize-io/hindsight" target="_blank" rel="noopener">Hindsight Constellation</a></p></div>
    <div class="actions"><label class="field inline">Escopo<select id="nmap-scope"><option value="all" ${slug === "all" ? "selected" : ""}>Todos</option>${scopeOpts}</select></label></div></header>
    <div class="nmap-wrap constellation-wrap" id="nmap-wrap">
      <div class="nmap-toolbar" id="nmap-toolbar">
        <button class="btn" type="button" data-act="zoom-out" title="Afastar">−</button>
        <button class="btn" type="button" data-act="zoom-in" title="Aproximar">+</button>
        <button class="btn" type="button" data-act="fit" title="Enquadrar">Fit</button>
        <button class="btn on" type="button" data-act="motion" title="Movimento ambiente">Motion</button>
        <button class="btn" type="button" data-act="suggest" title="Gerar ligações similares">Ligações</button>
        <button class="btn" type="button" data-act="png" title="Exportar PNG">PNG</button>
        <button class="btn" type="button" data-act="full" title="Tela cheia">Full</button>
      </div>
      <canvas id="nmap" aria-label="Constelação de memórias"></canvas>
      <div class="nmap-tip" id="nmap-tip" role="tooltip"></div>
      <aside class="nmap-hud" id="nmap-hud"><p class="muted">Passe o mouse num ponto ou clique para abrir a memória.</p></aside>
    </div>`;
  stage.querySelector("#nmap-scope").onchange = (ev) => {
    history.pushState({}, "", "/mapa?scope=" + encodeURIComponent(ev.target.value));
    route().catch(fail);
  };
  runNeuralMap(stage.querySelector("#nmap"), stage.querySelector("#nmap-hud"), stage.querySelector("#nmap-wrap"), stage.querySelector("#nmap-tip"), data, byID, slug, scopeColors, scopeNames, edgeCounts);
}

function runNeuralMap(canvas, hud, wrap, tipEl, data, byID, slug, scopeColors, scopeNames, edgeCounts) {
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
      group: slug === "all" ? n.scope_id : null,
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

  const cluster = slug === "all";
  let view = null;

  function openMemory(node) {
    const sc = byID[node.metadata?.scope_id] || slug;
    history.pushState({}, "", `/memorias/${encodeURIComponent(node.id)}?project=${encodeURIComponent(sc)}`);
    route().catch(fail);
  }

  function showHUD(node) {
    if (!node) {
      hud.innerHTML = `<p class="muted nmap-edge-stats">similar ${edgeCounts.similar || 0} · link ${edgeCounts.link || 0} · supersedes ${edgeCounts.supersedes || 0}</p>
        <p class="muted" style="margin:0.2rem 0 0;font-size:12px">Modelo Hindsight: hover destaca vizinhos; ligações aparecem vivas no hover.</p>`;
      return;
    }
    const m = node.metadata || {};
    hud.innerHTML = `<div class="row">${pill(m.status)} <span class="pill">T${m.tier}</span><span>${esc(m.kind)}</span></div>
      <strong>${esc(m.title || node.label)}</strong>
      <p class="muted">${esc(m.scopeLabel || slug)} · grau ${m.degree || 0}</p>
      <div class="actions nmap-hud-actions"><button class="btn" type="button" id="nmap-open">Abrir</button></div>`;
    hud.querySelector("#nmap-open").onclick = (ev) => { ev.preventDefault(); openMemory(node); };
  }

  view = ImpamindConstellation.mount(canvas, {
    data: { nodes: graphNodes, links: graphLinks },
    tooltipEl: tipEl,
    clusterKeyFn: cluster ? (n) => n.group || null : undefined,
    clusterColorFn: cluster ? (key) => scopeColors[key] || "#0074d9" : undefined,
    clusterLabelFn: cluster ? (key) => scopeNames[key] || key : undefined,
    nodeSizeFn: (node) => 4 + Math.sqrt((nodeWeights.get(node.id) || 0) / maxWeight) * 9,
    onNodeClick: (node) => { showHUD(node); openMemory(node); },
    onFrameStats: ({ zoom }) => {
      const stats = stage.querySelector("#nmap-stats");
      if (stats) stats.textContent = `${graphNodes.length} memórias · ${graphLinks.length} ligações · zoom ${Math.round(zoom * 100)}% · ${slug}`;
    },
  });

  showHUD(null);
  const toolbar = wrap.querySelector("#nmap-toolbar");
  toolbar.onclick = (ev) => {
    const btn = ev.target.closest("[data-act]");
    if (!btn || !view) return;
    switch (btn.dataset.act) {
      case "zoom-in": view.zoomBy(1.12); break;
      case "zoom-out": view.zoomBy(1 / 1.12); break;
      case "fit": view.fitView(); break;
      case "motion": {
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
            await api("/api/graph/suggest-links?project=" + encodeURIComponent(slug) + "&scope=" + encodeURIComponent(slug), { method: "POST", body: "{}" });
            view.destroy();
            await neuralMap(slug === "all" ? "all" : slug);
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

'''

tail = lines[1160:]
p.write_text("".join(head) + new + "".join(tail), encoding="utf-8")
print("patched", len(head), "+", len(new.splitlines()), "+", len(tail))
