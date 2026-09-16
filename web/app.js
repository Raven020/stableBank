"use strict";

/* ==========================================================================
   stableBank demo console — vanilla ES2020, no build step, no CDN.
   ========================================================================== */

/* ---------------------------- api() helper ------------------------------ */

async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  try {
    const res = await fetch(path, opts);
    const text = await res.text();
    let data = null;
    if (text) {
      try {
        data = JSON.parse(text);
      } catch (e) {
        data = text;
      }
    }
    return { ok: res.ok, status: res.status, data, path };
  } catch (err) {
    return { ok: false, status: 0, data: { error: String(err && err.message ? err.message : err) }, path };
  }
}

const GET = (path) => api("GET", path);
const POST = (path, body) => api("POST", path, body === undefined ? {} : body);
const PUT = (path, body) => api("PUT", path, body === undefined ? {} : body);

function errText(res) {
  if (!res) return "unknown error";
  if (res.data && typeof res.data === "object") {
    if (res.data.error) {
      let s = res.data.error;
      if (res.data.details !== undefined && res.data.details !== null) {
        try {
          s += ": " + JSON.stringify(res.data.details);
        } catch (e) {
          /* ignore */
        }
      }
      return s;
    }
    try {
      return JSON.stringify(res.data);
    } catch (e) {
      return String(res.data);
    }
  }
  if (typeof res.data === "string" && res.data) return res.data;
  return "HTTP " + res.status;
}

function errBox(res, context) {
  return `<div class="error-box">${escapeHtml((context ? context + ": " : "") + errText(res))} (status ${res.status})</div>`;
}

/* ---------------------------- formatting --------------------------------- */

function escapeHtml(s) {
  if (s === null || s === undefined) return "";
  return String(s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function currencyDecimals(code) {
  return code === "USDC" ? 6 : 2;
}

function fmtMinor(minor, decimals) {
  if (minor === undefined || minor === null) return "";
  let neg = minor < 0;
  let m = Math.abs(Math.trunc(minor));
  let s = String(m).padStart(decimals + 1, "0");
  if (decimals === 0) return (neg ? "-" : "") + s;
  const intPart = s.slice(0, s.length - decimals);
  const fracPart = s.slice(s.length - decimals);
  return (neg ? "-" : "") + intPart + "." + fracPart;
}

function fmtCents(cents) {
  return fmtMinor(cents, 2);
}

function usd(cents) {
  if (cents === undefined || cents === null) return "—";
  return "$" + fmtCents(cents);
}

function fmtAmount(a) {
  if (!a) return "—";
  const dec = currencyDecimals(a.currency);
  return fmtMinor(a.minor, dec) + " " + a.currency;
}

function pct(v) {
  if (v === undefined || v === null || isNaN(v)) return "—";
  return Number(v).toFixed(2) + "%";
}

function fmtDate(iso) {
  if (!iso) return "—";
  try {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return String(iso);
    return d.toISOString().replace("T", " ").replace(/\.\d+Z$/, "Z");
  } catch (e) {
    return String(iso);
  }
}

function statusBadgeClass(status) {
  if (!status) return "badge-na";
  const s = String(status).toLowerCase();
  if (s === "ok" || s === "approved" || s === "active" || s === "live" || s === "settled" || s === "final" || s === "paid") return "badge-ok";
  if (s === "below_target" || s === "above_maximum" || s === "declined" || s === "defaulted" || s === "critical" || s === "missed") return "badge-critical";
  if (s === "n/a" || s === "na" || s === "unknown") return "badge-na";
  if (s === "pending" || s === "warning" || s === "stretched") return "badge-warning";
  return "badge-na";
}

function badge(status, label) {
  return `<span class="badge ${statusBadgeClass(status)}">${escapeHtml(label || status || "n/a")}</span>`;
}

const COLORS = {
  usd: "#3987e5",
  usdc: "#199e70",
  flowIn: "#0ca30c",
  flowOut: "#d95926",
  good: "#0ca30c",
  warning: "#fab219",
  critical: "#d03b3b",
};

function currencyColor(code) {
  return code === "USDC" ? COLORS.usdc : COLORS.usd;
}

/* ---------------------------- tiny UI helpers ---------------------------- */

function tile(label, value, sub, valueClass) {
  return `<div class="tile">
    <div class="tile-label">${escapeHtml(label)}</div>
    <div class="tile-value${valueClass ? " " + valueClass : ""}">${value}</div>
    ${sub ? `<div class="tile-sub">${sub}</div>` : ""}
  </div>`;
}

function jsonBlock(obj) {
  let text;
  try {
    text = JSON.stringify(obj, null, 2);
  } catch (e) {
    text = String(obj);
  }
  return `<pre class="json">${escapeHtml(text)}</pre>`;
}

function proportionBar(legs) {
  if (!legs || !legs.length) return '<p class="muted">No funding legs.</p>';
  const segs = legs
    .map((l) => {
      const pctv = l.share_pct || 0;
      const color = currencyColor(l.currency);
      return `<div class="seg" style="width:${pctv}%;background:${color}" title="${escapeHtml(l.currency)} ${pctv}%">${pctv >= 12 ? escapeHtml(l.currency) + " " + pctv + "%" : ""}</div>`;
    })
    .join("");
  const legend = legs
    .map((l) => `<span><span class="swatch" style="background:${currencyColor(l.currency)}"></span>${escapeHtml(l.currency)}: ${l.share_pct}% (${usd(l.usd_equivalent_cents)})</span>`)
    .join("");
  return `<div class="proportion-bar">${segs}</div><div class="proportion-legend">${legend}</div>`;
}

function svgBarChart(daily) {
  if (!daily || !daily.length) return '<p class="muted">No flow data for this window.</p>';
  const w = 760,
    h = 180,
    padTop = 10,
    padBottom = 24,
    padSide = 10;
  const max = Math.max(1, ...daily.flatMap((d) => [d.inflows_cents || 0, d.outflows_cents || 0]));
  const n = daily.length;
  const groupW = (w - padSide * 2) / n;
  const barW = Math.max(1.5, groupW / 2 - 2);
  const plotH = h - padTop - padBottom;
  let bars = "";
  daily.forEach((d, i) => {
    const x0 = padSide + i * groupW;
    const hIn = ((d.inflows_cents || 0) / max) * plotH;
    const hOut = ((d.outflows_cents || 0) / max) * plotH;
    const yIn = h - padBottom - hIn;
    const yOut = h - padBottom - hOut;
    bars += `<rect x="${x0.toFixed(1)}" y="${yIn.toFixed(1)}" width="${barW.toFixed(1)}" height="${hIn.toFixed(1)}" fill="${COLORS.flowIn}"><title>${escapeHtml(d.date)} in ${usd(d.inflows_cents)}</title></rect>`;
    bars += `<rect x="${(x0 + barW + 1.5).toFixed(1)}" y="${yOut.toFixed(1)}" width="${barW.toFixed(1)}" height="${hOut.toFixed(1)}" fill="${COLORS.flowOut}"><title>${escapeHtml(d.date)} out ${usd(d.outflows_cents)}</title></rect>`;
  });
  return `<svg viewBox="0 0 ${w} ${h}" class="bar-chart" role="img" aria-label="Daily inflows and outflows">
    ${bars}
    <line x1="${padSide}" y1="${h - padBottom}" x2="${w - padSide}" y2="${h - padBottom}" stroke="#383835" stroke-width="1"/>
  </svg>
  <div class="proportion-legend">
    <span><span class="swatch" style="background:${COLORS.flowIn}"></span>inflows</span>
    <span><span class="swatch" style="background:${COLORS.flowOut}"></span>outflows</span>
  </div>`;
}

function diffView(diffText) {
  if (!diffText) return '<p class="muted">No diff.</p>';
  const lines = String(diffText).split("\n");
  return (
    '<div class="diff-view">' +
    lines
      .map((line) => {
        let cls = "diff-ctx";
        if (line.startsWith("+")) cls = "diff-add";
        else if (line.startsWith("-")) cls = "diff-del";
        return `<div class="diff-line ${cls}">${escapeHtml(line)}</div>`;
      })
      .join("") +
    "</div>"
  );
}

function openModal(html) {
  const root = document.getElementById("modal-root");
  root.innerHTML = `<div class="modal-backdrop" onclick="if(event.target===this) closeModal()">
    <div class="modal">
      <button class="modal-close secondary" onclick="closeModal()">close ✕</button>
      <div>${html}</div>
    </div>
  </div>`;
}
function closeModal() {
  document.getElementById("modal-root").innerHTML = "";
}

/* ---------------------------- app shell ---------------------------------- */

const DEMO_ACCOUNT_ID = "demo-account-1";

const TABS = ["dashboard", "ledger", "account", "loans", "simulation", "rules", "demo"];
let activeTab = "dashboard";

const renderers = {}; // tab name -> async function()

function setGlobalError(msg) {
  const el = document.getElementById("global-error");
  el.textContent = msg || "";
  if (msg) setTimeout(() => { if (el.textContent === msg) el.textContent = ""; }, 6000);
}

async function refreshClock() {
  const res = await GET("/simulate/clock");
  const el = document.getElementById("clock-value");
  if (!res.ok) {
    el.textContent = "unavailable";
    return;
  }
  let now = null;
  if (res.data && typeof res.data === "object") {
    now = res.data.now || res.data.clock || res.data.time || res.data.sim_now;
  } else if (typeof res.data === "string") {
    now = res.data;
  }
  el.textContent = now ? fmtDate(now) : JSON.stringify(res.data);
}

async function showTab(name) {
  activeTab = name;
  document.querySelectorAll(".tab-btn").forEach((b) => b.classList.toggle("active", b.dataset.tab === name));
  document.querySelectorAll(".tab-panel").forEach((p) => p.classList.toggle("active", p.id === "tab-" + name));
  const fn = renderers[name];
  if (fn) {
    try {
      await fn();
    } catch (e) {
      const panel = document.getElementById("tab-" + name);
      if (panel) panel.innerHTML = `<div class="error-box">Unexpected UI error: ${escapeHtml(String(e && e.stack ? e.stack : e))}</div>`;
    }
  }
}

async function refreshAll() {
  await refreshClock();
  for (const t of TABS) {
    const fn = renderers[t];
    if (!fn) continue;
    try {
      await fn();
    } catch (e) {
      /* individual renderers already show their own errors */
    }
  }
}

// Lightweight refresh used after mutating actions in other tabs: only
// touches the dashboard tiles + account balances so the audience sees the
// effect ripple through, without a full-page re-render.
async function refreshEffects() {
  await refreshClock();
  if (renderers.dashboard) {
    try {
      await renderers.dashboard();
    } catch (e) {}
  }
  if (renderers.account) {
    try {
      await renderers.account();
    } catch (e) {}
  }
}

/* ==========================================================================
   Dashboard tab
   ========================================================================== */

let dashboardWindowDays = 30;

function ratioCardHtml(name, metric, key) {
  if (!metric) return `<div class="ratio-card"><h3>${escapeHtml(name)}</h3><p class="muted">no data</p></div>`;
  const status = metric.status || "n/a";
  return `<div class="ratio-card">
    <div class="ratio-head">
      <h3>${escapeHtml(name)}</h3>
      ${badge(status)}
    </div>
    <div class="ratio-value">${pct(metric.value_pct)}</div>
    <div class="ratio-target">target: ${pct(metric.target_pct)}</div>
    <div class="ratio-components">
      <div><span>numerator</span><span>${usd(metric.numerator_cents)}</span></div>
      <div><span>denominator</span><span>${usd(metric.denominator_cents)}</span></div>
      ${metric.components ? Object.entries(metric.components).map(([k, v]) => `<div><span>${escapeHtml(k)}</span><span>${escapeHtml(typeof v === "number" ? String(v) : JSON.stringify(v))}</span></div>`).join("") : ""}
    </div>
    <div class="why-link" onclick="Dash.whyFigure('${key}', '${encodeURIComponent(metric.rule_application_id || "")}')">why this figure? →</div>
    <div class="footnote" title="modeled on: ${escapeHtml(metric.modeled_on || "")}">${escapeHtml(metric.disclaimer || "Illustrative / PoC — not a certified regulatory figure")} · modeled on ${escapeHtml(metric.modeled_on || "n/a")}</div>
  </div>`;
}

const Dash = {
  async render() {
    const panel = document.getElementById("tab-dashboard");
    panel.innerHTML = `<p class="muted">Loading dashboard…</p>`;

    const summaryRes = await GET("/dashboard/summary");
    if (!summaryRes.ok) {
      panel.innerHTML = errBox(summaryRes, "GET /dashboard/summary");
      return;
    }
    const summary = summaryRes.data || {};
    const book = summary.ledger_book || {};
    const totals = book.totals || {};
    const ratios = summary.ratios || {};

    const treasuryFxHtml = (book.treasury_fx || [])
      .map((t) => `<div>${escapeHtml(t.currency)}: ${fmtMinor(t.minor, currencyDecimals(t.currency))} <span class="muted">(${usd(t.usd_equivalent_cents)})</span></div>`)
      .join("") || '<span class="muted">none</span>';

    const deposits = (ratios.loan_to_deposit && ratios.loan_to_deposit.denominator_cents) || 0;
    const loansOut = (ratios.loan_to_deposit && ratios.loan_to_deposit.numerator_cents) || 0;

    panel.innerHTML = `
      <div class="panel">
        <div class="card-header-row"><h2>Bank at a glance</h2><span class="muted">as of ${escapeHtml(fmtDate(summary.as_of))}</span></div>
        <div class="grid cols-4">
          ${tile("Total fiat (USD)", usd(totals.total_fiat_cents), "customer + treasury", "usd")}
          ${tile("Total USDC", fmtMinor(totals.total_usdc_micro, 6) + " USDC", "≈ " + usd(totals.total_usdc_usd_equivalent_cents), "usdc")}
          ${tile("Deposits", usd(deposits), "USD-equivalent customer deposits")}
          ${tile("Loans outstanding", usd(loansOut), "USD-equivalent receivables")}
        </div>
        <h3 style="margin-top:1em">Treasury FX exposure</h3>
        <div>${treasuryFxHtml}</div>
      </div>

      <div class="panel">
        <h2>Illustrative regulatory-style ratios</h2>
        <div class="grid cols-4" id="ratio-cards">
          ${ratioCardHtml("Loan-to-deposit", ratios.loan_to_deposit, "loan_to_deposit")}
          ${ratioCardHtml("LCR-style", ratios.lcr, "lcr")}
          ${ratioCardHtml("NSFR-style", ratios.nsfr, "nsfr")}
          ${ratioCardHtml("Capital-adequacy-style", ratios.capital_adequacy, "capital_adequacy")}
        </div>
        <div id="why-figure-panel"></div>
      </div>

      <div class="panel">
        <div class="card-header-row">
          <h2>Money in vs money out</h2>
          <div class="window-toggle">
            ${[7, 30, 90].map((d) => `<button class="${d === dashboardWindowDays ? "active" : ""}" onclick="Dash.setWindow(${d})">${d}d</button>`).join("")}
          </div>
        </div>
        <div id="flows-panel"><p class="muted">Loading…</p></div>
      </div>
    `;

    await Dash.loadFlows();
  },

  async setWindow(days) {
    dashboardWindowDays = days;
    await Dash.render();
  },

  async loadFlows() {
    const el = document.getElementById("flows-panel");
    if (!el) return;
    const res = await GET(`/dashboard/flows?window_days=${dashboardWindowDays}`);
    if (!res.ok) {
      el.innerHTML = errBox(res, "GET /dashboard/flows");
      return;
    }
    const f = res.data || {};
    const byKindRows = (f.by_kind || [])
      .map((k) => `<tr><td>${escapeHtml(k.kind)}</td><td class="num">${k.count}</td><td class="num">${usd(k.usd_equivalent_cents)}</td></tr>`)
      .join("");
    el.innerHTML = `
      <div class="grid cols-3" style="margin-bottom:1em">
        ${tile("Inflows", usd(f.inflows_cents))}
        ${tile("Outflows", usd(f.outflows_cents))}
        ${tile("Net", usd(f.net_cents))}
      </div>
      ${svgBarChart(f.daily)}
      <h3 style="margin-top:1em">By kind</h3>
      <div class="table-wrap"><table><thead><tr><th>Kind</th><th class="num">Count</th><th class="num">USD-equivalent</th></tr></thead><tbody>${byKindRows || '<tr><td colspan="3" class="muted">none</td></tr>'}</tbody></table></div>
    `;
  },

  async whyFigure(key, applicationIdEncoded) {
    const applicationId = applicationIdEncoded ? decodeURIComponent(applicationIdEncoded) : "";
    const panel = document.getElementById("why-figure-panel");
    if (!panel) return;
    panel.innerHTML = '<p class="muted">Loading rule application…</p>';
    const res = await GET(`/rules/dashboard_thresholds/applications?limit=10`);
    if (!res.ok) {
      panel.innerHTML = errBox(res, "GET /rules/dashboard_thresholds/applications");
      return;
    }
    const apps = (res.data && res.data.applications) || [];
    const match = applicationId ? apps.find((a) => a.id === applicationId) : null;
    const shown = match ? [match] : apps.slice(0, 3);
    panel.innerHTML = `<div class="info-box">
      <h3>Why this figure — ${escapeHtml(key)}</h3>
      ${shown.length ? shown.map((a) => jsonBlock(a)).join("") : '<p class="muted">No rule applications recorded yet.</p>'}
    </div>`;
  },
};
renderers.dashboard = Dash.render;

/* ==========================================================================
   Ledger tab
   ========================================================================== */

const Ledger = {
  async render() {
    const panel = document.getElementById("tab-ledger");
    panel.innerHTML = `
      <div class="panel" id="ledger-balances-panel"><p class="muted">Loading balances…</p></div>
      <div class="panel" id="ledger-tx-panel">
        <div class="card-header-row">
          <h2>Transactions</h2>
          <div class="inline-form">
            <label>Kind filter<input id="tx-kind-filter" placeholder="e.g. purchase"></label>
            <label>Account filter<input id="tx-account-filter" placeholder="account id"></label>
            <button onclick="Ledger.loadTransactions()">Apply</button>
          </div>
        </div>
        <div id="tx-table"><p class="muted">Loading transactions…</p></div>
      </div>
    `;
    await Promise.all([Ledger.loadBalances(), Ledger.loadTransactions()]);
  },

  async loadBalances() {
    const el = document.getElementById("ledger-balances-panel");
    const [accRes, balRes] = await Promise.all([GET("/ledger/accounts"), GET("/ledger/balances")]);
    if (!accRes.ok) {
      el.innerHTML = errBox(accRes, "GET /ledger/accounts");
      return;
    }
    if (!balRes.ok) {
      el.innerHTML = errBox(balRes, "GET /ledger/balances");
      return;
    }
    const accounts = accRes.data || [];
    const balances = balRes.data || {};
    const byType = {};
    for (const a of accounts) {
      byType[a.type] = byType[a.type] || [];
      byType[a.type].push(a);
    }
    const groups = Object.keys(byType)
      .sort()
      .map((type) => {
        const rows = byType[type]
          .map((a) => {
            const bal = balances[a.id] || { currency: a.currency, minor: 0 };
            return `<tr><td>${escapeHtml(a.id)}</td><td>${escapeHtml(a.name || "")}</td><td>${escapeHtml(a.currency)}</td><td class="num">${fmtAmount(bal)}</td></tr>`;
          })
          .join("");
        return `<h3>${escapeHtml(type)}</h3><div class="table-wrap"><table><thead><tr><th>Account ID</th><th>Name</th><th>Currency</th><th class="num">Balance</th></tr></thead><tbody>${rows}</tbody></table></div>`;
      })
      .join("");
    el.innerHTML = `<h2>Account balances</h2>${groups || '<p class="muted">No accounts.</p>'}`;
  },

  async loadTransactions() {
    const el = document.getElementById("tx-table");
    if (!el) return;
    el.innerHTML = '<p class="muted">Loading…</p>';
    const kind = (document.getElementById("tx-kind-filter") || {}).value || "";
    const accountId = (document.getElementById("tx-account-filter") || {}).value || "";
    const qs = new URLSearchParams();
    if (kind) qs.set("kind", kind);
    if (accountId) qs.set("account_id", accountId);
    const res = await GET(`/ledger/transactions${qs.toString() ? "?" + qs.toString() : ""}`);
    if (!res.ok) {
      el.innerHTML = errBox(res, "GET /ledger/transactions");
      return;
    }
    const txs = res.data || [];
    if (!txs.length) {
      el.innerHTML = '<p class="muted">No transactions match.</p>';
      return;
    }
    el.innerHTML = `<div class="table-wrap"><table><thead><tr><th>ID</th><th>Kind</th><th>Status</th><th>Entries</th><th>Occurred</th><th>Actions</th></tr></thead><tbody>
      ${txs
        .map((tx) => {
          const entries = (tx.entries || [])
            .map((e) => `${escapeHtml(e.account_id || e.AccountID)} ${escapeHtml(e.direction || e.Direction)} ${fmtAmount(e.amount || e.Amount)}`)
            .join("<br>");
          const actions = [];
          if (tx.status === "pending") actions.push(`<button onclick="Ledger.settle('${tx.id}')">Settle</button>`);
          if (tx.status === "settled") actions.push(`<button onclick="Ledger.finalize('${tx.id}')">Finalize</button>`);
          return `<tr><td>${escapeHtml(tx.id)}</td><td>${escapeHtml(tx.kind)}</td><td>${badge(tx.status)}</td><td>${entries}</td><td>${escapeHtml(fmtDate(tx.occurred_at))}</td><td>${actions.join(" ") || '<span class="muted">—</span>'}</td></tr>`;
        })
        .join("")}
    </tbody></table></div>`;
  },

  async settle(id) {
    const res = await POST(`/ledger/transactions/${encodeURIComponent(id)}/settle`);
    if (!res.ok) {
      setGlobalError("settle failed: " + errText(res));
    }
    await Ledger.loadTransactions();
    await Ledger.loadBalances();
    await refreshEffects();
  },

  async finalize(id) {
    const res = await POST(`/ledger/transactions/${encodeURIComponent(id)}/finalize`);
    if (!res.ok) {
      setGlobalError("finalize failed: " + errText(res));
    }
    await Ledger.loadTransactions();
    await Ledger.loadBalances();
    await refreshEffects();
  },
};
renderers.ledger = Ledger.render;

/* ==========================================================================
   Account & Purchases tab
   ========================================================================== */

const Acct = {
  lastReceipt: null,

  async render() {
    const panel = document.getElementById("tab-account");
    panel.innerHTML = `
      <div class="panel" id="acct-balances-panel"><p class="muted">Loading account…</p></div>

      <div class="grid cols-2">
        <div class="panel">
          <h2>Make a purchase</h2>
          <div class="inline-form">
            <label>Amount (USD)<input id="purchase-amount" type="number" min="0.01" step="0.01" value="25.00"></label>
            <label>Merchant<input id="purchase-merchant" value="Coffee Shop"></label>
            <button onclick="Acct.purchase()">Purchase</button>
          </div>
          <div id="purchase-result" style="margin-top:1em"></div>
        </div>

        <div class="panel">
          <h2>Bulk random purchases</h2>
          <div class="inline-form">
            <label>Count<input id="bulk-count" type="number" min="1" value="5"></label>
            <label>Min (USD)<input id="bulk-min" type="number" min="0.01" step="0.01" value="2.00"></label>
            <label>Max (USD)<input id="bulk-max" type="number" min="0.01" step="0.01" value="60.00"></label>
            <label>Seed (optional)<input id="bulk-seed" type="number"></label>
            <button onclick="Acct.bulkPurchase()">Run</button>
          </div>
          <div id="bulk-result" style="margin-top:1em"></div>
        </div>
      </div>

      <div class="panel">
        <h2>Deposit funds</h2>
        <div class="inline-form">
          <label>Currency<select id="deposit-currency"><option value="USD">USD</option><option value="USDC">USDC</option></select></label>
          <label>Amount<input id="deposit-amount" placeholder="100.00"></label>
          <button onclick="Acct.deposit()">Deposit</button>
        </div>
        <div id="deposit-result" style="margin-top:1em"></div>
      </div>

      <div class="panel">
        <h2>Purchase history</h2>
        <div id="purchase-history"><p class="muted">Loading…</p></div>
      </div>
    `;
    await Promise.all([Acct.loadAccount(), Acct.loadHistory()]);
  },

  async loadAccount() {
    const el = document.getElementById("acct-balances-panel");
    const res = await GET(`/accounts/${DEMO_ACCOUNT_ID}`);
    if (!res.ok) {
      el.innerHTML = errBox(res, `GET /accounts/${DEMO_ACCOUNT_ID}`);
      return;
    }
    const v = res.data || {};
    const balances = v.balances || {};
    el.innerHTML = `<h2>${escapeHtml(v.id || DEMO_ACCOUNT_ID)}</h2>
      <div class="grid cols-3">
        ${Object.entries(balances)
          .map(([cur, amt]) => tile(cur + " balance", fmtAmount(amt), "", cur === "USDC" ? "usdc" : "usd"))
          .join("")}
        ${tile("USD-equivalent total", usd(v.usd_equivalent_cents))}
      </div>`;
  },

  async purchase() {
    const amountUsd = parseFloat(document.getElementById("purchase-amount").value || "0");
    const merchant = document.getElementById("purchase-merchant").value || "";
    const amountCents = Math.round(amountUsd * 100);
    const resultEl = document.getElementById("purchase-result");
    resultEl.innerHTML = '<p class="muted">Submitting…</p>';
    const res = await POST("/simulate/purchase", { account_id: DEMO_ACCOUNT_ID, amount_cents: amountCents, merchant });
    const receipt = res.data && res.data.error ? res.data.details : res.data;
    if (!res.ok && res.status !== 409) {
      resultEl.innerHTML = errBox(res, "POST /simulate/purchase");
      return;
    }
    Acct.renderReceipt(resultEl, receipt, res.status === 409);
    await Acct.loadAccount();
    await Acct.loadHistory();
    await refreshEffects();
  },

  renderReceipt(el, receipt, declined) {
    if (!receipt || typeof receipt !== "object") {
      el.innerHTML = '<p class="muted">No receipt returned.</p>';
      return;
    }
    const wf = receipt.waterfall || {};
    el.innerHTML = `<div class="${declined ? "error-box" : "info-box"}">
      <h3>Receipt ${escapeHtml(receipt.purchase_id || "")} — ${badge(receipt.status)}</h3>
      <div>Amount: ${usd(receipt.amount_cents)} at ${escapeHtml(receipt.merchant || "")}</div>
      ${receipt.decline_reason ? `<div>Decline reason: ${escapeHtml(receipt.decline_reason)}</div>` : ""}
      <h3 style="margin-top:0.75em">Funded by</h3>
      ${proportionBar(receipt.funded_by)}
      <div class="muted" style="margin-top:0.5em">Matched rule: <b>${escapeHtml(wf.matched_rule || "n/a")}</b> — ${escapeHtml(wf.rule_id || "")} v${escapeHtml(wf.version)}</div>
    </div>`;
  },

  async bulkPurchase() {
    const count = parseInt(document.getElementById("bulk-count").value || "1", 10);
    const minCents = Math.round(parseFloat(document.getElementById("bulk-min").value || "0") * 100);
    const maxCents = Math.round(parseFloat(document.getElementById("bulk-max").value || "0") * 100);
    const seedRaw = document.getElementById("bulk-seed").value;
    const body = { account_id: DEMO_ACCOUNT_ID, count, min_cents: minCents, max_cents: maxCents };
    if (seedRaw) body.seed = parseInt(seedRaw, 10);
    const resultEl = document.getElementById("bulk-result");
    resultEl.innerHTML = '<p class="muted">Submitting…</p>';
    const res = await POST("/simulate/purchases/bulk", body);
    if (!res.ok) {
      resultEl.innerHTML = errBox(res, "POST /simulate/purchases/bulk");
      return;
    }
    const receipts = res.data || [];
    resultEl.innerHTML = `<div class="table-wrap"><table><thead><tr><th>ID</th><th>Status</th><th>Amount</th><th>Merchant</th></tr></thead><tbody>
      ${receipts.map((r) => `<tr><td>${escapeHtml(r.purchase_id)}</td><td>${badge(r.status)}</td><td class="num">${usd(r.amount_cents)}</td><td>${escapeHtml(r.merchant || "")}</td></tr>`).join("")}
    </tbody></table></div>`;
    await Acct.loadAccount();
    await Acct.loadHistory();
    await refreshEffects();
  },

  async deposit() {
    const currency = document.getElementById("deposit-currency").value;
    const amount = document.getElementById("deposit-amount").value;
    const resultEl = document.getElementById("deposit-result");
    resultEl.innerHTML = '<p class="muted">Submitting…</p>';
    const res = await api("POST", `/accounts/${DEMO_ACCOUNT_ID}/deposit`, { currency, amount });
    if (!res.ok) {
      resultEl.innerHTML = errBox(res, "POST /accounts/{id}/deposit");
      return;
    }
    resultEl.innerHTML = `<div class="info-box">Deposited. ${jsonBlock(res.data)}</div>`;
    await Acct.loadAccount();
    await refreshEffects();
  },

  async loadHistory() {
    const el = document.getElementById("purchase-history");
    if (!el) return;
    const res = await GET(`/accounts/${DEMO_ACCOUNT_ID}/purchases`);
    if (!res.ok) {
      el.innerHTML = errBox(res, "GET /accounts/{id}/purchases");
      return;
    }
    const receipts = res.data || [];
    if (!receipts.length) {
      el.innerHTML = '<p class="muted">No purchases yet.</p>';
      return;
    }
    el.innerHTML = `<div class="table-wrap"><table><thead><tr><th>ID</th><th>Status</th><th>Merchant</th><th class="num">Amount</th><th>Funded by</th><th>Occurred</th></tr></thead><tbody>
      ${receipts
        .map(
          (r) =>
            `<tr><td>${escapeHtml(r.purchase_id)}</td><td>${badge(r.status)}</td><td>${escapeHtml(r.merchant || "")}</td><td class="num">${usd(r.amount_cents)}</td><td>${(r.funded_by || []).map((f) => `${escapeHtml(f.currency)} ${f.share_pct}%`).join(", ")}</td><td>${escapeHtml(fmtDate(r.occurred_at))}</td></tr>`
        )
        .join("")}
    </tbody></table></div>`;
  },
};
renderers.account = Acct.render;

/* ==========================================================================
   Loans tab
   ========================================================================== */

const Loans = {
  applicants: [],
  loans: [],

  async render() {
    const panel = document.getElementById("tab-loans");
    panel.innerHTML = `
      <div class="panel">
        <h2>Underwrite</h2>
        <div class="inline-form">
          <label>Applicant<select id="uw-applicant"></select></label>
          <label>Principal (USD, optional)<input id="uw-principal" type="number" step="0.01"></label>
          <label>Term months (optional)<input id="uw-term" type="number"></label>
          <button onclick="Loans.underwrite()">Underwrite</button>
          <button class="secondary" onclick="Loans.originate()">Originate (if approved)</button>
        </div>
        <div id="underwrite-result" style="margin-top:1em"></div>
      </div>

      <div class="panel">
        <h2>Loans</h2>
        <div id="loans-table"><p class="muted">Loading…</p></div>
      </div>

      <div class="panel">
        <h2>Bank forecast</h2>
        <div id="forecast-panel"><p class="muted">Loading…</p></div>
      </div>
    `;
    await Promise.all([Loans.loadApplicants(), Loans.loadLoans(), Loans.loadForecast()]);
  },

  async loadApplicants() {
    const sel = document.getElementById("uw-applicant");
    const res = await GET("/loan/applicants");
    if (!res.ok) {
      sel.innerHTML = `<option>error: ${escapeHtml(errText(res))}</option>`;
      return;
    }
    Loans.applicants = res.data || [];
    sel.innerHTML = Loans.applicants.map((a) => `<option value="${escapeHtml(a.id)}">${escapeHtml(a.id)} — ${escapeHtml(a.name)} (score ${a.credit_score})</option>`).join("");
  },

  async underwrite() {
    const applicantId = document.getElementById("uw-applicant").value;
    const principalUsd = document.getElementById("uw-principal").value;
    const term = document.getElementById("uw-term").value;
    const body = { applicant_id: applicantId };
    if (principalUsd) body.requested_principal_cents = Math.round(parseFloat(principalUsd) * 100);
    if (term) body.term_months = parseInt(term, 10);
    const el = document.getElementById("underwrite-result");
    el.innerHTML = '<p class="muted">Submitting…</p>';
    const res = await POST("/loan/underwrite", body);
    if (!res.ok) {
      el.innerHTML = errBox(res, "POST /loan/underwrite");
      return;
    }
    const r = res.data || {};
    Loans.lastUnderwrite = { body, result: r };
    el.innerHTML = `<div class="info-box">
      <h3>Decision: ${badge(r.decision)}</h3>
      <div class="grid cols-3">
        ${tile("Risk score", r.risk ? r.risk.risk_score : "—", "tier " + (r.risk ? r.risk.tier : ""))}
        ${tile("Pricing", r.pricing ? r.pricing.annual_rate_bps + " bps" : "—", "tier " + (r.pricing ? r.pricing.tier : ""))}
        ${tile("Installment", usd(r.monthly_installment_cents), r.affordability ? r.affordability.flag : "")}
      </div>
      ${r.reasons && r.reasons.length ? `<div class="muted" style="margin-top:0.5em">Reasons: ${r.reasons.map(escapeHtml).join(", ")}</div>` : ""}
      <h3 style="margin-top:0.75em">Why this decision — rule applications</h3>
      <div class="table-wrap"><table><thead><tr><th>Rule</th><th>Version</th><th>Application ID</th></tr></thead><tbody>
        ${(r.rule_applications || []).map((ra) => `<tr><td>${escapeHtml(ra.rule_id)}</td><td>${ra.version}</td><td>${escapeHtml(ra.application_id)}</td></tr>`).join("")}
      </tbody></table></div>
    </div>`;
  },

  async originate() {
    if (!Loans.lastUnderwrite) {
      setGlobalError("run Underwrite first");
      return;
    }
    const { body } = Loans.lastUnderwrite;
    const originateBody = { applicant_id: body.applicant_id };
    if (body.requested_principal_cents) originateBody.principal_cents = body.requested_principal_cents;
    if (body.term_months) originateBody.term_months = body.term_months;
    const el = document.getElementById("underwrite-result");
    const res = await POST("/loan/originate", originateBody);
    if (!res.ok) {
      el.insertAdjacentHTML("beforeend", errBox(res, "POST /loan/originate"));
      return;
    }
    el.insertAdjacentHTML("beforeend", `<div class="info-box">Originated loan ${escapeHtml(res.data.id)}</div>`);
    await Loans.loadLoans();
    await Loans.loadForecast();
    await refreshEffects();
  },

  async loadLoans() {
    const el = document.getElementById("loans-table");
    if (!el) return;
    const res = await GET("/loans");
    if (!res.ok) {
      el.innerHTML = errBox(res, "GET /loans");
      return;
    }
    Loans.loans = res.data || [];
    if (!Loans.loans.length) {
      el.innerHTML = '<p class="muted">No loans yet.</p>';
      return;
    }
    el.innerHTML = `<div class="table-wrap"><table><thead><tr>
      <th>ID</th><th>Applicant</th><th>Status</th><th class="num">Principal</th><th class="num">Outstanding</th><th class="num">Rate (bps)</th><th>Tier</th><th>Missed</th><th>Actions</th>
    </tr></thead><tbody>
      ${Loans.loans
        .map(
          (l) => `<tr>
        <td>${escapeHtml(l.id)}</td><td>${escapeHtml(l.applicant_id)}</td><td>${badge(l.status)}</td>
        <td class="num">${usd(l.principal_cents)}</td><td class="num">${usd(l.outstanding_principal_cents)}</td>
        <td class="num">${l.annual_rate_bps}</td><td>${escapeHtml(l.tier)}</td><td>${l.missed_payments}</td>
        <td>
          <button ${l.status !== "active" ? "disabled" : ""} onclick="Loans.repay('${l.id}')">Repay</button>
          <button ${l.status !== "active" ? "disabled" : ""} onclick="Loans.missPayment('${l.id}')">Miss</button>
          <button class="danger" ${l.status !== "active" ? "disabled" : ""} onclick="Loans.defaultLoan('${l.id}')">Default</button>
          <button onclick="Loans.showSchedule('${l.id}')">Schedule</button>
          <button onclick="Loans.showWhatIf('${l.id}')">What-if</button>
        </td>
      </tr>`
        )
        .join("")}
    </tbody></table></div>`;
  },

  async repay(id) {
    const res = await POST(`/loans/${encodeURIComponent(id)}/repay`, {});
    if (!res.ok) setGlobalError("repay failed: " + errText(res));
    await Loans.loadLoans();
    await refreshEffects();
  },
  async missPayment(id) {
    const res = await POST(`/loans/${encodeURIComponent(id)}/miss-payment`, {});
    if (!res.ok) setGlobalError("miss-payment failed: " + errText(res));
    await Loans.loadLoans();
    await refreshEffects();
  },
  async defaultLoan(id) {
    const res = await POST(`/loans/${encodeURIComponent(id)}/default`, { reason: "manual demo trigger" });
    if (!res.ok) setGlobalError("default failed: " + errText(res));
    await Loans.loadLoans();
    await Loans.loadForecast();
    await refreshEffects();
  },

  async showSchedule(id) {
    const res = await GET(`/loans/${encodeURIComponent(id)}/schedule`);
    if (!res.ok) {
      openModal(errBox(res, "GET /loans/{id}/schedule"));
      return;
    }
    const sched = res.data || [];
    openModal(`<h2>Schedule — ${escapeHtml(id)}</h2>
      <div class="table-wrap"><table><thead><tr><th>#</th><th>Due</th><th class="num">Payment</th><th class="num">Principal</th><th class="num">Interest</th><th class="num">Remaining</th><th>Status</th></tr></thead><tbody>
      ${sched
        .map(
          (i) =>
            `<tr><td>${i.no}</td><td>${escapeHtml(fmtDate(i.due_date))}</td><td class="num">${usd(i.payment_cents)}</td><td class="num">${usd(i.principal_cents)}</td><td class="num">${usd(i.interest_cents)}</td><td class="num">${usd(i.remaining_principal_cents)}</td><td>${badge(i.status)}</td></tr>`
        )
        .join("")}
      </tbody></table></div>`);
  },

  async showWhatIf(id) {
    openModal(`<h2>What-if — ${escapeHtml(id)}</h2>
      <label>Extra monthly payment (USD)<input id="whatif-extra" type="number" step="0.01" value="50.00"></label>
      <button onclick="Loans.runWhatIf('${id}')">Run what-if</button>
      <div id="whatif-result" style="margin-top:1em"></div>`);
  },

  async runWhatIf(id) {
    const extraUsd = parseFloat(document.getElementById("whatif-extra").value || "0");
    const res = await POST(`/loans/${encodeURIComponent(id)}/what-if`, { extra_monthly_cents: Math.round(extraUsd * 100) });
    const el = document.getElementById("whatif-result");
    if (!res.ok) {
      el.innerHTML = errBox(res, "POST /loans/{id}/what-if");
      return;
    }
    const r = res.data || {};
    el.innerHTML = `<div class="grid cols-2">
      <div class="tile"><div class="tile-label">Baseline interest</div><div class="tile-value">${usd(r.baseline ? r.baseline.total_interest_cents : 0)}</div><div class="tile-sub">${r.baseline ? r.baseline.payoff_installments : "—"} installments</div></div>
      <div class="tile"><div class="tile-label">Scenario interest</div><div class="tile-value">${usd(r.scenario ? r.scenario.total_interest_cents : 0)}</div><div class="tile-sub">${r.scenario ? r.scenario.payoff_installments : "—"} installments</div></div>
    </div>
    <p>Interest saved: <b>${usd(r.interest_saved_cents)}</b> — Months saved: <b>${r.months_saved}</b></p>`;
  },

  async loadForecast() {
    const el = document.getElementById("forecast-panel");
    if (!el) return;
    const res = await GET("/loans/forecast/bank");
    if (!res.ok) {
      el.innerHTML = errBox(res, "GET /loans/forecast/bank");
      return;
    }
    const f = res.data || {};
    const cohorts = (f.cohorts || [])
      .map(
        (c) =>
          `<tr><td>${escapeHtml(c.month)}</td><td class="num">${c.loans}</td><td class="num">${usd(c.originated_cents)}</td><td class="num">${usd(c.outstanding_cents)}</td><td class="num">${usd(c.repaid_cents)}</td><td class="num">${c.defaulted}</td><td class="num">${escapeHtml(c.default_rate_pct)}%</td></tr>`
      )
      .join("");
    el.innerHTML = `
      <div class="grid cols-2" style="margin-bottom:1em">
        ${tile("Expected loss", usd(f.expected_loss_cents))}
        ${tile("Portfolio yield", (f.portfolio_yield_bps || 0) + " bps")}
      </div>
      <div class="table-wrap"><table><thead><tr><th>Month</th><th class="num">Loans</th><th class="num">Originated</th><th class="num">Outstanding</th><th class="num">Repaid</th><th class="num">Defaulted</th><th class="num">Default rate</th></tr></thead><tbody>
      ${cohorts || '<tr><td colspan="7" class="muted">No cohorts yet.</td></tr>'}
      </tbody></table></div>`;
  },
};
renderers.loans = Loans.render;

/* ==========================================================================
   Simulation tab
   ========================================================================== */

const Sim = {
  async render() {
    const panel = document.getElementById("tab-simulation");
    panel.innerHTML = `
      <div class="panel">
        <h2>Advance time</h2>
        <div class="inline-form">
          <label>Days<input id="advance-days" type="number" value="1" min="1"></label>
          <button onclick="Sim.advance(1)">+1 day</button>
          <button onclick="Sim.advance(7)">+7 days</button>
          <button onclick="Sim.advance(30)">+30 days</button>
          <button onclick="Sim.advanceCustom()">Advance</button>
        </div>
      </div>

      <div class="panel">
        <h2>Trigger default</h2>
        <div class="inline-form">
          <label>Loan<select id="default-loan"></select></label>
          <button onclick="Sim.triggerDefault()">Trigger default</button>
        </div>
      </div>

      <div class="panel">
        <h2>Run scenario</h2>
        <div class="inline-form">
          <label>Preset<select id="preset-select"><option value="">-- choose --</option></select></label>
          <button onclick="Sim.loadPreset()">Load into editor</button>
          <button onclick="Sim.runScenario()">Run scenario</button>
        </div>
        <textarea id="scenario-json" rows="8" placeholder='{"steps": [...]}'></textarea>
      </div>

      <div class="panel">
        <h2>Results</h2>
        <div id="sim-results"><p class="muted">No actions run yet.</p></div>
      </div>
    `;
    await Promise.all([Sim.loadLoanPicker(), Sim.loadPresets()]);
  },

  async loadLoanPicker() {
    const sel = document.getElementById("default-loan");
    const res = await GET("/loans");
    if (!res.ok) {
      sel.innerHTML = `<option>error</option>`;
      return;
    }
    const loans = (res.data || []).filter((l) => l.status === "active");
    sel.innerHTML = loans.map((l) => `<option value="${escapeHtml(l.id)}">${escapeHtml(l.id)} (${escapeHtml(l.applicant_id)})</option>`).join("") || "<option>no active loans</option>";
  },

  async loadPresets() {
    const sel = document.getElementById("preset-select");
    const res = await GET("/simulate/presets");
    if (!res.ok) {
      sel.insertAdjacentHTML("beforeend", `<option disabled>error: ${escapeHtml(errText(res))}</option>`);
      return;
    }
    let presets = res.data;
    let entries = [];
    if (Array.isArray(presets)) {
      entries = presets.map((p, i) => [p.name || p.id || "preset " + (i + 1), p]);
    } else if (presets && typeof presets === "object") {
      entries = Object.entries(presets);
    }
    Sim._presets = entries;
    sel.innerHTML =
      '<option value="">-- choose --</option>' + entries.map(([name], i) => `<option value="${i}">${escapeHtml(name)}</option>`).join("");
  },

  loadPreset() {
    const sel = document.getElementById("preset-select");
    const idx = sel.value;
    if (idx === "" || !Sim._presets) return;
    const [, body] = Sim._presets[idx];
    document.getElementById("scenario-json").value = JSON.stringify(body, null, 2);
  },

  async advance(days) {
    document.getElementById("advance-days").value = days;
    await Sim.advanceCustom();
  },

  async advanceCustom() {
    const days = parseInt(document.getElementById("advance-days").value || "1", 10);
    const res = await POST("/simulate/advance-time", { days });
    Sim.showResult("POST /simulate/advance-time", res);
    await refreshEffects();
  },

  async triggerDefault() {
    const loanId = document.getElementById("default-loan").value;
    if (!loanId) {
      setGlobalError("choose a loan first");
      return;
    }
    const res = await POST("/simulate/trigger-default", { loan_id: loanId });
    Sim.showResult("POST /simulate/trigger-default", res);
    await refreshEffects();
  },

  async runScenario() {
    const raw = document.getElementById("scenario-json").value;
    let body;
    try {
      body = raw ? JSON.parse(raw) : { steps: [] };
    } catch (e) {
      setGlobalError("invalid JSON in scenario editor: " + e.message);
      return;
    }
    const res = await POST("/simulate/run-scenario", body);
    Sim.showResult("POST /simulate/run-scenario", res);
    await refreshEffects();
  },

  showResult(label, res) {
    const el = document.getElementById("sim-results");
    const box = res.ok ? "info-box" : "error-box";
    const html = `<div class="${box}"><h3>${escapeHtml(label)} — ${res.status}</h3>${jsonBlock(res.data)}</div>`;
    el.innerHTML = html + el.innerHTML;
  },
};
renderers.simulation = Sim.render;

/* ==========================================================================
   Rules Editor tab
   ========================================================================== */

const RulesEditor = {
  rules: [],
  selectedId: null,
  liveRaw: "",
  liveVersion: null,
  pendingFailingExamples: null,

  async render() {
    const panel = document.getElementById("tab-rules");
    panel.innerHTML = `
      <div class="two-col">
        <div class="panel" id="rules-list"><p class="muted">Loading…</p></div>
        <div class="panel" id="rule-editor"><p class="muted">Select a rule on the left.</p></div>
      </div>
    `;
    await RulesEditor.loadList();
  },

  async loadList() {
    const el = document.getElementById("rules-list");
    const res = await GET("/rules");
    if (!res.ok) {
      el.innerHTML = errBox(res, "GET /rules");
      return;
    }
    RulesEditor.rules = (res.data && res.data.rules) || [];
    el.innerHTML =
      "<h2>Rules</h2>" +
      RulesEditor.rules
        .map(
          (r) =>
            `<div class="rule-list-item ${r.rule_id === RulesEditor.selectedId ? "selected" : ""}" onclick="RulesEditor.select('${r.rule_id}')">
          <div class="rid">${escapeHtml(r.rule_id)}</div>
          <div class="rmeta">${escapeHtml(r.domain)} · ${escapeHtml(r.owner)} · v${r.version} · reviewed ${escapeHtml(r.last_reviewed)}</div>
        </div>`
        )
        .join("");
  },

  async select(id) {
    RulesEditor.selectedId = id;
    RulesEditor.pendingFailingExamples = null;
    await RulesEditor.loadList();
    const el = document.getElementById("rule-editor");
    el.innerHTML = '<p class="muted">Loading rule…</p>';
    const res = await GET(`/rules/${encodeURIComponent(id)}`);
    if (!res.ok) {
      el.innerHTML = errBox(res, "GET /rules/{id}");
      return;
    }
    const r = res.data || {};
    RulesEditor.liveRaw = r.raw_yaml || "";
    RulesEditor.liveVersion = r.version;
    el.innerHTML = `
      <div class="card-header-row">
        <h2>${escapeHtml(id)} <span class="tag badge-live">LIVE v${r.version}</span><span class="tag" id="dirty-tag" style="display:none">DRAFT (unsaved)</span></h2>
      </div>
      <p class="muted">${escapeHtml((r.meta && r.meta.description) || "")}</p>
      <textarea id="rule-yaml-editor" rows="18" oninput="RulesEditor.onEdit()">${escapeHtml(r.raw_yaml || "")}</textarea>
      <div class="inline-form" style="margin-top:0.75em">
        <label>Change note<input id="rule-change-note" placeholder="what changed and why"></label>
        <button onclick="RulesEditor.validate()">Validate</button>
        <button onclick="RulesEditor.save(false)">Save as new version</button>
      </div>
      <div id="rule-action-result" style="margin-top:1em"></div>

      <h3 style="margin-top:1.25em">Version history</h3>
      <div id="rule-versions"><p class="muted">Loading…</p></div>
    `;
    await RulesEditor.loadVersions(id);
  },

  onEdit() {
    const ta = document.getElementById("rule-yaml-editor");
    const tag = document.getElementById("dirty-tag");
    if (!ta || !tag) return;
    tag.style.display = ta.value !== RulesEditor.liveRaw ? "inline-block" : "none";
  },

  async validate() {
    const id = RulesEditor.selectedId;
    const yaml = document.getElementById("rule-yaml-editor").value;
    const changeNote = document.getElementById("rule-change-note").value;
    const el = document.getElementById("rule-action-result");
    el.innerHTML = '<p class="muted">Validating…</p>';
    const res = await POST(`/rules/${encodeURIComponent(id)}/validate`, { yaml, change_note: changeNote, allow_failing_examples: false });
    if (!res.ok) {
      el.innerHTML = errBox(res, "POST /rules/{id}/validate");
      return;
    }
    const r = res.data || {};
    el.innerHTML = `<div class="${r.valid ? "info-box" : "error-box"}">
      <h3>Validation: ${r.valid ? "valid" : "invalid"}</h3>
      ${r.problems && r.problems.length ? `<ul>${r.problems.map((p) => `<li>${escapeHtml(p)}</li>`).join("")}</ul>` : ""}
      <h4>Examples</h4>
      ${RulesEditor.examplesTable(r.examples)}
      <h4>Diff vs live</h4>
      ${diffView(r.diff)}
    </div>`;
  },

  examplesTable(examples) {
    if (!examples || !examples.length) return '<p class="muted">No examples.</p>';
    return `<div class="table-wrap"><table><thead><tr><th>Name</th><th>Passed</th><th>Expected</th><th>Actual</th><th>Error</th></tr></thead><tbody>
      ${examples
        .map(
          (e) =>
            `<tr><td>${escapeHtml(e.name)}</td><td>${badge(e.passed ? "ok" : "critical", e.passed ? "pass" : "fail")}</td><td>${jsonBlock(e.expected)}</td><td>${jsonBlock(e.actual)}</td><td>${escapeHtml(e.error || "")}</td></tr>`
        )
        .join("")}
    </tbody></table></div>`;
  },

  async save(allowFailing) {
    const id = RulesEditor.selectedId;
    const yaml = document.getElementById("rule-yaml-editor").value;
    const changeNote = document.getElementById("rule-change-note").value;
    const el = document.getElementById("rule-action-result");
    el.innerHTML = '<p class="muted">Saving…</p>';
    const res = await PUT(`/rules/${encodeURIComponent(id)}`, { yaml, change_note: changeNote, allow_failing_examples: !!allowFailing });
    if (res.ok) {
      const r = res.data || {};
      el.innerHTML = `<div class="info-box">
        <h3>Saved as version ${r.version} (previous ${r.previous})</h3>
        <h4>Examples</h4>${RulesEditor.examplesTable(r.examples)}
        <h4>Diff</h4>${diffView(r.diff)}
      </div>`;
      RulesEditor.liveRaw = yaml;
      RulesEditor.liveVersion = r.version;
      const tag = document.getElementById("dirty-tag");
      if (tag) tag.style.display = "none";
      await RulesEditor.select(id);
      await refreshEffects();
      return;
    }
    if (res.status === 422) {
      const problems = (res.data && res.data.details) || [];
      el.innerHTML = `<div class="error-box"><h3>Validation failed (422)</h3><ul>${(Array.isArray(problems) ? problems : [problems]).map((p) => `<li>${escapeHtml(typeof p === "string" ? p : JSON.stringify(p))}</li>`).join("")}</ul></div>`;
      return;
    }
    if (res.status === 409) {
      const details = (res.data && res.data.details) || {};
      const examples = details.examples || [];
      el.innerHTML = `<div class="error-box">
        <h3>Examples failed (409) — saved version not created</h3>
        ${RulesEditor.examplesTable(examples)}
        <button onclick="RulesEditor.save(true)">Save anyway (accept failing examples)</button>
      </div>`;
      return;
    }
    el.innerHTML = errBox(res, "PUT /rules/{id}");
  },

  async loadVersions(id) {
    const el = document.getElementById("rule-versions");
    const res = await GET(`/rules/${encodeURIComponent(id)}/versions`);
    if (!res.ok) {
      el.innerHTML = errBox(res, "GET /rules/{id}/versions");
      return;
    }
    const versions = (res.data && res.data.versions) || [];
    el.innerHTML = `<div class="table-wrap"><table><thead><tr><th>Version</th><th>Effective from</th><th>Author</th><th>Change note</th><th>Actions</th></tr></thead><tbody>
      ${versions
        .map(
          (v) =>
            `<tr>
          <td>${v.version} ${v.version === RulesEditor.liveVersion ? badge("live", "LIVE") : ""}</td>
          <td>${escapeHtml(fmtDate(v.effective_from))}</td>
          <td>${escapeHtml(v.author || "")}</td>
          <td>${escapeHtml(v.change_note || "")}</td>
          <td><button ${v.version === RulesEditor.liveVersion ? "disabled" : ""} onclick="RulesEditor.reactivate(${v.version})">Reactivate</button></td>
        </tr>`
        )
        .join("")}
    </tbody></table></div>`;
  },

  async reactivate(version) {
    const id = RulesEditor.selectedId;
    const note = prompt("Change note for reactivating version " + version + "?", "reactivate v" + version) || "";
    const res = await POST(`/rules/${encodeURIComponent(id)}/reactivate`, { version, change_note: note });
    if (!res.ok) {
      setGlobalError("reactivate failed: " + errText(res));
      return;
    }
    await RulesEditor.select(id);
    await refreshEffects();
  },
};
renderers.rules = RulesEditor.render;

/* ==========================================================================
   Demo Mode tab
   ========================================================================== */

const Demo = {
  scenario: null,

  async render() {
    const panel = document.getElementById("tab-demo");
    panel.innerHTML = `<p class="muted">Loading scenarios…</p>`;
    const res = await GET("/demo/scenarios");
    if (!res.ok) {
      panel.innerHTML = errBox(res, "GET /demo/scenarios");
      return;
    }
    const scenarios = res.data || [];
    if (!scenarios.length) {
      panel.innerHTML = '<p class="muted">No demo scenarios configured.</p>';
      return;
    }
    Demo.scenario = scenarios[0];
    Demo.renderScenario();
  },

  renderScenario() {
    const panel = document.getElementById("tab-demo");
    const s = Demo.scenario || {};
    const steps = s.steps || [];
    const ranSteps = new Set(s.ran_steps || []);
    const rows = steps
      .map((step, i) => {
        const alreadyRan = ranSteps.has(step.id);
        // A step is runnable once the previous step in the list has run
        // (ran_steps / next_step_id chain); the first step is always
        // runnable; reset always re-enables everything.
        const canRun = i === 0 || ranSteps.has(steps[i - 1].id) || alreadyRan;
        return `<div class="step-row ${alreadyRan ? "done" : ""}">
          <button ${canRun ? "" : "disabled"} onclick="Demo.runStep('${escapeHtml(step.id)}')">${alreadyRan ? "✓ " : ""}${escapeHtml(step.label || step.id)}</button>
          <span class="narration">${escapeHtml(step.narration || "")}</span>
        </div>`;
      })
      .join("");

    panel.innerHTML = `
      <div class="panel">
        <div class="card-header-row">
          <h2>${escapeHtml(s.description || s.scenario_id || "Demo scenario")}</h2>
          <div>
            <button onclick="Demo.start()">Start</button>
            <button onclick="Demo.reset()">Reset</button>
          </div>
        </div>
        <div class="step-list">${rows || '<p class="muted">No steps.</p>'}</div>
      </div>
      <div class="panel">
        <h2>Last result</h2>
        <div id="demo-result"><p class="muted">Run a step to see its result here.</p></div>
      </div>
    `;
  },

  async start() {
    const id = Demo.scenario && (Demo.scenario.scenario_id || Demo.scenario.id);
    const res = await POST(`/demo/scenarios/${encodeURIComponent(id)}/start`);
    Demo.showResult("start", res);
    await Demo.reload();
  },

  async reset() {
    const id = Demo.scenario && (Demo.scenario.scenario_id || Demo.scenario.id);
    const res = await POST(`/demo/scenarios/${encodeURIComponent(id)}/reset`);
    Demo.showResult("reset", res);
    await Demo.reload();
    await refreshEffects();
  },

  async runStep(stepId) {
    const id = Demo.scenario && (Demo.scenario.scenario_id || Demo.scenario.id);
    const res = await POST(`/demo/scenarios/${encodeURIComponent(id)}/steps/${encodeURIComponent(stepId)}/run`);
    Demo.showResult("run " + stepId, res);
    await Demo.reload();
    await refreshEffects();
  },

  async reload() {
    const id = Demo.scenario && (Demo.scenario.scenario_id || Demo.scenario.id);
    const res = await GET(`/demo/scenarios/${encodeURIComponent(id)}`);
    if (res.ok && res.data) {
      Demo.scenario = res.data;
    } else {
      // fall back to the list endpoint if the single-scenario GET isn't ready
      const listRes = await GET("/demo/scenarios");
      if (listRes.ok && Array.isArray(listRes.data)) {
        const match = listRes.data.find((sc) => (sc.scenario_id || sc.id) === id);
        if (match) Demo.scenario = match;
      }
    }
    Demo.renderScenario();
  },

  showResult(label, res) {
    const el = document.getElementById("demo-result");
    if (!el) return;
    const box = res.ok ? "info-box" : "error-box";
    const r = res.data || {};
    let extra = "";
    if (r.action === "edit_rule" || (r.result && r.result.old_value !== undefined)) {
      const rr = r.result || r;
      extra = `<div class="muted">old → new: ${escapeHtml(JSON.stringify(rr.old_value))} → ${escapeHtml(JSON.stringify(rr.new_value))}</div>`;
    }
    if (r.action === "call_api" || (r.result && r.result.status !== undefined)) {
      const rr = r.result || r;
      extra += `<div class="muted">HTTP status: ${escapeHtml(rr.status)}</div>`;
    }
    el.innerHTML = `<div class="${box}">
      <h3>${escapeHtml(label)} — ${res.status}</h3>
      ${r.narration ? `<p>${escapeHtml(r.narration)}</p>` : ""}
      ${extra}
      ${jsonBlock(r)}
    </div>` + el.innerHTML;
  },
};
renderers.demo = Demo.render;

/* ==========================================================================
   bootstrap
   ========================================================================== */

document.querySelectorAll(".tab-btn").forEach((btn) => {
  btn.addEventListener("click", () => showTab(btn.dataset.tab));
});
document.getElementById("refresh-all-btn").addEventListener("click", refreshAll);

(async function init() {
  await showTab("dashboard");
  await refreshClock();
})();
