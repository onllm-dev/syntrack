// SynTrack Dashboard JavaScript

const API_BASE = '';
const REFRESH_INTERVAL = 60000;

// ── Auth helper: redirect to login on 401 ──
async function authFetch(url) {
  const res = await fetch(url);
  if (res.status === 401) {
    window.location.href = '/login';
    throw new Error('Session expired');
  }
  return res;
}

// ── Global State ──
const State = {
  chart: null,
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
  cyclesGroup: 300000,      // 5 minutes in ms (default)
  // Sessions table state
  sessionsSort: { key: null, dir: 'desc' },
  sessionsPage: 1,
  sessionsPageSize: 10,
  // Expanded session
  expandedSessionId: null,
  // Dynamic Y-axis max (preserved across theme changes)
  chartYMax: 100,
};

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

// ── Utilities ──

function formatDuration(seconds) {
  if (seconds < 0) return 'Resetting...';
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = seconds % 60;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return '< 1m';
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
    grid: style.getPropertyValue('--border-light').trim() || (isDark ? '#252830' : '#F0F1F3'),
    text: style.getPropertyValue('--text-muted').trim() || (isDark ? '#6B7280' : '#9CA3AF'),
    outline: style.getPropertyValue('--border-default').trim(),
    surfaceContainer: style.getPropertyValue('--surface-card').trim(),
    onSurface: style.getPropertyValue('--text-primary').trim(),
    isDark
  };
}

// ── Timezone Badge & Selector ──

// Active timezone (empty = browser default)
let activeTimezone = '';

function getEffectiveTimezone() {
  return activeTimezone || Intl.DateTimeFormat().resolvedOptions().timeZone;
}

function tzAbbr(tz) {
  try {
    return new Date().toLocaleTimeString('en-US', { timeZone: tz, timeZoneName: 'short' }).split(' ').pop();
  } catch (e) {
    return tz.split('/').pop();
  }
}

function initTimezoneBadge() {
  const badge = document.getElementById('timezone-badge');
  if (!badge) return;

  // Load saved timezone from API
  loadTimezoneFromAPI().then(() => {
    updateTimezoneBadgeDisplay();
  });

  // Make badge clickable
  badge.style.cursor = 'pointer';
  badge.addEventListener('click', (e) => {
    e.stopPropagation();
    toggleTimezoneDropdown();
  });
}

async function loadTimezoneFromAPI() {
  try {
    const res = await authFetch(`${API_BASE}/api/settings`);
    if (!res.ok) return;
    const data = await res.json();
    if (data.timezone) {
      activeTimezone = data.timezone;
    }
  } catch (e) {
    // Silent fail — use browser default
  }
}

function updateTimezoneBadgeDisplay() {
  const badge = document.getElementById('timezone-badge');
  if (!badge) return;
  const tz = getEffectiveTimezone();
  const abbr = tzAbbr(tz);
  badge.textContent = abbr;
  badge.title = tz + (activeTimezone ? ' (saved)' : ' (browser)');
}

function toggleTimezoneDropdown() {
  let dropdown = document.getElementById('tz-dropdown');
  if (dropdown) {
    dropdown.remove();
    return;
  }

  const badge = document.getElementById('timezone-badge');
  if (!badge) return;

  // Get all IANA timezones
  let tzList;
  try {
    tzList = Intl.supportedValuesOf('timeZone');
  } catch (e) {
    tzList = ['UTC', 'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles',
      'Europe/London', 'Europe/Paris', 'Europe/Berlin', 'Asia/Tokyo', 'Asia/Shanghai',
      'Asia/Kolkata', 'Asia/Dubai', 'Australia/Sydney', 'Pacific/Auckland'];
  }

  dropdown = document.createElement('div');
  dropdown.id = 'tz-dropdown';
  dropdown.className = 'tz-dropdown';

  const searchInput = document.createElement('input');
  searchInput.type = 'text';
  searchInput.placeholder = 'Search timezone...';
  searchInput.className = 'tz-search';
  dropdown.appendChild(searchInput);

  // "Browser default" option
  const defaultOpt = document.createElement('div');
  defaultOpt.className = 'tz-option' + (!activeTimezone ? ' active' : '');
  defaultOpt.textContent = 'Browser Default';
  defaultOpt.dataset.tz = '';
  dropdown.appendChild(defaultOpt);

  const list = document.createElement('div');
  list.className = 'tz-list';
  dropdown.appendChild(list);

  function renderList(filter) {
    const filtered = filter
      ? tzList.filter(tz => tz.toLowerCase().includes(filter.toLowerCase()))
      : tzList;
    list.innerHTML = '';
    filtered.slice(0, 50).forEach(tz => {
      const opt = document.createElement('div');
      opt.className = 'tz-option' + (tz === activeTimezone ? ' active' : '');
      const abbr = tzAbbr(tz);
      opt.innerHTML = `<span class="tz-name">${tz.replace(/_/g, ' ')}</span><span class="tz-abbr">${abbr}</span>`;
      opt.dataset.tz = tz;
      list.appendChild(opt);
    });
    if (filtered.length > 50) {
      const more = document.createElement('div');
      more.className = 'tz-more';
      more.textContent = `${filtered.length - 50} more — type to filter`;
      list.appendChild(more);
    }
  }

  renderList('');

  searchInput.addEventListener('input', () => renderList(searchInput.value));

  // Handle selection (delegated)
  dropdown.addEventListener('click', async (e) => {
    const opt = e.target.closest('.tz-option');
    if (!opt) return;
    const tz = opt.dataset.tz;
    await saveTimezone(tz);
    dropdown.remove();
  });

  // Position dropdown below badge
  const rect = badge.getBoundingClientRect();
  dropdown.style.top = (rect.bottom + 4) + 'px';
  dropdown.style.right = (window.innerWidth - rect.right) + 'px';

  document.body.appendChild(dropdown);
  searchInput.focus();

  // Close on outside click
  function closeOnOutside(e) {
    if (!dropdown.contains(e.target) && e.target !== badge) {
      dropdown.remove();
      document.removeEventListener('click', closeOnOutside);
    }
  }
  setTimeout(() => document.addEventListener('click', closeOnOutside), 0);

  // Close on Escape
  function closeOnEsc(e) {
    if (e.key === 'Escape') {
      dropdown.remove();
      document.removeEventListener('keydown', closeOnEsc);
      document.removeEventListener('click', closeOnOutside);
    }
  }
  document.addEventListener('keydown', closeOnEsc);
}

async function saveTimezone(tz) {
  activeTimezone = tz;
  updateTimezoneBadgeDisplay();
  try {
    await fetch(`${API_BASE}/api/settings`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ timezone: tz })
    });
  } catch (e) {
    console.error('Failed to save timezone:', e);
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
    localStorage.setItem('syntrack-theme', next);
    if (State.chart) updateChartTheme();
  });
}

// ── Card Updates ──

function updateCard(quotaType, data) {
  const prev = State.currentQuotas[quotaType];
  State.currentQuotas[quotaType] = data;

  const progressEl = document.getElementById(`progress-${quotaType}`);
  const fractionEl = document.getElementById(`fraction-${quotaType}`);
  const percentEl = document.getElementById(`percent-${quotaType}`);
  const statusEl = document.getElementById(`status-${quotaType}`);
  const resetEl = document.getElementById(`reset-${quotaType}`);
  const countdownEl = document.getElementById(`countdown-${quotaType}`);

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

  if (resetEl && data.renewsAt) {
    resetEl.textContent = `Resets: ${formatDateTime(data.renewsAt)}`;
  }

  if (countdownEl) {
    countdownEl.textContent = formatDuration(data.timeUntilResetSeconds);
    countdownEl.classList.toggle('imminent', data.timeUntilResetSeconds < 1800);
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
    const res = await authFetch(`${API_BASE}/api/current`);
    if (!res.ok) throw new Error('Failed to fetch');
    const data = await res.json();

    requestAnimationFrame(() => {
      updateCard('subscription', data.subscription);
      updateCard('search', data.search);
      updateCard('toolCalls', data.toolCalls);

      const lastUpdated = document.getElementById('last-updated');
      if (lastUpdated) {
        lastUpdated.textContent = `Last updated: ${new Date().toLocaleTimeString()}`;
      }

      const statusDot = document.getElementById('status-dot');
      if (statusDot) statusDot.classList.remove('stale');

    });
  } catch (err) {
    console.error('Fetch error:', err);
    const statusDot = document.getElementById('status-dot');
    if (statusDot) statusDot.classList.add('stale');
  }
}

// ── Deep Insights (Interactive Cards) ──

const insightIcons = {
  positive: '<path d="M20 6L9 17l-5-5"/>',
  warning: '<path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0zM12 9v4M12 17h.01"/>',
  negative: '<circle cx="12" cy="12" r="10"/><path d="M15 9l-6 6M9 9l6 6"/>',
  info: '<circle cx="12" cy="12" r="10"/><path d="M12 16v-4M12 8h.01"/>'
};

// Quota-specific icons for insight cards
const quotaIcons = {
  subscription: '<rect x="2" y="7" width="20" height="14" rx="2" ry="2"/><path d="M16 21V5a2 2 0 0 0-2-2h-4a2 2 0 0 0-2 2v16"/>',
  search: '<circle cx="11" cy="11" r="8"/><path d="M21 21l-4.35-4.35"/>',
  toolCalls: '<path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z"/>',
  session: '<path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/>'
};

async function fetchDeepInsights() {
  const statsEl = document.getElementById('insights-stats');
  const cardsEl = document.getElementById('insights-cards');
  if (!cardsEl) return;

  try {
    const res = await authFetch(`${API_BASE}/api/insights`);
    if (!res.ok) throw new Error('Failed to fetch insights');
    const data = await res.json();

    // Render stat summary cards
    if (statsEl && data.stats && data.stats.length > 0) {
      statsEl.innerHTML = data.stats.map(s =>
        `<div class="insight-stat">
          <div class="insight-stat-value">${s.value}</div>
          <div class="insight-stat-label">${s.label}</div>
        </div>`
      ).join('');
    }

    // Combine server insights + client-side live insights
    let allInsights = data.insights || [];
    allInsights = allInsights.concat(computeClientInsights());

    if (allInsights.length > 0) {
      cardsEl.innerHTML = allInsights.map((i, idx) => {
        const icon = (i.quotaType && quotaIcons[i.quotaType]) ? quotaIcons[i.quotaType] : (insightIcons[i.severity] || insightIcons.info);
        return `<div class="insight-card severity-${i.severity}" data-insight-idx="${idx}" role="button" tabindex="0">
          <div class="insight-card-header">
            <svg class="insight-card-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">${icon}</svg>
            <span class="insight-card-title">${i.title}</span>
            <svg class="insight-card-chevron" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M6 9l6 6 6-6"/></svg>
          </div>
          ${i.metric ? `<div class="insight-card-metric">${i.metric}</div>` : ''}
          ${i.sublabel ? `<div class="insight-card-sublabel">${i.sublabel}</div>` : ''}
          <div class="insight-card-detail">
            <div class="insight-card-desc">${i.description}</div>
          </div>
        </div>`;
      }).join('');

      // Attach toggle events
      cardsEl.querySelectorAll('.insight-card').forEach(card => {
        const toggle = () => {
          const wasExpanded = card.classList.contains('expanded');
          // Collapse all others
          cardsEl.querySelectorAll('.insight-card.expanded').forEach(c => c.classList.remove('expanded'));
          if (!wasExpanded) card.classList.add('expanded');
        };
        card.addEventListener('click', toggle);
        card.addEventListener('keydown', e => {
          if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggle(); }
        });
      });
    } else {
      cardsEl.innerHTML = '<p class="insight-text">Keep tracking to see deep analytics.</p>';
    }
  } catch (err) {
    console.error('Insights fetch error:', err);
    cardsEl.innerHTML = '<p class="insight-text">Unable to load insights.</p>';
  }
}

function computeClientInsights() {
  const insights = [];

  // Live remaining quota — show percentage remaining + time
  ['subscription', 'search', 'toolCalls'].forEach(type => {
    const q = State.currentQuotas[type];
    if (!q || !q.limit || q.limit === 0) return;

    const pctUsed = q.percent;
    const remaining = q.limit - q.usage;
    if (remaining > 0 && q.timeUntilResetSeconds > 0) {
      insights.push({
        type: 'live',
        quotaType: type,
        severity: pctUsed > 80 ? 'warning' : pctUsed > 50 ? 'info' : 'positive',
        title: `${quotaNames[type]}`,
        metric: `${pctUsed.toFixed(1)}%`,
        sublabel: `${formatNumber(remaining)} remaining`,
        description: `Currently at ${pctUsed.toFixed(1)}% utilization (${formatNumber(q.usage)} of ${formatNumber(q.limit)}). Resets in ${formatDuration(q.timeUntilResetSeconds)}.`
      });
    }
  });

  // Session avg consumption %
  if (State.allSessionsData.length >= 2) {
    const recent = State.allSessionsData.slice(0, Math.min(State.allSessionsData.length, 10));
    let totalCons = 0;
    let maxCons = 0;
    recent.forEach(s => {
      const c = (s.maxSubRequests || 0) + (s.maxSearchRequests || 0) + (s.maxToolRequests || 0);
      totalCons += c;
      if (c > maxCons) maxCons = c;
    });
    const avg = totalCons / recent.length;
    if (avg > 0) {
      insights.push({
        type: 'session', quotaType: 'session', severity: 'info',
        title: 'Session Avg',
        metric: formatNumber(avg),
        sublabel: `per session (last ${recent.length})`,
        description: `Average consumption: ${formatNumber(avg)} requests/session across last ${recent.length} sessions.${maxCons > avg * 1.5 ? ` Peak session: ${formatNumber(maxCons)} (${(maxCons / avg).toFixed(1)}x average).` : ''}`
      });
    }
  }

  return insights;
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

function computeYMax(datasets) {
  let maxVal = 0;
  datasets.forEach(ds => {
    ds.data.forEach(v => { if (v > maxVal) maxVal = v; });
  });
  // ~2x the peak so at most half the chart is empty space
  const yMax = Math.min(Math.max(Math.ceil(maxVal * 2 / 10) * 10, 20), 100);
  return yMax;
}

function initChart() {
  const ctx = document.getElementById('usage-chart');
  if (!ctx || typeof Chart === 'undefined') return;

  Chart.register(crosshairPlugin);

  const colors = getThemeColors();

  State.chart = new Chart(ctx, {
    type: 'line',
    data: {
      labels: [],
      datasets: [
        { label: 'Subscription', data: [], borderColor: getComputedStyle(document.documentElement).getPropertyValue('--chart-subscription').trim() || '#0D9488', backgroundColor: 'rgba(13, 148, 136, 0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4 },
        { label: 'Search', data: [], borderColor: getComputedStyle(document.documentElement).getPropertyValue('--chart-search').trim() || '#F59E0B', backgroundColor: 'rgba(245, 158, 11, 0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4 },
        { label: 'Tool Calls', data: [], borderColor: getComputedStyle(document.documentElement).getPropertyValue('--chart-toolcalls').trim() || '#3B82F6', backgroundColor: 'rgba(59, 130, 246, 0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4 }
      ]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      interaction: { mode: 'index', intersect: false },
      plugins: {
        legend: { labels: { color: colors.text, usePointStyle: true, boxWidth: 8 } },
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
    const activeBtn = document.querySelector('.range-btn.active');
    range = activeBtn ? activeBtn.dataset.range : '6h';
  }
  try {
    const res = await authFetch(`${API_BASE}/api/history?range=${range}`);
    if (!res.ok) throw new Error('Failed to fetch history');
    const data = await res.json();

    if (!State.chart) initChart();
    if (!State.chart) return;

    State.chart.data.labels = data.map(d => new Date(d.capturedAt).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' }));
    State.chart.data.datasets[0].data = data.map(d => d.subscriptionPercent);
    State.chart.data.datasets[1].data = data.map(d => d.searchPercent);
    State.chart.data.datasets[2].data = data.map(d => d.toolCallsPercent);

    // Dynamic Y-axis
    State.chartYMax = computeYMax(State.chart.data.datasets);
    State.chart.options.scales.y.max = State.chartYMax;

    State.chart.update();
  } catch (err) {
    console.error('History fetch error:', err);
  }
}

// ── Cycles Table (client-side search/sort/paginate) ──

async function fetchCycles(quotaType) {
  if (quotaType === undefined) {
    const select = document.getElementById('cycle-quota-select');
    quotaType = select ? select.value : 'subscription';
  }
  try {
    const res = await authFetch(`${API_BASE}/api/cycles?type=${quotaType}`);
    if (!res.ok) throw new Error('Failed to fetch cycles');
    State.allCyclesData = await res.json();
    State.cyclesPage = 1;
    renderCyclesTable();
  } catch (err) {
    console.error('Cycles fetch error:', err);
  }
}

function getCycleComputedFields(cycle) {
  const start = new Date(cycle.cycleStart);
  const end = cycle.cycleEnd ? new Date(cycle.cycleEnd) : new Date();
  const durationMins = Math.round((end - start) / 60000);
  const hours = Math.floor(durationMins / 60);
  const mins = durationMins % 60;
  const rate = durationMins > 0 ? cycle.totalDelta / (durationMins / 60) : 0;
  return { start, end, durationMins, durationStr: `${hours}h ${mins}m`, rate, isActive: !cycle.cycleEnd };
}

function groupCyclesData(cycles, groupMs) {
  if (!cycles.length) return [];
  // Group cycles into time buckets based on their start time
  const buckets = new Map();
  cycles.forEach(c => {
    const start = new Date(c.cycleStart).getTime();
    const bucketKey = Math.floor(start / groupMs) * groupMs;
    if (!buckets.has(bucketKey)) {
      buckets.set(bucketKey, {
        bucketStart: bucketKey,
        bucketEnd: bucketKey + groupMs,
        cycles: [],
        peakRequests: 0,
        totalDelta: 0,
        firstCycleId: c.id,
        cycleCount: 0,
        earliestStart: c.cycleStart,
        latestEnd: c.cycleEnd
      });
    }
    const bucket = buckets.get(bucketKey);
    bucket.cycles.push(c);
    bucket.cycleCount++;
    bucket.peakRequests = Math.max(bucket.peakRequests, c.peakRequests);
    bucket.totalDelta += c.totalDelta;
    if (new Date(c.cycleStart) < new Date(bucket.earliestStart)) bucket.earliestStart = c.cycleStart;
    if (c.cycleEnd) {
      if (!bucket.latestEnd || new Date(c.cycleEnd) > new Date(bucket.latestEnd)) bucket.latestEnd = c.cycleEnd;
    } else {
      bucket.latestEnd = null; // has an active cycle
    }
  });
  return Array.from(buckets.values()).sort((a, b) => b.bucketStart - a.bucketStart);
}

function renderCyclesTable() {
  const tbody = document.getElementById('cycles-tbody');
  const infoEl = document.getElementById('cycles-info');
  const paginationEl = document.getElementById('cycles-pagination');
  if (!tbody) return;

  const now = Date.now();
  const rangeMs = State.cyclesRange;
  const groupMs = State.cyclesGroup;
  const cutoff = now - rangeMs;

  // Filter by time range
  let filtered = State.allCyclesData.filter(c => new Date(c.cycleStart).getTime() >= cutoff);

  // Group into time buckets
  const grouped = groupCyclesData(filtered, groupMs);

  // Compute display fields for each bucket
  let data = grouped.map((bucket, i) => {
    const start = new Date(bucket.earliestStart);
    const end = bucket.latestEnd ? new Date(bucket.latestEnd) : new Date();
    const durationMins = Math.round((end - start) / 60000);
    const hours = Math.floor(durationMins / 60);
    const mins = durationMins % 60;
    const rate = durationMins > 0 ? bucket.totalDelta / (durationMins / 60) : 0;
    const isActive = !bucket.latestEnd;
    return {
      ...bucket,
      _display: {
        id: bucket.cycleCount === 1 ? `#${bucket.firstCycleId}` : `${bucket.cycleCount} cycles`,
        start,
        end,
        durationMins,
        durationStr: `${hours}h ${mins}m`,
        rate,
        isActive
      }
    };
  });

  // Sort
  if (State.cyclesSort.key) {
    const dir = State.cyclesSort.dir === 'asc' ? 1 : -1;
    data.sort((a, b) => {
      let va, vb;
      switch (State.cyclesSort.key) {
        case 'id': va = a.firstCycleId; vb = b.firstCycleId; break;
        case 'start': va = a._display.start.getTime(); vb = b._display.start.getTime(); break;
        case 'end': va = a._display.end.getTime(); vb = b._display.end.getTime(); break;
        case 'duration': va = a._display.durationMins; vb = b._display.durationMins; break;
        case 'peak': va = a.peakRequests; vb = b.peakRequests; break;
        case 'total': va = a.totalDelta; vb = b.totalDelta; break;
        case 'rate': va = a._display.rate; vb = b._display.rate; break;
        default: va = 0; vb = 0;
      }
      return va > vb ? dir : va < vb ? -dir : 0;
    });
  }

  const total = data.length;
  const pageSize = State.cyclesPageSize;
  const totalPages = pageSize > 0 ? Math.max(1, Math.ceil(total / pageSize)) : 1;
  if (State.cyclesPage > totalPages) State.cyclesPage = totalPages;
  const page = State.cyclesPage;
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
    tbody.innerHTML = '<tr><td colspan="7" class="empty-state">No cycle data in this range.</td></tr>';
  } else {
    tbody.innerHTML = pageData.map(row => {
      const d = row._display;
      return `<tr>
        <td>${d.id}${d.isActive ? ' <span class="badge">Active</span>' : ''}</td>
        <td>${d.start.toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })}</td>
        <td>${row.latestEnd ? new Date(row.latestEnd).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : 'Active'}</td>
        <td>${d.durationStr}</td>
        <td>${formatNumber(row.peakRequests)}</td>
        <td>${formatNumber(row.totalDelta)}</td>
        <td>${d.durationMins > 0 ? formatNumber(d.rate) + '/hr' : '-'}</td>
      </tr>`;
    }).join('');
  }

  // Pagination
  if (paginationEl) {
    if (pageSize > 0 && totalPages > 1) {
      let html = `<button class="page-btn" ${page <= 1 ? 'disabled' : ''} data-table="cycles" data-page="${page - 1}">&laquo;</button>`;
      for (let p = 1; p <= totalPages; p++) {
        html += `<button class="page-btn ${p === page ? 'active' : ''}" data-table="cycles" data-page="${p}">${p}</button>`;
      }
      html += `<button class="page-btn" ${page >= totalPages ? 'disabled' : ''} data-table="cycles" data-page="${page + 1}">&raquo;</button>`;
      paginationEl.innerHTML = html;
    } else {
      paginationEl.innerHTML = '';
    }
  }
}

// ── Sessions Table (client-side search/sort/paginate + expandable rows) ──

async function fetchSessions() {
  try {
    const res = await authFetch(`${API_BASE}/api/sessions`);
    if (!res.ok) throw new Error('Failed to fetch sessions');
    State.allSessionsData = await res.json();
    State.sessionsPage = 1;
    renderSessionsTable();
  } catch (err) {
    console.error('Sessions fetch error:', err);
  }
}

function getSessionComputedFields(session) {
  const start = new Date(session.startedAt);
  const end = session.endedAt ? new Date(session.endedAt) : new Date();
  const durationMins = Math.round((end - start) / 60000);
  const hours = Math.floor(durationMins / 60);
  const mins = durationMins % 60;
  const totalConsumption = (session.maxSubRequests || 0) + (session.maxSearchRequests || 0) + (session.maxToolRequests || 0);
  const durationHours = durationMins / 60;
  const consumptionRate = durationHours > 0 ? totalConsumption / durationHours : 0;
  const snapshotsPerMin = durationMins > 0 ? (session.snapshotCount || 0) / durationMins : 0;
  return {
    start, end, durationMins,
    durationStr: `${hours}h ${mins}m`,
    isActive: !session.endedAt,
    totalConsumption, consumptionRate, snapshotsPerMin, durationHours
  };
}

function renderSessionsTable() {
  const tbody = document.getElementById('sessions-tbody');
  const infoEl = document.getElementById('sessions-info');
  const paginationEl = document.getElementById('sessions-pagination');
  if (!tbody) return;

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
        case 'sub': va = a.maxSubRequests; vb = b.maxSubRequests; break;
        case 'search': va = a.maxSearchRequests; vb = b.maxSearchRequests; break;
        case 'tool': va = a.maxToolRequests; vb = b.maxToolRequests; break;
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
    tbody.innerHTML = '<tr><td colspan="7" class="empty-state">No sessions recorded yet.</td></tr>';
  } else {
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
        <td colspan="7">
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
                <span class="detail-value">${session.pollInterval || '-'}s</span>
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
    if (pageSize > 0 && totalPages > 1) {
      let html = `<button class="page-btn" ${page <= 1 ? 'disabled' : ''} data-table="sessions" data-page="${page - 1}">&laquo;</button>`;
      for (let p = 1; p <= totalPages; p++) {
        html += `<button class="page-btn ${p === page ? 'active' : ''}" data-table="sessions" data-page="${p}">${p}</button>`;
      }
      html += `<button class="page-btn" ${page >= totalPages ? 'disabled' : ''} data-table="sessions" data-page="${page + 1}">&raquo;</button>`;
      paginationEl.innerHTML = html;
    } else {
      paginationEl.innerHTML = '';
    }
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
  }
}

// ── KPI Card Modal ──

function openModal(quotaType) {
  const modal = document.getElementById('detail-modal');
  const titleEl = document.getElementById('modal-title');
  const bodyEl = document.getElementById('modal-body');
  if (!modal || !bodyEl) return;

  const data = State.currentQuotas[quotaType];
  if (!data) return;

  titleEl.textContent = quotaNames[quotaType] || quotaType;

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

  // Fetch modal-specific data
  loadModalChart(quotaType);
  loadModalCycles(quotaType);
}

async function loadModalChart(quotaType) {
  const ctx = document.getElementById('modal-chart');
  if (!ctx || typeof Chart === 'undefined') return;

  // Destroy previous modal chart
  if (State.modalChart) {
    State.modalChart.destroy();
    State.modalChart = null;
  }

  const activeRange = document.querySelector('.range-btn.active');
  const range = activeRange ? activeRange.dataset.range : '6h';

  try {
    const res = await authFetch(`${API_BASE}/api/history?range=${range}`);
    if (!res.ok) return;
    const data = await res.json();

    const datasetKey = quotaType === 'subscription' ? 'subscriptionPercent' : quotaType === 'search' ? 'searchPercent' : 'toolCallsPercent';
    const style = getComputedStyle(document.documentElement);
    const colorMap = { subscription: style.getPropertyValue('--chart-subscription').trim() || '#0D9488', search: style.getPropertyValue('--chart-search').trim() || '#F59E0B', toolCalls: style.getPropertyValue('--chart-toolcalls').trim() || '#3B82F6' };
    const bgMap = { subscription: 'rgba(13,148,136,0.08)', search: 'rgba(245,158,11,0.08)', toolCalls: 'rgba(59,130,246,0.08)' };

    const colors = getThemeColors();
    const chartData = data.map(d => d[datasetKey]);
    const maxVal = Math.max(...chartData, 0);
    const yMax = Math.max(Math.ceil((maxVal + 15) / 10) * 10, 20);

    State.modalChart = new Chart(ctx, {
      type: 'line',
      data: {
        labels: data.map(d => new Date(d.capturedAt).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' })),
        datasets: [{
          label: quotaNames[quotaType],
          data: chartData,
          borderColor: colorMap[quotaType],
          backgroundColor: bgMap[quotaType],
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
    console.error('Modal chart error:', err);
  }
}

async function loadModalCycles(quotaType) {
  const apiType = quotaType === 'toolCalls' ? 'toolcall' : quotaType;
  try {
    const res = await authFetch(`${API_BASE}/api/cycles?type=${apiType}`);
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
    console.error('Modal cycles error:', err);
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

function setupCycleSelector() {
  const select = document.getElementById('cycle-quota-select');
  if (select) {
    select.addEventListener('change', (e) => fetchCycles(e.target.value));
  }
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
      renderCyclesTable();
    });
  }

  // Group pills
  const groupPills = document.getElementById('cycle-group-pills');
  if (groupPills) {
    groupPills.addEventListener('click', (e) => {
      const pill = e.target.closest('.filter-pill');
      if (!pill) return;
      groupPills.querySelectorAll('.filter-pill').forEach(p => p.classList.remove('active'));
      pill.classList.add('active');
      State.cyclesGroup = parseInt(pill.dataset.group, 10);
      State.cyclesPage = 1;
      renderCyclesTable();
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

  // Sort headers (cycles)
  document.querySelectorAll('#cycles-table th[data-sort-key]').forEach(th => {
    th.addEventListener('click', () => handleTableSort('cycles', th));
    th.addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleTableSort('cycles', th); } });
  });

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
        fetchCurrent().then(() => fetchDeepInsights()),
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
    const handler = () => openModal(card.dataset.quota);
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
    fetchCurrent().then(() => fetchDeepInsights());
    fetchHistory();
    fetchCycles();
    fetchSessions();
  }, REFRESH_INTERVAL);
}

// ── Init ──

document.addEventListener('DOMContentLoaded', () => {
  initTheme();
  initTimezoneBadge();
  setupRangeSelector();
  setupCycleSelector();
  setupCycleFilters();
  setupPasswordToggle();
  setupTableControls();
  setupHeaderActions();
  setupCardModals();

  if (document.getElementById('usage-chart')) {
    initChart();
    fetchCurrent().then(() => fetchDeepInsights());
    fetchHistory('6h');
    fetchCycles('subscription');
    fetchSessions();
    startCountdowns();
    startAutoRefresh();
  }
});
