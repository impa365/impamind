/**
 * Constellation canvas renderer — adapted from Hindsight Control Plane
 * (vectorize-io/hindsight, hindsight-control-plane/src/components/constellation.tsx)
 * Visual model: star-field nodes, heat by link count, faint idle links, hover highlight.
 */
(function (global) {
  "use strict";

  const FONT_SMALL = '11px Inter, -apple-system, "Segoe UI", sans-serif';
  const FONT_BOLD = '600 10px Inter, -apple-system, "Segoe UI", sans-serif';
  const MONO = '11px "SF Mono", "Fira Code", Consolas, monospace';
  const BG = "#16181e";
  const MOTION_KEY = "impamind-constellation-motion";

  const LINK_TYPE_COLORS = {
    semantic: "#0074d9",
    similar: "#0074d9",
    temporal: "#009296",
    entity: "#f59e0b",
    link: "#f59e0b",
    causal: "#8b5cf6",
    supersedes: "#8b5cf6",
  };
  const DEFAULT_LINK = "#0074d9";

  function lerp(a, b, t) {
    return a + (b - a) * t;
  }

  function hexToRgba(hex, alpha) {
    if (!hex || !hex.startsWith("#")) return hex;
    const r = parseInt(hex.slice(1, 3), 16);
    const g = parseInt(hex.slice(3, 5), 16);
    const b = parseInt(hex.slice(5, 7), 16);
    return `rgba(${r},${g},${b},${alpha})`;
  }

  function heatColor(t) {
    const v = Math.max(0, Math.min(1, t));
    // Desaturated cool→warm so Estrelas is not a neon blob.
    const stops = [
      [140, 148, 168],
      [150, 142, 168],
      [168, 148, 132],
    ];
    const seg = v * (stops.length - 1);
    const i = Math.min(Math.floor(seg), stops.length - 2);
    const frac = seg - i;
    const a = stops[i];
    const b = stops[i + 1];
    const r = Math.round(a[0] + (b[0] - a[0]) * frac);
    const g = Math.round(a[1] + (b[1] - a[1]) * frac);
    const bl = Math.round(a[2] + (b[2] - a[2]) * frac);
    return `rgb(${r},${g},${bl})`;
  }

  function hashStr(s) {
    let h = 0;
    for (let i = 0; i < s.length; i++) h = ((h << 5) - h + s.charCodeAt(i)) | 0;
    return h;
  }

  function convexHull(points) {
    if (points.length < 3) return points.slice();
    const pts = points.slice().sort((a, b) => a[0] - b[0] || a[1] - b[1]);
    const cross = (o, a, b) => (a[0] - o[0]) * (b[1] - o[1]) - (a[1] - o[1]) * (b[0] - o[0]);
    const lower = [];
    for (const p of pts) {
      while (lower.length >= 2 && cross(lower[lower.length - 2], lower[lower.length - 1], p) <= 0) lower.pop();
      lower.push(p);
    }
    const upper = [];
    for (let i = pts.length - 1; i >= 0; i--) {
      const p = pts[i];
      while (upper.length >= 2 && cross(upper[upper.length - 2], upper[upper.length - 1], p) <= 0) upper.pop();
      upper.push(p);
    }
    lower.pop();
    upper.pop();
    return lower.concat(upper);
  }

  function truncateLabel(text, max) {
    text = (text || "").replace(/\s+/g, " ").trim();
    if (text.length <= max) return text;
    return text.slice(0, max - 1) + "…";
  }

  function prepareGraph(data, options) {
    const nodesIn = data.nodes || [];
    const linksIn = data.links || [];
    if (!nodesIn.length) {
      return { preparedNodes: [], linksWithIndices: [], linksByNode: new Map(), clusters: [] };
    }

    const nodeIndexMap = new Map();
    nodesIn.forEach((n, i) => nodeIndexMap.set(n.id, i));

    const linkCounts = new Map();
    for (const link of linksIn) {
      linkCounts.set(link.source, (linkCounts.get(link.source) || 0) + 1);
      linkCounts.set(link.target, (linkCounts.get(link.target) || 0) + 1);
    }
    let maxLinkCount = 1;
    for (const lc of linkCounts.values()) if (lc > maxLinkCount) maxLinkCount = lc;

    const count = nodesIn.length;
    const clusterKeyFn = options.clusterKeyFn;
    const clusterKeys = clusterKeyFn ? nodesIn.map((n) => clusterKeyFn(n)) : null;
    const clusterMembers = new Map();
    if (clusterKeys) {
      clusterKeys.forEach((k, i) => {
        if (k == null) return;
        const arr = clusterMembers.get(k);
        if (arr) arr.push(i);
        else clusterMembers.set(k, [i]);
      });
    }
    const clusterOrder = [...clusterMembers.keys()];
    const clusterCentroid = new Map();
    const clusterRingR = Math.sqrt(Math.max(count, 1)) * 60;
    clusterOrder.forEach((k, ci) => {
      const a = (ci / Math.max(clusterOrder.length, 1)) * Math.PI * 2;
      const members = clusterMembers.get(k);
      const blobR = 22 + Math.sqrt(members.length) * 18;
      clusterCentroid.set(k, { cx: Math.cos(a) * clusterRingR, cy: Math.sin(a) * clusterRingR, r: blobR });
    });

    const preparedNodes = nodesIn.map((node, i) => {
      const text = truncateLabel(node.label || node.id, 45);
      const ck = clusterKeys ? clusterKeys[i] : null;
      const color =
        (ck != null && options.clusterColorFn?.(ck)) ||
        options.nodeColorFn?.(node) ||
        node.color ||
        DEFAULT_LINK;
      const lc = linkCounts.get(node.id) || 0;
      const heat = options.nodeHeatFn
        ? heatColor(Math.max(0, Math.min(1, options.nodeHeatFn(node))))
        : heatColor(Math.sqrt(lc / maxLinkCount));

      const seed = hashStr(node.id);
      let wx, wy;
      const centroid = ck != null ? clusterCentroid.get(ck) : undefined;
      if (centroid) {
        const members = clusterMembers.get(ck);
        const j = members.indexOf(i);
        const la = (j / Math.max(members.length, 1)) * Math.PI * 2 + ((seed % 100) / 100) * 0.6;
        const lr = centroid.r * (0.25 + 0.75 * ((Math.abs(seed) % 1000) / 1000));
        wx = centroid.cx + Math.cos(la) * lr;
        wy = centroid.cy + Math.sin(la) * lr;
      } else {
        const angle = (i / count) * Math.PI * 2 + ((seed % 100) / 100) * 0.5;
        const baseRadius = Math.sqrt(count) * 30;
        const radius = baseRadius * 0.3 + ((Math.abs(seed) % 1000) / 1000) * baseRadius * 0.7;
        wx = Math.cos(angle) * radius + ((seed % 200) - 100) * 0.5;
        wy = Math.sin(angle) * radius + (((seed >> 8) % 200) - 100) * 0.5;
      }

      return {
        node,
        wx,
        wy,
        label: text,
        color,
        heatColor: centroid ? color : heat,
        linkCount: lc,
        phase: ((Math.abs(seed) % 1000) / 1000) * Math.PI * 2,
      };
    });

    const clusters = clusterOrder.map((key) => ({
      key,
      members: clusterMembers.get(key),
      color: options.clusterColorFn?.(key) || DEFAULT_LINK,
      label: options.clusterLabelFn?.(key) ?? key,
    }));

    const linksWithIndices = [];
    const linksByNode = new Map();
    for (let li = 0; li < linksIn.length; li++) {
      const link = linksIn[li];
      const ai = nodeIndexMap.get(link.source);
      const bi = nodeIndexMap.get(link.target);
      if (ai === undefined || bi === undefined) continue;
      const type = link.type || "similar";
      const color = options.linkColorFn?.(link) || link.color || LINK_TYPE_COLORS[type] || DEFAULT_LINK;
      const idx = linksWithIndices.length;
      linksWithIndices.push({ a: ai, b: bi, color, type });
      if (!linksByNode.has(ai)) linksByNode.set(ai, []);
      if (!linksByNode.has(bi)) linksByNode.set(bi, []);
      linksByNode.get(ai).push(idx);
      linksByNode.get(bi).push(idx);
    }

    return { preparedNodes, linksWithIndices, linksByNode, clusters };
  }

  function mount(canvas, options) {
    const tip = options.tooltipEl;
    const onNodeClick = options.onNodeClick;
    const onFrameStats = options.onFrameStats;
    const nodeSizeFn = options.nodeSizeFn;

    let graph = prepareGraph(options.data, options);
    let motionEnabled = localStorage.getItem(MOTION_KEY) !== "off";
    const clock = { seconds: 0, lastFrameMs: 0 };

    const state = {
      panX: 0,
      panY: 0,
      zoom: 0.5,
      targetPanX: 0,
      targetPanY: 0,
      targetZoom: 0.5,
      mouseX: -1,
      mouseY: -1,
      isDragging: false,
      dragStartX: 0,
      dragStartY: 0,
      panStartX: 0,
      panStartY: 0,
      hoverIndex: -1,
      prevHoverIndex: -2,
      W: 0,
      H: 0,
      dpr: 1,
    };

    let raf = 0;

    function autoFitZoom() {
      const { preparedNodes } = graph;
      if (!preparedNodes.length) return;
      let maxR = 0;
      for (const n of preparedNodes) {
        const d = Math.hypot(n.wx, n.wy);
        if (d > maxR) maxR = d;
      }
      const fitSpan = graph.clusters.length ? 3.2 : 2.5;
      const fitZoom = maxR > 0 ? Math.min(state.W, state.H) / (maxR * fitSpan) : 0.5;
      state.zoom = fitZoom;
      state.targetZoom = fitZoom;
      state.panX = state.targetPanX = 0;
      state.panY = state.targetPanY = 0;
    }

    function resize() {
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      const rect = canvas.getBoundingClientRect();
      const W = Math.max(320, rect.width);
      const H = Math.max(360, rect.height);
      canvas.width = Math.floor(W * dpr);
      canvas.height = Math.floor(H * dpr);
      canvas.style.width = W + "px";
      canvas.style.height = H + "px";
      state.dpr = dpr;
      state.W = W;
      state.H = H;
    }

    function drawLabel(ctx, n, sx, sy, r, isHovered, force, zoom) {
      const text = n.label;
      if (zoom > 1.5 || isHovered) {
        const cardW = Math.min(220, 80 + zoom * 25);
        const cardH = isHovered ? 56 : 40;
        const cardX = sx - cardW / 2;
        const cardY = sy + r + 4;
        ctx.fillStyle = isHovered ? "#1c1c1e" : "rgba(9,9,11,0.92)";
        ctx.beginPath();
        if (ctx.roundRect) ctx.roundRect(cardX, cardY, cardW, cardH, 6);
        else ctx.rect(cardX, cardY, cardW, cardH);
        ctx.fill();
        ctx.strokeStyle = isHovered ? n.color : "rgba(63,63,70,0.5)";
        ctx.lineWidth = isHovered ? 1.5 : 0.5;
        ctx.stroke();
        ctx.font = FONT_SMALL;
        ctx.fillStyle = isHovered ? "#f4f4f5" : "#e4e4e7";
        ctx.textAlign = "left";
        const line = text.length > 80 ? text.slice(0, 79) + "…" : text;
        ctx.fillText(line, cardX + 8, cardY + 20);
      } else {
        ctx.font = FONT_SMALL;
        ctx.fillStyle = isHovered ? "#f4f4f5" : "#d4d4d8";
        ctx.globalAlpha = isHovered ? 1 : force ? 0.95 : Math.min(1, 0.72 + (zoom - 0.3) * 0.4);
        ctx.textAlign = "left";
        ctx.fillText(text, sx + r + 5, sy + 4);
        ctx.globalAlpha = 1;
      }
    }

    function updateTooltip(mx, my) {
      if (!tip) return;
      const idx = state.hoverIndex;
      if (idx < 0 || state.isDragging) {
        tip.style.display = "none";
        state.prevHoverIndex = -1;
        return;
      }
      const pn = graph.preparedNodes[idx];
      if (!pn) {
        tip.style.display = "none";
        return;
      }
      if (idx !== state.prevHoverIndex) {
        state.prevHoverIndex = idx;
        const meta = pn.node.metadata || {};
        const muted = "#a1a1aa";
        const dimmed = "#71717a";
        let html = `<div style="display:flex;align-items:center;gap:8px;margin-bottom:6px">`;
        html += `<span style="font-size:10px;font-weight:600;text-transform:uppercase;letter-spacing:0.05em;color:${pn.heatColor};background:${pn.heatColor}18;padding:2px 8px;border-radius:4px">${meta.kind || "memory"}</span>`;
        html += `<span style="font-size:10px;color:${muted}">${pn.linkCount} ligaç${pn.linkCount !== 1 ? "ões" : "ão"}</span></div>`;
        html += `<div style="font-size:12px;line-height:1.6;margin-bottom:8px;color:#e4e4e7">${meta.title || pn.label}</div>`;
        if (meta.scopeLabel) {
          html += `<div style="font-size:11px;color:${dimmed};margin-bottom:4px">${meta.scopeLabel}</div>`;
        }
        html += `<div style="font-size:9px;font-family:monospace;color:${dimmed}">${pn.node.id}</div>`;
        tip.innerHTML = html;
      }
      tip.style.display = "block";
      const tipW = tip.offsetWidth;
      const tipH = tip.offsetHeight;
      const tx = Math.min(mx + 16, state.W - tipW - 12);
      const ty = Math.min(my + 16, state.H - tipH - 12);
      tip.style.left = `${Math.max(4, tx)}px`;
      tip.style.top = `${Math.max(4, ty)}px`;
    }

    function frame() {
      const ctx = canvas.getContext("2d");
      if (!ctx) return;

      state.panX = lerp(state.panX, state.targetPanX, 0.12);
      state.panY = lerp(state.panY, state.targetPanY, 0.12);
      state.zoom = lerp(state.zoom, state.targetZoom, 0.12);

      const { W, H, dpr, zoom, panX, panY, mouseX, mouseY } = state;
      const cx = W / 2 + panX;
      const cy = H / 2 + panY;
      const margin = 60;
      const { preparedNodes, linksWithIndices, linksByNode, clusters } = graph;

      ctx.clearRect(0, 0, canvas.width, canvas.height);
      ctx.save();
      ctx.scale(dpr, dpr);
      ctx.fillStyle = BG;
      ctx.fillRect(0, 0, W, H);

      const nowMs = performance.now();
      if (motionEnabled && clock.lastFrameMs > 0) clock.seconds += (nowMs - clock.lastFrameMs) / 1000;
      clock.lastFrameMs = nowMs;
      const time = clock.seconds;
      const DRIFT_AMP = 16;

      const screenX = new Float32Array(preparedNodes.length);
      const screenY = new Float32Array(preparedNodes.length);
      const visible = new Uint8Array(preparedNodes.length);

      for (let i = 0; i < preparedNodes.length; i++) {
        const n = preparedNodes[i];
        const driftX = motionEnabled ? DRIFT_AMP * Math.sin(time * 0.6 + n.phase) : 0;
        const driftY = motionEnabled ? DRIFT_AMP * Math.cos(time * 0.5 + n.phase * 1.3) : 0;
        const sx = cx + (n.wx + driftX) * zoom;
        const sy = cy + (n.wy + driftY) * zoom;
        screenX[i] = sx;
        screenY[i] = sy;
        visible[i] = sx > -margin && sx < W + margin && sy > -margin && sy < H + margin ? 1 : 0;
      }

      if (mouseX >= 0 && !state.isDragging) {
        let bestDist = zoom > 1.5 ? 80 : 30;
        let bestIdx = -1;
        for (let i = 0; i < preparedNodes.length; i++) {
          if (!visible[i]) continue;
          const dx = mouseX - screenX[i];
          const dy = mouseY - screenY[i];
          const dist = Math.hypot(dx, dy);
          if (dist < bestDist) {
            bestDist = dist;
            bestIdx = i;
          }
        }
        state.hoverIndex = bestIdx;
      }

      const hoverIndex = state.hoverIndex;
      const hoveredLinks = new Set();
      if (hoverIndex >= 0) {
        for (const li of linksByNode.get(hoverIndex) || []) hoveredLinks.add(li);
      }

      let linksDrawn = 0;
      const maxLinks = 6000;

      if (hoverIndex >= 0) {
        for (const li of hoveredLinks) {
          const link = linksWithIndices[li];
          const ax = screenX[link.a], ay = screenY[link.a];
          const bx = screenX[link.b], by = screenY[link.b];
          ctx.strokeStyle = link.color;
          ctx.globalAlpha = 0.5;
          ctx.lineWidth = 1.5;
          const midX = (ax + bx) / 2 + (by - ay) * 0.08;
          const midY = (ay + by) / 2 - (bx - ax) * 0.08;
          ctx.beginPath();
          ctx.moveTo(ax, ay);
          ctx.quadraticCurveTo(midX, midY, bx, by);
          ctx.stroke();
          if (motionEnabled) {
            const fromHovered = link.a === hoverIndex;
            const raw = (time * 0.22 + (li % 13) / 13) % 1;
            const u = fromHovered ? raw : 1 - raw;
            const iu = 1 - u;
            const px = iu * iu * ax + 2 * iu * u * midX + u * u * bx;
            const py = iu * iu * ay + 2 * iu * u * midY + u * u * by;
            ctx.globalAlpha = 0.9 * (0.4 + 0.6 * Math.sin(u * Math.PI));
            ctx.fillStyle = link.color;
            ctx.shadowColor = link.color;
            ctx.shadowBlur = 6;
            ctx.beginPath();
            ctx.arc(px, py, 2, 0, Math.PI * 2);
            ctx.fill();
            ctx.shadowBlur = 0;
            ctx.globalAlpha = 0.5;
          }
          linksDrawn++;
        }
        ctx.globalAlpha = 1;
      } else {
        const shimmer = motionEnabled ? 1 + 0.12 * Math.sin(time * 0.6) : 1;
        const baseAlpha = (0.28 + Math.min(zoom * 0.08, 0.2)) * shimmer;
        ctx.lineWidth = 0.4;
        for (const link of linksWithIndices) {
          if (linksDrawn >= maxLinks) break;
          const ax = screenX[link.a], ay = screenY[link.a];
          const bx = screenX[link.b], by = screenY[link.b];
          if ((ax < -margin && bx < -margin) || (ax > W + margin && bx > W + margin) ||
              (ay < -margin && by < -margin) || (ay > H + margin && by > H + margin)) continue;
          ctx.strokeStyle = link.color;
          ctx.globalAlpha = baseAlpha;
          const midX = (ax + bx) / 2 + (by - ay) * 0.08;
          const midY = (ay + by) / 2 - (bx - ax) * 0.08;
          ctx.beginPath();
          ctx.moveTo(ax, ay);
          ctx.quadraticCurveTo(midX, midY, bx, by);
          ctx.stroke();
          linksDrawn++;
        }
        ctx.globalAlpha = 1;
      }

      if (clusters.length > 0) {
        ctx.save();
        ctx.lineJoin = "round";
        for (const cluster of clusters) {
          const pts = [];
          for (const idx of cluster.members) {
            if (idx < screenX.length) pts.push([screenX[idx], screenY[idx]]);
          }
          if (!pts.length) continue;
          let mx = 0, my = 0;
          for (const [x, y] of pts) { mx += x; my += y; }
          mx /= pts.length;
          my /= pts.length;
          const pad = 26;
          let rad = pad;
          ctx.fillStyle = hexToRgba(cluster.color, 0.04);
          ctx.strokeStyle = hexToRgba(cluster.color, 0.14);
          ctx.lineWidth = 0.8;
          ctx.beginPath();
          if (pts.length < 3) {
            for (const [x, y] of pts) rad = Math.max(rad, Math.hypot(x - mx, y - my) + pad);
            ctx.arc(mx, my, rad, 0, Math.PI * 2);
          } else {
            const hull = convexHull(pts);
            for (let j = 0; j < hull.length; j++) {
              const [hx, hy] = hull[j];
              const dx = hx - mx, dy = hy - my;
              const d = Math.hypot(dx, dy) || 1;
              const ex = hx + (dx / d) * pad;
              const ey = hy + (dy / d) * pad;
              if (j === 0) ctx.moveTo(ex, ey);
              else ctx.lineTo(ex, ey);
            }
            ctx.closePath();
          }
          ctx.fill();
          ctx.stroke();
          if (cluster.label && zoom > 0.15) {
            ctx.font = "600 11px ui-sans-serif, system-ui, sans-serif";
            const tw = ctx.measureText(cluster.label).width;
            const padX = 8;
            const pillH = 19;
            let topY = my;
            for (const [, y] of pts) topY = Math.min(topY, y);
            const ly = topY - pad - 12;
            const pw = tw + padX * 2;
            ctx.fillStyle = cluster.color;
            ctx.beginPath();
            if (ctx.roundRect) ctx.roundRect(mx - pw / 2, ly - pillH / 2, pw, pillH, 9.5);
            else ctx.rect(mx - pw / 2, ly - pillH / 2, pw, pillH);
            ctx.fill();
            ctx.fillStyle = "#ffffff";
            ctx.textAlign = "center";
            ctx.textBaseline = "middle";
            ctx.fillText(cluster.label, mx, ly + 0.5);
          }
        }
        ctx.restore();
        ctx.textAlign = "left";
        ctx.textBaseline = "alphabetic";
      }

      const GRID_CELL = 90;
      const labelGrid = new Set();
      function canPlace(lx, ly, lw, lh) {
        const c0 = Math.floor(lx / GRID_CELL);
        const c1 = Math.floor((lx + lw) / GRID_CELL);
        const r0 = Math.floor(ly / GRID_CELL);
        const r1 = Math.floor((ly + lh) / GRID_CELL);
        for (let c = c0; c <= c1; c++)
          for (let r = r0; r <= r1; r++) if (labelGrid.has(`${c},${r}`)) return false;
        return true;
      }
      function claim(lx, ly, lw, lh) {
        const c0 = Math.floor(lx / GRID_CELL);
        const c1 = Math.floor((lx + lw) / GRID_CELL);
        const r0 = Math.floor(ly / GRID_CELL);
        const r1 = Math.floor((ly + lh) / GRID_CELL);
        for (let c = c0; c <= c1; c++)
          for (let r = r0; r <= r1; r++) labelGrid.add(`${c},${r}`);
      }

      let labelsShown = 0;
      let visibleCount = 0;
      const order = [];
      for (let i = 0; i < preparedNodes.length; i++) {
        if (!visible[i]) continue;
        if (i !== hoverIndex) order.push(i);
      }
      if (hoverIndex >= 0 && visible[hoverIndex]) order.push(hoverIndex);

      for (const i of order) {
        const n = preparedNodes[i];
        const sx = screenX[i], sy = screenY[i];
        visibleCount++;
        const isHovered = i === hoverIndex;
        const isNeighbor = hoveredLinks.size > 0 && (linksByNode.get(i) || []).some((li) => hoveredLinks.has(li));
        const rawR = Math.max(2.2, nodeSizeFn ? nodeSizeFn(n.node) : 2.2 + Math.min(n.linkCount * 0.12, 2.4));
        const pulse = motionEnabled ? 1 + 0.04 * Math.sin(time * 1.05 + n.phase) : 1;
        const r = Math.max(2.0, rawR * pulse * Math.min(Math.max(zoom, 0.85), 1.4));
        const twinkleAlpha = motionEnabled ? 0.92 + 0.08 * Math.sin(time * 1.4 + n.phase * 2.1) : 1;
        const baseAlpha = Math.min(1, (0.88 + Math.min(n.linkCount * 0.015, 0.08)) * twinkleAlpha);

        // Soft halo only on hover — no permanent colored discs.
        if (isHovered) {
          ctx.beginPath();
          ctx.arc(sx, sy, r * 1.55, 0, Math.PI * 2);
          ctx.fillStyle = n.heatColor;
          ctx.globalAlpha = 0.2;
          ctx.fill();
        }

        ctx.beginPath();
        ctx.arc(sx, sy, r, 0, Math.PI * 2);
        ctx.fillStyle = n.heatColor;
        ctx.globalAlpha = isHovered ? 1 : isNeighbor ? 0.95 : hoverIndex >= 0 ? 0.22 : baseAlpha;
        ctx.fill();
        if (isHovered) {
          ctx.strokeStyle = "rgba(245,245,248,0.85)";
          ctx.lineWidth = 1.1;
          ctx.globalAlpha = 1;
          ctx.stroke();
        }
        ctx.shadowBlur = 0;
        ctx.globalAlpha = 1;

        if (isHovered) {
          drawLabel(ctx, n, sx, sy, r, true, true, zoom);
          labelsShown++;
        } else if (zoom > 1.5) {
          const cardW = Math.min(200, 80 + zoom * 25);
          const lx = sx - cardW / 2;
          const ly = sy + r + 4;
          if (canPlace(lx, ly, cardW, 50)) {
            claim(lx, ly, cardW, 50);
            drawLabel(ctx, n, sx, sy, r, false, isNeighbor, zoom);
            labelsShown++;
          }
        } else if (zoom > 0.5 || isNeighbor) {
          const lw = 155, lh = 16;
          const lx = sx + r + 5;
          const ly = sy - 8;
          if (canPlace(lx, ly, lw, lh)) {
            claim(lx, ly, lw, lh);
            drawLabel(ctx, n, sx, sy, r, false, isNeighbor, zoom);
            labelsShown++;
          }
        }
      }

      ctx.font = MONO;
      ctx.fillStyle = "#71717a";
      ctx.textAlign = "left";
      ctx.fillText(
        `${preparedNodes.length} memórias · ${visibleCount} visíveis · ${labelsShown} rótulos · ${linksDrawn} ligações · zoom ${zoom.toFixed(2)}`,
        12,
        H - 12
      );

      ctx.textAlign = "right";
      let legendX = W - 12;
      ctx.font = FONT_BOLD;
      const legendTypes = [
        ["similar", "semântico"],
        ["link", "entidade"],
        ["supersedes", "causal"],
      ];
      for (let t = legendTypes.length - 1; t >= 0; t--) {
        const [type, label] = legendTypes[t];
        const color = LINK_TYPE_COLORS[type];
        const tw = ctx.measureText(label).width;
        ctx.fillStyle = "#a1a1aa";
        ctx.fillText(label, legendX, H - 12);
        legendX -= tw + 4;
        ctx.fillStyle = color;
        ctx.beginPath();
        ctx.arc(legendX, H - 15, 3, 0, Math.PI * 2);
        ctx.fill();
        legendX -= 14;
      }

      if (!clusters.length) {
        ctx.textAlign = "left";
        ctx.font = FONT_BOLD;
        ctx.fillStyle = "#a1a1aa";
        ctx.fillText("LIGAÇÕES", 12, 36);
        const gradW = 80;
        for (let gx = 0; gx < gradW; gx++) {
          ctx.fillStyle = heatColor(gx / gradW);
          ctx.fillRect(12 + gx, 42, 1, 6);
        }
        ctx.font = MONO;
        ctx.fillStyle = "#a1a1aa";
        ctx.fillText("poucas", 12, 60);
        ctx.textAlign = "right";
        ctx.fillText("muitas", 12 + gradW, 60);
      }

      ctx.textAlign = "left";
      ctx.font = MONO;
      ctx.fillStyle = "#52525b";
      ctx.fillText("Scroll = zoom · arrastar = pan · hover = explorar · clique = abrir", 12, 16);

      ctx.restore();

      if (onFrameStats) onFrameStats({ zoom, visible: visibleCount, links: linksDrawn });

      raf = requestAnimationFrame(frame);
    }

    function onWheel(e) {
      e.preventDefault();
      const factor = e.deltaY > 0 ? 0.9 : 1.1;
      state.targetZoom = Math.max(0.03, Math.min(8, state.targetZoom * factor));
    }

    function onMove(e) {
      const rect = canvas.getBoundingClientRect();
      const mx = e.clientX - rect.left;
      const my = e.clientY - rect.top;
      state.mouseX = mx;
      state.mouseY = my;
      if (state.isDragging) {
        state.targetPanX = state.panStartX + (e.clientX - state.dragStartX);
        state.targetPanY = state.panStartY + (e.clientY - state.dragStartY);
        canvas.style.cursor = "grabbing";
        if (tip) tip.style.display = "none";
      } else {
        canvas.style.cursor = state.hoverIndex >= 0 ? "pointer" : "grab";
        updateTooltip(mx, my);
      }
    }

    function onDown(e) {
      state.isDragging = true;
      state.dragStartX = e.clientX;
      state.dragStartY = e.clientY;
      state.panStartX = state.panX;
      state.panStartY = state.panY;
    }

    function onUp() {
      if (state.isDragging) {
        const dx = Math.abs(state.panX - state.panStartX);
        const dy = Math.abs(state.panY - state.panStartY);
        if (dx < 3 && dy < 3 && state.hoverIndex >= 0) {
          const node = graph.preparedNodes[state.hoverIndex]?.node;
          if (node && onNodeClick) onNodeClick(node);
        }
      }
      state.isDragging = false;
      canvas.style.cursor = state.hoverIndex >= 0 ? "pointer" : "grab";
    }

    function onLeave() {
      state.mouseX = -1;
      state.mouseY = -1;
      state.isDragging = false;
      state.hoverIndex = -1;
      if (tip) tip.style.display = "none";
    }

    resize();
    autoFitZoom();
    raf = requestAnimationFrame(frame);

    const ro = new ResizeObserver(() => resize());
    ro.observe(canvas);
    canvas.addEventListener("wheel", onWheel, { passive: false });
    canvas.addEventListener("pointermove", onMove);
    canvas.addEventListener("pointerdown", onDown);
    canvas.addEventListener("pointerup", onUp);
    canvas.addEventListener("pointerleave", onLeave);
    canvas.addEventListener("pointercancel", onLeave);

    return {
      setMotion(on) {
        motionEnabled = !!on;
        localStorage.setItem(MOTION_KEY, on ? "on" : "off");
      },
      getMotion() {
        return motionEnabled;
      },
      fitView() {
        autoFitZoom();
      },
      zoomBy(factor) {
        state.targetZoom = Math.max(0.03, Math.min(8, state.targetZoom * factor));
      },
      setData(data) {
        graph = prepareGraph(data, options);
        autoFitZoom();
      },
      exportPNG(filename) {
        const ctx = canvas.getContext("2d");
        if (!ctx) return;
        const a = document.createElement("a");
        a.download = filename || "constellation.png";
        a.href = canvas.toDataURL("image/png");
        a.click();
      },
      destroy() {
        cancelAnimationFrame(raf);
        ro.disconnect();
        canvas.removeEventListener("wheel", onWheel);
        canvas.removeEventListener("pointermove", onMove);
        canvas.removeEventListener("pointerdown", onDown);
        canvas.removeEventListener("pointerup", onUp);
        canvas.removeEventListener("pointerleave", onLeave);
        canvas.removeEventListener("pointercancel", onLeave);
      },
    };
  }

  global.ImpamindConstellation = { mount, heatColor, LINK_TYPE_COLORS };
})(window);
