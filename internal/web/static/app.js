// onWatch Dashboard JavaScript

const API_BASE = '';
const REFRESH_INTERVAL = 60000;

// ── Lazy Loading via IntersectionObserver ──
const _lazyLoaded = new Set();
function lazyLoadOnVisible(selector, callback) {
  const el = document.querySelector(selector);
  if (!el) return;
  if (typeof IntersectionObserver === 'undefined') { callback(); return; }
  const observer = new IntersectionObserver((entries) => {
    entries.forEach(entry => {
      if (entry.isIntersecting && !_lazyLoaded.has(selector)) {
        _lazyLoaded.add(selector);
        observer.unobserve(entry.target);
        callback();
      }
    });
  }, { rootMargin: '200px' });
  observer.observe(el);
}

// ── Auth helper: redirect to login on 401 ──
async function authFetch(url, options) {
  // Add CSRF protection header for state-changing requests
  options = options || {};
  const method = (options.method || 'GET').toUpperCase();
  if (method !== 'GET' && method !== 'HEAD') {
    options.headers = options.headers || {};
    if (!options.headers['X-Requested-With']) {
      options.headers['X-Requested-With'] = 'XMLHttpRequest';
    }
  }
  const res = await fetch(url, options);
  if (res.status === 401) {
    // Don't redirect if already on the login page (avoids infinite refresh loop)
    if (window.location.pathname !== '/login') {
      window.location.href = '/login';
    }
    throw new Error('Session expired');
  }
  return res;
}

// ── Provider State ──
function getCurrentProvider() {
  const bothView = document.getElementById('both-view');
  if (bothView) return 'both';
  const anthropicGrid = document.getElementById('quota-grid-anthropic');
  if (anthropicGrid) return 'anthropic';
  const copilotGrid = document.getElementById('quota-grid-copilot');
  if (copilotGrid) return 'copilot';
  const miniMaxGrid = document.getElementById('quota-grid-minimax');
  if (miniMaxGrid) return 'minimax';
  const grid = document.getElementById('quota-grid');
  return (grid && grid.dataset.provider) || 'synthetic';
}

function providerParam() {
  return `provider=${getCurrentProvider()}`;
}

// ── Global State ──
const State = {
  chart: null,
  chartSyn: null,
  chartZai: null,
  chartAnth: null,
  modalChart: null,
  countdownInterval: null,
  refreshInterval: null,
  currentQuotas: {},
  // Table data caches
  allCyclesData: [],
  allSessionsData: [],
  // Cycles table state
  cyclesSort: { key: null, dir: 'desc' },
  cyclesPage: 1,
  cyclesPageSize: 10,
  cyclesRange: 259200000,   // 3 days in ms (default)
  cyclesQuotaNames: [],     // dynamic quota column names
  // Sessions table state
  sessionsSort: { key: null, dir: 'desc' },
  sessionsPage: 1,
  sessionsPageSize: 10,
  // Expanded session
  expandedSessionId: null,
  // Dynamic Y-axis max (preserved across theme changes)
  chartYMax: 100,
  // Hidden quota datasets (persisted in localStorage)
  hiddenQuotas: new Set(),
  // Hidden insight keys (persisted in DB via settings API)
  hiddenInsights: new Set(),
  // Insights time range (1d / 7d / 30d)
  insightsRange: '7d',
  // Anthropic session column names (sorted, max 3 — mirrors backend positional mapping)
  anthropicSessionQuotas: [],
  // Cycle Overview state
  allOverviewData: [],
  overviewQuotaNames: [],
  overviewGroupBy: null,
  overviewSort: { key: null, dir: 'desc' },
  overviewPage: 1,
  overviewPageSize: 10,
};

// ── Persistence ──

function loadHiddenQuotas() {
  try {
    const stored = localStorage.getItem('onwatch-hidden-quotas');
    if (stored) {
      State.hiddenQuotas = new Set(JSON.parse(stored));
    }
  } catch (e) {
    // silent — localStorage read failure is non-critical
    State.hiddenQuotas = new Set();
  }
}

function saveHiddenQuotas() {
  try {
    localStorage.setItem('onwatch-hidden-quotas', JSON.stringify([...State.hiddenQuotas]));
  } catch (e) {
    // silent
  }
}

// ── Insight Visibility (DB-persisted) ──

// Cross-provider insight correlation map (mirrors backend)
const insightCorrelations = [
  ['cycle_utilization', 'token_budget'],
  ['tool_share', 'tool_breakdown'],
  ['trend', 'trend_24h'],
  ['weekly_pace', 'usage_7d'],
];

// Expand a set of hidden keys with all correlated keys
function expandCorrelatedKeys(keys) {
  const expanded = new Set(keys);
  for (const group of insightCorrelations) {
    if (group.some(k => expanded.has(k))) {
      group.forEach(k => expanded.add(k));
    }
  }
  return expanded;
}

// Get all correlated keys for a given key (returns array including the key itself)
function getCorrelatedKeys(key) {
  const related = [key];
  for (const group of insightCorrelations) {
    if (group.includes(key)) {
      group.forEach(k => { if (!related.includes(k)) related.push(k); });
    }
  }
  return related;
}

async function loadHiddenInsights() {
  try {
    const res = await authFetch(`${API_BASE}/api/settings`);
    if (res.ok) {
      const data = await res.json();
      if (data.hidden_insights && Array.isArray(data.hidden_insights)) {
        State.hiddenInsights = new Set(data.hidden_insights);
      }
    }
  } catch (e) {
    // silent
  }
}

async function saveHiddenInsights() {
  try {
    await authFetch(`${API_BASE}/api/settings`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ hidden_insights: [...State.hiddenInsights] })
    });
  } catch (e) {
    // silent
  }
}

async function toggleInsightVisibility(key) {
  const related = getCorrelatedKeys(key);
  const isHidden = State.hiddenInsights.has(key);
  related.forEach(k => {
    if (isHidden) State.hiddenInsights.delete(k);
    else State.hiddenInsights.add(k);
  });
  await saveHiddenInsights();
  fetchDeepInsights(); // Re-fetch (backend will filter)
}

async function unhideInsight(key) {
  const related = getCorrelatedKeys(key);
  related.forEach(k => State.hiddenInsights.delete(k));
  await saveHiddenInsights();
  fetchDeepInsights();
}

// ── Provider Persistence ──

function loadDefaultProvider() {
  try {
    return localStorage.getItem('onwatch-default-provider') || '';
  } catch (e) {
    return '';
  }
}

function saveDefaultProvider(provider) {
  try {
    localStorage.setItem('onwatch-default-provider', provider);
  } catch (e) {
    // silent
  }
}

function toggleQuotaVisibility(quotaType) {
  if (State.hiddenQuotas.has(quotaType)) {
    State.hiddenQuotas.delete(quotaType);
  } else {
    State.hiddenQuotas.add(quotaType);
  }
  saveHiddenQuotas();
  
  // Update chart if it exists
  if (State.chart) {
    updateChartVisibility();
  }
}

function updateChartVisibility() {
  if (getCurrentProvider() === 'both') return; // Both mode uses separate charts
  if (!State.chart) return;
  
  const provider = getCurrentProvider();
  const quotaMap = provider === 'zai'
    ? { 0: 'tokensLimit', 1: 'timeLimit', 2: 'toolCalls' }
    : { 0: 'subscription', 1: 'search', 2: 'toolCalls' };
  
  State.chart.data.datasets.forEach((ds, index) => {
    const quotaType = quotaMap[index];
    if (quotaType) {
      ds.hidden = State.hiddenQuotas.has(quotaType);
    }
  });
  
  // Recompute Y-axis based on visible datasets only
  State.chartYMax = computeYMax(State.chart.data.datasets, State.chart);
  State.chart.options.scales.y.max = State.chartYMax;
  State.chart.update('none'); // Update without animation
}

const statusConfig = {
  healthy: { label: 'Healthy', icon: 'M20 6L9 17l-5-5' },
  warning: { label: 'Warning', icon: 'M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0zM12 9v4M12 17h.01' },
  danger: { label: 'Danger', icon: 'M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0zM12 9v4M12 17h.01' },
  critical: { label: 'Critical', icon: 'M12 9v4M12 17h.01M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z' }
};

const quotaNames = {
  subscription: 'Subscription Quota',
  search: 'Search (Hourly)',
  toolCalls: 'Tool Call Discounts'
};

// Anthropic display names (mirrors backend anthropicDisplayNames)
const anthropicDisplayNames = {
  five_hour: '5-Hour Limit',
  seven_day: 'Weekly All-Model',
  seven_day_sonnet: 'Weekly Sonnet',
  monthly_limit: 'Monthly Limit',
  extra_usage: 'Extra Usage'
};

// Anthropic quota icons (mapped by key)
const anthropicQuotaIcons = {
  five_hour: '<circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/>',       // clock
  seven_day: '<rect x="3" y="4" width="18" height="18" rx="2"/><line x1="16" y1="2" x2="16" y2="6"/><line x1="8" y1="2" x2="8" y2="6"/><line x1="3" y1="10" x2="21" y2="10"/>', // calendar
  seven_day_sonnet: '<path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/>', // layers
  monthly_limit: '<rect x="2" y="7" width="20" height="14" rx="2" ry="2"/><path d="M16 21V5a2 2 0 0 0-2-2h-4a2 2 0 0 0-2 2v16"/>', // briefcase
  extra_usage: '<path d="M21.21 15.89A10 10 0 1 1 8 2.83"/><path d="M22 12A10 10 0 0 0 12 2v10z"/>' // pie-chart
};

// Anthropic chart colors keyed by quota name (stable regardless of which quotas exist)
const anthropicChartColorMap = {
  five_hour:        { border: '#D97757', bg: 'rgba(217, 119, 87, 0.08)' },   // coral
  seven_day:        { border: '#10B981', bg: 'rgba(16, 185, 129, 0.08)' },   // emerald
  seven_day_sonnet: { border: '#3B82F6', bg: 'rgba(59, 130, 246, 0.08)' },   // blue
  monthly_limit:    { border: '#A855F7', bg: 'rgba(168, 85, 247, 0.08)' },   // violet
  extra_usage:      { border: '#F59E0B', bg: 'rgba(245, 158, 11, 0.08)' }    // amber
};
const anthropicChartColorFallback = [
  { border: '#14B8A6', bg: 'rgba(20, 184, 166, 0.08)' },
  { border: '#EC4899', bg: 'rgba(236, 72, 153, 0.08)' }
];

// ── Copilot display names (mirrors backend CopilotDisplayName) ──
const copilotDisplayNames = {
  premium_interactions: 'Premium Requests',
  chat: 'Chat',
  completions: 'Completions'
};

// Copilot quota icons (mapped by key)
const copilotQuotaIcons = {
  premium_interactions: '<path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/>', // layers
  chat: '<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>', // message-square
  completions: '<polyline points="20 6 9 17 4 12"/>' // check
};

// Copilot chart colors keyed by quota name
const copilotChartColorMap = {
  premium_interactions: { border: '#6e40c9', bg: 'rgba(110, 64, 201, 0.08)' }, // GitHub purple
  chat:                 { border: '#2ea043', bg: 'rgba(46, 160, 67, 0.08)' },  // GitHub green
  completions:          { border: '#58a6ff', bg: 'rgba(88, 166, 255, 0.08)' }  // GitHub blue
};
const copilotChartColorFallback = [
  { border: '#f78166', bg: 'rgba(247, 129, 102, 0.08)' },
  { border: '#a371f7', bg: 'rgba(163, 113, 247, 0.08)' }
];

// ── MiniMax display names/colors ──
const miniMaxDisplayNames = {
  'MiniMax-M2': 'MiniMax-M2',
  'MiniMax-Text-01': 'MiniMax-Text-01'
};

const miniMaxChartColorMap = {
  'MiniMax-M2': { border: '#0EA5E9', bg: 'rgba(14, 165, 233, 0.08)' },
  'MiniMax-Text-01': { border: '#22C55E', bg: 'rgba(34, 197, 94, 0.08)' }
};

const miniMaxChartColorFallback = [
  { border: '#A855F7', bg: 'rgba(168, 85, 247, 0.08)' },
  { border: '#F97316', bg: 'rgba(249, 115, 22, 0.08)' }
];

// ── Renewal Categories for Cycle Overview ──

const renewalCategories = {
  anthropic: [
    { label: '5-Hour', groupBy: 'five_hour' },
    { label: 'Weekly', groupBy: 'seven_day' },
    { label: 'Sonnet', groupBy: 'seven_day_sonnet' },
    { label: 'Extra', groupBy: 'extra_usage' }
  ],
  synthetic: [
    { label: 'Subscription', groupBy: 'subscription' },
    { label: 'Tool Calls', groupBy: 'toolcall' }
  ],
  zai: [
    { label: 'Tokens', groupBy: 'tokens' },
    { label: 'Time', groupBy: 'time' }
  ],
  copilot: [
    { label: 'Premium', groupBy: 'premium_interactions' },
    { label: 'Chat', groupBy: 'chat' },
    { label: 'Completions', groupBy: 'completions' }
  ],
  minimax: [
    { label: 'M2', groupBy: 'MiniMax-M2' },
    { label: 'Text-01', groupBy: 'MiniMax-Text-01' }
  ]
};

const overviewQuotaDisplayNames = {
  subscription: 'Subscription',
  toolcall: 'Tool Calls',
  tokens: 'Tokens',
  time: 'Time',
  five_hour: '5-Hour',
  seven_day: 'Weekly',
  seven_day_sonnet: 'Sonnet',
  monthly_limit: 'Monthly',
  extra_usage: 'Extra',
  premium_interactions: 'Premium',
  chat: 'Chat',
  completions: 'Completions',
  'MiniMax-M2': 'M2',
  'MiniMax-Text-01': 'Text-01'
};

// ── Anthropic Dynamic Card Rendering ──

function renderAnthropicQuotaCards(quotas, containerId) {
  const container = document.getElementById(containerId);
  if (!container) return;

  // Build cards for each quota
  container.innerHTML = quotas.map((q, i) => {
    const icon = anthropicQuotaIcons[q.name] || '<circle cx="12" cy="12" r="10"/><path d="M12 16v-4M12 8h.01"/>';
    const displayName = q.displayName || anthropicDisplayNames[q.name] || q.name;
    const utilPct = (q.utilization || 0).toFixed(1);
    const status = q.status || 'healthy';
    const statusCfg = statusConfig[status] || statusConfig.healthy;
    const countdownId = `countdown-anth-${q.name}`;
    const progressId = `progress-anth-${q.name}`;
    const percentId = `percent-anth-${q.name}`;
    const statusId = `status-anth-${q.name}`;
    const resetId = `reset-anth-${q.name}`;

    return `<article class="quota-card anthropic-card" data-quota="${q.name}" data-provider="anthropic" role="button" tabindex="0" aria-label="View ${displayName} details" style="animation-delay: ${i * 60}ms">
      <header class="card-header">
        <h2 class="quota-title">
          <svg class="quota-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">${icon}</svg>
          ${displayName}
        </h2>
        <span class="countdown" id="${countdownId}">${q.timeUntilResetSeconds > 0 ? formatDuration(q.timeUntilResetSeconds) : '--:--'}</span>
      </header>
      <div class="progress-stats">
        <span class="usage-percent" id="${percentId}">${utilPct}%</span>
        <span class="usage-fraction">Utilization</span>
      </div>
      <div class="progress-wrapper">
        <div class="progress-bar" role="progressbar" aria-valuenow="${Math.round(q.utilization || 0)}" aria-valuemin="0" aria-valuemax="100">
          <div class="progress-fill" id="${progressId}" style="width: ${utilPct}%" data-status="${status}"></div>
        </div>
      </div>
      <footer class="card-footer">
        <span class="status-badge" id="${statusId}" data-status="${status}">
          <svg class="status-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="${statusCfg.icon}"/></svg>
          ${statusCfg.label}
        </span>
        <span class="reset-time" id="${resetId}">${q.resetsAt ? 'Resets: ' + formatDateTime(q.resetsAt) : ''}</span>
      </footer>
    </article>`;
  }).join('');

  // Re-attach modal click handlers for new cards
  container.querySelectorAll('.quota-card[role="button"]').forEach(card => {
    const handler = () => {
      const providerCol = card.closest('.provider-column');
      const providerOverride = providerCol ? providerCol.dataset.provider : 'anthropic';
      openAnthropicModal(card.dataset.quota, providerOverride);
    };
    card.addEventListener('click', handler);
    card.addEventListener('keydown', e => {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handler(); }
    });
  });
}

function updateAnthropicCard(quota) {
  const key = `anth-${quota.name}`;
  const prev = State.currentQuotas[key];
  State.currentQuotas[key] = {
    percent: quota.utilization || 0,
    usage: quota.utilization || 0,
    limit: 100,
    status: quota.status || 'healthy',
    renewsAt: quota.resetsAt,
    timeUntilReset: quota.timeUntilReset,
    timeUntilResetSeconds: quota.timeUntilResetSeconds || 0,
    name: quota.name,
    displayName: quota.displayName
  };

  const progressEl = document.getElementById(`progress-anth-${quota.name}`);
  const percentEl = document.getElementById(`percent-anth-${quota.name}`);
  const statusEl = document.getElementById(`status-anth-${quota.name}`);
  const resetEl = document.getElementById(`reset-anth-${quota.name}`);
  const countdownEl = document.getElementById(`countdown-anth-${quota.name}`);

  const utilPct = (quota.utilization || 0).toFixed(1);
  const status = quota.status || 'healthy';

  if (progressEl) {
    progressEl.style.width = `${utilPct}%`;
    progressEl.setAttribute('data-status', status);
  }
  if (percentEl) {
    const oldVal = prev ? prev.percent : 0;
    if (Math.abs(oldVal - quota.utilization) > 0.2) {
      animateValue(percentEl, oldVal, quota.utilization, 400, v => `${v.toFixed(1)}%`);
    } else {
      percentEl.textContent = `${utilPct}%`;
    }
  }
  if (statusEl) {
    const config = statusConfig[status] || statusConfig.healthy;
    statusEl.setAttribute('data-status', status);
    statusEl.innerHTML = `<svg class="status-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="${config.icon}"/></svg>${config.label}`;
  }
  if (resetEl) {
    resetEl.textContent = quota.resetsAt ? `Resets: ${formatDateTime(quota.resetsAt)}` : '';
  }
  if (countdownEl) {
    if (quota.timeUntilResetSeconds > 0) {
      countdownEl.textContent = formatDuration(quota.timeUntilResetSeconds);
      countdownEl.classList.toggle('imminent', quota.timeUntilResetSeconds < 1800);
      countdownEl.style.display = '';
    } else {
      countdownEl.style.display = 'none';
    }
  }
}

// Anthropic quota detail modal
function openAnthropicModal(quotaName, providerOverride) {
  const key = `anth-${quotaName}`;
  const data = State.currentQuotas[key];
  if (!data) return;

  const modal = document.getElementById('detail-modal');
  const titleEl = document.getElementById('modal-title');
  const bodyEl = document.getElementById('modal-body');
  if (!modal || !bodyEl) return;

  const displayName = data.displayName || anthropicDisplayNames[quotaName] || quotaName;
  titleEl.textContent = displayName;

  const statusCfg = statusConfig[data.status] || statusConfig.healthy;
  const timeLeft = data.timeUntilResetSeconds > 0 ? formatDuration(data.timeUntilResetSeconds) : 'N/A';

  bodyEl.innerHTML = `
    <div class="modal-kpi-row">
      <div class="modal-kpi">
        <div class="modal-kpi-value">${data.percent.toFixed(1)}%</div>
        <div class="modal-kpi-label">Utilization</div>
      </div>
      <div class="modal-kpi">
        <div class="modal-kpi-value"><span class="status-badge" data-status="${data.status}"><svg class="status-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="${statusCfg.icon}"/></svg>${statusCfg.label}</span></div>
        <div class="modal-kpi-label">Status</div>
      </div>
      <div class="modal-kpi">
        <div class="modal-kpi-value">${timeLeft}</div>
        <div class="modal-kpi-label">Until Reset</div>
      </div>
    </div>
    <h3 class="modal-section-title">Usage History</h3>
    <div class="modal-chart-container">
      <canvas id="modal-chart"></canvas>
    </div>
    <h3 class="modal-section-title">Recent Cycles</h3>
    <div class="table-wrapper">
      <table class="data-table" id="modal-cycles-table">
        <thead><tr><th>Cycle</th><th>Duration</th><th>Peak %</th><th>Total %</th></tr></thead>
        <tbody id="modal-cycles-tbody"><tr><td colspan="4" class="empty-state">Loading...</td></tr></tbody>
      </table>
    </div>
  `;

  modal.hidden = false;
  document.getElementById('modal-close').focus();

  // Load chart and cycles for this Anthropic quota
  loadAnthropicModalChart(quotaName);
  loadAnthropicModalCycles(quotaName);
}

async function loadAnthropicModalChart(quotaName) {
  const ctx = document.getElementById('modal-chart');
  if (!ctx || typeof Chart === 'undefined') return;
  if (State.modalChart) { State.modalChart.destroy(); State.modalChart = null; }

  const activeRange = document.querySelector('.range-btn[data-range].active');
  const range = activeRange ? activeRange.dataset.range : '6h';

  try {
    const res = await authFetch(`${API_BASE}/api/history?range=${range}&provider=anthropic`);
    if (!res.ok) return;
    const data = await res.json();
    if (!Array.isArray(data) || data.length === 0) return;

    const colors = getThemeColors();
    const chartData = data.map(d => d[quotaName] || 0);
    const maxVal = Math.max(...chartData, 0);
    let yMax = maxVal <= 0 ? 10 : maxVal < 5 ? 10 : Math.min(Math.max(Math.ceil((maxVal * 1.2) / 5) * 5, 10), 100);

    State.modalChart = new Chart(ctx, {
      type: 'line',
      data: {
        labels: data.map(d => new Date(d.capturedAt).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' })),
        datasets: [(() => { const c = anthropicChartColorMap[quotaName] || { border: '#D97706', bg: 'rgba(217, 119, 6, 0.08)' }; return {
          label: anthropicDisplayNames[quotaName] || quotaName,
          data: chartData,
          borderColor: c.border,
          backgroundColor: c.bg,
          fill: true, tension: 0.3, borderWidth: 2.5, pointRadius: 0, pointHoverRadius: 5
        }; })()]
      },
      options: {
        responsive: true, maintainAspectRatio: false,
        plugins: {
          legend: { display: false },
          tooltip: { backgroundColor: colors.surfaceContainer, titleColor: colors.onSurface, bodyColor: colors.text, borderColor: colors.outline, borderWidth: 1, callbacks: { label: c => `${c.parsed.y.toFixed(1)}%` } }
        },
        scales: {
          x: { grid: { color: colors.grid, drawBorder: false }, ticks: { color: colors.text, maxTicksLimit: 6 } },
          y: { grid: { color: colors.grid, drawBorder: false }, ticks: { color: colors.text, callback: v => v + '%' }, min: 0, max: yMax }
        }
      }
    });
  } catch (err) { /* modal chart error — non-critical */ }
}

async function loadAnthropicModalCycles(quotaName) {
  try {
    const res = await authFetch(`${API_BASE}/api/cycles?type=${quotaName}&provider=anthropic`);
    if (!res.ok) return;
    const cycles = await res.json();
    const tbody = document.getElementById('modal-cycles-tbody');
    if (!tbody) return;
    const recent = cycles.slice(0, 5);
    if (recent.length === 0) {
      tbody.innerHTML = '<tr><td colspan="4" class="empty-state">No cycles yet.</td></tr>';
      return;
    }
    tbody.innerHTML = recent.map(cycle => {
      const start = new Date(cycle.cycleStart);
      const end = cycle.cycleEnd ? new Date(cycle.cycleEnd) : new Date();
      const durationMins = Math.round((end - start) / 60000);
      const isActive = !cycle.cycleEnd;
      return `<tr>
        <td>#${cycle.id}${isActive ? ' <span class="badge">Active</span>' : ''}</td>
        <td>${formatDurationMins(durationMins)}</td>
        <td>${formatNumber(cycle.peakUtilization || 0)}%</td>
        <td>${formatNumber(cycle.totalDelta || 0)}%</td>
      </tr>`;
    }).join('');
  } catch (err) { /* modal cycles error — non-critical */ }
}

// ── Copilot Dynamic Card Rendering ──

function renderMiniMaxQuotaCards(quotas, containerId) {
  const container = document.getElementById(containerId);
  if (!container) return;

  container.innerHTML = quotas.map((q, i) => {
    const displayName = q.displayName || miniMaxDisplayNames[q.name] || q.name;
    const usagePct = (q.usedPercent || 0).toFixed(1);
    const status = q.status || 'healthy';
    const statusCfg = statusConfig[status] || statusConfig.healthy;
    const countdownId = `countdown-minimax-${q.name}`;
    const progressId = `progress-minimax-${q.name}`;
    const percentId = `percent-minimax-${q.name}`;
    const fractionId = `fraction-minimax-${q.name}`;
    const statusId = `status-minimax-${q.name}`;
    const resetId = `reset-minimax-${q.name}`;

    return `<article class="quota-card minimax-card" data-quota="${q.name}" data-provider="minimax" role="button" tabindex="0" aria-label="View ${displayName} details" style="animation-delay: ${i * 60}ms">
      <header class="card-header">
        <h2 class="quota-title">${displayName}</h2>
        <span class="countdown" id="${countdownId}">${q.timeUntilResetSeconds > 0 ? formatDuration(q.timeUntilResetSeconds) : '--:--'}</span>
      </header>
      <div class="progress-stats">
        <span class="usage-percent" id="${percentId}">${usagePct}%</span>
        <span class="usage-fraction" id="${fractionId}">${formatNumber(q.used)} / ${formatNumber(q.total)}</span>
      </div>
      <div class="progress-wrapper">
        <div class="progress-bar" role="progressbar" aria-valuenow="${Math.round(q.usedPercent || 0)}" aria-valuemin="0" aria-valuemax="100">
          <div class="progress-fill" id="${progressId}" style="width: ${usagePct}%" data-status="${status}"></div>
        </div>
      </div>
      <footer class="card-footer">
        <span class="status-badge" id="${statusId}" data-status="${status}">
          <svg class="status-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="${statusCfg.icon}"/></svg>
          ${statusCfg.label}
        </span>
        <span class="reset-time" id="${resetId}">${q.resetAt ? 'Resets: ' + formatDateTime(q.resetAt) : ''}</span>
      </footer>
    </article>`;
  }).join('');

  container.querySelectorAll('.quota-card[role="button"]').forEach(card => {
    const handler = () => {
      const providerCol = card.closest('.provider-column');
      const providerOverride = providerCol ? providerCol.dataset.provider : 'minimax';
      openModal(card.dataset.quota, providerOverride);
    };
    card.addEventListener('click', handler);
    card.addEventListener('keydown', e => {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handler(); }
    });
  });
}

function updateMiniMaxCard(quota) {
  const key = `minimax-${quota.name}`;
  const prev = State.currentQuotas[key];
  State.currentQuotas[key] = {
    percent: quota.usedPercent || 0,
    usage: quota.used,
    limit: quota.total,
    remain: quota.remain,
    status: quota.status || 'healthy',
    renewsAt: quota.resetAt,
    timeUntilReset: quota.timeUntilReset,
    timeUntilResetSeconds: quota.timeUntilResetSeconds || 0,
    name: quota.name,
    displayName: quota.displayName
  };

  const progressEl = document.getElementById(`progress-minimax-${quota.name}`);
  const percentEl = document.getElementById(`percent-minimax-${quota.name}`);
  const fractionEl = document.getElementById(`fraction-minimax-${quota.name}`);
  const statusEl = document.getElementById(`status-minimax-${quota.name}`);
  const resetEl = document.getElementById(`reset-minimax-${quota.name}`);
  const countdownEl = document.getElementById(`countdown-minimax-${quota.name}`);

  const usagePct = (quota.usedPercent || 0).toFixed(1);
  const status = quota.status || 'healthy';

  if (progressEl) {
    progressEl.style.width = `${usagePct}%`;
    progressEl.setAttribute('data-status', status);
  }
  if (percentEl) {
    const oldVal = prev ? prev.percent : 0;
    if (Math.abs(oldVal - (quota.usedPercent || 0)) > 0.2) {
      animateValue(percentEl, oldVal, quota.usedPercent || 0, 400, v => `${v.toFixed(1)}%`);
    } else {
      percentEl.textContent = `${usagePct}%`;
    }
  }
  if (fractionEl) {
    fractionEl.textContent = `${formatNumber(quota.used)} / ${formatNumber(quota.total)}`;
  }
  if (statusEl) {
    const config = statusConfig[status] || statusConfig.healthy;
    statusEl.setAttribute('data-status', status);
    statusEl.innerHTML = `<svg class="status-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="${config.icon}"/></svg>${config.label}`;
  }
  if (resetEl) {
    resetEl.textContent = quota.resetAt ? `Resets: ${formatDateTime(quota.resetAt)}` : '';
  }
  if (countdownEl) {
    if (quota.timeUntilResetSeconds > 0) {
      countdownEl.textContent = formatDuration(quota.timeUntilResetSeconds);
      countdownEl.classList.toggle('imminent', quota.timeUntilResetSeconds < 1800);
      countdownEl.style.display = '';
    } else {
      countdownEl.style.display = 'none';
    }
  }
}

function renderCopilotQuotaCards(quotas, containerId) {
  const container = document.getElementById(containerId);
  if (!container) return;

  // Build cards for each quota
  container.innerHTML = quotas.map((q, i) => {
    const icon = copilotQuotaIcons[q.name] || '<circle cx="12" cy="12" r="10"/><path d="M12 16v-4M12 8h.01"/>';
    const displayName = q.displayName || copilotDisplayNames[q.name] || q.name;
    const usagePct = (q.usagePercent || 0).toFixed(1);
    const status = q.status || 'healthy';
    const statusCfg = statusConfig[status] || statusConfig.healthy;
    const countdownId = `countdown-copilot-${q.name}`;
    const progressId = `progress-copilot-${q.name}`;
    const percentId = `percent-copilot-${q.name}`;
    const fractionId = `fraction-copilot-${q.name}`;
    const statusId = `status-copilot-${q.name}`;
    const resetId = `reset-copilot-${q.name}`;

    // Format the usage fraction (remaining / entitlement or ∞ for unlimited)
    let fractionText = '';
    if (q.unlimited) {
      fractionText = '∞ Unlimited';
    } else {
      const used = q.entitlement - q.remaining;
      fractionText = `${formatNumber(used)} / ${formatNumber(q.entitlement)}`;
    }

    return `<article class="quota-card copilot-card" data-quota="${q.name}" data-provider="copilot" role="button" tabindex="0" aria-label="View ${displayName} details" style="animation-delay: ${i * 60}ms">
      <header class="card-header">
        <h2 class="quota-title">
          <svg class="quota-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">${icon}</svg>
          ${displayName}
          ${q.unlimited ? '<span class="unlimited-badge">∞</span>' : ''}
        </h2>
        <span class="countdown" id="${countdownId}">${q.timeUntilResetSeconds > 0 ? formatDuration(q.timeUntilResetSeconds) : '--:--'}</span>
      </header>
      <div class="progress-stats">
        <span class="usage-percent" id="${percentId}">${q.unlimited ? '0' : usagePct}%</span>
        <span class="usage-fraction" id="${fractionId}">${fractionText}</span>
      </div>
      <div class="progress-wrapper">
        <div class="progress-bar" role="progressbar" aria-valuenow="${q.unlimited ? 0 : Math.round(q.usagePercent || 0)}" aria-valuemin="0" aria-valuemax="100">
          <div class="progress-fill" id="${progressId}" style="width: ${q.unlimited ? 0 : usagePct}%" data-status="${status}"></div>
        </div>
      </div>
      <footer class="card-footer">
        <span class="status-badge" id="${statusId}" data-status="${status}">
          <svg class="status-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="${statusCfg.icon}"/></svg>
          ${statusCfg.label}
        </span>
        <span class="reset-time" id="${resetId}">${q.resetDate ? 'Resets: ' + formatDateTime(q.resetDate) : ''}</span>
      </footer>
    </article>`;
  }).join('');

  // Re-attach modal click handlers for new cards
  container.querySelectorAll('.quota-card[role="button"]').forEach(card => {
    const handler = () => {
      const providerCol = card.closest('.provider-column');
      const providerOverride = providerCol ? providerCol.dataset.provider : 'copilot';
      openCopilotModal(card.dataset.quota, providerOverride);
    };
    card.addEventListener('click', handler);
    card.addEventListener('keydown', e => {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handler(); }
    });
  });
}

function updateCopilotCard(quota) {
  const key = `copilot-${quota.name}`;
  const prev = State.currentQuotas[key];
  State.currentQuotas[key] = {
    percent: quota.usagePercent || 0,
    usage: quota.entitlement - quota.remaining,
    limit: quota.entitlement,
    remaining: quota.remaining,
    unlimited: quota.unlimited,
    status: quota.status || 'healthy',
    renewsAt: quota.resetDate,
    timeUntilReset: quota.timeUntilReset,
    timeUntilResetSeconds: quota.timeUntilResetSeconds || 0,
    name: quota.name,
    displayName: quota.displayName
  };

  const progressEl = document.getElementById(`progress-copilot-${quota.name}`);
  const percentEl = document.getElementById(`percent-copilot-${quota.name}`);
  const fractionEl = document.getElementById(`fraction-copilot-${quota.name}`);
  const statusEl = document.getElementById(`status-copilot-${quota.name}`);
  const resetEl = document.getElementById(`reset-copilot-${quota.name}`);
  const countdownEl = document.getElementById(`countdown-copilot-${quota.name}`);

  const usagePct = quota.unlimited ? 0 : (quota.usagePercent || 0).toFixed(1);
  const status = quota.status || 'healthy';

  if (progressEl) {
    progressEl.style.width = `${usagePct}%`;
    progressEl.setAttribute('data-status', status);
  }
  if (percentEl) {
    const oldVal = prev ? prev.percent : 0;
    if (!quota.unlimited && Math.abs(oldVal - quota.usagePercent) > 0.2) {
      animateValue(percentEl, oldVal, quota.usagePercent, 400, v => `${v.toFixed(1)}%`);
    } else {
      percentEl.textContent = quota.unlimited ? '0%' : `${usagePct}%`;
    }
  }
  if (fractionEl) {
    if (quota.unlimited) {
      fractionEl.textContent = '∞ Unlimited';
    } else {
      const used = quota.entitlement - quota.remaining;
      fractionEl.textContent = `${formatNumber(used)} / ${formatNumber(quota.entitlement)}`;
    }
  }
  if (statusEl) {
    const config = statusConfig[status] || statusConfig.healthy;
    statusEl.setAttribute('data-status', status);
    statusEl.innerHTML = `<svg class="status-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="${config.icon}"/></svg>${config.label}`;
  }
  if (resetEl) {
    resetEl.textContent = quota.resetDate ? `Resets: ${formatDateTime(quota.resetDate)}` : '';
  }
  if (countdownEl) {
    if (quota.timeUntilResetSeconds > 0) {
      countdownEl.textContent = formatDuration(quota.timeUntilResetSeconds);
      countdownEl.classList.toggle('imminent', quota.timeUntilResetSeconds < 1800);
      countdownEl.style.display = '';
    } else {
      countdownEl.style.display = 'none';
    }
  }
}

// Copilot quota detail modal
function openCopilotModal(quotaName, providerOverride) {
  const key = `copilot-${quotaName}`;
  const data = State.currentQuotas[key];
  if (!data) return;

  const modal = document.getElementById('detail-modal');
  const titleEl = document.getElementById('modal-title');
  const bodyEl = document.getElementById('modal-body');
  if (!modal || !bodyEl) return;

  const displayName = data.displayName || copilotDisplayNames[quotaName] || quotaName;
  titleEl.textContent = displayName;

  const usagePct = data.unlimited ? 0 : (data.percent || 0).toFixed(1);
  const used = data.unlimited ? 0 : (data.limit - data.remaining);

  bodyEl.innerHTML = `
    <div class="modal-stats-grid">
      <div class="modal-stat">
        <span class="modal-stat-label">Usage</span>
        <span class="modal-stat-value">${usagePct}%</span>
      </div>
      <div class="modal-stat">
        <span class="modal-stat-label">Used / Total</span>
        <span class="modal-stat-value">${data.unlimited ? '∞' : formatNumber(used) + ' / ' + formatNumber(data.limit)}</span>
      </div>
      <div class="modal-stat">
        <span class="modal-stat-label">Status</span>
        <span class="modal-stat-value" data-status="${data.status}">${(statusConfig[data.status] || statusConfig.healthy).label}</span>
      </div>
      <div class="modal-stat">
        <span class="modal-stat-label">Resets In</span>
        <span class="modal-stat-value">${data.timeUntilResetSeconds > 0 ? formatDuration(data.timeUntilResetSeconds) : '--'}</span>
      </div>
    </div>
    <div class="modal-chart-container">
      <canvas id="modal-chart" height="200"></canvas>
    </div>
    <h4 class="modal-section-title">Recent Cycles</h4>
    <table class="modal-cycles-table">
      <thead><tr><th>Cycle</th><th>Duration</th><th>Peak Used</th><th>Total Delta</th></tr></thead>
      <tbody id="modal-cycles-tbody"><tr><td colspan="4">Loading...</td></tr></tbody>
    </table>
  `;

  modal.classList.add('open');
  document.body.classList.add('modal-open');
  modal.querySelector('.modal-close')?.focus();

  loadCopilotModalChart(quotaName);
  loadCopilotModalCycles(quotaName);
}

async function loadCopilotModalChart(quotaName) {
  try {
    const res = await authFetch(`${API_BASE}/api/history?range=6h&provider=copilot`);
    if (!res.ok) return;
    const history = await res.json();
    if (!history || history.length === 0) return;

    // Extract data points for this quota
    const data = history.filter(h => {
      if (!h.quotas) return false;
      return h.quotas.some(q => q.name === quotaName);
    }).map(h => {
      const q = h.quotas.find(q => q.name === quotaName);
      return { capturedAt: h.capturedAt, usagePercent: q ? q.usagePercent : 0 };
    });

    if (data.length === 0) return;

    const canvas = document.getElementById('modal-chart');
    if (!canvas) return;
    const ctx = canvas.getContext('2d');

    // Clean up existing chart if any
    if (State.modalChart) {
      State.modalChart.destroy();
      State.modalChart = null;
    }

    const colors = getThemeColors();
    const chartData = data.map(d => d.usagePercent);
    const maxVal = Math.max(...chartData, 10);
    const yMax = Math.min(Math.ceil(maxVal / 10) * 10 + 10, 110);

    State.modalChart = new Chart(ctx, {
      type: 'line',
      data: {
        labels: data.map(d => new Date(d.capturedAt).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' })),
        datasets: [(() => { const c = copilotChartColorMap[quotaName] || { border: '#6e40c9', bg: 'rgba(110, 64, 201, 0.08)' }; return {
          label: copilotDisplayNames[quotaName] || quotaName,
          data: chartData,
          borderColor: c.border,
          backgroundColor: c.bg,
          fill: true, tension: 0.3, borderWidth: 2.5, pointRadius: 0, pointHoverRadius: 5
        }; })()]
      },
      options: {
        responsive: true, maintainAspectRatio: false,
        plugins: {
          legend: { display: false },
          tooltip: { backgroundColor: colors.surfaceContainer, titleColor: colors.onSurface, bodyColor: colors.text, borderColor: colors.outline, borderWidth: 1, callbacks: { label: c => `${c.parsed.y.toFixed(1)}%` } }
        },
        scales: {
          x: { grid: { color: colors.grid, drawBorder: false }, ticks: { color: colors.text, maxTicksLimit: 6 } },
          y: { grid: { color: colors.grid, drawBorder: false }, ticks: { color: colors.text, callback: v => v + '%' }, min: 0, max: yMax }
        }
      }
    });
  } catch (err) { /* modal chart error — non-critical */ }
}

async function loadCopilotModalCycles(quotaName) {
  try {
    const res = await authFetch(`${API_BASE}/api/cycles?type=${quotaName}&provider=copilot`);
    if (!res.ok) return;
    const cycles = await res.json();
    const tbody = document.getElementById('modal-cycles-tbody');
    if (!tbody) return;
    const recent = cycles.slice(0, 5);
    if (recent.length === 0) {
      tbody.innerHTML = '<tr><td colspan="4" class="empty-state">No cycles yet.</td></tr>';
      return;
    }
    tbody.innerHTML = recent.map(cycle => {
      const start = new Date(cycle.cycleStart);
      const end = cycle.cycleEnd ? new Date(cycle.cycleEnd) : new Date();
      const durationMins = Math.round((end - start) / 60000);
      const isActive = !cycle.cycleEnd;
      return `<tr>
        <td>#${cycle.id}${isActive ? ' <span class="badge">Active</span>' : ''}</td>
        <td>${formatDurationMins(durationMins)}</td>
        <td>${formatNumber(cycle.peakUsed || 0)}</td>
        <td>${formatNumber(cycle.totalDelta || 0)}</td>
      </tr>`;
    }).join('');
  } catch (err) { /* modal cycles error — non-critical */ }
}

// ── Utilities ──

function formatDuration(seconds) {
  if (seconds < 0) return 'Resetting...';
  const totalHours = Math.floor(seconds / 3600);
  const d = Math.floor(totalHours / 24);
  const h = totalHours % 24;
  const m = Math.floor((seconds % 3600) / 60);
  const s = seconds % 60;
  if (d > 0 && h > 0) return `${d}d ${h}h`;
  if (d > 0) return `${d}d ${m}m`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return '< 1m';
}

function formatDurationMins(durationMins) {
  const days = Math.floor(durationMins / 1440);
  const hours = Math.floor((durationMins % 1440) / 60);
  const mins = durationMins % 60;
  if (days > 0 && hours > 0) return `${days}d ${hours}h`;
  if (days > 0) return `${days}d ${mins}m`;
  return `${hours}h ${mins}m`;
}

function formatNumber(num) {
  return num.toLocaleString('en-US', { maximumFractionDigits: 1 });
}

function formatDateTime(isoString) {
  const d = new Date(isoString);
  const opts = { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' };
  if (typeof getEffectiveTimezone === 'function') {
    opts.timeZone = getEffectiveTimezone();
  }
  return d.toLocaleString('en-US', opts);
}


function getThemeColors() {
  const style = getComputedStyle(document.documentElement);
  const isDark = document.documentElement.getAttribute('data-theme') !== 'light';
  return {
    grid: style.getPropertyValue('--border-light').trim() || (isDark ? '#2A2E37' : '#F0F1F3'),
    text: style.getPropertyValue('--text-muted').trim() || (isDark ? '#8891A0' : '#6B7280'),
    outline: style.getPropertyValue('--border-default').trim(),
    surfaceContainer: style.getPropertyValue('--surface-card').trim(),
    onSurface: style.getPropertyValue('--text-primary').trim(),
    isDark
  };
}

// ── Timezone Badge & Selector ──

// Active timezone (empty = browser default)
let activeTimezone = '';

// Legacy → canonical timezone aliases
const TZ_ALIASES = {
  'Asia/Calcutta': 'Asia/Kolkata',
  'US/Eastern': 'America/New_York',
  'US/Central': 'America/Chicago',
  'US/Mountain': 'America/Denver',
  'US/Pacific': 'America/Los_Angeles',
};

function normalizeTz(tz) { return TZ_ALIASES[tz] || tz; }

// Curated timezone list sorted by UTC offset (descending: east → west).
// India (Asia/Kolkata) is always present.
const TZ_LIST = (() => {
  const base = [
    { tz: 'Pacific/Auckland', label: 'Auckland' },
    { tz: 'Australia/Sydney', label: 'Sydney' },
    { tz: 'Asia/Tokyo', label: 'Tokyo' },
    { tz: 'Asia/Shanghai', label: 'Shanghai' },
    { tz: 'Asia/Singapore', label: 'Singapore' },
    { tz: 'Asia/Kolkata', label: 'India' },
    { tz: 'Asia/Dubai', label: 'Dubai' },
    { tz: 'Europe/Moscow', label: 'Moscow' },
    { tz: 'Europe/Istanbul', label: 'Istanbul' },
    { tz: 'Europe/Berlin', label: 'Berlin' },
    { tz: 'Europe/Paris', label: 'Paris' },
    { tz: 'Europe/London', label: 'London' },
    { tz: 'UTC', label: 'UTC' },
    { tz: 'America/Sao_Paulo', label: 'Sao Paulo' },
    { tz: 'America/New_York', label: 'New York' },
    { tz: 'America/Chicago', label: 'Chicago' },
    { tz: 'America/Denver', label: 'Denver' },
    { tz: 'America/Los_Angeles', label: 'Los Angeles' },
    { tz: 'Pacific/Honolulu', label: 'Honolulu' },
  ];
  // Insert user's browser timezone if not already in list (after normalization)
  const browserTz = normalizeTz(Intl.DateTimeFormat().resolvedOptions().timeZone);
  if (!base.some(e => e.tz === browserTz)) {
    const label = browserTz.split('/').pop().replace(/_/g, ' ');
    const off = tzOffsetMin(browserTz);
    let inserted = false;
    for (let i = 0; i < base.length; i++) {
      if (tzOffsetMin(base[i].tz) < off) {
        base.splice(i, 0, { tz: browserTz, label });
        inserted = true;
        break;
      }
    }
    if (!inserted) base.push({ tz: browserTz, label });
  }
  return base;
})();

function tzOffsetMin(tz) {
  try {
    const d = new Date();
    const parts = d.toLocaleString('en-US', { timeZone: tz, timeZoneName: 'shortOffset' }).split('GMT');
    if (parts.length < 2 || !parts[1]) return 0;
    const str = parts[1].trim();
    const m = str.match(/^([+-]?)(\d{1,2})(?::(\d{2}))?$/);
    if (!m) return 0;
    const sign = m[1] === '-' ? -1 : 1;
    return sign * (parseInt(m[2]) * 60 + parseInt(m[3] || '0'));
  } catch (e) { return 0; }
}

function getEffectiveTimezone() {
  return activeTimezone || normalizeTz(Intl.DateTimeFormat().resolvedOptions().timeZone);
}

function tzAbbr(tz) {
  try {
    return new Date().toLocaleTimeString('en-US', { timeZone: tz, timeZoneName: 'short' }).split(' ').pop();
  } catch (e) {
    return tz.split('/').pop();
  }
}

function findTzIndex(tz) {
  const normalized = normalizeTz(tz);
  const idx = TZ_LIST.findIndex(e => e.tz === normalized);
  return idx >= 0 ? idx : 0;
}

function initTimezoneBadge() {
  const badge = document.getElementById('timezone-badge');
  if (!badge) return;

  loadTimezoneFromAPI().then(() => {
    updateBadgeText(badge);
    badge.style.cursor = 'pointer';
    badge.addEventListener('click', (e) => {
      e.stopPropagation();
      toggleTzPicker(badge);
    });
  });
}

async function loadTimezoneFromAPI() {
  try {
    const res = await authFetch(`${API_BASE}/api/settings`);
    if (!res.ok) return;
    const data = await res.json();
    if (data.timezone) {
      activeTimezone = normalizeTz(data.timezone);
    }
  } catch (e) {}
}

function updateBadgeText(badge) {
  if (!badge) badge = document.getElementById('timezone-badge');
  if (!badge) return;
  const tz = getEffectiveTimezone();
  const entry = TZ_LIST.find(e => e.tz === tz);
  const label = entry ? entry.label : tz.split('/').pop().replace(/_/g, ' ');
  badge.textContent = `${label} (${tzAbbr(tz)})`;
  badge.title = tz;
}

function toggleTzPicker(badge) {
  let existing = document.getElementById('tz-picker');
  if (existing) { existing.remove(); return; }

  const picker = document.createElement('div');
  picker.id = 'tz-picker';
  picker.className = 'tz-picker';

  const list = document.createElement('div');
  list.className = 'tz-picker-list';

  const ITEM_H = 36;
  const VISIBLE = 7;
  const COPIES = 3;
  const totalItems = TZ_LIST.length;

  // Render 3 copies for infinite scroll illusion
  for (let copy = 0; copy < COPIES; copy++) {
    TZ_LIST.forEach((entry, i) => {
      const item = document.createElement('div');
      item.className = 'tz-picker-item';
      if (entry.tz === getEffectiveTimezone()) item.classList.add('active');
      item.dataset.tz = entry.tz;
      item.dataset.idx = i;
      const abbr = tzAbbr(entry.tz);
      item.innerHTML = `<span class="tz-picker-label">${entry.label}</span><span class="tz-picker-abbr">${abbr}</span>`;
      item.addEventListener('click', () => selectTz(entry.tz, picker, badge));
      list.appendChild(item);
    });
  }

  list.style.height = (VISIBLE * ITEM_H) + 'px';
  picker.appendChild(list);

  // Position below badge
  const rect = badge.getBoundingClientRect();
  picker.style.top = (rect.bottom + 4) + 'px';
  picker.style.right = (window.innerWidth - rect.right) + 'px';

  document.body.appendChild(picker);

  // Scroll to center current timezone in middle copy
  const activeIdx = findTzIndex(getEffectiveTimezone());
  const midStart = totalItems; // start of middle copy
  const targetScroll = (midStart + activeIdx) * ITEM_H - Math.floor(VISIBLE / 2) * ITEM_H;
  list.scrollTop = targetScroll;

  // Infinite scroll: snap to middle copy when reaching edges
  list.addEventListener('scroll', () => {
    const maxScroll = totalItems * COPIES * ITEM_H - list.clientHeight;
    if (list.scrollTop < totalItems * ITEM_H * 0.25) {
      list.scrollTop += totalItems * ITEM_H;
    } else if (list.scrollTop > totalItems * ITEM_H * 1.75) {
      list.scrollTop -= totalItems * ITEM_H;
    }
  });

  // Close on outside click
  function closeOutside(e) {
    if (!picker.contains(e.target) && e.target !== badge) {
      picker.remove();
      document.removeEventListener('click', closeOutside);
      document.removeEventListener('keydown', closeEsc);
    }
  }
  function closeEsc(e) {
    if (e.key === 'Escape') {
      picker.remove();
      document.removeEventListener('click', closeOutside);
      document.removeEventListener('keydown', closeEsc);
    }
  }
  setTimeout(() => {
    document.addEventListener('click', closeOutside);
    document.addEventListener('keydown', closeEsc);
  }, 0);
}

async function selectTz(tz, picker, badge) {
  activeTimezone = tz;
  updateBadgeText(badge);
  if (picker) picker.remove();
  try {
    await authFetch(`${API_BASE}/api/settings`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ timezone: tz })
    });
  } catch (e) {
    // silent
  }
}

// ── Theme ──

function initTheme() {
  const toggle = document.getElementById('theme-toggle');
  if (!toggle) return;
  toggle.addEventListener('click', () => {
    const current = document.documentElement.getAttribute('data-theme');
    const next = current === 'light' ? 'dark' : 'light';
    document.documentElement.setAttribute('data-theme', next);
    localStorage.setItem('onwatch-theme', next);
    if (State.chart) updateChartTheme();
  });
}

// ── Card Updates ──

function updateCard(quotaType, data, suffix) {
  const key = suffix ? `${quotaType}_${suffix}` : quotaType;
  const prev = State.currentQuotas[key];
  State.currentQuotas[key] = data;

  const idSuffix = suffix ? `${quotaType}-${suffix}` : quotaType;
  const progressEl = document.getElementById(`progress-${idSuffix}`);
  const fractionEl = document.getElementById(`fraction-${idSuffix}`);
  const percentEl = document.getElementById(`percent-${idSuffix}`);
  const statusEl = document.getElementById(`status-${idSuffix}`);
  const resetEl = document.getElementById(`reset-${idSuffix}`);
  const countdownEl = document.getElementById(`countdown-${idSuffix}`);

  if (progressEl) {
    progressEl.style.width = `${data.percent}%`;
    progressEl.setAttribute('data-status', data.status);
    progressEl.parentElement.setAttribute('aria-valuenow', Math.round(data.percent));
  }

  if (fractionEl) {
    fractionEl.textContent = `${formatNumber(data.usage)} / ${formatNumber(data.limit)}`;
  }

  if (percentEl) {
    // Animate percentage from old to new
    const oldVal = prev ? prev.percent : 0;
    const newVal = data.percent;
    if (Math.abs(oldVal - newVal) > 0.2) {
      animateValue(percentEl, oldVal, newVal, 400, v => `${v.toFixed(1)}%`);
    } else {
      percentEl.textContent = `${data.percent.toFixed(1)}%`;
    }
  }

  if (statusEl) {
    const config = statusConfig[data.status] || statusConfig.healthy;
    const prevStatus = statusEl.getAttribute('data-status');
    statusEl.setAttribute('data-status', data.status);
    statusEl.innerHTML = `<svg class="status-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="${config.icon}"/></svg>${config.label}`;
  }

  if (resetEl) {
    if (data.renewsAt && data.timeUntilReset !== 'N/A') {
      resetEl.textContent = `Resets: ${formatDateTime(data.renewsAt)}`;
      resetEl.style.display = '';
    } else {
      resetEl.textContent = '';
      resetEl.style.display = 'none';
    }
  }

  if (countdownEl) {
    if (data.timeUntilResetSeconds > 0) {
      countdownEl.textContent = formatDuration(data.timeUntilResetSeconds);
      countdownEl.classList.toggle('imminent', data.timeUntilResetSeconds < 1800);
      countdownEl.style.display = '';
    } else if (data.timeUntilReset === 'N/A') {
      countdownEl.style.display = 'none';
    } else {
      countdownEl.textContent = '< 1m';
      countdownEl.style.display = '';
    }
  }

  // Render per-tool breakdown for Z.ai Time Limit card
  const detailsEl = document.getElementById(`usage-details-${idSuffix}`);
  if (detailsEl && data.usageDetails && data.usageDetails.length > 0) {
    detailsEl.innerHTML = data.usageDetails.map(d =>
      `<div class="usage-detail-row">
        <span class="usage-detail-model">${d.modelCode || d.ModelCode}</span>
        <span class="usage-detail-count">${formatNumber(d.usage || d.Usage)}</span>
      </div>`
    ).join('');
    detailsEl.style.display = '';
  } else if (detailsEl) {
    detailsEl.style.display = 'none';
  }
}

function animateValue(el, from, to, duration, formatter) {
  const start = performance.now();
  function step(now) {
    const progress = Math.min((now - start) / duration, 1);
    const eased = 1 - Math.pow(1 - progress, 3); // ease-out cubic
    const val = from + (to - from) * eased;
    el.textContent = formatter(val);
    if (progress < 1) requestAnimationFrame(step);
  }
  requestAnimationFrame(step);
}

function startCountdowns() {
  if (State.countdownInterval) clearInterval(State.countdownInterval);
  State.countdownInterval = setInterval(() => {
    Object.keys(State.currentQuotas).forEach(type => {
      const data = State.currentQuotas[type];
      if (data && data.timeUntilResetSeconds > 0) {
        data.timeUntilResetSeconds--;
        const el = document.getElementById(`countdown-${type}`);
        if (el) {
          el.textContent = formatDuration(data.timeUntilResetSeconds);
          el.classList.toggle('imminent', data.timeUntilResetSeconds < 1800);
        }
      }
    });
  }, 1000);
}

// ── Data Fetching ──

async function fetchCurrent() {
  try {
    const res = await authFetch(`${API_BASE}/api/current?${providerParam()}`);
    if (!res.ok) throw new Error('Failed to fetch');
    const data = await res.json();

    requestAnimationFrame(() => {
      const provider = getCurrentProvider();
      if (provider === 'both') {
        // "both" response: { synthetic: {...}, zai: {...}, anthropic: {...} }
        if (data.synthetic) {
          updateCard('subscription', data.synthetic.subscription);
          updateCard('search', data.synthetic.search);
          updateCard('toolCalls', data.synthetic.toolCalls, 'syn');
        }
        if (data.zai) {
          updateCard('tokensLimit', data.zai.tokensLimit);
          updateCard('timeLimit', data.zai.timeLimit);
          updateCard('toolCalls', data.zai.toolCalls, 'zai');
        }
        if (data.anthropic && data.anthropic.quotas) {
          const container = document.getElementById('quota-grid-anthropic-both');
          if (container && container.children.length === 0) {
            renderAnthropicQuotaCards(data.anthropic.quotas, 'quota-grid-anthropic-both');
          } else {
            data.anthropic.quotas.forEach(q => updateAnthropicCard(q));
          }
        }
        if (data.copilot && data.copilot.quotas) {
          const container = document.getElementById('quota-grid-copilot-both');
          if (container && container.children.length === 0) {
            renderCopilotQuotaCards(data.copilot.quotas, 'quota-grid-copilot-both');
          } else {
            data.copilot.quotas.forEach(q => updateCopilotCard(q));
          }
        }
        if (data.minimax && data.minimax.quotas) {
          const container = document.getElementById('quota-grid-minimax-both');
          if (container && container.children.length === 0) {
            renderMiniMaxQuotaCards(data.minimax.quotas, 'quota-grid-minimax-both');
          } else {
            data.minimax.quotas.forEach(q => updateMiniMaxCard(q));
          }
        }
      } else if (provider === 'copilot') {
        // Copilot response: { capturedAt: ..., quotas: [...] }
        if (data.quotas) {
          const container = document.getElementById('quota-grid-copilot');
          if (container && container.children.length === 0) {
            renderCopilotQuotaCards(data.quotas, 'quota-grid-copilot');
          } else {
            data.quotas.forEach(q => updateCopilotCard(q));
          }
        }
      } else if (provider === 'minimax') {
        if (data.quotas) {
          const container = document.getElementById('quota-grid-minimax');
          if (container && container.children.length === 0) {
            renderMiniMaxQuotaCards(data.quotas, 'quota-grid-minimax');
          } else {
            data.quotas.forEach(q => updateMiniMaxCard(q));
          }
        }
      } else if (provider === 'anthropic') {
        // Anthropic response: { capturedAt: ..., quotas: [...] }
        if (data.quotas) {
          const container = document.getElementById('quota-grid-anthropic');
          if (container && container.children.length === 0) {
            renderAnthropicQuotaCards(data.quotas, 'quota-grid-anthropic');
          } else {
            data.quotas.forEach(q => updateAnthropicCard(q));
          }
          // Store sorted quota names for session table headers (mirrors backend positional mapping)
          if (State.anthropicSessionQuotas.length === 0) {
            State.anthropicSessionQuotas = data.quotas.map(q => q.name).sort().slice(0, 3);
            updateAnthropicSessionHeaders();
          }
        }
      } else if (provider === 'zai') {
        updateCard('tokensLimit', data.tokensLimit);
        updateCard('timeLimit', data.timeLimit);
        updateCard('toolCalls', data.toolCalls);
      } else {
        updateCard('subscription', data.subscription);
        updateCard('search', data.search);
        updateCard('toolCalls', data.toolCalls);
      }

      const lastUpdated = document.getElementById('last-updated');
      if (lastUpdated) {
        lastUpdated.textContent = `Last updated: ${new Date().toLocaleTimeString()}`;
      }

      const statusDot = document.getElementById('status-dot');
      if (statusDot) statusDot.classList.remove('stale');

    });
  } catch (err) {
    // fetch error — cards show fallback state
    const statusDot = document.getElementById('status-dot');
    if (statusDot) statusDot.classList.add('stale');
  }
}


// ── Anthropic Session Table Header Updates ──

// Mapping from sorted quota API keys to the 3 positional session columns (sub, search, tool)
// Backend sorts ActiveQuotaNames() alphabetically and maps first 3 to these DB columns.
const anthropicSessionSlots = ['sub', 'search', 'tool'];

function updateAnthropicSessionHeaders() {
  const quotas = State.anthropicSessionQuotas;
  if (!quotas || quotas.length === 0) return;

  for (let i = 0; i < 3; i++) {
    const el = document.getElementById(`anth-session-col-${i}`);
    if (el && quotas[i]) {
      const shortName = anthropicDisplayNames[quotas[i]] || quotas[i];
      // Remove trailing " Limit" for compact table headers
      const label = shortName.replace(/ Limit$/, '');
      el.innerHTML = `${label} % <span class="sort-arrow"></span>`;
    }
  }
}

// Get the display label for Anthropic session column by positional index (0, 1, 2)
function getAnthropicSessionLabel(idx) {
  const quotas = State.anthropicSessionQuotas;
  if (quotas && quotas[idx]) {
    const name = anthropicDisplayNames[quotas[idx]] || quotas[idx];
    return name.replace(/ Limit$/, '');
  }
  // Fallback labels if quota data hasn't loaded yet
  const fallbacks = ['5-Hour', 'Weekly', 'Weekly Sonnet'];
  return fallbacks[idx] || `Quota ${idx + 1}`;
}

// ── Deep Insights (Interactive Cards) ──

// Title-specific icons for insight cards (Feather/Lucide style)
const insightTitleIcons = {
  'Avg Cycle Utilization': '<circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/>', // clock/gauge
  '30-Day Usage': '<rect x="3" y="4" width="18" height="18" rx="2"/><line x1="16" y1="2" x2="16" y2="6"/><line x1="8" y1="2" x2="8" y2="6"/><line x1="3" y1="10" x2="21" y2="10"/>', // calendar
  'Weekly Pace': '<polyline points="23 6 13.5 15.5 8.5 10.5 1 18"/><polyline points="17 6 23 6 23 12"/>', // trending-up
  'Tool Call Share': '<path d="M21.21 15.89A10 10 0 1 1 8 2.83"/><path d="M22 12A10 10 0 0 0 12 2v10z"/>', // pie-chart
  'Session Avg': '<line x1="18" y1="20" x2="18" y2="10"/><line x1="12" y1="20" x2="12" y2="4"/><line x1="6" y1="20" x2="6" y2="14"/>', // bar-chart
  'Coverage': '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/>', // shield
  'High Variance': '<polyline points="22 12 18 12 15 21 9 3 6 12 2 12"/>', // activity
  'Usage Spread': '<line x1="12" y1="20" x2="12" y2="10"/><line x1="18" y1="20" x2="18" y2="4"/><line x1="6" y1="20" x2="6" y2="16"/>', // bar-chart-2
  'Consistent': '<line x1="5" y1="12" x2="19" y2="12"/>', // minus (steady)
  'Trend': '<polyline points="23 6 13.5 15.5 8.5 10.5 1 18"/><polyline points="17 6 23 6 23 12"/>', // trending-up
  'Getting Started': '<circle cx="12" cy="12" r="10"/><path d="M12 16v-4M12 8h.01"/>', // info
  // Z.ai-specific insight icons
  'Token Budget': '<circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/>', // clock/gauge
  'Token Rate': '<polyline points="23 6 13.5 15.5 8.5 10.5 1 18"/><polyline points="17 6 23 6 23 12"/>', // trending-up
  'Projected Usage': '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/>', // shield
  'Tool Breakdown': '<path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z"/>', // wrench
  'Time Budget': '<circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/>', // clock
  '24h Trend': '<polyline points="23 6 13.5 15.5 8.5 10.5 1 18"/><polyline points="17 6 23 6 23 12"/>', // trending-up
  '7-Day Usage': '<rect x="3" y="4" width="18" height="18" rx="2"/><line x1="16" y1="2" x2="16" y2="6"/><line x1="8" y1="2" x2="8" y2="6"/><line x1="3" y1="10" x2="21" y2="10"/>', // calendar
  'Plan Capacity': '<path d="M2 20h.01"/><path d="M7 20v-4"/><path d="M12 20v-8"/><path d="M17 20V8"/><path d="M22 4v16"/>', // signal/tiers
  'Tokens Per Call': '<path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/>', // layers
  'Top Tool': '<polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2"/>', // star
};

// Quota-type icons (used for live quota insight cards)
const quotaIcons = {
  subscription: '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M3 9h18M9 21V9"/>', // credit-card/subscription
  search: '<circle cx="11" cy="11" r="8"/><path d="M21 21l-4.35-4.35"/>', // search
  toolCalls: '<path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z"/>', // wrench
  tokensLimit: '<path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/>', // layers
  timeLimit: '<circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/>', // clock
  session: '<line x1="18" y1="20" x2="18" y2="10"/><line x1="12" y1="20" x2="12" y2="4"/><line x1="6" y1="20" x2="6" y2="14"/>', // bar-chart
};

// Severity fallback icons
const insightIcons = {
  positive: '<path d="M20 6L9 17l-5-5"/>',
  warning: '<path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0zM12 9v4M12 17h.01"/>',
  negative: '<circle cx="12" cy="12" r="10"/><path d="M15 9l-6 6M9 9l6 6"/>',
  info: '<circle cx="12" cy="12" r="10"/><path d="M12 16v-4M12 8h.01"/>'
};

async function fetchDeepInsights() {
  const panel = document.querySelector('.insights-panel');
  const statsEl = document.getElementById('insights-stats');
  const cardsEl = document.getElementById('insights-cards');
  if (!cardsEl) return;

  // Render range selector pills in the insights header (once)
  renderInsightsRangePills();

  try {
    const res = await authFetch(`${API_BASE}/api/insights?${providerParam()}&range=${State.insightsRange}`);
    if (!res.ok) throw new Error('Failed to fetch insights');
    const data = await res.json();

    const provider = getCurrentProvider();

    if (provider === 'both') {
      // "both" mode: render two separate provider boxes
      renderBothInsights(data, statsEl, cardsEl);
    } else {
      // Single provider mode
      const allStats = data.stats || [];
      let allInsights = data.insights || [];

      // Filter out hidden insights
      const expandedHidden = expandCorrelatedKeys(State.hiddenInsights);
      allInsights = allInsights.filter(i => !i.key || !expandedHidden.has(i.key));

      // Render stats
      if (statsEl) {
        statsEl.innerHTML = allStats.length > 0 ? allStats.map(s =>
          `<div class="insight-stat">
            <div class="insight-stat-value">${s.value}</div>
            <div class="insight-stat-label">${s.label}</div>
            ${s.sublabel ? `<div class="insight-stat-sublabel">${s.sublabel}</div>` : ''}
          </div>`
        ).join('') : '';
      }

      // Render insight cards
      renderInsightCards(cardsEl, allInsights);
    }

    // Render hidden insights badge
    renderHiddenInsightsBadge();
  } catch (err) {
    // insights fetch error — panel shows fallback state
    if (statsEl) statsEl.innerHTML = '';
    cardsEl.innerHTML = '<p class="insight-text">Unable to load insights.</p>';
  }
}

function renderInsightsRangePills() {
  const header = document.querySelector('.insights-panel .section-header');
  if (!header || header.querySelector('.insights-range-selector')) return;

  const selector = document.createElement('div');
  selector.className = 'range-selector insights-range-selector';
  selector.setAttribute('role', 'group');
  selector.setAttribute('aria-label', 'Insights time range');

  const ranges = [
    { value: '1d', label: '1d' },
    { value: '7d', label: '7d' },
    { value: '30d', label: '30d' },
  ];

  selector.innerHTML = ranges.map(r =>
    `<button class="range-btn ${r.value === State.insightsRange ? 'active' : ''}" data-insights-range="${r.value}">${r.label}</button>`
  ).join('');

  selector.addEventListener('click', (e) => {
    const btn = e.target.closest('[data-insights-range]');
    if (!btn) return;
    State.insightsRange = btn.dataset.insightsRange;
    selector.querySelectorAll('.range-btn').forEach(b => b.classList.remove('active'));
    btn.classList.add('active');
    fetchDeepInsights();
  });

  header.appendChild(selector);
}

function renderBothInsights(data, statsEl, cardsEl) {
  // Clear the single-mode containers
  if (statsEl) statsEl.innerHTML = '';

  const expandedHidden = expandCorrelatedKeys(State.hiddenInsights);

  let html = '';

  // Synthetic box
  if (data.synthetic) {
    const synStats = data.synthetic.stats || [];
    let synInsights = (data.synthetic.insights || []).filter(i => !i.key || !expandedHidden.has(i.key));
    html += `<div class="provider-insights-box" data-provider="synthetic">
      <h4 class="provider-insights-label">Synthetic</h4>
      <div class="insights-stats">${synStats.map(s =>
        `<div class="insight-stat">
          <div class="insight-stat-value">${s.value}</div>
          <div class="insight-stat-label">${s.label}</div>
          ${s.sublabel ? `<div class="insight-stat-sublabel">${s.sublabel}</div>` : ''}
        </div>`
      ).join('')}</div>
      <div class="insights-cards">${buildInsightCardsHTML(synInsights)}</div>
    </div>`;
  }

  // Z.ai box
  if (data.zai) {
    const zaiStats = data.zai.stats || [];
    let zaiInsights = (data.zai.insights || []).filter(i => !i.key || !expandedHidden.has(i.key));
    html += `<div class="provider-insights-box" data-provider="zai">
      <h4 class="provider-insights-label">Z.ai</h4>
      <div class="insights-stats">${zaiStats.map(s =>
        `<div class="insight-stat">
          <div class="insight-stat-value">${s.value}</div>
          <div class="insight-stat-label">${s.label}</div>
          ${s.sublabel ? `<div class="insight-stat-sublabel">${s.sublabel}</div>` : ''}
        </div>`
      ).join('')}</div>
      <div class="insights-cards">${buildInsightCardsHTML(zaiInsights)}</div>
    </div>`;
  }

  // Anthropic box
  if (data.anthropic) {
    const anthStats = data.anthropic.stats || [];
    let anthInsights = (data.anthropic.insights || []).filter(i => !i.key || !expandedHidden.has(i.key));
    html += `<div class="provider-insights-box" data-provider="anthropic">
      <h4 class="provider-insights-label">Anthropic</h4>
      <div class="insights-stats">${anthStats.map(s =>
        `<div class="insight-stat">
          <div class="insight-stat-value">${s.value}</div>
          <div class="insight-stat-label">${s.label}</div>
          ${s.sublabel ? `<div class="insight-stat-sublabel">${s.sublabel}</div>` : ''}
        </div>`
      ).join('')}</div>
      <div class="insights-cards">${buildInsightCardsHTML(anthInsights)}</div>
    </div>`;
  }

  // Copilot box
  if (data.copilot) {
    const copilotStats = data.copilot.stats || [];
    let copilotInsights = (data.copilot.insights || []).filter(i => !i.key || !expandedHidden.has(i.key));
    html += `<div class="provider-insights-box" data-provider="copilot">
      <h4 class="provider-insights-label">Copilot <span class="beta-badge">Beta</span></h4>
      <div class="insights-stats">${copilotStats.map(s =>
        `<div class="insight-stat">
          <div class="insight-stat-value">${s.value}</div>
          <div class="insight-stat-label">${s.label}</div>
          ${s.sublabel ? `<div class="insight-stat-sublabel">${s.sublabel}</div>` : ''}
        </div>`
      ).join('')}</div>
      <div class="insights-cards">${buildInsightCardsHTML(copilotInsights)}</div>
    </div>`;
  }

  cardsEl.innerHTML = html || '<p class="insight-text">No insights available.</p>';

  // Attach events to all insight cards within both boxes
  cardsEl.querySelectorAll('.insight-card').forEach(card => {
    attachInsightCardEvents(card, cardsEl);
  });
  cardsEl.querySelectorAll('.insight-eye-btn').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      toggleInsightVisibility(btn.dataset.key);
    });
  });
}

function buildInsightCardsHTML(insights) {
  if (insights.length === 0) return '<p class="insight-text">Keep tracking to see deep analytics.</p>';
  return insights.map((i, idx) => {
    const icon = insightTitleIcons[i.title] || (i.quotaType && quotaIcons[i.quotaType]) || insightIcons[i.severity] || insightIcons.info;
    const hideBtn = i.key ? `<button class="insight-eye-btn" data-key="${i.key}" aria-label="Hide this insight" title="Hide this insight">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>
      </button>` : '';
    return `<div class="insight-card severity-${i.severity}" data-insight-idx="${idx}" data-key="${i.key || ''}" role="button" tabindex="0">
      <div class="insight-card-header">
        <svg class="insight-card-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">${icon}</svg>
        <span class="insight-card-title">${i.title}</span>
        ${i.metric || i.sublabel ? `<span class="insight-card-values">${i.metric ? `<span class="insight-card-metric">${i.metric}</span>` : ''}${i.sublabel ? `<span class="insight-card-sublabel">${i.sublabel}</span>` : ''}</span>` : ''}
        ${hideBtn}
        <svg class="insight-card-chevron" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 9l6 6 6-6"/></svg>
      </div>
      <div class="insight-card-detail">
        <div class="insight-card-desc">${i.description}</div>
      </div>
    </div>`;
  }).join('');
}

function renderInsightCards(container, insights) {
  if (insights.length > 0) {
    container.innerHTML = buildInsightCardsHTML(insights);
    container.querySelectorAll('.insight-card').forEach(card => {
      attachInsightCardEvents(card, container);
    });
    container.querySelectorAll('.insight-eye-btn').forEach(btn => {
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        toggleInsightVisibility(btn.dataset.key);
      });
    });
  } else {
    container.innerHTML = '<p class="insight-text">Keep tracking to see deep analytics.</p>';
  }
}

function attachInsightCardEvents(card, container) {
  const toggle = (e) => {
    if (e.target.closest('.insight-eye-btn')) return;
    const wasExpanded = card.classList.contains('expanded');
    // Only collapse siblings within the same parent container
    const parent = card.closest('.insights-cards') || container;
    parent.querySelectorAll('.insight-card.expanded').forEach(c => c.classList.remove('expanded'));
    if (!wasExpanded) card.classList.add('expanded');
  };
  card.addEventListener('click', toggle);
  card.addEventListener('keydown', e => {
    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggle(e); }
  });
}

// ── Hidden Insights Badge ──

function renderHiddenInsightsBadge() {
  const panel = document.querySelector('.insights-panel');
  if (!panel) return;

  // Remove existing badge
  const existing = panel.querySelector('.hidden-insights-badge');
  if (existing) existing.remove();

  const hiddenCount = State.hiddenInsights.size;
  if (hiddenCount === 0) return;

  const badge = document.createElement('div');
  badge.className = 'hidden-insights-badge';
  badge.innerHTML = `
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="hidden-badge-icon">
      <path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94"/>
      <path d="M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19"/>
      <line x1="1" y1="1" x2="23" y2="23"/>
    </svg>
    <span>${hiddenCount} hidden</span>
    <button class="hidden-badge-show" title="Show all hidden insights">Show all</button>
  `;

  badge.querySelector('.hidden-badge-show').addEventListener('click', async () => {
    State.hiddenInsights.clear();
    await saveHiddenInsights();
    fetchDeepInsights();
  });

  // Insert after section header
  const header = panel.querySelector('.section-header');
  if (header) {
    header.after(badge);
  } else {
    panel.prepend(badge);
  }
}

// ── Chart: Crosshair Plugin ──

const crosshairPlugin = {
  id: 'crosshair',
  afterDraw(chart, args, options) {
    const { ctx, chartArea, tooltip } = chart;
    if (!tooltip || !tooltip.opacity || tooltip.dataPoints.length === 0) return;
    const x = tooltip.dataPoints[0].element.x;
    ctx.save();
    ctx.beginPath();
    ctx.setLineDash([4, 4]);
    ctx.strokeStyle = getComputedStyle(document.documentElement).getPropertyValue('--border-default').trim() || '#E5E7EB';
    ctx.lineWidth = 1;
    ctx.moveTo(x, chartArea.top);
    ctx.lineTo(x, chartArea.bottom);
    ctx.stroke();
    ctx.restore();
  }
};

// ── Chart Init & Update ──

function computeYMax(datasets, chart) {
  // Filter out hidden datasets — check both ds.hidden and chart metadata visibility
  const visibleDatasets = datasets.filter((ds, i) => {
    if (ds.hidden) return false;
    if (chart && chart.getDatasetMeta(i).hidden) return false;
    return ds.data && ds.data.length > 0;
  });

  // If no visible datasets, return default 10%
  if (visibleDatasets.length === 0) return 10;

  let maxVal = 0;
  visibleDatasets.forEach(ds => {
    ds.data.forEach(v => {
      const val = typeof v === 'number' ? v : 0;
      if (val > maxVal) maxVal = val;
    });
  });
  
  // If max is 0 or very low, show up to 10% to give visual context
  if (maxVal <= 0) return 10;
  if (maxVal < 5) return 10;
  
  // Add 30% headroom above the max value for better visualization
  // Round up to nearest 5 for cleaner axis labels
  const paddedMax = maxVal * 1.3;
  const yMax = Math.min(Math.max(Math.ceil(paddedMax / 5) * 5, 10), 100);

  return yMax;
}

function initChart() {
  if (getCurrentProvider() === 'both') return; // Both mode uses dual charts
  const ctx = document.getElementById('usage-chart');
  if (!ctx || typeof Chart === 'undefined') return;

  Chart.register(crosshairPlugin);

  const colors = getThemeColors();

  // Map dataset indices to quota types for visibility toggle
  const provider = getCurrentProvider();
  let quotaMap = ['subscription', 'search', 'toolCalls'];
  if (provider === 'zai') {
    quotaMap = ['tokensLimit', 'timeLimit', 'toolCalls'];
  } else if (provider === 'copilot') {
    quotaMap = ['premium_interactions', 'chat', 'completions'];
  } else if (provider === 'minimax') {
    quotaMap = ['MiniMax-M2', 'MiniMax-Text-01'];
  }

  const defaultDatasets = provider === 'minimax'
    ? [
        { label: 'M2', data: [], borderColor: miniMaxChartColorMap['MiniMax-M2'].border, backgroundColor: miniMaxChartColorMap['MiniMax-M2'].bg, fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4, hidden: State.hiddenQuotas.has('MiniMax-M2') },
        { label: 'Text-01', data: [], borderColor: miniMaxChartColorMap['MiniMax-Text-01'].border, backgroundColor: miniMaxChartColorMap['MiniMax-Text-01'].bg, fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4, hidden: State.hiddenQuotas.has('MiniMax-Text-01') }
      ]
    : [
        { label: 'Subscription', data: [], borderColor: getComputedStyle(document.documentElement).getPropertyValue('--chart-subscription').trim() || '#0D9488', backgroundColor: 'rgba(13, 148, 136, 0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4, hidden: State.hiddenQuotas.has('subscription') },
        { label: 'Search', data: [], borderColor: getComputedStyle(document.documentElement).getPropertyValue('--chart-search').trim() || '#F59E0B', backgroundColor: 'rgba(245, 158, 11, 0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4, hidden: State.hiddenQuotas.has('search') },
        { label: 'Tool Calls', data: [], borderColor: getComputedStyle(document.documentElement).getPropertyValue('--chart-toolcalls').trim() || '#3B82F6', backgroundColor: 'rgba(59, 130, 246, 0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4, hidden: State.hiddenQuotas.has('toolCalls') }
      ];

  State.chart = new Chart(ctx, {
    type: 'line',
    data: {
      labels: [],
      datasets: defaultDatasets
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      interaction: { mode: 'index', intersect: false },
      plugins: {
        legend: {
          labels: { color: colors.text, usePointStyle: true, boxWidth: 8 },
          onClick: function(e, legendItem, legend) {
            // Default toggle behavior
            const index = legendItem.datasetIndex;
            const ci = legend.chart;
            const meta = ci.getDatasetMeta(index);
            meta.hidden = meta.hidden === null ? !ci.data.datasets[index].hidden : null;
            ci.update('none');
            // Recalculate Y-axis based on visible datasets
            State.chartYMax = computeYMax(ci.data.datasets, ci);
            ci.options.scales.y.max = State.chartYMax;
            ci.update();
          }
        },
        tooltip: {
          mode: 'index',
          intersect: false,
          backgroundColor: colors.surfaceContainer || '#1E1E1E',
          titleColor: colors.onSurface || '#E6E1E5',
          bodyColor: colors.text || '#CAC4D0',
          borderColor: colors.outline || '#938F99',
          borderWidth: 1,
          padding: 12,
          displayColors: true,
          usePointStyle: true,
          callbacks: {
            label: function(ctx) {
              return `${ctx.dataset.label}: ${ctx.parsed.y.toFixed(1)}%`;
            }
          }
        }
      },
      scales: {
        x: { grid: { color: colors.grid, drawBorder: false }, ticks: { color: colors.text, maxTicksLimit: 6 } },
        y: { grid: { color: colors.grid, drawBorder: false }, ticks: { color: colors.text, callback: v => v + '%' }, min: 0, max: State.chartYMax }
      }
    }
  });
}

function updateChartTheme() {
  if (getCurrentProvider() === 'both') {
    // Re-fetch to rebuild both charts with new theme colors
    fetchHistory();
    return;
  }
  if (!State.chart) return;
  const colors = getThemeColors();
  const style = getComputedStyle(document.documentElement);

  // Update line colors for theme
  const chartColors = [
    style.getPropertyValue('--chart-subscription').trim() || '#0D9488',
    style.getPropertyValue('--chart-search').trim() || '#F59E0B',
    style.getPropertyValue('--chart-toolcalls').trim() || '#3B82F6'
  ];
  State.chart.data.datasets.forEach((ds, i) => {
    if (chartColors[i]) ds.borderColor = chartColors[i];
  });

  State.chart.options.scales.x.grid.color = colors.grid;
  State.chart.options.scales.x.ticks.color = colors.text;
  State.chart.options.scales.y.grid.color = colors.grid;
  State.chart.options.scales.y.ticks.color = colors.text;
  State.chart.options.scales.y.max = State.chartYMax;
  State.chart.options.plugins.legend.labels.color = colors.text;
  State.chart.options.plugins.tooltip.backgroundColor = colors.surfaceContainer;
  State.chart.options.plugins.tooltip.titleColor = colors.onSurface;
  State.chart.options.plugins.tooltip.bodyColor = colors.text;
  State.chart.options.plugins.tooltip.borderColor = colors.outline;
  State.chart.update('none');
}

async function fetchHistory(range) {
  if (range === undefined) {
    const activeBtn = document.querySelector('.range-btn[data-range].active');
    range = activeBtn ? activeBtn.dataset.range : '6h';
  }
  try {
    const res = await authFetch(`${API_BASE}/api/history?range=${range}&${providerParam()}`);
    if (!res.ok) throw new Error('Failed to fetch history');
    const data = await res.json();

    const provider = getCurrentProvider();

    if (provider === 'both') {
      // "both" response: { synthetic: [...], zai: [...], anthropic: [...], copilot: [...], minimax: [...] }
      updateBothCharts(data);
      return;
    }

    // Normalize flat history format to array-of-quotas format for dynamic providers.
    const normalizeDynamicHistory = (rows) => {
      if (!Array.isArray(rows)) return [];
      return rows.map(row => {
        if (Array.isArray(row.quotas)) return row;
        const quotas = Object.keys(row)
          .filter(k => k !== 'capturedAt')
          .map(name => ({ name, usagePercent: row[name], usedPercent: row[name] }));
        return { capturedAt: row.capturedAt, quotas };
      });
    };

    if (!State.chart) initChart();
    if (!State.chart) return;

    State.chart.data.labels = data.map(d => new Date(d.capturedAt).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' }));

    if (provider === 'anthropic') {
      // Anthropic history: array of { capturedAt, five_hour, seven_day, ... }
      // Dynamic datasets based on available quota keys
      const quotaKeys = new Set();
      data.forEach(d => {
        Object.keys(d).forEach(k => { if (k !== 'capturedAt') quotaKeys.add(k); });
      });
      const sortedKeys = [...quotaKeys].sort();
      let fallbackIdx = 0;
      State.chart.data.datasets = sortedKeys.map((key) => {
        const color = anthropicChartColorMap[key] || anthropicChartColorFallback[fallbackIdx++ % anthropicChartColorFallback.length];
        return {
          label: anthropicDisplayNames[key] || key,
          data: data.map(d => d[key] || 0),
          borderColor: color.border,
          backgroundColor: color.bg,
          fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4,
          hidden: State.hiddenQuotas.has(key)
        };
      });
      State.chart.data.labels = data.map(d => new Date(d.capturedAt).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' }));
      State.chartYMax = computeYMax(State.chart.data.datasets, State.chart);
      State.chart.options.scales.y.max = State.chartYMax;
      State.chart.update();
      return;
    }

    if (provider === 'copilot' || provider === 'minimax') {
      const isCopilot = provider === 'copilot';
      const displayNames = isCopilot ? copilotDisplayNames : miniMaxDisplayNames;
      const colorMap = isCopilot ? copilotChartColorMap : miniMaxChartColorMap;
      const fallbackColors = isCopilot ? copilotChartColorFallback : miniMaxChartColorFallback;
      const valueField = isCopilot ? 'usagePercent' : 'usedPercent';

      const normalized = normalizeDynamicHistory(data);
      const quotaKeys = new Set();
      normalized.forEach(d => {
        if (d.quotas) d.quotas.forEach(q => quotaKeys.add(q.name));
      });
      const sortedKeys = [...quotaKeys].sort();
      let fallbackIdx = 0;
      State.chart.data.datasets = sortedKeys.map((key) => {
        const color = colorMap[key] || fallbackColors[fallbackIdx++ % fallbackColors.length];
        return {
          label: displayNames[key] || key,
          data: normalized.map(d => {
            const q = d.quotas ? d.quotas.find(q => q.name === key) : null;
            return q ? (q[valueField] || 0) : 0;
          }),
          borderColor: color.border,
          backgroundColor: color.bg,
          fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4,
          hidden: State.hiddenQuotas.has(key)
        };
      });
      State.chart.data.labels = normalized.map(d => new Date(d.capturedAt).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' }));
      State.chartYMax = computeYMax(State.chart.data.datasets, State.chart);
      State.chart.options.scales.y.max = State.chartYMax;
      State.chart.update();
      return;
    }

    if (provider === 'zai') {
      while (State.chart.data.datasets.length > 3) State.chart.data.datasets.pop();
      if (State.chart.data.datasets.length < 3) {
        State.chart.data.datasets = [
          { label: 'Tokens', data: [], borderColor: getComputedStyle(document.documentElement).getPropertyValue('--chart-subscription').trim() || '#0D9488', backgroundColor: 'rgba(13, 148, 136, 0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4, hidden: State.hiddenQuotas.has('tokensLimit') },
          { label: 'Time', data: [], borderColor: getComputedStyle(document.documentElement).getPropertyValue('--chart-search').trim() || '#F59E0B', backgroundColor: 'rgba(245, 158, 11, 0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4, hidden: State.hiddenQuotas.has('timeLimit') },
          { label: 'Tool Calls', data: [], borderColor: getComputedStyle(document.documentElement).getPropertyValue('--chart-toolcalls').trim() || '#3B82F6', backgroundColor: 'rgba(59, 130, 246, 0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4, hidden: State.hiddenQuotas.has('toolCalls') },
        ];
      }
      State.chart.data.datasets[0].label = 'Tokens';
      State.chart.data.datasets[0].data = data.map(d => d.tokensPercent);
      State.chart.data.datasets[0].hidden = State.hiddenQuotas.has('tokensLimit');
      State.chart.data.datasets[1].label = 'Time';
      State.chart.data.datasets[1].data = data.map(d => d.timePercent);
      State.chart.data.datasets[1].hidden = State.hiddenQuotas.has('timeLimit');
      State.chart.data.datasets[2].label = 'Tool Calls';
      State.chart.data.datasets[2].data = data.map(d => d.toolCallsPercent);
      State.chart.data.datasets[2].hidden = State.hiddenQuotas.has('toolCalls');
    } else {
      while (State.chart.data.datasets.length < 3) {
        State.chart.data.datasets.push({ label: '', data: [], borderColor: '#3B82F6', backgroundColor: 'rgba(59, 130, 246, 0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4 });
      }
      while (State.chart.data.datasets.length > 3) State.chart.data.datasets.pop();
      State.chart.data.datasets[0].label = 'Subscription';
      State.chart.data.datasets[0].data = data.map(d => d.subscriptionPercent);
      State.chart.data.datasets[0].hidden = State.hiddenQuotas.has('subscription');
      State.chart.data.datasets[1].label = 'Search';
      State.chart.data.datasets[1].data = data.map(d => d.searchPercent);
      State.chart.data.datasets[1].hidden = State.hiddenQuotas.has('search');
      State.chart.data.datasets[2].label = 'Tool Calls';
      State.chart.data.datasets[2].data = data.map(d => d.toolCallsPercent);
      State.chart.data.datasets[2].hidden = State.hiddenQuotas.has('toolCalls');
    }

    State.chartYMax = computeYMax(State.chart.data.datasets, State.chart);
    State.chart.options.scales.y.max = State.chartYMax;
    State.chart.update();
  } catch (err) {
    // history fetch error — chart shows empty state
  }
}

// ── "Both" Mode: Dual Charts ──

function updateBothCharts(data) {
  const container = document.querySelector('.chart-container');
  if (!container) return;

  const hasAnthData = data.anthropic && Array.isArray(data.anthropic) && data.anthropic.length > 0;
  const hasMiniData = data.minimax && Array.isArray(data.minimax) && data.minimax.length > 0;
  // Create multi-chart layout if not exists
  if (!container.classList.contains('both-charts')) {
    container.classList.add('both-charts');
    let html = `
      <div class="chart-half"><h4 class="chart-half-label">Synthetic</h4><canvas id="usage-chart-syn"></canvas></div>
      <div class="chart-half"><h4 class="chart-half-label">Z.ai</h4><canvas id="usage-chart-zai"></canvas></div>
    `;
    if (hasAnthData) {
      html += `<div class="chart-half"><h4 class="chart-half-label">Anthropic</h4><canvas id="usage-chart-anth"></canvas></div>`;
    }
    if (hasMiniData) {
      html += `<div class="chart-half"><h4 class="chart-half-label">MiniMax</h4><canvas id="usage-chart-minimax"></canvas></div>`;
    }
    container.innerHTML = html;
    // Hide the original single chart canvas
    const origCanvas = document.getElementById('usage-chart');
    if (origCanvas) origCanvas.style.display = 'none';
  } else if (hasAnthData && !document.getElementById('usage-chart-anth')) {
    container.insertAdjacentHTML('beforeend', '<div class="chart-half"><h4 class="chart-half-label">Anthropic</h4><canvas id="usage-chart-anth"></canvas></div>');
  } else if (hasMiniData && !document.getElementById('usage-chart-minimax')) {
    container.insertAdjacentHTML('beforeend', '<div class="chart-half"><h4 class="chart-half-label">MiniMax</h4><canvas id="usage-chart-minimax"></canvas></div>');
  }

  const style = getComputedStyle(document.documentElement);
  const colors = getThemeColors();

  // Synthetic chart (3 datasets)
  const synCanvas = document.getElementById('usage-chart-syn');
  if (synCanvas && data.synthetic) {
    if (State.chartSyn) State.chartSyn.destroy();
    const synData = data.synthetic;
    const synDatasets = [
      { label: 'Subscription', data: synData.map(d => d.subscriptionPercent), borderColor: style.getPropertyValue('--chart-subscription').trim() || '#0D9488', backgroundColor: 'rgba(13,148,136,0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4 },
      { label: 'Search', data: synData.map(d => d.searchPercent), borderColor: style.getPropertyValue('--chart-search').trim() || '#F59E0B', backgroundColor: 'rgba(245,158,11,0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4 },
      { label: 'Tool Calls', data: synData.map(d => d.toolCallsPercent), borderColor: style.getPropertyValue('--chart-toolcalls').trim() || '#3B82F6', backgroundColor: 'rgba(59,130,246,0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4 }
    ];
    State.chartSyn = new Chart(synCanvas, {
      type: 'line',
      data: {
        labels: synData.map(d => new Date(d.capturedAt).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' })),
        datasets: synDatasets
      },
      options: buildChartOptions(colors, computeYMax(synDatasets))
    });
  }

  // Z.ai chart (3 datasets)
  const zaiCanvas = document.getElementById('usage-chart-zai');
  if (zaiCanvas && data.zai) {
    if (State.chartZai) State.chartZai.destroy();
    const zaiData = data.zai;
    const zaiDatasets = [
      { label: 'Tokens', data: zaiData.map(d => d.tokensPercent), borderColor: style.getPropertyValue('--chart-subscription').trim() || '#0D9488', backgroundColor: 'rgba(13,148,136,0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4 },
      { label: 'Time', data: zaiData.map(d => d.timePercent), borderColor: style.getPropertyValue('--chart-search').trim() || '#F59E0B', backgroundColor: 'rgba(245,158,11,0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4 },
      { label: 'Tool Calls', data: zaiData.map(d => d.toolCallsPercent), borderColor: style.getPropertyValue('--chart-toolcalls').trim() || '#3B82F6', backgroundColor: 'rgba(59,130,246,0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4 }
    ];
    State.chartZai = new Chart(zaiCanvas, {
      type: 'line',
      data: {
        labels: zaiData.map(d => new Date(d.capturedAt).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' })),
        datasets: zaiDatasets
      },
      options: buildChartOptions(colors, computeYMax(zaiDatasets))
    });
  }

  // Anthropic chart (dynamic dataset count)
  const anthCanvas = document.getElementById('usage-chart-anth');
  if (anthCanvas && hasAnthData) {
    if (State.chartAnth) State.chartAnth.destroy();
    const anthData = data.anthropic;
    // Discover dynamic quota keys
    const quotaKeys = new Set();
    anthData.forEach(d => {
      Object.keys(d).forEach(k => { if (k !== 'capturedAt') quotaKeys.add(k); });
    });
    const sortedKeys = [...quotaKeys].sort();
    const anthDatasets = (() => { let fi = 0; return sortedKeys.map((key) => {
      const color = anthropicChartColorMap[key] || anthropicChartColorFallback[fi++ % anthropicChartColorFallback.length];
      return {
        label: anthropicDisplayNames[key] || key,
        data: anthData.map(d => d[key] || 0),
        borderColor: color.border, backgroundColor: color.bg,
        fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4
      };
    }); })();
    State.chartAnth = new Chart(anthCanvas, {
      type: 'line',
      data: {
        labels: anthData.map(d => new Date(d.capturedAt).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' })),
        datasets: anthDatasets
      },
      options: buildChartOptions(colors, computeYMax(anthDatasets))
    });
  }

  // MiniMax chart (dynamic dataset count)
  const miniCanvas = document.getElementById('usage-chart-minimax');
  if (miniCanvas && hasMiniData) {
    const miniData = data.minimax;
    const quotaKeys = new Set();
    miniData.forEach(d => {
      Object.keys(d).forEach(k => { if (k !== 'capturedAt') quotaKeys.add(k); });
    });
    const sortedKeys = [...quotaKeys].sort();
    let fi = 0;
    const miniDatasets = sortedKeys.map((key) => {
      const color = miniMaxChartColorMap[key] || miniMaxChartColorFallback[fi++ % miniMaxChartColorFallback.length];
      return {
        label: miniMaxDisplayNames[key] || key,
        data: miniData.map(d => d[key] || 0),
        borderColor: color.border,
        backgroundColor: color.bg,
        fill: true,
        tension: 0.4,
        borderWidth: 2,
        pointRadius: 0,
        pointHoverRadius: 4,
      };
    });
    const chartMini = new Chart(miniCanvas, {
      type: 'line',
      data: {
        labels: miniData.map(d => new Date(d.capturedAt).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' })),
        datasets: miniDatasets,
      },
      options: buildChartOptions(colors, computeYMax(miniDatasets)),
    });
    State.chartMiniMax = chartMini;
  }
}

function buildChartOptions(colors, yMax) {
  return {
    responsive: true,
    maintainAspectRatio: false,
    interaction: { mode: 'index', intersect: false },
    plugins: {
      legend: { labels: { color: colors.text, usePointStyle: true, boxWidth: 8 } },
      tooltip: {
        mode: 'index', intersect: false,
        backgroundColor: colors.surfaceContainer || '#1E1E1E',
        titleColor: colors.onSurface || '#E6E1E5',
        bodyColor: colors.text || '#CAC4D0',
        borderColor: colors.outline || '#938F99',
        borderWidth: 1, padding: 12, displayColors: true, usePointStyle: true,
        callbacks: { label: ctx => `${ctx.dataset.label}: ${ctx.parsed.y.toFixed(1)}%` }
      }
    },
    scales: {
      x: { grid: { color: colors.grid, drawBorder: false }, ticks: { color: colors.text, maxTicksLimit: 4 } },
      y: { grid: { color: colors.grid, drawBorder: false }, ticks: { color: colors.text, callback: v => v + '%' }, min: 0, max: yMax || 100 }
    }
  };
}

// ── Cycles Table (client-side search/sort/paginate) ──

async function fetchCycles() {
  const provider = getCurrentProvider();

  // Use cycle-overview endpoint to get all quotas in one view
  // Determine the groupBy based on provider (use first available renewal category)
  let groupBy = 'five_hour'; // default for Anthropic
  if (provider === 'synthetic') {
    groupBy = 'subscription';
  } else if (provider === 'zai') {
    groupBy = 'tokens';
  } else if (provider === 'copilot') {
    groupBy = 'premium_interactions';
  } else if (provider === 'minimax') {
    groupBy = 'MiniMax-M2';
  } else if (provider === 'both') {
    // Use anthropic by default for "both" mode
    groupBy = 'five_hour';
  }

  const url = `/api/cycle-overview?${providerParam()}&groupBy=${groupBy}&limit=200`;

  try {
    const res = await authFetch(url);
    if (!res.ok) throw new Error('Failed to fetch cycles');
    const data = await res.json();
    State.allCyclesData = data.cycles || [];
    State.cyclesQuotaNames = data.quotaNames || [];
    State.cyclesPage = 1;
    renderCyclesTable();
  } catch (err) {
    // cycles fetch error — table shows empty state
  }
}

function renderCyclesTable() {
  const thead = document.getElementById('cycles-thead');
  const tbody = document.getElementById('cycles-tbody');
  const infoEl = document.getElementById('cycles-info');
  const paginationEl = document.getElementById('cycles-pagination');
  if (!thead || !tbody) return;

  const provider = getCurrentProvider();
  const quotaNames = State.cyclesQuotaNames;
  const usePercent = provider === 'anthropic' || provider === 'copilot';

  // Build dynamic header (similar to Cycle Overview)
  let headerHtml = `
    <tr>
      <th data-sort-key="id" role="button" tabindex="0">Cycle <span class="sort-arrow"></span></th>
      <th data-sort-key="start" role="button" tabindex="0">Start <span class="sort-arrow"></span></th>
      <th data-sort-key="end" role="button" tabindex="0">End <span class="sort-arrow"></span></th>
      <th data-sort-key="duration" role="button" tabindex="0">Duration <span class="sort-arrow"></span></th>
      <th data-sort-key="totalDelta" role="button" tabindex="0">Total Δ${usePercent ? ' %' : ''} <span class="sort-arrow"></span></th>`;

  quotaNames.forEach(qn => {
    const displayName = overviewQuotaDisplayNames[qn] || qn;
    const suffix = usePercent ? ' %' : '';
    headerHtml += `<th data-sort-key="cq_${qn}" role="button" tabindex="0">${displayName}${suffix} <span class="sort-arrow"></span></th>`;
  });
  headerHtml += '</tr>';
  thead.innerHTML = headerHtml;

  // Attach sort handlers to new headers and restore sort indicator
  thead.querySelectorAll('th[data-sort-key]').forEach(th => {
    th.addEventListener('click', () => handleTableSort('cycles', th));
    th.addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleTableSort('cycles', th); } });
    if (State.cyclesSort.key === th.dataset.sortKey) {
      th.setAttribute('data-sort-dir', State.cyclesSort.dir);
    }
  });

  // Filter by time range
  const now = Date.now();
  const rangeMs = State.cyclesRange;
  const cutoff = now - rangeMs;

  let data = State.allCyclesData.filter(c =>
    new Date(c.cycleStart).getTime() >= cutoff || !c.cycleEnd
  );

  // Sort
  if (State.cyclesSort.key) {
    const { key, dir } = State.cyclesSort;
    data.sort((a, b) => {
      let va, vb;
      if (key === 'id') { va = a.cycleId; vb = b.cycleId; }
      else if (key === 'start') { va = a.cycleStart; vb = b.cycleStart; }
      else if (key === 'end') { va = a.cycleEnd || ''; vb = b.cycleEnd || ''; }
      else if (key === 'duration') {
        va = a.cycleEnd ? new Date(a.cycleEnd) - new Date(a.cycleStart) : 0;
        vb = b.cycleEnd ? new Date(b.cycleEnd) - new Date(b.cycleStart) : 0;
      }
      else if (key === 'totalDelta') { va = a.totalDelta; vb = b.totalDelta; }
      else if (key.startsWith('cq_')) {
        const qn = key.slice(3);
        va = getCrossQuotaPercent(a, qn);
        vb = getCrossQuotaPercent(b, qn);
      }
      else { va = 0; vb = 0; }
      if (va < vb) return dir === 'asc' ? -1 : 1;
      if (va > vb) return dir === 'asc' ? 1 : -1;
      return 0;
    });
  }

  // Pagination
  const pageSize = State.cyclesPageSize || 10;
  const totalRows = data.length;
  const totalPages = pageSize > 0 ? Math.ceil(totalRows / pageSize) : 1;
  if (State.cyclesPage > totalPages) State.cyclesPage = totalPages || 1;
  const page = State.cyclesPage;
  const startIdx = pageSize > 0 ? (page - 1) * pageSize : 0;
  const pageData = pageSize > 0 ? data.slice(startIdx, startIdx + pageSize) : data;

  // Format value with rate: "45.2% [⚡5.2%/hr]"
  const fmtCyclesWithRate = (val, durationHrs, suffix) => {
    if (typeof val !== 'number') return '--';
    const valStr = val.toFixed(1) + suffix;
    if (durationHrs > 0) {
      const rate = val / durationHrs;
      return `${valStr} <span class="rate-indicator">[⚡${rate.toFixed(1)}${suffix}/hr]</span>`;
    }
    return valStr;
  };

  const colCount = 5 + quotaNames.length;

  if (pageData.length === 0) {
    tbody.innerHTML = `<tr><td colspan="${colCount}" class="empty-state">No polling data in this range.</td></tr>`;
  } else {
    tbody.innerHTML = pageData.map(row => {
      const start = row.cycleStart || null;
      const end = row.cycleEnd || null;
      const startDate = start ? new Date(start) : null;
      const endDate = end ? new Date(end) : null;
      const durationMs = (startDate && endDate) ? endDate - startDate : 0;
      const durationHrs = durationMs / 3600000;
      const duration = durationMs > 0 ? formatDuration(Math.floor(durationMs / 1000)) : '--';
      const suffix = usePercent ? '%' : '';

      let html = `<tr>
        <td>${row.cycleId}${!end ? ' <span class="badge">Active</span>' : ''}</td>
        <td>${start ? formatDateTime(start) : '--'}</td>
        <td>${end ? formatDateTime(end) : '<span class="badge">Active</span>'}</td>
        <td>${duration}</td>
        <td>${fmtCyclesWithRate(row.totalDelta, durationHrs, suffix)}</td>`;

      quotaNames.forEach(qn => {
        const pct = getCrossQuotaPercent(row, qn);
        const delta = getCrossQuotaDelta(row, qn);
        const cls = getThresholdClass(pct);
        let cellVal = '--';
        if (pct >= 0) {
          if (usePercent) {
            cellVal = fmtPctWithDelta(pct, delta);
          } else {
            const cq = getCrossQuotaValue(row, qn);
            cellVal = cq ? formatNumber(cq.value) : pct.toFixed(1) + '%';
          }
        }
        html += `<td class="${cls}">${cellVal}</td>`;
      });

      html += '</tr>';
      return html;
    }).join('');
  }

  // Info
  if (infoEl) {
    const showStart = totalRows > 0 ? startIdx + 1 : 0;
    const showEnd = pageSize > 0 ? Math.min(startIdx + pageSize, totalRows) : totalRows;
    infoEl.textContent = `Showing ${showStart}-${showEnd} of ${totalRows}`;
  }

  // Pagination buttons
  if (paginationEl) {
    paginationEl.innerHTML = renderPagination('cycles', page, totalPages);
  }
}

// ── Sessions Table (client-side search/sort/paginate + expandable rows) ──

async function fetchSessions() {
  try {
    const res = await authFetch(`${API_BASE}/api/sessions?${providerParam()}`);
    if (!res.ok) throw new Error('Failed to fetch sessions');
    const data = await res.json();
    const provider = getCurrentProvider();

    if (provider === 'both') {
      // "both" response: { synthetic: [...], zai: [...], anthropic: [...] }
      let merged = [];
      if (data.synthetic) merged = merged.concat(data.synthetic.map(s => ({ ...s, _provider: 'Syn' })));
      if (data.zai) merged = merged.concat(data.zai.map(s => ({ ...s, _provider: 'Z.ai' })));
      if (data.anthropic) merged = merged.concat(data.anthropic.map(s => ({ ...s, _provider: 'Anth' })));
      merged.sort((a, b) => new Date(b.startedAt).getTime() - new Date(a.startedAt).getTime());
      State.allSessionsData = merged;
    } else {
      State.allSessionsData = data;
    }
    State.sessionsPage = 1;
    renderSessionsTable();
    // Update Anthropic session headers with actual quota names after render
    if (getCurrentProvider() === 'anthropic') {
      updateAnthropicSessionHeaders();
    }
  } catch (err) {
    // sessions fetch error — table shows empty state
  }
}

function getSessionComputedFields(session) {
  const start = new Date(session.startedAt);
  const end = session.endedAt ? new Date(session.endedAt) : new Date();
  const durationMins = Math.round((end - start) / 60000);
  const totalConsumption = (session.maxSubRequests || 0) + (session.maxSearchRequests || 0) + (session.maxToolRequests || 0);
  const durationHours = durationMins / 60;
  const consumptionRate = durationHours > 0 ? totalConsumption / durationHours : 0;
  const snapshotsPerMin = durationMins > 0 ? (session.snapshotCount || 0) / durationMins : 0;
  return {
    start, end, durationMins,
    durationStr: formatDurationMins(durationMins),
    isActive: !session.endedAt,
    totalConsumption, consumptionRate, snapshotsPerMin, durationHours
  };
}

function renderSessionsTable() {
  const tbody = document.getElementById('sessions-tbody');
  const infoEl = document.getElementById('sessions-info');
  const paginationEl = document.getElementById('sessions-pagination');
  if (!tbody) return;

  const provider = getCurrentProvider();
  const isBoth = provider === 'both';
  const isZai = provider === 'zai';
  const isAnthropic = provider === 'anthropic';
  const colSpan = isBoth ? 6 : isZai ? 5 : 7;

  let data = State.allSessionsData.map((s, i) => ({ ...s, _computed: getSessionComputedFields(s), _index: i }));

  // Sort
  if (State.sessionsSort.key) {
    const dir = State.sessionsSort.dir === 'asc' ? 1 : -1;
    data.sort((a, b) => {
      let va, vb;
      switch (State.sessionsSort.key) {
        case 'id': va = a.id; vb = b.id; break;
        case 'start': va = a._computed.start.getTime(); vb = b._computed.start.getTime(); break;
        case 'end': va = a._computed.end.getTime(); vb = b._computed.end.getTime(); break;
        case 'duration': va = a._computed.durationMins; vb = b._computed.durationMins; break;
        case 'snapshots': va = a.snapshotCount || 0; vb = b.snapshotCount || 0; break;
        case 'sub': va = a.maxSubRequests; vb = b.maxSubRequests; break;
        case 'search': va = a.maxSearchRequests; vb = b.maxSearchRequests; break;
        case 'tool': va = a.maxToolRequests; vb = b.maxToolRequests; break;
        case 'provider': va = a._provider || ''; vb = b._provider || ''; break;
        default: va = 0; vb = 0;
      }
      return va > vb ? dir : va < vb ? -dir : 0;
    });
  }

  const total = data.length;
  const pageSize = State.sessionsPageSize;
  const totalPages = pageSize > 0 ? Math.max(1, Math.ceil(total / pageSize)) : 1;
  if (State.sessionsPage > totalPages) State.sessionsPage = totalPages;
  const page = State.sessionsPage;
  const startIdx = pageSize > 0 ? (page - 1) * pageSize : 0;
  const pageData = pageSize > 0 ? data.slice(startIdx, startIdx + pageSize) : data;

  if (infoEl) {
    if (total === 0) {
      infoEl.textContent = 'No results';
    } else {
      infoEl.textContent = `Showing ${startIdx + 1}-${Math.min(startIdx + pageData.length, total)} of ${total}`;
    }
  }

  if (total === 0) {
    tbody.innerHTML = `<tr><td colspan="${colSpan}" class="empty-state">No sessions recorded yet.</td></tr>`;
  } else if (isBoth) {
    // Both: show Provider, Session, Start, End, Duration, Snapshots
    tbody.innerHTML = pageData.map(session => {
      const c = session._computed;
      return `<tr class="session-row">
        <td><span class="badge">${session._provider || '-'}</span></td>
        <td>${session.id.slice(0, 8)}${c.isActive ? ' <span class="badge">Active</span>' : ''}</td>
        <td>${c.start.toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })}</td>
        <td>${session.endedAt ? new Date(session.endedAt).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : 'Active'}</td>
        <td>${c.durationStr}</td>
        <td>${session.snapshotCount || 0}</td>
      </tr>`;
    }).join('');
  } else if (isZai) {
    // Z.ai: show Session, Start, End, Duration, Snapshots
    tbody.innerHTML = pageData.map(session => {
      const c = session._computed;
      const isExpanded = State.expandedSessionId === session.id;
      const mainRow = `<tr class="session-row" role="button" tabindex="0" data-session-id="${session.id}">
        <td>${session.id.slice(0, 8)}${c.isActive ? ' <span class="badge">Active</span>' : ''}</td>
        <td>${c.start.toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })}</td>
        <td>${session.endedAt ? new Date(session.endedAt).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : 'Active'}</td>
        <td>${c.durationStr}</td>
        <td>${session.snapshotCount || 0}</td>
      </tr>`;
      const detailRow = `<tr class="session-detail-row ${isExpanded ? 'expanded' : ''}" data-detail-for="${session.id}">
        <td colspan="${colSpan}">
          <div class="session-detail-content">
            <div class="session-detail-grid">
              <div class="detail-item">
                <span class="detail-label">Poll Interval</span>
                <span class="detail-value">${session.pollInterval ? Math.round(session.pollInterval / 1000) : '-'}s</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">Snapshots</span>
                <span class="detail-value">${session.snapshotCount || 0}</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">Snapshots/min</span>
                <span class="detail-value">${c.snapshotsPerMin.toFixed(2)}</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">Duration</span>
                <span class="detail-value">${c.durationStr}</span>
              </div>
            </div>
          </div>
        </td>
      </tr>`;
      return mainRow + detailRow;
    }).join('');
  } else if (isAnthropic) {
    // Anthropic: show Session, Start, End, Duration, + dynamic quota columns (max 3)
    // Labels come from State.anthropicSessionQuotas (populated on first current-data fetch)
    const lbl0 = getAnthropicSessionLabel(0);
    const lbl1 = getAnthropicSessionLabel(1);
    const lbl2 = getAnthropicSessionLabel(2);
    tbody.innerHTML = pageData.map(session => {
      const c = session._computed;
      const isExpanded = State.expandedSessionId === session.id;
      const fmtPct = (v) => v != null ? v.toFixed(1) + '%' : '-';
      const fmtDelta = (start, end) => {
        const d = (end || 0) - (start || 0);
        return d >= 0 ? `+${d.toFixed(1)}%` : `${d.toFixed(1)}%`;
      };
      // Format with delta inline: "45.2% (+12.3%)"
      const fmtWithDelta = (start, max) => {
        const pct = max != null ? max.toFixed(1) + '%' : '-';
        if (start == null || max == null) return pct;
        const d = max - start;
        const delta = d >= 0 ? `+${d.toFixed(1)}%` : `${d.toFixed(1)}%`;
        return `${pct} <span class="delta">(${delta})</span>`;
      };
      const mainRow = `<tr class="session-row" role="button" tabindex="0" data-session-id="${session.id}">
        <td>${session.id.slice(0, 8)}${c.isActive ? ' <span class="badge">Active</span>' : ''}</td>
        <td>${c.start.toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })}</td>
        <td>${session.endedAt ? new Date(session.endedAt).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : 'Active'}</td>
        <td>${c.durationStr}</td>
        <td>${fmtWithDelta(session.startSubRequests, session.maxSubRequests)}</td>
        <td>${fmtWithDelta(session.startSearchRequests, session.maxSearchRequests)}</td>
        <td>${fmtWithDelta(session.startToolRequests, session.maxToolRequests)}</td>
      </tr>`;
      const detailRow = `<tr class="session-detail-row ${isExpanded ? 'expanded' : ''}" data-detail-for="${session.id}">
        <td colspan="${colSpan}">
          <div class="session-detail-content">
            <div class="session-detail-grid">
              <div class="detail-item">
                <span class="detail-label">${lbl0}</span>
                <span class="detail-value">${fmtPct(session.startSubRequests)} &rarr; ${fmtPct(session.maxSubRequests)} (${fmtDelta(session.startSubRequests, session.maxSubRequests)})</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">${lbl1}</span>
                <span class="detail-value">${fmtPct(session.startSearchRequests)} &rarr; ${fmtPct(session.maxSearchRequests)} (${fmtDelta(session.startSearchRequests, session.maxSearchRequests)})</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">${lbl2}</span>
                <span class="detail-value">${fmtPct(session.startToolRequests)} &rarr; ${fmtPct(session.maxToolRequests)} (${fmtDelta(session.startToolRequests, session.maxToolRequests)})</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">Snapshots</span>
                <span class="detail-value">${session.snapshotCount || 0}</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">Snapshots/min</span>
                <span class="detail-value">${c.snapshotsPerMin.toFixed(2)}</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">Duration</span>
                <span class="detail-value">${c.durationStr}</span>
              </div>
            </div>
          </div>
        </td>
      </tr>`;
      return mainRow + detailRow;
    }).join('');
  } else {
    // Synthetic: show Session, Start, End, Duration, Sub, Search, Tool
    tbody.innerHTML = pageData.map(session => {
      const c = session._computed;
      const isExpanded = State.expandedSessionId === session.id;
      const mainRow = `<tr class="session-row" role="button" tabindex="0" data-session-id="${session.id}">
        <td>${session.id.slice(0, 8)}${c.isActive ? ' <span class="badge">Active</span>' : ''}</td>
        <td>${c.start.toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })}</td>
        <td>${session.endedAt ? new Date(session.endedAt).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : 'Active'}</td>
        <td>${c.durationStr}</td>
        <td>${formatNumber(session.maxSubRequests)}</td>
        <td>${formatNumber(session.maxSearchRequests)}</td>
        <td>${formatNumber(session.maxToolRequests)}</td>
      </tr>`;
      const detailRow = `<tr class="session-detail-row ${isExpanded ? 'expanded' : ''}" data-detail-for="${session.id}">
        <td colspan="${colSpan}">
          <div class="session-detail-content">
            <div class="session-detail-grid">
              <div class="detail-item">
                <span class="detail-label">Sub Max</span>
                <span class="detail-value">${formatNumber(session.maxSubRequests)}</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">Search Max</span>
                <span class="detail-value">${formatNumber(session.maxSearchRequests)}</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">Tool Max</span>
                <span class="detail-value">${formatNumber(session.maxToolRequests)}</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">Total Consumption</span>
                <span class="detail-value">${formatNumber(c.totalConsumption)}</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">Rate</span>
                <span class="detail-value">${formatNumber(c.consumptionRate)}/hr</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">Snapshots/min</span>
                <span class="detail-value">${c.snapshotsPerMin.toFixed(2)}</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">Poll Interval</span>
                <span class="detail-value">${session.pollInterval ? Math.round(session.pollInterval / 1000) : '-'}s</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">Snapshots</span>
                <span class="detail-value">${session.snapshotCount || 0}</span>
              </div>
            </div>
          </div>
        </td>
      </tr>`;
      return mainRow + detailRow;
    }).join('');
  }

  // Pagination
  if (paginationEl) {
    paginationEl.innerHTML = (pageSize > 0 && totalPages > 1) ? renderPagination('sessions', page, totalPages) : '';
  }
}

// ── Session Row Expansion ──

function handleSessionRowClick(e) {
  const row = e.target.closest('.session-row');
  if (!row) return;
  const sessionId = row.dataset.sessionId;
  if (State.expandedSessionId === sessionId) {
    State.expandedSessionId = null;
  } else {
    State.expandedSessionId = sessionId;
  }
  // Toggle expansion without full re-render for smoothness
  document.querySelectorAll('.session-detail-row').forEach(dr => {
    dr.classList.toggle('expanded', dr.dataset.detailFor === State.expandedSessionId);
  });
}

// ── Table Sort ──

function handleTableSort(tableId, th) {
  const key = th.dataset.sortKey;
  if (!key) return;

  const table = th.closest('table');
  // Clear other sort indicators in this table
  table.querySelectorAll('th[data-sort-key]').forEach(h => {
    if (h !== th) h.removeAttribute('data-sort-dir');
  });

  const currentDir = th.getAttribute('data-sort-dir');
  const newDir = currentDir === 'asc' ? 'desc' : 'asc';
  th.setAttribute('data-sort-dir', newDir);

  if (tableId === 'cycles') {
    State.cyclesSort = { key, dir: newDir };
    State.cyclesPage = 1;
    renderCyclesTable();
  } else if (tableId === 'sessions') {
    State.sessionsSort = { key, dir: newDir };
    State.sessionsPage = 1;
    renderSessionsTable();
  } else if (tableId === 'overview') {
    State.overviewSort = { key, dir: newDir };
    State.overviewPage = 1;
    renderOverviewTable();
  }
}

// ── KPI Card Modal ──

function openModal(quotaType, providerOverride) {
  const modal = document.getElementById('detail-modal');
  const titleEl = document.getElementById('modal-title');
  const bodyEl = document.getElementById('modal-body');
  if (!modal || !bodyEl) return;

  // In "both" mode with a specific provider override, resolve the correct state key
  const currentProv = getCurrentProvider();
  const effectiveProvider = (currentProv === 'both' && providerOverride) ? providerOverride : currentProv;

  let quotaKey = quotaType;
  if (currentProv === 'both' && providerOverride === 'synthetic' && quotaType === 'toolCalls') {
    quotaKey = 'toolCalls_syn';
  } else if (currentProv === 'both' && providerOverride === 'zai' && quotaType === 'toolCalls') {
    quotaKey = 'toolCalls_zai';
  }

  const data = State.currentQuotas[quotaKey];
  if (!data) return;

  const zaiQuotaNames = { tokensLimit: 'Tokens Limit', timeLimit: 'Time Limit', toolCalls: 'Tool Calls' };
  const names = effectiveProvider === 'zai' ? zaiQuotaNames : quotaNames;
  titleEl.textContent = names[quotaType] || quotaType;

  const statusCfg = statusConfig[data.status] || statusConfig.healthy;
  const timeLeft = formatDuration(data.timeUntilResetSeconds);
  const pctUsed = data.percent.toFixed(1);
  const remaining = data.limit - data.usage;

  bodyEl.innerHTML = `
    <div class="modal-kpi-row">
      <div class="modal-kpi">
        <div class="modal-kpi-value">${pctUsed}%</div>
        <div class="modal-kpi-label">Usage</div>
      </div>
      <div class="modal-kpi">
        <div class="modal-kpi-value">${formatNumber(data.usage)}</div>
        <div class="modal-kpi-label">Used</div>
      </div>
      <div class="modal-kpi">
        <div class="modal-kpi-value">${formatNumber(remaining)}</div>
        <div class="modal-kpi-label">Remaining</div>
      </div>
      <div class="modal-kpi">
        <div class="modal-kpi-value">${timeLeft}</div>
        <div class="modal-kpi-label">Until Reset</div>
      </div>
    </div>
    <h3 class="modal-section-title">Usage History</h3>
    <div class="modal-chart-container">
      <canvas id="modal-chart"></canvas>
    </div>
    ${data.insight ? `<h3 class="modal-section-title">Insight</h3><div class="modal-insight">${data.insight}</div>` : ''}
    <h3 class="modal-section-title">Recent Cycles</h3>
    <div class="table-wrapper">
      <table class="data-table" id="modal-cycles-table">
        <thead><tr><th>Cycle</th><th>Duration</th><th>Peak</th><th>Total</th><th>Rate</th></tr></thead>
        <tbody id="modal-cycles-tbody"><tr><td colspan="5" class="empty-state">Loading...</td></tr></tbody>
      </table>
    </div>
  `;

  modal.hidden = false;
  // Trap focus: focus the close button
  document.getElementById('modal-close').focus();

  // Fetch modal-specific data (use effectiveProvider to avoid "both" API responses)
  loadModalChart(quotaType, effectiveProvider);
  loadModalCycles(quotaType, effectiveProvider);
}

async function loadModalChart(quotaType, effectiveProvider) {
  const ctx = document.getElementById('modal-chart');
  if (!ctx || typeof Chart === 'undefined') return;

  // Destroy previous modal chart
  if (State.modalChart) {
    State.modalChart.destroy();
    State.modalChart = null;
  }

  const activeRange = document.querySelector('.range-btn[data-range].active');
  const range = activeRange ? activeRange.dataset.range : '6h';

  const provider = effectiveProvider || getCurrentProvider();
  try {
    const res = await authFetch(`${API_BASE}/api/history?range=${range}&provider=${provider}`);
    if (!res.ok) return;
    const data = await res.json();
    if (!Array.isArray(data) || data.length === 0) return;
    let datasetKey;
    if (provider === 'zai') {
      datasetKey = quotaType === 'tokensLimit' ? 'tokensPercent' : quotaType === 'toolCalls' ? 'toolCallsPercent' : 'timePercent';
    } else {
      datasetKey = quotaType === 'subscription' ? 'subscriptionPercent' : quotaType === 'search' ? 'searchPercent' : 'toolCallsPercent';
    }
    const style = getComputedStyle(document.documentElement);
    const colorMap = { subscription: style.getPropertyValue('--chart-subscription').trim() || '#0D9488', search: style.getPropertyValue('--chart-search').trim() || '#F59E0B', toolCalls: style.getPropertyValue('--chart-toolcalls').trim() || '#3B82F6', tokensLimit: style.getPropertyValue('--chart-subscription').trim() || '#0D9488', timeLimit: style.getPropertyValue('--chart-search').trim() || '#F59E0B' };
    const bgMap = { subscription: 'rgba(13,148,136,0.08)', search: 'rgba(245,158,11,0.08)', toolCalls: 'rgba(59,130,246,0.08)', tokensLimit: 'rgba(13,148,136,0.08)', timeLimit: 'rgba(245,158,11,0.08)' };

    const colors = getThemeColors();
    const chartData = data.map(d => d[datasetKey]);
    const maxVal = Math.max(...chartData, 0);
    
    // Dynamic Y-axis: if max is 0 or very low, show up to 10%
    // Otherwise add 20% padding, rounded to nearest 5
    let yMax;
    if (maxVal <= 0) {
      yMax = 10;
    } else if (maxVal < 5) {
      yMax = 10;
    } else {
      yMax = Math.min(Math.max(Math.ceil((maxVal * 1.2) / 5) * 5, 10), 100);
    }

    State.modalChart = new Chart(ctx, {
      type: 'line',
      data: {
        labels: data.map(d => new Date(d.capturedAt).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' })),
        datasets: [{
          label: (provider === 'zai' ? { tokensLimit: 'Tokens Limit', timeLimit: 'Time Limit', toolCalls: 'Tool Calls' } : quotaNames)[quotaType] || quotaType,
          data: chartData,
          borderColor: colorMap[quotaType] || '#3B82F6',
          backgroundColor: bgMap[quotaType] || 'rgba(59,130,246,0.08)',
          fill: true,
          tension: 0.3,
          borderWidth: 2.5,
          pointRadius: 0,
          pointHoverRadius: 5
        }]
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        plugins: {
          legend: { display: false },
          tooltip: {
            backgroundColor: colors.surfaceContainer,
            titleColor: colors.onSurface,
            bodyColor: colors.text,
            borderColor: colors.outline,
            borderWidth: 1,
            callbacks: { label: c => `${c.parsed.y.toFixed(1)}%` }
          }
        },
        scales: {
          x: { grid: { color: colors.grid, drawBorder: false }, ticks: { color: colors.text, maxTicksLimit: 6 } },
          y: { grid: { color: colors.grid, drawBorder: false }, ticks: { color: colors.text, callback: v => v + '%' }, min: 0, max: yMax }
        }
      }
    });
  } catch (err) {
    // modal chart error — non-critical
  }
}

async function loadModalCycles(quotaType, effectiveProvider) {
  const provider = effectiveProvider || getCurrentProvider();
  let apiType;
  if (provider === 'zai') {
    apiType = quotaType === 'tokensLimit' ? 'tokens' : 'time';
  } else {
    apiType = quotaType === 'toolCalls' ? 'toolcall' : quotaType;
  }
  try {
    const res = await authFetch(`${API_BASE}/api/cycles?type=${apiType}&provider=${provider}`);
    if (!res.ok) return;
    const cycles = await res.json();

    const tbody = document.getElementById('modal-cycles-tbody');
    if (!tbody) return;

    const recent = cycles.slice(0, 5);
    if (recent.length === 0) {
      tbody.innerHTML = '<tr><td colspan="5" class="empty-state">No cycles yet.</td></tr>';
      return;
    }

    tbody.innerHTML = recent.map(cycle => {
      const c = getCycleComputedFields(cycle);
      return `<tr>
        <td>#${cycle.id}${c.isActive ? ' <span class="badge">Active</span>' : ''}</td>
        <td>${c.durationStr}</td>
        <td>${formatNumber(cycle.peakRequests)}</td>
        <td>${formatNumber(cycle.totalDelta)}</td>
        <td>${c.durationMins > 0 ? formatNumber(c.rate) + '/hr' : '-'}</td>
      </tr>`;
    }).join('');
  } catch (err) {
    // modal cycles error — non-critical
  }
}

function closeModal() {
  const modal = document.getElementById('detail-modal');
  if (!modal) return;
  modal.hidden = true;
  // Destroy modal chart to free memory
  if (State.modalChart) {
    State.modalChart.destroy();
    State.modalChart = null;
  }
}

// ── Event Setup ──

function setupRangeSelector() {
  const buttons = document.querySelectorAll('.range-btn');
  buttons.forEach(btn => {
    btn.addEventListener('click', () => {
      buttons.forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      fetchHistory(btn.dataset.range);
    });
  });
}

function setupCycleFilters() {
  // Range pills
  const rangePills = document.getElementById('cycle-range-pills');
  if (rangePills) {
    rangePills.addEventListener('click', (e) => {
      const pill = e.target.closest('.filter-pill');
      if (!pill) return;
      rangePills.querySelectorAll('.filter-pill').forEach(p => p.classList.remove('active'));
      pill.classList.add('active');
      State.cyclesRange = parseInt(pill.dataset.range, 10);
      State.cyclesPage = 1;
      fetchCycles(); // Re-fetch with new range
    });
  }
}

function setupPasswordToggle() {
  const toggle = document.querySelector('.toggle-password');
  const input = document.getElementById('password');
  if (toggle && input) {
    toggle.addEventListener('click', () => {
      const isVisible = input.type === 'text';
      input.type = isVisible ? 'password' : 'text';
      toggle.classList.toggle('showing', !isVisible);
    });
  }
}

// ── Cycle Overview ──

function getOverviewCategories() {
  const provider = getCurrentProvider();
  if (provider === 'both') {
    return [
      ...(renewalCategories.anthropic || []),
      ...(renewalCategories.synthetic || []),
      ...(renewalCategories.zai || []),
      ...(renewalCategories.copilot || []),
      ...(renewalCategories.minimax || [])
    ];
  }
  return renewalCategories[provider] || [];
}

function setupOverviewControls() {
  const pillsContainer = document.getElementById('overview-period-pills');
  if (!pillsContainer) return;

  const categories = getOverviewCategories();
  if (categories.length === 0) {
    pillsContainer.innerHTML = '<span class="filter-label">No categories available</span>';
    return;
  }

  pillsContainer.innerHTML = categories.map((cat, i) =>
    `<button class="filter-pill ${i === 0 ? 'active' : ''}" data-group-by="${cat.groupBy}">${cat.label}</button>`
  ).join('');

  // Set default groupBy
  State.overviewGroupBy = categories[0].groupBy;

  // Click handler for pills
  pillsContainer.addEventListener('click', (e) => {
    const pill = e.target.closest('.filter-pill');
    if (!pill) return;
    pillsContainer.querySelectorAll('.filter-pill').forEach(p => p.classList.remove('active'));
    pill.classList.add('active');
    State.overviewGroupBy = pill.dataset.groupBy;
    State.overviewPage = 1;
    fetchCycleOverview();
  });

  // Page size
  const pageSizeEl = document.getElementById('overview-page-size');
  if (pageSizeEl) {
    pageSizeEl.addEventListener('change', () => {
      State.overviewPageSize = parseInt(pageSizeEl.value, 10);
      State.overviewPage = 1;
      renderOverviewTable();
    });
  }
}

async function fetchCycleOverview() {
  if (!State.overviewGroupBy) return;
  const provider = getCurrentProvider();

  let url;
  if (provider === 'both') {
    // Determine which provider this groupBy belongs to
    let effectiveProvider = 'synthetic';
    for (const [prov, cats] of Object.entries(renewalCategories)) {
      if (cats.some(c => c.groupBy === State.overviewGroupBy)) {
        effectiveProvider = prov;
        break;
      }
    }
    url = `/api/cycle-overview?provider=${effectiveProvider}&groupBy=${State.overviewGroupBy}&limit=50`;
  } else {
    url = `/api/cycle-overview?${providerParam()}&groupBy=${State.overviewGroupBy}&limit=50`;
  }

  try {
    const res = await authFetch(url);
    if (!res.ok) return;
    const data = await res.json();
    State.allOverviewData = data.cycles || [];
    State.overviewQuotaNames = data.quotaNames || [];
    renderOverviewTable();
  } catch (e) {
    // cycle overview fetch error — non-critical
  }
}

function renderOverviewTable() {
  const thead = document.getElementById('overview-thead');
  const tbody = document.getElementById('overview-tbody');
  const info = document.getElementById('overview-info');
  const pagination = document.getElementById('overview-pagination');
  if (!thead || !tbody) return;

  const quotaNames = State.overviewQuotaNames;
  const overviewProv = getOverviewProvider();
  const usePercent = overviewProv === 'anthropic';

  // Build dynamic header
  let headerHtml = `
    <tr>
      <th data-sort-key="id" role="button" tabindex="0">Cycle <span class="sort-arrow"></span></th>
      <th data-sort-key="start" role="button" tabindex="0">Start <span class="sort-arrow"></span></th>
      <th data-sort-key="end" role="button" tabindex="0">End <span class="sort-arrow"></span></th>
      <th data-sort-key="duration" role="button" tabindex="0">Duration <span class="sort-arrow"></span></th>
      <th data-sort-key="totalDelta" role="button" tabindex="0">Total Delta${usePercent ? ' %' : ''} <span class="sort-arrow"></span></th>`;

  quotaNames.forEach(qn => {
    const isPrimary = qn === State.overviewGroupBy;
    const displayName = overviewQuotaDisplayNames[qn] || qn;
    const suffix = usePercent ? ' %' : '';
    const maxIndicator = isPrimary ? ' Max' : '';
    headerHtml += `<th data-sort-key="cq_${qn}" role="button" tabindex="0" ${isPrimary ? 'class="overview-primary-col"' : ''}>${displayName}${maxIndicator}${suffix} <span class="sort-arrow"></span></th>`;
  });
  headerHtml += '</tr>';
  thead.innerHTML = headerHtml;

  // Attach sort handlers to new headers and restore sort indicator
  thead.querySelectorAll('th[data-sort-key]').forEach(th => {
    th.addEventListener('click', () => handleTableSort('overview', th));
    th.addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleTableSort('overview', th); } });
    // Restore sort indicator if this column is currently sorted
    if (State.overviewSort.key === th.dataset.sortKey) {
      th.setAttribute('data-sort-dir', State.overviewSort.dir);
    }
  });

  let data = [...State.allOverviewData];

  // Sort
  if (State.overviewSort.key) {
    const { key, dir } = State.overviewSort;
    data.sort((a, b) => {
      let va, vb;
      if (key === 'id') { va = a.cycleId; vb = b.cycleId; }
      else if (key === 'start') { va = a.cycleStart; vb = b.cycleStart; }
      else if (key === 'end') { va = a.cycleEnd || ''; vb = b.cycleEnd || ''; }
      else if (key === 'duration') {
        va = a.cycleEnd ? new Date(a.cycleEnd) - new Date(a.cycleStart) : 0;
        vb = b.cycleEnd ? new Date(b.cycleEnd) - new Date(b.cycleStart) : 0;
      }
      else if (key === 'totalDelta') { va = a.totalDelta; vb = b.totalDelta; }
      else if (key.startsWith('cq_')) {
        const qn = key.slice(3);
        va = getCrossQuotaPercent(a, qn);
        vb = getCrossQuotaPercent(b, qn);
      }
      else { va = 0; vb = 0; }
      if (va < vb) return dir === 'asc' ? -1 : 1;
      if (va > vb) return dir === 'asc' ? 1 : -1;
      return 0;
    });
  }

  // Pagination
  const pageSize = State.overviewPageSize || 10;
  const totalRows = data.length;
  const totalPages = pageSize > 0 ? Math.ceil(totalRows / pageSize) : 1;
  if (State.overviewPage > totalPages) State.overviewPage = totalPages || 1;
  const page = State.overviewPage;
  const startIdx = pageSize > 0 ? (page - 1) * pageSize : 0;
  const pageData = pageSize > 0 ? data.slice(startIdx, startIdx + pageSize) : data;

  // Format value with rate: "45.2% [⚡5.2%/hr]"
  const fmtOverviewWithRate = (val, durationHrs, suffix) => {
    if (typeof val !== 'number') return '--';
    const valStr = val.toFixed(1) + suffix;
    if (durationHrs > 0) {
      const rate = val / durationHrs;
      return `${valStr} <span class="rate-indicator">[⚡${rate.toFixed(1)}${suffix}/hr]</span>`;
    }
    return valStr;
  };

  if (pageData.length === 0) {
    const colCount = 5 + quotaNames.length;
    tbody.innerHTML = `<tr><td colspan="${colCount}" class="empty-state">No completed cycles found for this period.</td></tr>`;
  } else {
    tbody.innerHTML = pageData.map(row => {
      const start = row.cycleStart || null;
      const end = row.cycleEnd || null;
      const startDate = start ? new Date(start) : null;
      const endDate = end ? new Date(end) : null;
      const durationMs = (startDate && endDate) ? endDate - startDate : 0;
      const durationHrs = durationMs / 3600000;
      const duration = durationMs > 0 ? formatDuration(Math.floor(durationMs / 1000)) : '--';
      const suffix = usePercent ? '%' : '';

      let html = `<tr>
        <td>${row.cycleId}</td>
        <td>${start ? formatDateTime(start) : '--'}</td>
        <td>${end ? formatDateTime(end) : '<span class="badge">Active</span>'}</td>
        <td>${duration}</td>
        <td>${fmtOverviewWithRate(row.totalDelta, durationHrs, suffix)}</td>`;

      quotaNames.forEach(qn => {
        const pct = getCrossQuotaPercent(row, qn);
        const delta = getCrossQuotaDelta(row, qn);
        const isPrimary = qn === State.overviewGroupBy;
        const cls = getThresholdClass(pct);
        let cellVal = '--';
        if (pct >= 0) {
          if (usePercent) {
            cellVal = fmtPctWithDelta(pct, delta);
          } else {
            const cq = getCrossQuotaValue(row, qn);
            cellVal = cq ? formatNumber(cq.value) : pct.toFixed(1) + '%';
          }
        }
        html += `<td class="${cls}${isPrimary ? ' overview-primary-val' : ''}">${cellVal}</td>`;
      });

      html += '</tr>';
      return html;
    }).join('');
  }

  // Info
  if (info) {
    const showStart = totalRows > 0 ? startIdx + 1 : 0;
    const showEnd = pageSize > 0 ? Math.min(startIdx + pageSize, totalRows) : totalRows;
    info.textContent = `Showing ${showStart}-${showEnd} of ${totalRows}`;
  }

  // Pagination buttons
  if (pagination) {
    pagination.innerHTML = renderPagination('overview', page, totalPages);
  }
}

function getCrossQuotaPercent(row, quotaName) {
  if (!row.crossQuotas || row.crossQuotas.length === 0) return -1;
  const entry = row.crossQuotas.find(cq => cq.name === quotaName);
  return entry ? entry.percent : -1;
}

function getCrossQuotaDelta(row, quotaName) {
  if (!row.crossQuotas || row.crossQuotas.length === 0) return null;
  const entry = row.crossQuotas.find(cq => cq.name === quotaName);
  return entry ? entry.delta : null;
}

function getCrossQuotaValue(row, quotaName) {
  if (!row.crossQuotas || row.crossQuotas.length === 0) return null;
  return row.crossQuotas.find(cq => cq.name === quotaName) || null;
}

// Format value with delta inline: "24.0% (+12.3%)"
function fmtPctWithDelta(pct, delta) {
  if (pct == null || pct < 0) return '--';
  const pctStr = pct.toFixed(1) + '%';
  if (delta == null) return pctStr;
  const deltaStr = delta >= 0 ? `+${delta.toFixed(1)}%` : `${delta.toFixed(1)}%`;
  return `${pctStr} <span class="delta">(${deltaStr})</span>`;
}

function getOverviewProvider() {
  const gb = State.overviewGroupBy;
  if (!gb) return getCurrentProvider();
  for (const [prov, cats] of Object.entries(renewalCategories)) {
    if (cats.some(c => c.groupBy === gb)) return prov;
  }
  return getCurrentProvider();
}

function getThresholdClass(pct) {
  if (pct < 0) return '';
  if (pct >= 95) return 'threshold-critical';
  if (pct >= 80) return 'threshold-danger';
  if (pct >= 50) return 'threshold-warning';
  return 'threshold-healthy';
}

function setupTableControls() {
  // Cycles page size
  const cyclesPageSizeEl = document.getElementById('cycles-page-size');
  if (cyclesPageSizeEl) {
    cyclesPageSizeEl.addEventListener('change', () => {
      State.cyclesPageSize = parseInt(cyclesPageSizeEl.value, 10);
      State.cyclesPage = 1;
      renderCyclesTable();
    });
  }

  // Sessions page size
  const sessionsPageSizeEl = document.getElementById('sessions-page-size');
  if (sessionsPageSizeEl) {
    sessionsPageSizeEl.addEventListener('change', () => {
      State.sessionsPageSize = parseInt(sessionsPageSizeEl.value, 10);
      State.sessionsPage = 1;
      renderSessionsTable();
    });
  }

  // Sort headers (cycles): attached dynamically in renderCyclesTable() since headers are dynamic

  // Sort headers (sessions)
  document.querySelectorAll('#sessions-table th[data-sort-key]').forEach(th => {
    th.addEventListener('click', () => handleTableSort('sessions', th));
    th.addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleTableSort('sessions', th); } });
  });

  // Pagination (delegated)
  document.addEventListener('click', (e) => {
    const btn = e.target.closest('.page-btn');
    if (!btn || btn.disabled) return;
    const table = btn.dataset.table;
    const page = parseInt(btn.dataset.page, 10);
    if (table === 'cycles') {
      State.cyclesPage = page;
      renderCyclesTable();
    } else if (table === 'sessions') {
      State.sessionsPage = page;
      renderSessionsTable();
    } else if (table === 'overview') {
      State.overviewPage = page;
      renderOverviewTable();
    }
  });

  // Session row expansion (delegated)
  const sessionsTbody = document.getElementById('sessions-tbody');
  if (sessionsTbody) {
    sessionsTbody.addEventListener('click', handleSessionRowClick);
    sessionsTbody.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' || e.key === ' ') {
        const row = e.target.closest('.session-row');
        if (row) { e.preventDefault(); handleSessionRowClick(e); }
      }
    });
  }
}

function setupProviderSelector() {
  const tabs = document.getElementById('provider-tabs');
  if (!tabs) return;
  tabs.querySelectorAll('.provider-tab').forEach(tab => {
    tab.addEventListener('click', () => {
      const provider = tab.dataset.provider;
      saveDefaultProvider(provider);
      window.location.href = `/?provider=${provider}`;
    });
  });
}

function setupHeaderActions() {
  // Scroll to top
  const scrollBtn = document.getElementById('scroll-top');
  if (scrollBtn) {
    scrollBtn.addEventListener('click', (e) => {
      e.preventDefault();
      window.scrollTo({ top: 0, behavior: 'smooth' });
    });
  }

  // Manual refresh
  const refreshBtn = document.getElementById('refresh-btn');
  if (refreshBtn) {
    refreshBtn.addEventListener('click', () => {
      refreshBtn.classList.add('spinning');
      Promise.all([
        fetchCurrent(),
        fetchDeepInsights(),
        fetchHistory(),
        fetchCycles(),
        fetchSessions()
      ]).finally(() => {
        setTimeout(() => refreshBtn.classList.remove('spinning'), 600);
      });
    });
  }
}

function setupCardModals() {
  document.querySelectorAll('.quota-card[role="button"]').forEach(card => {
    const handler = () => {
      // In "both" mode, detect which provider column the card belongs to
      const providerCol = card.closest('.provider-column');
      const providerOverride = providerCol ? providerCol.dataset.provider : null;
      openModal(card.dataset.quota, providerOverride);
    };
    card.addEventListener('click', handler);
    card.addEventListener('keydown', e => {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handler(); }
    });
  });

  // Modal close
  const closeBtn = document.getElementById('modal-close');
  if (closeBtn) closeBtn.addEventListener('click', closeModal);

  const overlay = document.getElementById('detail-modal');
  if (overlay) {
    overlay.addEventListener('click', (e) => {
      if (e.target === overlay) closeModal();
    });
  }

  // ESC to close
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') closeModal();
  });
}

function startAutoRefresh() {
  if (State.refreshInterval) clearInterval(State.refreshInterval);
  State.refreshInterval = setInterval(() => {
    // Always refresh above-fold data
    fetchCurrent(); fetchDeepInsights(); fetchHistory();
    // Only refresh below-fold sections that have been loaded
    if (_lazyLoaded.has('.cycles-section')) fetchCycles();
    if (_lazyLoaded.has('.cycle-overview-section')) fetchCycleOverview();
    if (_lazyLoaded.has('.sessions-section')) fetchSessions();
  }, REFRESH_INTERVAL);
}

// ── Pagination Helper ──

function renderPagination(table, page, totalPages) {
  if (totalPages <= 1) return '';
  const maxVisible = 7; // max page buttons (excluding prev/next)
  let pages = [];

  if (totalPages <= maxVisible) {
    for (let p = 1; p <= totalPages; p++) pages.push(p);
  } else {
    // Always show first, last, and a window around current
    const wing = 2;
    let start = Math.max(2, page - wing);
    let end = Math.min(totalPages - 1, page + wing);
    // Shift window if near edges
    if (start <= 2) end = Math.min(totalPages - 1, maxVisible - 2);
    if (end >= totalPages - 1) start = Math.max(2, totalPages - maxVisible + 3);

    pages.push(1);
    if (start > 2) pages.push('...');
    for (let p = start; p <= end; p++) pages.push(p);
    if (end < totalPages - 1) pages.push('...');
    pages.push(totalPages);
  }

  let html = `<button class="page-btn" ${page <= 1 ? 'disabled' : ''} data-table="${table}" data-page="${page - 1}">&laquo;</button>`;
  for (const p of pages) {
    if (p === '...') {
      html += `<span class="page-ellipsis">&hellip;</span>`;
    } else {
      html += `<button class="page-btn ${p === page ? 'active' : ''}" data-table="${table}" data-page="${p}">${p}</button>`;
    }
  }
  html += `<button class="page-btn" ${page >= totalPages ? 'disabled' : ''} data-table="${table}" data-page="${page + 1}">&raquo;</button>`;
  return html;
}

// ── Self-Update ──

async function checkForUpdate() {
  try {
    const res = await authFetch('/api/update/check');
    const data = await res.json();
    const badge = document.getElementById('update-badge');
    if (data.available) {
      const versionSpan = document.getElementById('update-version');
      if (badge && versionSpan) {
        versionSpan.textContent = data.latest_version;
        badge.hidden = false;
      }
    } else if (badge) {
      badge.hidden = true;
    }
  } catch (e) {
    // Silent fail — update check is best-effort
  }
}

async function applyUpdate() {
  const btn = document.getElementById('update-btn');
  if (!btn) return;

  const origText = btn.textContent;
  btn.textContent = 'Updating...';
  btn.disabled = true;

  try {
    const res = await authFetch('/api/update/apply', { method: 'POST' });
    if (!res.ok) {
      const data = await res.json();
      btn.textContent = 'Update failed';
      btn.disabled = false;
      // update failed — error shown in UI
      setTimeout(() => { btn.textContent = origText; }, 3000);
      return;
    }
    btn.textContent = 'Restarting...';
    // Poll until server comes back with new version
    setTimeout(() => pollForRestart(), 3000);
  } catch (e) {
    btn.textContent = 'Update failed';
    btn.disabled = false;
    setTimeout(() => { btn.textContent = origText; }, 3000);
  }
}

function pollForRestart() {
  let serverWentDown = false;
  const interval = setInterval(async () => {
    try {
      await fetch('/api/update/check');
      if (serverWentDown) {
        // Server is back up after going down — force fresh page load (no cache)
        clearInterval(interval);
        window.location.href = window.location.pathname + '?_=' + Date.now();
      }
      // Server still responding (hasn't died yet) — keep waiting
    } catch (e) {
      // Network error = server went down
      serverWentDown = true;
    }
  }, 1000);
  // Force reload after 30s even if we didn't detect restart
  setTimeout(() => {
    clearInterval(interval);
    window.location.href = window.location.pathname + '?_=' + Date.now();
  }, 30000);
}

// ═══════════════════════════════════════════
// SETTINGS PAGE
// ═══════════════════════════════════════════

function isSettingsPage() {
  return window.location.pathname === '/settings';
}

function initSettingsPage() {
  setupSettingsTabs();
  loadSettings();
  setupSettingsSave();
  setupSMTPTest();
  setupPushNotifications();
  setupSettingsPassword();
  setupThresholdSliders();
  setupOverrides();
  populateTimezoneSelect();
}

function setupSettingsTabs() {
  const tabs = document.querySelectorAll('.settings-tab');
  const panels = document.querySelectorAll('.settings-panel');
  tabs.forEach(tab => {
    tab.addEventListener('click', () => {
      const target = tab.dataset.tab;
      tabs.forEach(t => { t.classList.remove('active'); t.setAttribute('aria-selected', 'false'); });
      panels.forEach(p => { p.classList.remove('active'); p.hidden = true; });
      tab.classList.add('active');
      tab.setAttribute('aria-selected', 'true');
      const panel = document.getElementById('panel-' + target);
      if (panel) { panel.classList.add('active'); panel.hidden = false; }
    });
  });
}

function setupThresholdSliders() {
  // Warning threshold
  const wSlider = document.getElementById('threshold-warning-slider');
  const wInput = document.getElementById('threshold-warning');
  if (wSlider && wInput) {
    wSlider.addEventListener('input', () => { wInput.value = wSlider.value; });
    wInput.addEventListener('input', () => { wSlider.value = wInput.value; });
  }
  // Critical threshold
  const cSlider = document.getElementById('threshold-critical-slider');
  const cInput = document.getElementById('threshold-critical');
  if (cSlider && cInput) {
    cSlider.addEventListener('input', () => { cInput.value = cSlider.value; });
    cInput.addEventListener('input', () => { cSlider.value = cInput.value; });
  }
}

async function loadSettings() {
  try {
    const resp = await authFetch('/api/settings');
    if (!resp.ok) return;
    const data = await resp.json();

    // Timezone
    const tzSelect = document.getElementById('settings-timezone');
    if (tzSelect && data.timezone) { tzSelect.value = data.timezone; }

    // SMTP
    if (data.smtp) {
      const s = data.smtp;
      setVal('smtp-host', s.host);
      setVal('smtp-port', s.port);
      setVal('smtp-protocol', s.protocol);
      setVal('smtp-username', s.username);
      setVal('smtp-from-address', s.from_address);
      setVal('smtp-from-name', s.from_name);
      setVal('smtp-to', s.to);
      if (s.password_set) {
        const pwdInput = document.getElementById('smtp-password');
        if (pwdInput) pwdInput.placeholder = '********** (saved)';
      }
    }

    // Notifications
    if (data.notifications) {
      const n = data.notifications;
      setVal('threshold-warning', n.warning_threshold);
      setVal('threshold-warning-slider', n.warning_threshold);
      setVal('threshold-critical', n.critical_threshold);
      setVal('threshold-critical-slider', n.critical_threshold);
      const warnCheck = document.getElementById('notify-warning');
      const critCheck = document.getElementById('notify-critical');
      const resetCheck = document.getElementById('notify-reset');
      if (warnCheck) warnCheck.checked = n.notify_warning !== false;
      if (critCheck) critCheck.checked = n.notify_critical !== false;
      if (resetCheck) resetCheck.checked = n.notify_reset !== false;
      setVal('notify-cooldown', n.cooldown_minutes || 30);
      // Load channel preferences
      if (n.channels) {
        const emailToggle = document.getElementById('channel-email');
        const pushToggle = document.getElementById('channel-push');
        if (emailToggle) emailToggle.checked = n.channels.email !== false;
        if (pushToggle) pushToggle.checked = n.channels.push !== false;
      }
      // Load overrides
      if (n.overrides && n.overrides.length > 0) {
        n.overrides.forEach(o => addOverrideRow(o.quota_key, o.provider, o.warning, o.critical, o.is_absolute));
      }
    }

    // Provider visibility
    if (data.provider_visibility) {
      populateProviderToggles(data.provider_visibility);
    } else {
      populateProviderToggles({});
    }
  } catch (e) {
    // Settings load failed silently
  }
}

function setVal(id, val) {
  const el = document.getElementById(id);
  if (el && val !== undefined && val !== null) el.value = val;
}

function populateTimezoneSelect() {
  const select = document.getElementById('settings-timezone');
  if (!select) return;
  const zones = [
    'UTC', 'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles',
    'America/Sao_Paulo', 'Europe/London', 'Europe/Paris', 'Europe/Berlin', 'Europe/Moscow',
    'Asia/Dubai', 'Asia/Kolkata', 'Asia/Shanghai', 'Asia/Tokyo', 'Asia/Seoul',
    'Australia/Sydney', 'Pacific/Auckland'
  ];
  zones.forEach(tz => {
    const opt = document.createElement('option');
    opt.value = tz;
    opt.textContent = tz.replace(/_/g, ' ');
    select.appendChild(opt);
  });
}

function populateProviderToggles(visibility) {
  const container = document.getElementById('provider-toggles');
  if (!container) return;
  const providers = [
    { key: 'anthropic', name: 'Anthropic', desc: 'Claude Code usage tracking' },
    { key: 'synthetic', name: 'Synthetic', desc: 'Synthetic API quota monitoring' },
    { key: 'zai', name: 'Z.ai', desc: 'Z.ai API usage tracking' },
  ];

  container.innerHTML = '';
  providers.forEach(p => {
    const vis = visibility[p.key] || { dashboard: true, polling: true };
    const row = document.createElement('div');
    row.className = 'settings-toggle-row settings-toggle-row-dual';
    row.innerHTML = `
      <div class="settings-toggle-info">
        <div class="settings-toggle-label">${p.name}</div>
        <div class="settings-toggle-sublabel">${p.desc}</div>
      </div>
      <div class="settings-toggle-group">
        <div class="settings-toggle-item">
          <div class="settings-toggle-item-label">Telemetry</div>
          <div class="settings-toggle-item-hint">Track usage data in background</div>
          <label class="settings-toggle" title="Telemetry">
            <input type="checkbox" data-provider="${p.key}" data-role="polling" ${vis.polling !== false ? 'checked' : ''}>
            <span class="settings-toggle-track"></span>
          </label>
        </div>
        <div class="settings-toggle-item">
          <div class="settings-toggle-item-label">Dashboard</div>
          <div class="settings-toggle-item-hint">Show as individual tab</div>
          <label class="settings-toggle" title="Dashboard">
            <input type="checkbox" data-provider="${p.key}" data-role="dashboard" ${vis.dashboard !== false ? 'checked' : ''}>
            <span class="settings-toggle-track"></span>
          </label>
        </div>
      </div>
    `;
    container.appendChild(row);
  });
}

function gatherSettings() {
  const settings = {};

  // SMTP
  const smtpHost = document.getElementById('smtp-host');
  if (smtpHost) {
    settings.smtp = {
      host: smtpHost.value.trim(),
      port: parseInt(document.getElementById('smtp-port')?.value) || 587,
      protocol: document.getElementById('smtp-protocol')?.value || 'tls',
      username: document.getElementById('smtp-username')?.value.trim() || '',
      password: document.getElementById('smtp-password')?.value || '',
      from_address: document.getElementById('smtp-from-address')?.value.trim() || '',
      from_name: document.getElementById('smtp-from-name')?.value.trim() || '',
      to: document.getElementById('smtp-to')?.value.trim() || '',
    };
  }

  // Notifications
  const warningInput = document.getElementById('threshold-warning');
  if (warningInput) {
    const overrides = [];
    document.querySelectorAll('.settings-override-row').forEach(row => {
      const quota = row.querySelector('.override-quota')?.value;
      const provider = row.querySelector('.override-provider')?.value;
      const w = parseFloat(row.querySelector('.override-warning')?.value);
      const c = parseFloat(row.querySelector('.override-critical')?.value);
      const isAbs = row.querySelector('.override-is-absolute')?.value === 'true';
      if (quota && !isNaN(w) && !isNaN(c)) {
        overrides.push({ quota_key: quota, provider: provider || '', warning: w, critical: c, is_absolute: isAbs });
      }
    });

    settings.notifications = {
      warning_threshold: parseFloat(warningInput.value) || 80,
      critical_threshold: parseFloat(document.getElementById('threshold-critical')?.value) || 95,
      notify_warning: document.getElementById('notify-warning')?.checked ?? true,
      notify_critical: document.getElementById('notify-critical')?.checked ?? true,
      notify_reset: document.getElementById('notify-reset')?.checked ?? true,
      cooldown_minutes: parseInt(document.getElementById('notify-cooldown')?.value) || 30,
      channels: {
        email: document.getElementById('channel-email')?.checked ?? true,
        push: document.getElementById('channel-push')?.checked ?? true,
      },
      overrides: overrides,
    };
  }

  // Provider visibility
  const toggles = document.querySelectorAll('#provider-toggles input[type="checkbox"]');
  if (toggles.length > 0) {
    const vis = {};
    toggles.forEach(t => {
      const prov = t.dataset.provider;
      const role = t.dataset.role;
      if (!vis[prov]) vis[prov] = {};
      vis[prov][role] = t.checked;
    });
    settings.provider_visibility = vis;
  }

  // Timezone
  const tzSelect = document.getElementById('settings-timezone');
  if (tzSelect) {
    settings.timezone = tzSelect.value;
  }

  return settings;
}

function setupSettingsSave() {
  const saveBtn = document.getElementById('settings-save-btn');
  const feedback = document.getElementById('settings-feedback');
  if (!saveBtn) return;

  saveBtn.addEventListener('click', async () => {
    saveBtn.disabled = true;
    saveBtn.textContent = 'Saving...';
    if (feedback) { feedback.hidden = true; }

    const settings = gatherSettings();

    // Client-side validation
    if (settings.notifications) {
      if (settings.notifications.warning_threshold >= settings.notifications.critical_threshold) {
        showSettingsFeedback(feedback, 'Warning threshold must be less than critical threshold.', 'error');
        saveBtn.disabled = false;
        saveBtn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"/><polyline points="17 21 17 13 7 13 7 21"/><polyline points="7 3 7 8 15 8"/></svg> Save Settings';
        return;
      }
    }

    try {
      const resp = await authFetch('/api/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(settings),
      });
      const data = await resp.json();
      if (!resp.ok) {
        showSettingsFeedback(feedback, data.error || 'Failed to save settings.', 'error');
      } else {
        showSettingsFeedback(feedback, 'Settings saved successfully.', 'success');
      }
    } catch (e) {
      showSettingsFeedback(feedback, 'Network error. Please try again.', 'error');
    } finally {
      saveBtn.disabled = false;
      saveBtn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"/><polyline points="17 21 17 13 7 13 7 21"/><polyline points="7 3 7 8 15 8"/></svg> Save Settings';
    }
  });
}

function showSettingsFeedback(el, msg, type) {
  if (!el) return;
  el.textContent = msg;
  el.className = 'settings-feedback ' + type;
  el.hidden = false;
  setTimeout(() => { el.hidden = true; }, 5000);
}

function setupSMTPTest() {
  const testBtn = document.getElementById('smtp-test-btn');
  const result = document.getElementById('smtp-test-result');
  if (!testBtn) return;

  testBtn.addEventListener('click', async () => {
    testBtn.disabled = true;
    testBtn.textContent = 'Sending...';
    if (result) { result.textContent = ''; result.className = 'settings-test-result'; }

    try {
      const resp = await authFetch('/api/settings/smtp/test', { method: 'POST' });
      const data = await resp.json();
      if (result) {
        result.textContent = data.message || (data.success ? 'Test email sent.' : 'Test failed.');
        result.className = 'settings-test-result ' + (data.success ? 'success' : 'error');
      }
    } catch (e) {
      if (result) {
        result.textContent = 'Network error.';
        result.className = 'settings-test-result error';
      }
    } finally {
      testBtn.disabled = false;
      testBtn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M22 2L11 13M22 2l-7 20-4-9-9-4 20-7z"/></svg> Send Test Email';
    }
  });
}

function setupPushNotifications() {
  var statusLabel = document.getElementById('push-status-label');
  var subscribeBtn = document.getElementById('push-subscribe-btn');
  var channelToggle = document.getElementById('channel-push');
  var testActions = document.getElementById('push-test-actions');
  var testBtn = document.getElementById('push-test-btn');
  var testResult = document.getElementById('push-test-result');

  if (!statusLabel) return;

  // Check for HTTPS - required for Push API on mobile devices
  // Note: window.isSecureContext is true for localhost over HTTP, but Android Chrome
  // still requires actual HTTPS for push notifications to work reliably
  var isHttps = location.protocol === 'https:';
  if (!isHttps) {
    statusLabel.textContent = 'Push notifications require HTTPS';
    if (channelToggle) { channelToggle.disabled = true; }
    if (subscribeBtn) { subscribeBtn.disabled = true; subscribeBtn.hidden = true; }
    // Add warning message below status
    var warning = document.createElement('div');
    warning.className = 'push-http-warning';
    warning.textContent = 'Requires HTTPS connection. Push notifications are unavailable over HTTP.';
    warning.style.cssText = 'color: #dc2626; font-size: 11px; margin-top: 4px;';
    statusLabel.parentNode.appendChild(warning);
    return;
  }

  if (!('serviceWorker' in navigator) || !('PushManager' in window)) {
    statusLabel.textContent = 'Push notifications not supported in this browser';
    if (channelToggle) { channelToggle.disabled = true; }
    return;
  }

  // Register service worker
  navigator.serviceWorker.register('/sw.js').then(function(reg) {
    return reg.pushManager.getSubscription();
  }).then(function(sub) {
    if (sub) {
      statusLabel.textContent = 'Subscribed — push notifications active';
      if (subscribeBtn) { subscribeBtn.hidden = true; }
      if (testActions) { testActions.hidden = false; }
    } else {
      statusLabel.textContent = 'Not subscribed — click Enable to subscribe';
      if (subscribeBtn) { subscribeBtn.hidden = false; }
    }
  }).catch(function() {
    statusLabel.textContent = 'Service worker registration failed';
  });

  // Subscribe button
  if (subscribeBtn) {
    subscribeBtn.addEventListener('click', async function() {
      subscribeBtn.disabled = true;
      subscribeBtn.textContent = 'Enabling...';
      try {
        var vapidResp = await authFetch('/api/push/vapid');
        if (!vapidResp.ok) throw new Error('Failed to get VAPID key');
        var vapidData = await vapidResp.json();
        var applicationServerKey = urlBase64ToUint8Array(vapidData.public_key);

        var reg = await navigator.serviceWorker.ready;
        var sub = await reg.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: applicationServerKey
        });

        var subJSON = sub.toJSON();
        var saveResp = await authFetch('/api/push/subscribe', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            endpoint: subJSON.endpoint,
            keys: { p256dh: subJSON.keys.p256dh, auth: subJSON.keys.auth }
          })
        });
        if (!saveResp.ok) throw new Error('Failed to save subscription');

        statusLabel.textContent = 'Subscribed — push notifications active';
        subscribeBtn.hidden = true;
        if (testActions) testActions.hidden = false;
        if (channelToggle) channelToggle.checked = true;
      } catch (e) {
        statusLabel.textContent = 'Failed: ' + (e.message || 'unknown error');
      } finally {
        subscribeBtn.disabled = false;
        subscribeBtn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9M13.73 21a2 2 0 0 1-3.46 0"/></svg> Enable';
      }
    });
  }

  // Unsubscribe when push toggle is turned off
  if (channelToggle) {
    channelToggle.addEventListener('change', async function() {
      if (!channelToggle.checked) {
        try {
          var reg = await navigator.serviceWorker.ready;
          var sub = await reg.pushManager.getSubscription();
          if (sub) {
            var endpoint = sub.endpoint;
            await sub.unsubscribe();
            await authFetch('/api/push/subscribe', {
              method: 'DELETE',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ endpoint: endpoint })
            });
          }
          statusLabel.textContent = 'Not subscribed — click Enable to subscribe';
          if (subscribeBtn) subscribeBtn.hidden = false;
          if (testActions) testActions.hidden = true;
        } catch (e) {
          statusLabel.textContent = 'Failed to unsubscribe';
          channelToggle.checked = true;
        }
      }
    });
  }

  // Test push button
  if (testBtn) {
    testBtn.addEventListener('click', async function() {
      testBtn.disabled = true;
      testBtn.textContent = 'Sending...';
      if (testResult) { testResult.textContent = ''; testResult.className = 'settings-test-result'; }

      try {
        var resp = await authFetch('/api/push/test', { method: 'POST' });
        var data = await resp.json();
        if (testResult) {
          testResult.textContent = data.message || (data.success ? 'Test push sent.' : 'Test failed.');
          testResult.className = 'settings-test-result ' + (data.success ? 'success' : 'error');
        }
      } catch (e) {
        if (testResult) {
          testResult.textContent = 'Network error.';
          testResult.className = 'settings-test-result error';
        }
      } finally {
        testBtn.disabled = false;
        testBtn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9M13.73 21a2 2 0 0 1-3.46 0"/></svg> Send Test Push';
      }
    });
  }
}

function urlBase64ToUint8Array(base64String) {
  var padding = '='.repeat((4 - base64String.length % 4) % 4);
  var base64 = (base64String + padding).replace(/-/g, '+').replace(/_/g, '/');
  var rawData = atob(base64);
  var outputArray = new Uint8Array(rawData.length);
  for (var i = 0; i < rawData.length; ++i) {
    outputArray[i] = rawData.charCodeAt(i);
  }
  return outputArray;
}

function setupSettingsPassword() {
  const saveBtn = document.getElementById('password-save-btn');
  const feedback = document.getElementById('settings-password-feedback');
  if (!saveBtn) return;

  saveBtn.addEventListener('click', async () => {
    if (feedback) { feedback.hidden = true; }
    const currentPass = document.getElementById('settings-current-password')?.value;
    const newPass = document.getElementById('settings-new-password')?.value;
    const confirmPass = document.getElementById('settings-confirm-password')?.value;

    if (!currentPass || !newPass) {
      showSettingsFeedback(feedback, 'Please fill in all password fields.', 'error');
      return;
    }
    if (newPass !== confirmPass) {
      showSettingsFeedback(feedback, 'New passwords do not match.', 'error');
      return;
    }
    if (newPass.length < 6) {
      showSettingsFeedback(feedback, 'New password must be at least 6 characters.', 'error');
      return;
    }

    saveBtn.disabled = true;
    saveBtn.textContent = 'Updating...';

    try {
      const resp = await authFetch('/api/password', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ current_password: currentPass, new_password: newPass }),
      });
      const data = await resp.json();
      if (!resp.ok) {
        showSettingsFeedback(feedback, data.error || 'Failed to update password.', 'error');
      } else {
        showSettingsFeedback(feedback, 'Password updated! Redirecting to login...', 'success');
        setTimeout(() => { window.location.href = '/login'; }, 1500);
      }
    } catch (e) {
      showSettingsFeedback(feedback, 'Network error.', 'error');
    } finally {
      saveBtn.disabled = false;
      saveBtn.textContent = 'Update Password';
    }
  });
}

function setupOverrides() {
  const addBtn = document.getElementById('add-override-btn');
  if (addBtn) {
    addBtn.addEventListener('click', () => addOverrideRow('', 'anthropic', 80, 95, false));
  }
}

const _overrideQuotasByProvider = {
  anthropic: [
    { key: 'five_hour', label: '5-Hour Limit' },
    { key: 'seven_day', label: 'Weekly All-Model' },
    { key: 'seven_day_sonnet', label: 'Weekly Sonnet' },
    { key: 'monthly_limit', label: 'Monthly Limit' },
  ],
  synthetic: [
    { key: 'subscription', label: 'Subscription' },
    { key: 'search', label: 'Search Queries' },
    { key: 'toolcall', label: 'Tool Calls' },
  ],
  zai: [
    { key: 'tokens', label: 'Tokens Limit' },
    { key: 'time', label: 'Time Limit' },
  ],
};

function _isAbsoluteProvider(provider) {
  return provider === 'synthetic' || provider === 'zai';
}

function _updateOverrideQuotas(row) {
  const provSelect = row.querySelector('.override-provider-select');
  const quotaSelect = row.querySelector('.override-quota');
  const warnInput = row.querySelector('.override-warning');
  const critInput = row.querySelector('.override-critical');
  const absInput = row.querySelector('.override-is-absolute');
  const provider = provSelect.value;
  const quotas = _overrideQuotasByProvider[provider] || [];
  const prevQuota = quotaSelect.value;

  quotaSelect.innerHTML = '<option value="">Select quota...</option>';
  quotas.forEach(q => {
    const opt = document.createElement('option');
    opt.value = q.key;
    opt.textContent = q.label;
    if (q.key === prevQuota) opt.selected = true;
    quotaSelect.appendChild(opt);
  });

  const isAbs = _isAbsoluteProvider(provider);
  absInput.value = isAbs ? 'true' : 'false';
  if (isAbs) {
    warnInput.removeAttribute('max');
    warnInput.placeholder = 'Warn';
    warnInput.title = 'Warning threshold (absolute value)';
    critInput.removeAttribute('max');
    critInput.placeholder = 'Crit';
    critInput.title = 'Critical threshold (absolute value)';
  } else {
    warnInput.setAttribute('max', '100');
    warnInput.placeholder = 'Warn%';
    warnInput.title = 'Warning threshold (%)';
    critInput.setAttribute('max', '100');
    critInput.placeholder = 'Crit%';
    critInput.title = 'Critical threshold (%)';
  }
}

function addOverrideRow(quotaKey, provider, warning, critical, isAbsolute) {
  const list = document.getElementById('override-list');
  if (!list) return;

  // Determine provider from quota key if not provided
  if (!provider && quotaKey) {
    for (const [prov, quotas] of Object.entries(_overrideQuotasByProvider)) {
      if (quotas.some(q => q.key === quotaKey)) { provider = prov; break; }
    }
  }

  const row = document.createElement('div');
  row.className = 'settings-override-row';
  row.innerHTML = `
    <select class="settings-input override-provider-select" style="flex:1">
      <option value="anthropic" ${provider === 'anthropic' ? 'selected' : ''}>Anthropic</option>
      <option value="synthetic" ${provider === 'synthetic' ? 'selected' : ''}>Synthetic</option>
      <option value="zai" ${provider === 'zai' ? 'selected' : ''}>Z.ai</option>
    </select>
    <select class="settings-input override-quota" style="flex:2">
      <option value="">Select quota...</option>
    </select>
    <input type="number" class="settings-input settings-input-sm override-warning" value="${warning}" min="0" placeholder="Warn%">
    <input type="number" class="settings-input settings-input-sm override-critical" value="${critical}" min="0" placeholder="Crit%">
    <input type="hidden" class="override-provider" value="${provider || 'anthropic'}">
    <input type="hidden" class="override-is-absolute" value="${isAbsolute ? 'true' : 'false'}">
    <button class="override-remove" title="Remove override" type="button">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 6L6 18M6 6l12 12"/></svg>
    </button>
  `;

  const provSelect = row.querySelector('.override-provider-select');
  const hiddenProv = row.querySelector('.override-provider');
  provSelect.addEventListener('change', () => {
    hiddenProv.value = provSelect.value;
    _updateOverrideQuotas(row);
  });

  row.querySelector('.override-remove').addEventListener('click', () => row.remove());
  list.appendChild(row);

  // Populate quota options for the selected provider
  _updateOverrideQuotas(row);

  // Re-select the quota key if restoring
  if (quotaKey) {
    const quotaSelect = row.querySelector('.override-quota');
    quotaSelect.value = quotaKey;
  }
}

// ── Init ──

document.addEventListener('DOMContentLoaded', () => {
  // Register service worker for PWA + push (all pages)
  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/sw.js').catch(function() {});
  }

  // Settings page has its own initialization
  if (isSettingsPage()) {
    initTheme();
    initSettingsPage();
    return;
  }

  // Redirect to saved default provider if no explicit provider in URL
  // Only when multiple providers are available (tabs exist)
  const urlParams = new URLSearchParams(window.location.search);
  const providerTabs = document.getElementById('provider-tabs');
  if (!urlParams.has('provider') && providerTabs) {
    const savedProvider = loadDefaultProvider();
    if (savedProvider) {
      const availableProviders = [...providerTabs.querySelectorAll('.provider-tab')].map(t => t.dataset.provider);
      // Only redirect if saved provider is available and different from server default
      if (availableProviders.includes(savedProvider) && savedProvider !== availableProviders[0]) {
        window.location.href = `/?provider=${savedProvider}`;
        return;
      }
    }
  }

  // Load persisted state (localStorage only — no API calls before auth check)
  loadHiddenQuotas();

  initTheme();
  initTimezoneBadge();
  setupProviderSelector();
  setupRangeSelector();
  setupCycleFilters();
  setupPasswordToggle();
  setupTableControls();
  setupOverviewControls();
  setupHeaderActions();
  setupCardModals();

  if (document.getElementById('usage-chart') || document.getElementById('both-view')) {
    initChart();

    // Critical path: fetch above-fold data in parallel
    Promise.all([
      loadHiddenInsights(),
      fetchCurrent(),
      fetchDeepInsights(),
      fetchHistory('6h'),
    ]);

    // Below-fold: lazy-load when sections scroll into view
    lazyLoadOnVisible('.cycles-section', () => fetchCycles());
    lazyLoadOnVisible('.cycle-overview-section', () => fetchCycleOverview());
    lazyLoadOnVisible('.sessions-section', () => fetchSessions());

    startCountdowns();
    startAutoRefresh();

    // Check for updates on load and every 60 minutes
    checkForUpdate();
    setInterval(checkForUpdate, 3600000);

    // Update button click handler
    const updateBtn = document.getElementById('update-btn');
    if (updateBtn) {
      updateBtn.addEventListener('click', applyUpdate);
    }

    // Update sessions table header for "both" mode
    const provider = getCurrentProvider();
    if (provider === 'both') {
      const sessionsHead = document.querySelector('#sessions-table thead tr');
      if (sessionsHead) {
        sessionsHead.innerHTML = `
          <th data-sort-key="provider" role="button" tabindex="0">Provider <span class="sort-arrow"></span></th>
          <th data-sort-key="id" role="button" tabindex="0">Session <span class="sort-arrow"></span></th>
          <th data-sort-key="start" role="button" tabindex="0">Started <span class="sort-arrow"></span></th>
          <th data-sort-key="end" role="button" tabindex="0">Ended <span class="sort-arrow"></span></th>
          <th data-sort-key="duration" role="button" tabindex="0">Duration <span class="sort-arrow"></span></th>
          <th data-sort-key="snapshots" role="button" tabindex="0">Snapshots <span class="sort-arrow"></span></th>
        `;
      }
      // Update cycles table for "both" - add provider column
      const cyclesHead = document.querySelector('#cycles-table thead tr');
      if (cyclesHead) {
        cyclesHead.innerHTML = `
          <th data-sort-key="provider" role="button" tabindex="0">Provider <span class="sort-arrow"></span></th>
          <th data-sort-key="id" role="button" tabindex="0">Cycle <span class="sort-arrow"></span></th>
          <th data-sort-key="start" role="button" tabindex="0">Start <span class="sort-arrow"></span></th>
          <th data-sort-key="end" role="button" tabindex="0">End <span class="sort-arrow"></span></th>
          <th data-sort-key="duration" role="button" tabindex="0">Duration <span class="sort-arrow"></span></th>
          <th data-sort-key="peak" role="button" tabindex="0">Peak <span class="sort-arrow"></span></th>
          <th data-sort-key="total" role="button" tabindex="0">Total <span class="sort-arrow"></span></th>
          <th data-sort-key="rate" role="button" tabindex="0">Rate <span class="sort-arrow"></span></th>
        `;
      }
      // Re-attach sort event listeners (headers were replaced, losing original listeners)
      document.querySelectorAll('#cycles-table th[data-sort-key]').forEach(th => {
        th.addEventListener('click', () => handleTableSort('cycles', th));
        th.addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleTableSort('cycles', th); } });
      });
      document.querySelectorAll('#sessions-table th[data-sort-key]').forEach(th => {
        th.addEventListener('click', () => handleTableSort('sessions', th));
        th.addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleTableSort('sessions', th); } });
      });
    }
  }

  // ═══════════════════════════════════════════
  // PASSWORD CHANGE MODAL
  // ═══════════════════════════════════════════

  const passwordModal = document.getElementById('password-modal');
  const passwordForm = document.getElementById('password-form');
  const changePasswordBtn = document.getElementById('change-password-btn');
  const passwordModalClose = document.getElementById('password-modal-close');
  const passwordError = document.getElementById('password-error');
  const passwordSuccess = document.getElementById('password-success');

  if (changePasswordBtn && passwordModal) {
    changePasswordBtn.addEventListener('click', () => {
      passwordModal.hidden = false;
      if (passwordError) { passwordError.hidden = true; passwordError.textContent = ''; }
      if (passwordSuccess) { passwordSuccess.hidden = true; passwordSuccess.textContent = ''; }
      if (passwordForm) passwordForm.reset();
      const firstInput = document.getElementById('current-password');
      if (firstInput) firstInput.focus();
    });

    if (passwordModalClose) {
      passwordModalClose.addEventListener('click', () => { passwordModal.hidden = true; });
    }

    passwordModal.addEventListener('click', e => {
      if (e.target === passwordModal) passwordModal.hidden = true;
    });

    document.addEventListener('keydown', e => {
      if (e.key === 'Escape' && !passwordModal.hidden) passwordModal.hidden = true;
    });
  }

  if (passwordForm) {
    passwordForm.addEventListener('submit', async e => {
      e.preventDefault();
      if (passwordError) { passwordError.hidden = true; passwordError.textContent = ''; }
      if (passwordSuccess) { passwordSuccess.hidden = true; passwordSuccess.textContent = ''; }

      const currentPass = document.getElementById('current-password').value;
      const newPass = document.getElementById('new-password').value;
      const confirmPass = document.getElementById('confirm-password').value;

      if (newPass !== confirmPass) {
        if (passwordError) { passwordError.textContent = 'New passwords do not match.'; passwordError.hidden = false; }
        return;
      }
      if (newPass.length < 6) {
        if (passwordError) { passwordError.textContent = 'New password must be at least 6 characters.'; passwordError.hidden = false; }
        return;
      }

      const submitBtn = document.getElementById('password-submit-btn');
      if (submitBtn) { submitBtn.disabled = true; submitBtn.textContent = 'Updating...'; }

      try {
        const resp = await authFetch('/api/password', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ current_password: currentPass, new_password: newPass })
        });
        const data = await resp.json();
        if (!resp.ok) {
          if (passwordError) { passwordError.textContent = data.error || 'Failed to update password.'; passwordError.hidden = false; }
        } else {
          if (passwordSuccess) { passwordSuccess.textContent = 'Password updated! Redirecting to login...'; passwordSuccess.hidden = true; passwordSuccess.hidden = false; }
          passwordForm.reset();
          setTimeout(() => { window.location.href = '/login'; }, 1500);
        }
      } catch (err) {
        if (passwordError) { passwordError.textContent = 'Network error. Please try again.'; passwordError.hidden = false; }
      } finally {
        if (submitBtn) { submitBtn.disabled = false; submitBtn.textContent = 'Update Password'; }
      }
    });
  }
});
