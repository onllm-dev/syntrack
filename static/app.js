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

// ── Provider State ──
function getCurrentProvider() {
  const bothView = document.getElementById('both-view');
  if (bothView) return 'both';
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
  // Hidden quota datasets (persisted in localStorage)
  hiddenQuotas: new Set(),
};

// ── Persistence ──

function loadHiddenQuotas() {
  try {
    const stored = localStorage.getItem('syntrack-hidden-quotas');
    if (stored) {
      State.hiddenQuotas = new Set(JSON.parse(stored));
    }
  } catch (e) {
    console.warn('Failed to load hidden quotas:', e);
    State.hiddenQuotas = new Set();
  }
}

function saveHiddenQuotas() {
  try {
    localStorage.setItem('syntrack-hidden-quotas', JSON.stringify([...State.hiddenQuotas]));
  } catch (e) {
    console.warn('Failed to save hidden quotas:', e);
  }
}

// ── Provider Persistence ──

function loadDefaultProvider() {
  try {
    return localStorage.getItem('syntrack-default-provider') || '';
  } catch (e) {
    return '';
  }
}

function saveDefaultProvider(provider) {
  try {
    localStorage.setItem('syntrack-default-provider', provider);
  } catch (e) {
    console.warn('Failed to save default provider:', e);
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
    ? { 0: 'tokensLimit', 1: 'timeLimit' }
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
        // "both" response: { synthetic: {...}, zai: {...} }
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
    console.error('Fetch error:', err);
    const statusDot = document.getElementById('status-dot');
    if (statusDot) statusDot.classList.add('stale');
  }
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
  const statsEl = document.getElementById('insights-stats');
  const cardsEl = document.getElementById('insights-cards');
  if (!cardsEl) return;

  try {
    const res = await authFetch(`${API_BASE}/api/insights?${providerParam()}`);
    if (!res.ok) throw new Error('Failed to fetch insights');
    const data = await res.json();

    const provider = getCurrentProvider();
    let allStats = [];
    let allInsights = [];

    if (provider === 'both') {
      // "both" response: { synthetic: {stats, insights}, zai: {stats, insights} }
      if (data.synthetic) {
        if (data.synthetic.stats) allStats = allStats.concat(data.synthetic.stats.map(s => ({ ...s, label: `${s.label} (Syn)` })));
        if (data.synthetic.insights) allInsights = allInsights.concat(data.synthetic.insights.map(i => ({ ...i, title: `${i.title} (Syn)` })));
      }
      if (data.zai) {
        if (data.zai.stats) allStats = allStats.concat(data.zai.stats.map(s => ({ ...s, label: `${s.label} (Z.ai)` })));
        if (data.zai.insights) allInsights = allInsights.concat(data.zai.insights.map(i => ({ ...i, title: `${i.title} (Z.ai)` })));
      }
    } else {
      if (data.stats) allStats = data.stats;
      allInsights = data.insights || [];
    }
    allInsights = allInsights.concat(computeClientInsights());

    // Render stat summary cards
    if (statsEl && allStats.length > 0) {
      statsEl.innerHTML = allStats.map(s =>
        `<div class="insight-stat">
          <div class="insight-stat-value">${s.value}</div>
          <div class="insight-stat-label">${s.label}</div>
        </div>`
      ).join('');
    }

    if (allInsights.length > 0) {
      cardsEl.innerHTML = allInsights.map((i, idx) => {
        const icon = insightTitleIcons[i.title] || (i.quotaType && quotaIcons[i.quotaType]) || insightIcons[i.severity] || insightIcons.info;
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
  const provider = getCurrentProvider();

  if (provider === 'both') return insights; // Server handles both-mode insights

  // Live remaining quota — show percentage remaining + time
  const quotaTypes = provider === 'zai'
    ? ['tokensLimit', 'timeLimit', 'toolCalls']
    : ['subscription', 'search', 'toolCalls'];

  const zaiQuotaNames = { tokensLimit: 'Tokens Limit', timeLimit: 'Time Limit', toolCalls: 'Tool Calls' };

  quotaTypes.forEach(type => {
    const q = State.currentQuotas[type];
    if (!q || !q.limit || q.limit === 0) return;

    const names = provider === 'zai' ? zaiQuotaNames : quotaNames;
    const pctUsed = q.percent;
    const remaining = q.limit - q.usage;
    if (remaining > 0) {
      const hasReset = q.timeUntilResetSeconds && q.timeUntilResetSeconds > 0;
      insights.push({
        type: 'live',
        quotaType: type,
        severity: pctUsed > 80 ? 'warning' : pctUsed > 50 ? 'info' : 'positive',
        title: `${names[type] || type}`,
        metric: `${pctUsed.toFixed(1)}%`,
        sublabel: `${formatNumber(remaining)} remaining`,
        description: `Currently at ${pctUsed.toFixed(1)}% utilization (${formatNumber(q.usage)} of ${formatNumber(q.limit)}).${hasReset ? ` Resets in ${formatDuration(q.timeUntilResetSeconds)}.` : ''}`
      });
    }
  });

  // Session avg consumption % (only for Synthetic — Z.ai sessions don't track per-quota max values)
  if (provider !== 'zai' && State.allSessionsData.length >= 2) {
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
  
  // Add 20% padding above the max value for better visualization
  // Round up to nearest 5 for cleaner axis labels
  const paddedMax = maxVal * 1.2;
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
  const quotaMap = provider === 'zai' 
    ? ['tokensLimit', 'timeLimit']
    : ['subscription', 'search', 'toolCalls'];
  
  State.chart = new Chart(ctx, {
    type: 'line',
    data: {
      labels: [],
      datasets: [
        { label: 'Subscription', data: [], borderColor: getComputedStyle(document.documentElement).getPropertyValue('--chart-subscription').trim() || '#0D9488', backgroundColor: 'rgba(13, 148, 136, 0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4, hidden: State.hiddenQuotas.has('subscription') },
        { label: 'Search', data: [], borderColor: getComputedStyle(document.documentElement).getPropertyValue('--chart-search').trim() || '#F59E0B', backgroundColor: 'rgba(245, 158, 11, 0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4, hidden: State.hiddenQuotas.has('search') },
        { label: 'Tool Calls', data: [], borderColor: getComputedStyle(document.documentElement).getPropertyValue('--chart-toolcalls').trim() || '#3B82F6', backgroundColor: 'rgba(59, 130, 246, 0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4, hidden: State.hiddenQuotas.has('toolCalls') }
      ]
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
    const activeBtn = document.querySelector('.range-btn.active');
    range = activeBtn ? activeBtn.dataset.range : '6h';
  }
  try {
    const res = await authFetch(`${API_BASE}/api/history?range=${range}&${providerParam()}`);
    if (!res.ok) throw new Error('Failed to fetch history');
    const data = await res.json();

    const provider = getCurrentProvider();

    if (provider === 'both') {
      // "both" response: { synthetic: [...], zai: [...] }
      updateBothCharts(data);
      return;
    }

    if (!State.chart) initChart();
    if (!State.chart) return;

    State.chart.data.labels = data.map(d => new Date(d.capturedAt).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' }));

    if (provider === 'zai') {
      while (State.chart.data.datasets.length > 2) State.chart.data.datasets.pop();
      if (State.chart.data.datasets.length < 2) {
        State.chart.data.datasets = [
          { label: 'Tokens', data: [], borderColor: getComputedStyle(document.documentElement).getPropertyValue('--chart-subscription').trim() || '#0D9488', backgroundColor: 'rgba(13, 148, 136, 0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4, hidden: State.hiddenQuotas.has('tokensLimit') },
          { label: 'Time', data: [], borderColor: getComputedStyle(document.documentElement).getPropertyValue('--chart-search').trim() || '#F59E0B', backgroundColor: 'rgba(245, 158, 11, 0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4, hidden: State.hiddenQuotas.has('timeLimit') },
        ];
      }
      State.chart.data.datasets[0].label = 'Tokens';
      State.chart.data.datasets[0].data = data.map(d => d.tokensPercent);
      State.chart.data.datasets[0].hidden = State.hiddenQuotas.has('tokensLimit');
      State.chart.data.datasets[1].label = 'Time';
      State.chart.data.datasets[1].data = data.map(d => d.timePercent);
      State.chart.data.datasets[1].hidden = State.hiddenQuotas.has('timeLimit');
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
    console.error('History fetch error:', err);
  }
}

// ── "Both" Mode: Dual Charts ──

function updateBothCharts(data) {
  const container = document.querySelector('.chart-container');
  if (!container) return;

  // Create dual chart layout if not exists
  if (!container.classList.contains('both-charts')) {
    container.classList.add('both-charts');
    container.innerHTML = `
      <div class="chart-half"><h4 class="chart-half-label">Synthetic</h4><canvas id="usage-chart-syn"></canvas></div>
      <div class="chart-half"><h4 class="chart-half-label">Z.ai</h4><canvas id="usage-chart-zai"></canvas></div>
    `;
    // Hide the original single chart canvas
    const origCanvas = document.getElementById('usage-chart');
    if (origCanvas) origCanvas.style.display = 'none';
  }

  const style = getComputedStyle(document.documentElement);
  const colors = getThemeColors();

  // Synthetic chart
  const synCanvas = document.getElementById('usage-chart-syn');
  if (synCanvas && data.synthetic) {
    if (State.chartSyn) State.chartSyn.destroy();
    const synData = data.synthetic;
    State.chartSyn = new Chart(synCanvas, {
      type: 'line',
      data: {
        labels: synData.map(d => new Date(d.capturedAt).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' })),
        datasets: [
          { label: 'Subscription', data: synData.map(d => d.subscriptionPercent), borderColor: style.getPropertyValue('--chart-subscription').trim() || '#0D9488', backgroundColor: 'rgba(13,148,136,0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4 },
          { label: 'Search', data: synData.map(d => d.searchPercent), borderColor: style.getPropertyValue('--chart-search').trim() || '#F59E0B', backgroundColor: 'rgba(245,158,11,0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4 },
          { label: 'Tool Calls', data: synData.map(d => d.toolCallsPercent), borderColor: style.getPropertyValue('--chart-toolcalls').trim() || '#3B82F6', backgroundColor: 'rgba(59,130,246,0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4 }
        ]
      },
      options: buildChartOptions(colors)
    });
  }

  // Z.ai chart
  const zaiCanvas = document.getElementById('usage-chart-zai');
  if (zaiCanvas && data.zai) {
    if (State.chartZai) State.chartZai.destroy();
    const zaiData = data.zai;
    State.chartZai = new Chart(zaiCanvas, {
      type: 'line',
      data: {
        labels: zaiData.map(d => new Date(d.capturedAt).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' })),
        datasets: [
          { label: 'Tokens', data: zaiData.map(d => d.tokensPercent), borderColor: style.getPropertyValue('--chart-subscription').trim() || '#0D9488', backgroundColor: 'rgba(13,148,136,0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4 },
          { label: 'Time', data: zaiData.map(d => d.timePercent), borderColor: style.getPropertyValue('--chart-search').trim() || '#F59E0B', backgroundColor: 'rgba(245,158,11,0.06)', fill: true, tension: 0.4, borderWidth: 2, pointRadius: 0, pointHoverRadius: 4 }
        ]
      },
      options: buildChartOptions(colors)
    });
  }
}

function buildChartOptions(colors) {
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
      y: { grid: { color: colors.grid, drawBorder: false }, ticks: { color: colors.text, callback: v => v + '%' }, min: 0, max: 100 }
    }
  };
}

// ── Cycles Table (client-side search/sort/paginate) ──

async function fetchCycles(quotaType) {
  const provider = getCurrentProvider();
  if (quotaType === undefined) {
    const select = document.getElementById('cycle-quota-select');
    quotaType = select ? select.value : (provider === 'zai' ? 'tokens' : 'subscription');
  }

  // In "both" mode, route the type to the correct provider param
  let cycleUrl = `${API_BASE}/api/cycles?type=${quotaType}&${providerParam()}`;
  if (provider === 'both') {
    const zaiTypes = ['tokens', 'time'];
    if (zaiTypes.includes(quotaType)) {
      cycleUrl = `${API_BASE}/api/cycles?type=subscription&zaiType=${quotaType}&${providerParam()}`;
    }
  }

  try {
    const res = await authFetch(cycleUrl);
    if (!res.ok) throw new Error('Failed to fetch cycles');
    const data = await res.json();

    if (provider === 'both') {
      // "both" response: { synthetic: [...], zai: [...] }
      let merged = [];
      if (data.synthetic) merged = merged.concat(data.synthetic.map(c => ({ ...c, _provider: 'Syn' })));
      if (data.zai) merged = merged.concat(data.zai.map(c => ({ ...c, _provider: 'Z.ai' })));
      merged.sort((a, b) => new Date(b.cycleStart).getTime() - new Date(a.cycleStart).getTime());
      State.allCyclesData = merged;
    } else {
      State.allCyclesData = data || [];
    }
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
        latestEnd: c.cycleEnd,
        _provider: c._provider
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
        case 'provider': va = a._provider || ''; vb = b._provider || ''; break;
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

  const isBothCycles = getCurrentProvider() === 'both';
  const cycleColSpan = isBothCycles ? 8 : 7;

  if (total === 0) {
    tbody.innerHTML = `<tr><td colspan="${cycleColSpan}" class="empty-state">No cycle data in this range.</td></tr>`;
  } else {
    tbody.innerHTML = pageData.map(row => {
      const d = row._display;
      const providerCol = isBothCycles ? `<td><span class="badge">${row._provider || row.cycles?.[0]?._provider || '-'}</span></td>` : '';
      return `<tr>
        ${providerCol}
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
    const res = await authFetch(`${API_BASE}/api/sessions?${providerParam()}`);
    if (!res.ok) throw new Error('Failed to fetch sessions');
    const data = await res.json();
    const provider = getCurrentProvider();

    if (provider === 'both') {
      // "both" response: { synthetic: [...], zai: [...] }
      let merged = [];
      if (data.synthetic) merged = merged.concat(data.synthetic.map(s => ({ ...s, _provider: 'Syn' })));
      if (data.zai) merged = merged.concat(data.zai.map(s => ({ ...s, _provider: 'Z.ai' })));
      merged.sort((a, b) => new Date(b.startedAt).getTime() - new Date(a.startedAt).getTime());
      State.allSessionsData = merged;
    } else {
      State.allSessionsData = data;
    }
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

  const provider = getCurrentProvider();
  const isBoth = provider === 'both';
  const isZai = provider === 'zai';
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

  const activeRange = document.querySelector('.range-btn.active');
  const range = activeRange ? activeRange.dataset.range : '6h';

  const provider = effectiveProvider || getCurrentProvider();
  try {
    const res = await authFetch(`${API_BASE}/api/history?range=${range}&provider=${provider}`);
    if (!res.ok) return;
    const data = await res.json();
    let datasetKey;
    if (provider === 'zai') {
      datasetKey = quotaType === 'tokensLimit' ? 'tokensPercent' : 'timePercent';
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
    console.error('Modal chart error:', err);
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

function setupQuotaToggles() {
  // Add eye icons to quota cards for toggling visibility
  document.querySelectorAll('.quota-card').forEach(card => {
    const quotaType = card.dataset.quota;
    if (!quotaType) return;
    
    // Create eye toggle button
    const toggleBtn = document.createElement('button');
    toggleBtn.className = 'quota-toggle-btn';
    toggleBtn.setAttribute('aria-label', `Toggle ${quotaType} visibility`);
    toggleBtn.setAttribute('title', 'Click to hide/show on graph');
    toggleBtn.innerHTML = `
      <svg class="icon-eye" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
        <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/>
        <circle cx="12" cy="12" r="3"/>
      </svg>
      <svg class="icon-eye-off" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
        <path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/>
        <line x1="1" y1="1" x2="23" y2="23"/>
      </svg>
    `;
    
    // Check if initially hidden
    if (State.hiddenQuotas.has(quotaType)) {
      card.classList.add('quota-hidden');
      toggleBtn.classList.add('hidden');
    }
    
    // Add click handler (prevent modal from opening)
    toggleBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      toggleQuotaVisibility(quotaType);
      card.classList.toggle('quota-hidden', State.hiddenQuotas.has(quotaType));
      toggleBtn.classList.toggle('hidden', State.hiddenQuotas.has(quotaType));
    });
    
    // Add to card header
    const header = card.querySelector('.card-header');
    if (header) {
      header.appendChild(toggleBtn);
    }
  });
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
    fetchCurrent().then(() => fetchDeepInsights());
    fetchHistory();
    fetchCycles();
    fetchSessions();
  }, REFRESH_INTERVAL);
}

// ── Init ──

document.addEventListener('DOMContentLoaded', () => {
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

  // Load persisted state
  loadHiddenQuotas();
  
  initTheme();
  initTimezoneBadge();
  setupProviderSelector();
  setupRangeSelector();
  setupCycleSelector();
  setupCycleFilters();
  setupPasswordToggle();
  setupTableControls();
  setupHeaderActions();
  setupCardModals();
  setupQuotaToggles();

  if (document.getElementById('usage-chart') || document.getElementById('both-view')) {
    const provider = getCurrentProvider();
    const defaultCycleType = provider === 'both' ? 'subscription' : provider === 'zai' ? 'tokens' : 'subscription';
    initChart();
    fetchCurrent().then(() => fetchDeepInsights());
    fetchHistory('6h');
    fetchCycles(defaultCycleType);
    fetchSessions();
    startCountdowns();
    startAutoRefresh();

    // Update sessions table header for "both" mode
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
});
