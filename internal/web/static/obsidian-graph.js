/**
 * Force-directed graph (Obsidian-style): small dots, faint edges, airy layout.
 * Settles once then freezes. Drag moves only that node; Padrão re-runs physics.
 */
(function (global) {
  "use strict";

  // v3: discard hairball layouts from the large-node / tight-force era.
  const LAYOUT_PREFIX = "impamind-graph-layout:v5:";
  const BG = "#1e1e1e";
  const NODE_DEFAULT = "#9a9aa3";
  const EDGE_HOT = "rgba(210,212,220,0.78)";

  /** Cap stacked opacity so thousands of edges don't become a white blotch. */
  function edgeIdleStyle(linkCount) {
    // Dense graphs stay faint; sparse graphs get a bit more ink (Obsidian-like).
    const a = Math.max(0.045, Math.min(0.16, 110 / Math.max(linkCount, 1)));
    return `rgba(140,142,150,${a.toFixed(3)})`;
  }
  function edgeDimStyle(linkCount) {
    const a = Math.max(0.018, Math.min(0.06, 40 / Math.max(linkCount, 1)));
    return `rgba(90,92,100,${a.toFixed(3)})`;
  }

  function hash01(s) {
    let h = 2166136261;
    for (let i = 0; i < s.length; i++) {
      h ^= s.charCodeAt(i);
      h = Math.imul(h, 16777619);
    }
    return (h >>> 0) / 4294967296;
  }

  function loadLayout(scopeKey) {
    try {
      const raw = localStorage.getItem(LAYOUT_PREFIX + scopeKey);
      if (!raw) return null;
      return JSON.parse(raw);
    } catch (_) {
      return null;
    }
  }

  function saveLayout(scopeKey, nodes) {
    const positions = {};
    for (const n of nodes) {
      positions[n.id] = { x: n.x, y: n.y, pinned: !!n.pinned };
    }
    localStorage.setItem(
      LAYOUT_PREFIX + scopeKey,
      JSON.stringify({ positions, frozen: true, at: Date.now() })
    );
  }

  function clearLayout(scopeKey) {
    localStorage.removeItem(LAYOUT_PREFIX + scopeKey);
    // Drop legacy hairballs too.
    try {
      localStorage.removeItem("impamind-graph-layout:v1:" + scopeKey);
      localStorage.removeItem("impamind-graph-layout:v2:" + scopeKey);
      localStorage.removeItem("impamind-graph-layout:v3:" + scopeKey);
      localStorage.removeItem("impamind-graph-layout:v4:" + scopeKey);
    } catch (_) { /* ignore */ }
  }

  /** Obsidian-like radius: tiny dots; hubs grow slowly and hard-cap. */
  function nodeRadius(deg, zoom) {
    const d = Math.max(0, deg || 0);
    const base = 1.55 + Math.min(2.6, Math.sqrt(d) * 0.38);
    const z = Math.max(0.55, Math.min(1.35, zoom || 1));
    return base * z;
  }

  function mount(canvas, options) {
    const scopeKey = options.scopeKey || "default";
    const tip = options.tooltipEl;
    const onNodeClick = options.onNodeClick;
    const onFrameStats = options.onFrameStats;

    const simNodes = [];
    const idx = new Map();
    for (const n of options.data.nodes || []) {
      const scopeID = n.metadata?.scope_id || n.scope_id || "";
      const sn = {
        id: n.id,
        label: n.label || n.id.slice(0, 12),
        meta: n,
        scopeID,
        x: (hash01(n.id) - 0.5) * 80,
        y: (hash01(n.id + "y") - 0.5) * 80,
        vx: 0,
        vy: 0,
        pinned: false,
        color: options.nodeColorFn?.(n) || NODE_DEFAULT,
        r: 2,
      };
      simNodes.push(sn);
      idx.set(n.id, sn);
    }

    // Deduplicate undirected edges — keep first type.
    const links = [];
    const seenPair = new Set();
    for (const l of options.data.links || []) {
      const a = idx.get(l.source);
      const b = idx.get(l.target);
      if (!a || !b || a === b) continue;
      const ka = a.id < b.id ? a.id : b.id;
      const kb = a.id < b.id ? b.id : a.id;
      const key = ka + "\0" + kb;
      if (seenPair.has(key)) continue;
      seenPair.add(key);
      links.push({ a, b, type: l.type || "similar" });
    }

    const adj = new Map();
    for (const sn of simNodes) adj.set(sn.id, []);
    for (const e of links) {
      adj.get(e.a.id).push(e);
      adj.get(e.b.id).push(e);
    }
    const degree = new Map(simNodes.map((n) => [n.id, 0]));
    for (const e of links) {
      degree.set(e.a.id, (degree.get(e.a.id) || 0) + 1);
      degree.set(e.b.id, (degree.get(e.b.id) || 0) + 1);
    }
    for (const sn of simNodes) sn.degree = degree.get(sn.id) || 0;

    // Soft ghost edges for true orphans (visual only).
    const ghosts = [];
    function rebuildGhosts() {
      ghosts.length = 0;
      const orphans = simNodes.filter((n) => (n.degree || 0) === 0);
      if (!orphans.length || orphans.length > 80) return;
      for (const a of orphans) {
        let best = null;
        let bestD = 1e15;
        for (const b of simNodes) {
          if (b === a || (b.degree || 0) === 0) continue;
          const d2 = (a.x - b.x) * (a.x - b.x) + (a.y - b.y) * (a.y - b.y);
          if (d2 < bestD) {
            bestD = d2;
            best = b;
          }
        }
        if (best) ghosts.push({ a, b: best });
      }
    }

    // Community seeds: one angular sector per scope (or single cloud).
    function seedPositions() {
      const byScope = new Map();
      for (const sn of simNodes) {
        const k = sn.scopeID || "_";
        if (!byScope.has(k)) byScope.set(k, []);
        byScope.get(k).push(sn);
      }
      const scopes = [...byScope.keys()];
      const nScopes = Math.max(1, scopes.length);
      const spread = 220 + Math.sqrt(Math.max(simNodes.length, 1)) * 28;
      const phi = 2.399963229728653;

      scopes.forEach((key, si) => {
        const group = byScope.get(key);
        const sector = (si / nScopes) * Math.PI * 2 + 0.35;
        const cx = Math.cos(sector) * (nScopes > 1 ? spread * 0.55 : 0);
        const cy = Math.sin(sector) * (nScopes > 1 ? spread * 0.55 : 0) * 0.92;
        group.forEach((sn, i) => {
          const deg = sn.degree || 0;
          sn.pinned = false;
          sn.vx = sn.vy = 0;
          const ang = i * phi + sector;
          const rad =
            24 +
            Math.sqrt(i + 1) * (nScopes > 1 ? 14 : 18) +
            Math.min(40, deg * 1.2) +
            hash01(sn.id) * 18;
          sn.x = cx + Math.cos(ang) * rad;
          sn.y = cy + Math.sin(ang) * rad * 0.9;
        });
      });
    }

    seedPositions();
    rebuildGhosts();
    const maxR = 520 + Math.sqrt(Math.max(simNodes.length, 1)) * 55;

    const saved = loadLayout(scopeKey);
    let restored = 0;
    if (saved?.positions) {
      for (const sn of simNodes) {
        const p = saved.positions[sn.id];
        if (p && typeof p.x === "number" && typeof p.y === "number") {
          sn.x = p.x;
          sn.y = p.y;
          sn.pinned = !!p.pinned;
          sn.vx = sn.vy = 0;
          restored++;
        }
      }
    }

    let frozen =
      restored > 0 &&
      restored >= Math.min(simNodes.length, Math.max(1, Math.floor(simNodes.length * 0.6)));
    let alpha = frozen ? 0 : 1;

    const state = {
      panX: 0,
      panY: 0,
      zoom: 1,
      targetPanX: 0,
      targetPanY: 0,
      targetZoom: 1,
      W: 0,
      H: 0,
      dpr: 1,
      hover: null,
      selected: null,
      dragNode: null,
      dragGroup: null, // [{ node, ox, oy }] when Ctrl/Cmd moves the ego-network
      dragOffX: 0,
      dragOffY: 0,
      panning: false,
      panStartX: 0,
      panStartY: 0,
      panOriginX: 0,
      panOriginY: 0,
      moved: false,
      mouseX: -1,
      mouseY: -1,
    };

    let raf = 0;
    let didSettleFit = frozen;
    let userMoved = false;

    function freezeLayout() {
      if (frozen) return;
      frozen = true;
      alpha = 0;
      for (const n of simNodes) {
        n.vx = 0;
        n.vy = 0;
      }
      rebuildGhosts();
      saveLayout(scopeKey, simNodes);
    }

    function toWorld(sx, sy) {
      return {
        x: (sx - state.W / 2 - state.panX) / state.zoom,
        y: (sy - state.H / 2 - state.panY) / state.zoom,
      };
    }

    function toScreen(wx, wy) {
      return {
        x: state.W / 2 + state.panX + wx * state.zoom,
        y: state.H / 2 + state.panY + wy * state.zoom,
      };
    }

    function resize() {
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      const rect = canvas.getBoundingClientRect();
      state.W = Math.max(320, rect.width);
      state.H = Math.max(360, rect.height);
      canvas.width = Math.floor(state.W * dpr);
      canvas.height = Math.floor(state.H * dpr);
      canvas.style.width = state.W + "px";
      canvas.style.height = state.H + "px";
      state.dpr = dpr;
    }

    function fitView() {
      if (!simNodes.length) return;
      // Robust bbox: ignore far outliers so Fit doesn't shrink the dense core to a pea.
      let cx = 0,
        cy = 0;
      for (const n of simNodes) {
        cx += n.x;
        cy += n.y;
      }
      cx /= simNodes.length;
      cy /= simNodes.length;
      const dists = simNodes
        .map((n) => ({ n, d: Math.hypot(n.x - cx, n.y - cy) }))
        .sort((a, b) => a.d - b.d);
      const keep = dists.slice(0, Math.max(8, Math.ceil(dists.length * 0.92))).map((x) => x.n);
      let minX = Infinity,
        maxX = -Infinity,
        minY = Infinity,
        maxY = -Infinity;
      for (const n of keep) {
        minX = Math.min(minX, n.x - 10);
        maxX = Math.max(maxX, n.x + 10);
        minY = Math.min(minY, n.y - 10);
        maxY = Math.max(maxY, n.y + 10);
      }
      const bw = Math.max(140, maxX - minX);
      const bh = Math.max(140, maxY - minY);
      const z = Math.max(0.22, Math.min(2.4, Math.min((state.W - 110) / bw, (state.H - 110) / bh)));
      state.zoom = state.targetZoom = z;
      state.panX = state.targetPanX = (-(minX + maxX) / 2) * z;
      state.panY = state.targetPanY = (-(minY + maxY) / 2) * z;
    }

    function tick() {
      if (frozen || alpha < 0.008) {
        if (!frozen && alpha < 0.008) freezeLayout();
        return;
      }
      const n = simNodes.length;
      // Weak springs + strong repulsion → airy Obsidian cloud, not a hairball.
      // Dense similar-graphs (thousands of edges) need even weaker springs.
      const density = links.length / Math.max(n, 1);
      const linkK = (density > 8 ? 0.0038 : 0.006) * alpha;
      const linkDist = 110 + Math.sqrt(n) * 9 + Math.min(50, density * 1.2);
      const charge = -720 - Math.min(1100, n * 4);
      const reach = 400 + Math.sqrt(n) * 15;

      for (const l of links) {
        const dx = l.b.x - l.a.x;
        const dy = l.b.y - l.a.y;
        const d = Math.hypot(dx, dy) || 0.01;
        // Degree-weighted springs (ForceAtlas-style): hubs don't collapse the graph.
        const degA = (l.a.degree || 0) + 1;
        const degB = (l.b.degree || 0) + 1;
        const k = linkK / Math.sqrt(degA * degB);
        const ideal = linkDist * (0.85 + 0.08 * Math.min(degA, degB));
        const f = (d - ideal) * k;
        const fx = (dx / d) * f;
        const fy = (dy / d) * f;
        if (!l.a.pinned) {
          l.a.vx += fx;
          l.a.vy += fy;
        }
        if (!l.b.pinned) {
          l.b.vx -= fx;
          l.b.vy -= fy;
        }
      }

      for (let i = 0; i < n; i++) {
        const a = simNodes[i];
        for (let j = i + 1; j < n; j++) {
          const b = simNodes[j];
          let dx = a.x - b.x;
          let dy = a.y - b.y;
          const d2 = dx * dx + dy * dy || 0.01;
          if (d2 > reach * reach) continue;
          // Extra push when same community is too tight — keep clusters, not a solid ball.
          const same = a.scopeID && a.scopeID === b.scopeID;
          const f = (charge * (same ? 0.72 : 1.15)) / d2;
          const d = Math.sqrt(d2);
          dx /= d;
          dy /= d;
          if (!a.pinned) {
            a.vx += dx * f;
            a.vy += dy * f;
          }
          if (!b.pinned) {
            b.vx -= dx * f;
            b.vy -= dy * f;
          }
        }
      }

      // Very soft gravity — just keep the graph from drifting to infinity.
      for (const node of simNodes) {
        if (node.pinned) {
          node.vx = node.vy = 0;
          continue;
        }
        const pull = 0.0035 * alpha;
        node.vx -= node.x * pull;
        node.vy -= node.y * pull;
        node.vx *= 0.78;
        node.vy *= 0.78;
        node.x += node.vx;
        node.y += node.vy;
        const rd = Math.hypot(node.x, node.y);
        if (rd > maxR) {
          node.x *= maxR / rd;
          node.y *= maxR / rd;
          node.vx = node.vy = 0;
        }
      }
      // Dense graphs: cool a bit faster so we don't sit in "organizando" forever.
      alpha *= links.length > 2000 ? 0.988 : 0.992;
      if (alpha < 0.015) freezeLayout();
    }

    function pick(sx, sy) {
      const w = toWorld(sx, sy);
      let best = null;
      let bestD = 1e9;
      for (const n of simNodes) {
        const hit = (nodeRadius(n.degree, state.zoom) + 7) / Math.max(state.zoom, 0.35);
        const dx = w.x - n.x;
        const dy = w.y - n.y;
        const d = dx * dx + dy * dy;
        if (d < hit * hit && d < bestD) {
          bestD = d;
          best = n;
        }
      }
      return best;
    }

    function focusLinks(node) {
      const set = new Set();
      if (!node) return set;
      for (const e of adj.get(node.id) || []) set.add(e);
      return set;
    }

    /** Node + direct neighbors — the same set that lights up on hover. */
    function egoNodes(node) {
      const out = [node];
      const seen = new Set([node.id]);
      for (const e of adj.get(node.id) || []) {
        const other = e.a === node ? e.b : e.a;
        if (!seen.has(other.id)) {
          seen.add(other.id);
          out.push(other);
        }
      }
      return out;
    }

    function wantsClusterDrag(ev) {
      return !!(ev && (ev.ctrlKey || ev.metaKey));
    }

    function draw() {
      const ctx = canvas.getContext("2d");
      if (!ctx) return;

      state.panX += (state.targetPanX - state.panX) * 0.14;
      state.panY += (state.targetPanY - state.panY) * 0.14;
      state.zoom += (state.targetZoom - state.zoom) * 0.14;

      const wasFrozen = frozen;
      if (!state.dragNode && !state.panning) tick();
      if (!didSettleFit && !userMoved && !wasFrozen && frozen) {
        didSettleFit = true;
        fitView();
      }

      ctx.clearRect(0, 0, canvas.width, canvas.height);
      ctx.save();
      ctx.scale(state.dpr, state.dpr);
      ctx.fillStyle = BG;
      ctx.fillRect(0, 0, state.W, state.H);

      const focus = state.hover || state.selected;
      const lit = focusLinks(focus);
      const dim = !!focus;
      const neighborIDs = new Set();
      if (focus) {
        neighborIDs.add(focus.id);
        for (const e of lit) {
          neighborIDs.add(e.a.id);
          neighborIDs.add(e.b.id);
        }
      }

      ctx.lineCap = "round";

      if (!dim) {
        for (const g of ghosts) {
          const p1 = toScreen(g.a.x, g.a.y);
          const p2 = toScreen(g.b.x, g.b.y);
          ctx.beginPath();
          ctx.moveTo(p1.x, p1.y);
          ctx.lineTo(p2.x, p2.y);
          ctx.strokeStyle = "rgba(120,122,130,0.08)";
          ctx.lineWidth = 0.4;
          ctx.stroke();
        }
      }

      // Real edges — hairlines; opacity scales down with density.
      // At low zoom, subsample draw only (data/hover still use full adj).
      const idleStroke = edgeIdleStyle(links.length);
      const dimStroke = edgeDimStyle(links.length);
      const step =
        !dim && state.zoom < 0.55 && links.length > 900
          ? Math.min(6, Math.ceil(links.length / 1200))
          : 1;
      for (let ei = 0; ei < links.length; ei += step) {
        const e = links[ei];
        const on = !dim || lit.has(e);
        if (dim && !on && ei % 3 !== 0) continue;
        const p1 = toScreen(e.a.x, e.a.y);
        const p2 = toScreen(e.b.x, e.b.y);
        ctx.beginPath();
        ctx.moveTo(p1.x, p1.y);
        ctx.lineTo(p2.x, p2.y);
        if (on && dim) {
          ctx.strokeStyle = EDGE_HOT;
          ctx.lineWidth = 1.05;
        } else if (on) {
          ctx.strokeStyle = idleStroke;
          ctx.lineWidth = 0.45;
        } else {
          ctx.strokeStyle = dimStroke;
          ctx.lineWidth = 0.35;
        }
        ctx.stroke();
      }
      // When subsampled + hover, always draw the lit ego edges fully.
      if (dim && step > 1) {
        for (const e of lit) {
          const p1 = toScreen(e.a.x, e.a.y);
          const p2 = toScreen(e.b.x, e.b.y);
          ctx.beginPath();
          ctx.moveTo(p1.x, p1.y);
          ctx.lineTo(p2.x, p2.y);
          ctx.strokeStyle = EDGE_HOT;
          ctx.lineWidth = 1.05;
          ctx.stroke();
        }
      }

      for (const n of simNodes) {
        const on = !dim || neighborIDs.has(n.id);
        const p = toScreen(n.x, n.y);
        const r = nodeRadius(n.degree, state.zoom);
        const isFocus = n === focus;
        ctx.beginPath();
        ctx.arc(p.x, p.y, r, 0, Math.PI * 2);
        ctx.fillStyle = n.color;
        ctx.globalAlpha = on ? (isFocus ? 1 : 0.92) : 0.12;
        ctx.fill();
        // Ring only on hover/selection — no permanent colored halos.
        if (isFocus) {
          ctx.strokeStyle = "rgba(245,245,248,0.9)";
          ctx.lineWidth = 1.25;
          ctx.globalAlpha = 1;
          ctx.stroke();
        } else if (n.pinned && on) {
          ctx.strokeStyle = "rgba(255,255,255,0.28)";
          ctx.lineWidth = 0.8;
          ctx.stroke();
        }
      }
      ctx.globalAlpha = 1;

      if (focus) {
        const p = toScreen(focus.x, focus.y);
        const r = nodeRadius(focus.degree, state.zoom);
        ctx.font = '600 12px Inter, system-ui, sans-serif';
        ctx.fillStyle = "rgba(248,248,250,0.95)";
        const t = focus.label.length > 42 ? focus.label.slice(0, 41) + "…" : focus.label;
        ctx.fillText(t, p.x + r + 5, p.y + 4);
      } else if (state.zoom > 1.45) {
        ctx.font = '10px Inter, system-ui, sans-serif';
        ctx.fillStyle = "rgba(180,182,190,0.55)";
        const hubs = [...simNodes]
          .sort((a, b) => (b.degree || 0) - (a.degree || 0))
          .slice(0, 5);
        for (const n of hubs) {
          if ((n.degree || 0) < 2) continue;
          const p = toScreen(n.x, n.y);
          const t = n.label.length > 26 ? n.label.slice(0, 25) + "…" : n.label;
          ctx.fillText(t, p.x + 5, p.y + 3);
        }
      }

      ctx.font = '11px "SF Mono", Consolas, monospace';
      ctx.fillStyle = "#6e6e78";
      const status = frozen ? "congelado" : "organizando…";
      ctx.fillText(
        `${simNodes.length} nós · ${links.length} ligações · ${status} · zoom ${state.zoom.toFixed(2)}`,
        12,
        state.H - 12
      );
      ctx.restore();

      if (onFrameStats) {
        onFrameStats({
          zoom: state.zoom,
          pinned: simNodes.filter((n) => n.pinned).length,
          frozen,
        });
      }
      raf = requestAnimationFrame(draw);
    }

    function updateTip(mx, my) {
      if (!tip) return;
      const h = state.hover;
      if (!h || state.dragNode || state.panning) {
        tip.style.display = "none";
        return;
      }
      const m = h.meta?.metadata || {};
      tip.innerHTML = `<strong>${h.label}</strong>${
        m.scopeLabel
          ? `<div style="color:#a1a1aa;font-size:11px;margin-top:4px">${m.scopeLabel}</div>`
          : ""
      }`;
      tip.style.display = "block";
      tip.style.left = Math.min(mx + 14, state.W - tip.offsetWidth - 8) + "px";
      tip.style.top = Math.min(my + 14, state.H - tip.offsetHeight - 8) + "px";
    }

    function localXY(ev) {
      const r = canvas.getBoundingClientRect();
      return { x: ev.clientX - r.left, y: ev.clientY - r.top };
    }

    function onPointerDown(ev) {
      canvas.setPointerCapture(ev.pointerId);
      const p = localXY(ev);
      state.mouseX = p.x;
      state.mouseY = p.y;
      state.moved = false;
      const hit = pick(p.x, p.y);
      if (hit) {
        if (!frozen) freezeLayout();
        state.dragNode = hit;
        state.hover = hit; // keep ego highlight while dragging
        const w = toWorld(p.x, p.y);
        state.dragOffX = hit.x - w.x;
        state.dragOffY = hit.y - w.y;
        if (wantsClusterDrag(ev)) {
          // Freeze relative offsets so the whole starburst translates rigidly.
          state.dragGroup = egoNodes(hit).map((n) => ({
            node: n,
            ox: n.x - hit.x,
            oy: n.y - hit.y,
          }));
        } else {
          state.dragGroup = null;
        }
      } else {
        state.panning = true;
        state.panStartX = ev.clientX;
        state.panStartY = ev.clientY;
        state.panOriginX = state.targetPanX;
        state.panOriginY = state.targetPanY;
      }
    }

    function onPointerMove(ev) {
      const p = localXY(ev);
      state.mouseX = p.x;
      state.mouseY = p.y;
      if (state.dragNode) {
        state.moved = true;
        const w = toWorld(p.x, p.y);
        const nx = w.x + state.dragOffX;
        const ny = w.y + state.dragOffY;
        if (state.dragGroup && state.dragGroup.length) {
          for (const g of state.dragGroup) {
            g.node.x = nx + g.ox;
            g.node.y = ny + g.oy;
            g.node.vx = g.node.vy = 0;
          }
        } else {
          state.dragNode.x = nx;
          state.dragNode.y = ny;
          state.dragNode.vx = state.dragNode.vy = 0;
        }
        state.hover = state.dragNode;
        canvas.style.cursor = state.dragGroup ? "move" : "grabbing";
        return;
      }
      if (state.panning) {
        state.moved = true;
        userMoved = true;
        state.targetPanX = state.panOriginX + (ev.clientX - state.panStartX);
        state.targetPanY = state.panOriginY + (ev.clientY - state.panStartY);
        return;
      }
      state.hover = pick(p.x, p.y);
      updateTip(p.x, p.y);
      if (state.hover && wantsClusterDrag(ev)) {
        canvas.style.cursor = "move";
      } else {
        canvas.style.cursor = state.hover ? "grab" : "default";
      }
    }

    function onPointerUp(ev) {
      try {
        canvas.releasePointerCapture(ev.pointerId);
      } catch (_) {
        /* ignore */
      }
      if (state.dragNode) {
        if (state.moved) {
          const group = state.dragGroup?.length
            ? state.dragGroup.map((g) => g.node)
            : [state.dragNode];
          for (const n of group) {
            n.pinned = true;
            n.vx = n.vy = 0;
          }
          frozen = true;
          alpha = 0;
          saveLayout(scopeKey, simNodes);
        } else if (onNodeClick) {
          state.selected = state.dragNode;
          onNodeClick(state.dragNode.meta);
        }
        state.dragNode = null;
        state.dragGroup = null;
      } else if (state.panning && !state.moved) {
        if (state.hover && onNodeClick) {
          state.selected = state.hover;
          onNodeClick(state.hover.meta);
        } else {
          state.selected = null;
        }
      }
      state.panning = false;
      canvas.style.cursor = state.hover ? "grab" : "default";
    }

    function onWheel(ev) {
      userMoved = true;
      ev.preventDefault();
      const p = localXY(ev);
      const before = toWorld(p.x, p.y);
      const factor = ev.deltaY > 0 ? 0.9 : 1.1;
      state.targetZoom = Math.max(0.08, Math.min(4, state.targetZoom * factor));
      const after = toWorld(p.x, p.y);
      state.targetPanX += (after.x - before.x) * state.targetZoom;
      state.targetPanY += (after.y - before.y) * state.targetZoom;
    }

    resize();
    fitView();
    raf = requestAnimationFrame(draw);

    canvas.addEventListener("wheel", onWheel, { passive: false });
    canvas.addEventListener("pointerdown", onPointerDown);
    canvas.addEventListener("pointermove", onPointerMove);
    canvas.addEventListener("pointerup", onPointerUp);
    canvas.addEventListener("pointerleave", () => {
      state.hover = null;
      if (tip) tip.style.display = "none";
    });
    const ro = new ResizeObserver(() => resize());
    ro.observe(canvas);

    return {
      fitView,
      zoomBy(f) {
        state.targetZoom = Math.max(0.08, Math.min(4, state.targetZoom * f));
      },
      resetLayout() {
        clearLayout(scopeKey);
        seedPositions();
        rebuildGhosts();
        frozen = false;
        alpha = 1;
        didSettleFit = false;
        userMoved = false;
        state.selected = null;
      },
      exportPNG(name) {
        const a = document.createElement("a");
        a.download = name || "grafo.png";
        a.href = canvas.toDataURL("image/png");
        a.click();
      },
      destroy() {
        cancelAnimationFrame(raf);
        ro.disconnect();
        canvas.removeEventListener("wheel", onWheel);
        canvas.removeEventListener("pointerdown", onPointerDown);
        canvas.removeEventListener("pointermove", onPointerMove);
        canvas.removeEventListener("pointerup", onPointerUp);
      },
    };
  }

  global.ImpamindGraph = { mount, clearLayout, LAYOUT_PREFIX };
})(window);
