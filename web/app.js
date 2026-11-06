// SPDX-FileCopyrightText: 2026 The odh-freshness contributors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

"use strict";

(async function () {
  const $ = (id) => document.getElementById(id);
  const nf = new Intl.NumberFormat("en");
  const fmtN = (n) => nf.format(n);
  const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
  const dateFmt = { format: (d) => d.getUTCDate() + " " + MONTHS[d.getUTCMonth()] + " " + d.getUTCFullYear() };
  const dayMonth = { format: (d) => d.getUTCDate() + " " + MONTHS[d.getUTCMonth()] };
  const STATUS_LABEL = { live: "Live", late: "Late", silent: "Silent", dead: "Dead", nodata: "No measurements" };
  const PAGE = 100;

  // Small DOM builder: text always goes through textContent, never innerHTML,
  // because station names and sources come from external data.
  function el(tag, attrs, ...kids) {
    const e = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v == null || v === false) continue;
      if (k === "class") e.className = v;
      else if (k === "style") e.style.cssText = v;
      else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
      else e.setAttribute(k, v === true ? "" : v);
    }
    for (const k of kids.flat()) if (k != null && k !== false) e.append(k instanceof Node ? k : String(k));
    return e;
  }
  const svgEl = (tag, attrs) => {
    const e = document.createElementNS("http://www.w3.org/2000/svg", tag);
    for (const [k, v] of Object.entries(attrs || {})) e.setAttribute(k, v);
    return e;
  };

  let data;
  try {
    const res = await fetch("data.json", { cache: "no-cache" });
    if (!res.ok) throw new Error("HTTP " + res.status);
    data = await res.json();
  } catch (err) {
    $("hero-lede").textContent = "Could not load data.json (" + err.message + "). Run the monitor first, then open this page through a web server.";
    return;
  }

  const now = new Date(data.generatedAt).getTime() / 1000;
  const T = data.totals;
  const total = Object.values(T).reduce((a, b) => a + b, 0);
  const stopped = (T.silent || 0) + (T.dead || 0);
  const pct = (n, d) => (d ? (n * 100) / d : 0);
  const pctText = (n, d) => { const p = pct(n, d); return (p > 0 && p < 1 ? p.toFixed(1) : Math.round(p)) + "%"; };

  function ago(unix) {
    if (unix == null) return "never";
    const h = (now - unix) / 3600;
    if (h < 48) return Math.round(h) + " hours";
    const d = h / 24;
    if (d < 60) return Math.round(d) + " days";
    if (d < 730) return Math.round(d / 30.44) + " months";
    return (d / 365.25).toFixed(1) + " years";
  }
  const dayOf = (unix) => (unix == null ? "" : new Date(unix * 1000).toISOString().slice(0, 10));

  // Rows: [typeIndex, originIndex, name, code, status, lastSeenUnix, lat, lon]
  const rows = data.stopped.map((r) => ({
    type: data.types[r[0]].name, origin: data.origins[r[1]].name, name: r[2] || r[3], code: r[3],
    status: r[4], last: r[5], day: dayOf(r[5]), lat: r[6], lon: r[7],
  }));
  rows.forEach((r) => { r.search = (r.name + " " + r.code).toLowerCase(); });

  /* ---------------- Hero ---------------- */
  $("updated").textContent = "Updated " + dateFmt.format(new Date(data.generatedAt)) + ", " +
    new Date(data.generatedAt).toISOString().slice(11, 16) + " UTC";
  $("hero-num").textContent = fmtN(stopped);
  $("hero-lede").textContent = "That is " + pctText(stopped, total) + " of the " + fmtN(total) +
    " active stations in the Open Data Hub. They are still marked active, but their latest value is more than a day old.";
  for (const s of ["live", "late", "silent", "dead"]) {
    $("kpis").append(el("div", { class: "kpi" },
      el("div", { class: "n" }, fmtN(T[s] || 0)),
      el("div", { class: "l" }, el("i", { class: "sw " + s }), STATUS_LABEL[s]),
      el("div", { class: "p" }, pctText(T[s] || 0, total) + " of active stations")));
  }
  for (const s of ["live", "late", "silent", "dead", "nodata"]) {
    if (T[s]) $("bigbar").append(el("i", { class: s, style: "width:" + pct(T[s], total) + "%", title: STATUS_LABEL[s] + ": " + fmtN(T[s]) }));
  }
  $("bigbar").setAttribute("aria-label", fmtN(stopped) + " of " + fmtN(total) + " active stations stopped reporting");
  if (T.nodata) {
    $("nodata-note").textContent = "Not counted: " + fmtN(T.nodata) + " active stations (grey) have no values at all. Most of them are places, like NOI-Place, not sensors.";
  }

  /* ---------------- Outage timeline ---------------- */
  $("outage-min").textContent = String(data.outageMin);
  const outageDays = new Map();
  for (const o of data.outages) {
    if (!outageDays.has(o.date)) outageDays.set(o.date, []);
    outageDays.get(o.date).push(o);
  }
  const tip = $("tip");
  const showTip = (ev, nodes) => {
    tip.replaceChildren(...nodes);
    tip.hidden = false;
    const x = Math.min(ev.clientX + 14, window.innerWidth - tip.offsetWidth - 8);
    tip.style.left = x + "px";
    tip.style.top = ev.clientY + 14 + "px";
  };
  const hideTip = () => { tip.hidden = true; };

  (function drawTimeline() {
    const days = data.timeline.days;
    const from = Date.parse(data.timeline.from + "T00:00:00Z");
    const W = 1000, H = 280, x0 = 8, x1 = 992, yTop = 70, yBase = 240;
    const max = Math.max(1, ...days);
    const bw = (x1 - x0) / days.length;
    const svg = svgEl("svg", { viewBox: `0 0 ${W} ${H}`, role: "img", "aria-label": "Stations by the day they last sent data, past 12 months" });
    svg.append(svgEl("line", { class: "axis", x1: x0, x2: x1, y1: yBase + 0.5, y2: yBase + 0.5 }));
    const dateAt = (i) => new Date(from + i * 864e5).toISOString().slice(0, 10);

    days.forEach((n, i) => {
      if (!n) return;
      const d = dateAt(i);
      const h = Math.max(2, ((yBase - yTop) * n) / max);
      const r = svgEl("rect", { class: "bar" + (outageDays.has(d) ? " out" : ""), x: x0 + i * bw, y: yBase - h, width: Math.max(1, bw - 0.6), height: h });
      r.style.cursor = "pointer";
      r.addEventListener("mousemove", (ev) => {
        const lines = [el("b", null, dateFmt.format(new Date(d))), el("br"), fmtN(n) + " stations sent their last value this day"];
        for (const o of outageDays.get(d) || []) lines.push(el("br"), "Outage: " + o.origin + " (" + fmtN(o.stations) + ")");
        showTip(ev, lines);
      });
      r.addEventListener("mouseleave", hideTip);
      r.addEventListener("click", () => { setState({ date: d, origin: "", type: "", status: "", q: "" }); go("explorer"); });
      svg.append(r);
    });

    // Month ticks
    for (let i = 0; i < days.length; i++) {
      const d = new Date(from + i * 864e5);
      if (d.getUTCDate() !== 1) continue;
      const x = x0 + i * bw;
      svg.append(svgEl("line", { class: "axis", x1: x, x2: x, y1: yBase, y2: yBase + 6 }));
      const t = svgEl("text", { x: x + 3, y: yBase + 22 });
      t.textContent = d.getUTCMonth() === 0 ? String(d.getUTCFullYear()) : MONTHS[d.getUTCMonth()];
      svg.append(t);
    }

    // Label up to three of the biggest outages, skipping any within 30 days of
    // one already labelled so labels never collide; stagger their heights.
    const picked = [];
    for (const o of data.outages) {
      if (o.date < data.timeline.from) continue;
      const t = Date.parse(o.date);
      if (picked.some((p) => Math.abs(Date.parse(p.date) - t) < 30 * 864e5)) continue;
      picked.push(o);
      if (picked.length === 3) break;
    }
    const inWindow = picked.sort((a, b) => a.date.localeCompare(b.date));
    const levels = [14, 44, 14];
    inWindow.forEach((o, k) => {
      const i = Math.round((Date.parse(o.date + "T00:00:00Z") - from) / 864e5);
      const n = days[i] || o.stations;
      const x = x0 + i * bw + bw / 2;
      const yBar = yBase - Math.max(2, ((yBase - yTop) * n) / max);
      const y = levels[k % levels.length];
      const right = x > W * 0.62;
      svg.append(svgEl("line", { class: "lead", x1: x, x2: x, y1: y + 22, y2: yBar - 3 }));
      const a = svgEl("text", { class: "anno", x: right ? x - 6 : x + 6, y: y + 4, "text-anchor": right ? "end" : "start" });
      a.textContent = dayMonth.format(new Date(o.date)) + " · " + fmtN(o.stations) + " stations";
      const b = svgEl("text", { class: "anno-sub", x: right ? x - 6 : x + 6, y: y + 20, "text-anchor": right ? "end" : "start" });
      b.textContent = o.origin;
      svg.append(a, b);
    });
    $("timeline").append(svg);
    // On narrow screens the chart scrolls sideways; start at the most recent months.
    $("timeline").scrollLeft = $("timeline").scrollWidth;

    const older = Object.entries(data.timeline.older || {}).sort();
    const olderSum = older.reduce((a, [, n]) => a + n, 0);
    if (olderSum) {
      $("older-note").textContent = "Older than this chart: " + fmtN(olderSum) + " more stations stopped before " +
        dateFmt.format(new Date(from)) + ", the oldest in " + older[0][0] + ". They are still marked active.";
    }
  })();

  for (const o of data.outages.slice(0, 12)) {
    const types = Object.entries(o.types).sort((a, b) => b[1] - a[1]).map(([t, n]) => t + " " + fmtN(n)).join(" · ");
    $("outage-list").append(el("button", {
      type: "button", class: "outage",
      onclick: () => { setState({ origin: o.origin, date: o.date, type: "", status: "", q: "" }); go("explorer"); },
    },
    el("div", { class: "d" }, dateFmt.format(new Date(o.date))),
    el("div", { class: "n" }, fmtN(o.stations)),
    el("div", { class: "o" }, o.origin),
    el("div", { class: "t" }, types),
    el("div", { class: "go" }, "Show these stations")));
  }
  if (!data.outages.length) $("outage-list").append(el("p", { class: "fine" }, "No outages detected in this run."));

  /* ---------------- Filters & state ---------------- */
  const state = { q: "", type: "", origin: "", status: "", date: "", sort: "last", dir: "asc", shown: PAGE };
  const typeSel = $("f-type"), originSel = $("f-origin"), qIn = $("f-q");
  const byStopped = (a, b) => (b.counts.silent || 0) + (b.counts.dead || 0) - ((a.counts.silent || 0) + (a.counts.dead || 0));
  for (const g of [...data.types].sort(byStopped)) {
    const n = (g.counts.silent || 0) + (g.counts.dead || 0);
    if (n) typeSel.append(el("option", { value: g.name }, g.name + " (" + fmtN(n) + ")"));
  }
  for (const g of [...data.origins].sort(byStopped)) {
    const n = (g.counts.silent || 0) + (g.counts.dead || 0);
    if (n) originSel.append(el("option", { value: g.name }, g.name + " (" + fmtN(n) + ")"));
  }

  function readHash() {
    const p = new URLSearchParams(location.hash.slice(1));
    for (const k of ["q", "type", "origin", "status", "date"]) state[k] = p.get(k) || "";
  }
  function writeHash() {
    const p = new URLSearchParams();
    for (const k of ["q", "type", "origin", "status", "date"]) if (state[k]) p.set(k, state[k]);
    const h = p.toString();
    history.replaceState(null, "", h ? "#" + h : location.pathname + location.search);
  }
  function setState(patch, opts = {}) {
    const fitMap = ["type", "origin", "date"].some((k) => k in patch && patch[k] !== state[k]);
    Object.assign(state, patch);
    state.shown = PAGE;
    writeHash();
    render(fitMap || opts.fit);
  }
  const go = (id) => document.getElementById(id).scrollIntoView({ behavior: "smooth" });

  qIn.addEventListener("input", () => setState({ q: qIn.value.trim() }));
  typeSel.addEventListener("change", () => setState({ type: typeSel.value }));
  originSel.addEventListener("change", () => setState({ origin: originSel.value }));
  document.querySelectorAll(".seg button").forEach((b) => b.addEventListener("click", () => setState({ status: b.dataset.status })));
  document.querySelectorAll("th button").forEach((b) => b.addEventListener("click", () => {
    const k = b.dataset.sort;
    state.dir = state.sort === k && state.dir === "asc" ? "desc" : "asc";
    state.sort = k;
    render(false);
  }));
  $("more").addEventListener("click", () => { state.shown += PAGE * 2; renderTable(filtered()); });

  function filtered() {
    const q = state.q.toLowerCase();
    return rows.filter((r) =>
      (!state.type || r.type === state.type) &&
      (!state.origin || r.origin === state.origin) &&
      (!state.status || r.status === state.status) &&
      (!state.date || r.day === state.date) &&
      (!q || r.search.includes(q)));
  }

  const statusRank = { dead: 0, silent: 1 };
  function sorted(list) {
    const k = state.sort, m = state.dir === "asc" ? 1 : -1;
    const val = (r) => (k === "last" ? (r.last == null ? -Infinity : r.last) : k === "status" ? statusRank[r.status] : (r[k] || "").toLowerCase());
    return [...list].sort((a, b) => {
      const va = val(a), vb = val(b);
      return (va < vb ? -1 : va > vb ? 1 : 0) * m || a.name.localeCompare(b.name);
    });
  }

  function renderChips() {
    const box = $("active-filters");
    const chips = [];
    const labels = { q: "Search", type: "Type", origin: "Source", status: "Status", date: "Last data on" };
    for (const k of ["date", "origin", "type", "status", "q"]) {
      if (!state[k]) continue;
      const v = k === "date" ? dateFmt.format(new Date(state[k])) : k === "status" ? STATUS_LABEL[state[k]] : state[k];
      chips.push(el("button", { type: "button", class: "chip", title: "Remove filter", onclick: () => setState({ [k]: "" }) }, labels[k] + ": " + v + "  ×"));
    }
    if (chips.length > 1) chips.push(el("button", { type: "button", class: "chip clear", onclick: () => setState({ q: "", type: "", origin: "", status: "", date: "" }) }, "Clear all"));
    box.replaceChildren(...chips);
  }

  function renderTable(list) {
    const items = sorted(list);
    const shown = items.slice(0, state.shown);
    $("rows").replaceChildren(...shown.map((r) => el("tr", null,
      el("td", null, r.name, el("span", { class: "code" }, r.code)),
      el("td", null, r.type),
      el("td", null, r.origin),
      el("td", null, el("span", { class: "st" }, el("i", { class: "dot " + r.status }), STATUS_LABEL[r.status])),
      el("td", { class: "num" }, r.last == null ? "—" : dateFmt.format(new Date(r.last * 1000)), el("span", { class: "ago" }, ago(r.last) + " ago")))));
    $("result-count").textContent = fmtN(items.length) + (items.length === 1 ? " station" : " stations") +
      (items.length > shown.length ? " · showing " + fmtN(shown.length) : "");
    $("more").hidden = items.length <= shown.length;
    document.querySelectorAll("th button").forEach((b) => b.dataset.dir = b.dataset.sort === state.sort ? state.dir : "");
  }

  /* ---------------- Map ---------------- */
  const map = L.map("mapbox", { preferCanvas: true, scrollWheelZoom: false, worldCopyJump: true });
  // CARTO's muted Positron tiles need a free key (carto.com/basemaps/apikey).
  // Without one, fall back to OpenStreetMap's own tiles, shown in grayscale.
  const OSM = '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors';
  if (data.cartoKey) {
    L.tileLayer("https://basemaps.cartocdn.com/rastertiles/light_all/{z}/{x}/{y}{r}.png?key=" + encodeURIComponent(data.cartoKey), {
      maxZoom: 19, attribution: OSM + ' &copy; <a href="https://carto.com/attributions">CARTO</a>',
    }).addTo(map);
  } else {
    L.tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", { maxZoom: 19, attribution: OSM, className: "tiles-gray" }).addTo(map);
  }
  map.on("click", () => map.scrollWheelZoom.enable());
  const layer = L.layerGroup().addTo(map);
  const COLORS = { silent: getComputedStyle(document.documentElement).getPropertyValue("--silent").trim(), dead: getComputedStyle(document.documentElement).getPropertyValue("--dead").trim() };

  function renderMap(list, fit) {
    layer.clearLayers();
    const pts = [];
    let unplaced = 0;
    for (const r of list) {
      if (r.lat == null || r.lon == null) { unplaced++; continue; }
      pts.push([r.lat, r.lon]);
      const m = L.circleMarker([r.lat, r.lon], { radius: 5, weight: 1, color: "#000", fillColor: COLORS[r.status], fillOpacity: 0.9 });
      m.bindPopup(() => el("div", null, el("b", null, r.name), el("br"),
        el("span", { class: "pop-meta" }, r.type + " · " + r.origin), el("br"),
        STATUS_LABEL[r.status] + ": last data " + (r.last == null ? "never" : dateFmt.format(new Date(r.last * 1000))) + " (" + ago(r.last) + " ago)"));
      layer.addLayer(m);
    }
    $("map-lede").textContent = fmtN(pts.length) + " stations that stopped sending data." +
      (unplaced ? " " + fmtN(unplaced) + " more have no coordinates, so they are not on the map." : "");
    if (fit) {
      if (pts.length) map.fitBounds(L.latLngBounds(pts).pad(0.08), { maxZoom: 12 });
      else map.setView([46.6, 11.3], 8);
    }
  }

  /* ---------------- Source tiles ---------------- */
  (function renderTiles() {
    const groups = data.origins.map((g) => {
      const t = Object.values(g.counts).reduce((a, b) => a + b, 0);
      const s = (g.counts.silent || 0) + (g.counts.dead || 0);
      return { g, t, s };
    }).filter((x) => x.t > 0).sort((a, b) => b.s - a.s || pct(b.s, b.t) - pct(a.s, a.t));
    $("tiles").replaceChildren(...groups.map(({ g, t, s }) => el("button", {
      type: "button", class: "tile" + (s ? "" : " ok"),
      onclick: s ? () => { setState({ origin: g.name, type: "", status: "", date: "", q: "" }); go("explorer"); } : null,
      "aria-disabled": s ? null : "true",
    },
    el("div", { class: "o" }, g.name),
    el("div", { class: "pc" }, pctText(s, t)),
    el("div", { class: "mini" }, ["live", "late", "silent", "dead", "nodata"].filter((k) => g.counts[k]).map((k) => el("i", { class: k, style: "width:" + pct(g.counts[k], t) + "%" }))),
    el("div", { class: "sub" }, fmtN(s) + " of " + fmtN(t) + " stopped"))));
  })();

  /* ---------------- Method ---------------- */
  const th = data.thresholds;
  $("rules").replaceChildren(
    el("li", null, el("b", null, "Live: "), "newest value within 3× its reporting period, at least " + th.minLiveHours + " hour."),
    el("li", null, el("b", null, "Late: "), "older than that, under " + th.activeHours + " hours."),
    el("li", null, el("b", null, "Silent: "), "1 to " + th.deadDays + " days without data."),
    el("li", null, el("b", null, "Dead: "), "over " + th.deadDays + " days without data."),
    el("li", null, el("b", null, "Outage: "), data.outageMin + " or more stations of one source sent their last value on the same day."));
  if (data.staticTypes && data.staticTypes.length) {
    $("static-note").textContent = "Left out because no station has any measurement (places, not sensors): " +
      data.staticTypes.map((s) => s.name + " (" + fmtN(s.stations) + ")").join(", ") + ".";
  }
  if (data.errors && data.errors.length) {
    $("error-note").textContent = fmtN(data.errors.length) + " requests failed in this run, so some station types are missing: " + data.errors.join("; ");
  }

  /* ---------------- Render ---------------- */
  function render(fit) {
    qIn.value = state.q;
    typeSel.value = state.type;
    originSel.value = state.origin;
    document.querySelectorAll(".seg button").forEach((b) => b.setAttribute("aria-pressed", String(b.dataset.status === state.status)));
    const list = filtered();
    renderChips();
    renderTable(list);
    renderMap(list, fit);
  }

  readHash();
  render(true);
  // Section links (#map, #explorer) also change the hash; only filter hashes carry "=".
  window.addEventListener("hashchange", () => { if (location.hash.includes("=")) { readHash(); render(true); } });
})();
