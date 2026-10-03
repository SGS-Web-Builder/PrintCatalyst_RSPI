import {loadPaperStock} from './paper-stock.mjs';
import { initializeDashboardShell } from './dashboard-shell.mjs';
import { createPricingGrid } from './pricing-grid.mjs';
let pricingGridController;
import { createPrinterSetup } from './printer-setup.mjs';
let printerSetupController;
import { createOrderQueue } from './order-queue.mjs';
let orderQueueController;
import { loadShopSettings } from './shop-settings.mjs';
import { createOwnerClient } from './client.mjs';
import { fromMinorUnits, toMinorUnits, formatMoney } from './money.mjs';
const client = createOwnerClient();
const message = document.getElementById('message');
const authScreen = document.getElementById('auth-screen');
const appShell = document.getElementById('app-shell');
const sidebar = document.getElementById('sidebar');
const sidebarBackdrop = document.getElementById('sidebar-backdrop');
const sidebarOpen = document.getElementById('sidebar-open');
const sidebarClose = document.getElementById('sidebar-close');
// Phase 3F adds the orders surface: list, status actions and invoice issue. Both
// roles see the queue; owner-only actions are gated by the server. Phase 3G adds
// the reports and notifications surfaces (settings + SSE stream). Phase 4 adds
// the printer registry surface (discovery / enable / disable / verification).
// Phase 8 adds the licensing surface (8a) and payments surface (8b).

// sections: the panel IDs that participate in the signed-in app shell.
// `auth` is the unauthenticated login/create state and is handled separately
// because it shows the centered auth card with no sidebar.
const sections = ['business', 'pricing', 'operators', 'orders', 'reports', 'notifications', 'printers', 'paper-stock', 'idcards', 'passports', 'tunnel', 'licence', 'payments', 'pairing'];
// Keep the studio implementations available, but hide them from both dashboard roles.
const showPhotoStudios = false;
const ownerSectionAccess = {
  'paper-stock':true,
  business: true, pricing: true, operators: true,
  orders: true, reports: true, notifications: true,
  printers: true, idcards: showPhotoStudios, passports: showPhotoStudios,
  tunnel: true, licence: true, payments: true, pairing: true,
};
const operatorSectionAccess = {
  pricing: true, orders: true, reports: true, notifications: true,
  printers: true, idcards: showPhotoStudios, passports: showPhotoStudios,
};
let role = null;
let currentUsername = '';
let activeSection = 'orders';

// showAuth flips between the centered unauthenticated card and the full
// signed-in app shell. The auth screen is rendered without a sidebar; the
// app shell is rendered with one.
const showAuth = (mode /* 'create' | 'login' */) => {
  authScreen.hidden = false;
  appShell.hidden = true;
  document.getElementById('create-panel').hidden = mode !== 'create';
  document.getElementById('login-panel').hidden = mode !== 'login';
  if (message) message.textContent = '';
};

// showShell reveals the signed-in app shell and activates the requested
// section. Sections the current role cannot access are hidden — operators
// for example do not see the licence panel.
const showShell = (section) => {
  authScreen.hidden = true;
  appShell.hidden = false;
  applyRoleVisibility();
  renderUserInfo(currentUsername, role);
  setActiveSection(section || activeSection);
};

// Hide sidebar nav items the current role cannot access. Owners see every
// section; operators lose the business-management, licensing and pairing
// surfaces that the server enforces owner-only on.
const applyRoleVisibility = () => {
  const access = role === 'owner' ? ownerSectionAccess : operatorSectionAccess;
  document.querySelectorAll('[data-section]').forEach(item => {
    const sec = item.dataset.section;
    item.hidden = !access[sec];
  });
  // Hide entire nav-group labels whose every item is hidden so we don't
  // leave dangling "Business" or "Settings" headers.
  document.querySelectorAll('.nav-group').forEach(group => {
    const items = [...group.querySelectorAll('.nav-item')];
    const allHidden = items.every(i => i.hidden);
    const label = group.querySelector('.nav-group-label');
    if (label) label.hidden = allHidden;
  });
};

const setActiveSection = (section) => {
  if (section === 'tunnel') { section = 'business'; dashboardShell.selectSettingsTab('qr'); dashboardShell.selectQRTab('domain'); }
  const access = role === 'owner' ? ownerSectionAccess : operatorSectionAccess;
  const allowed = access[section];
  if (!allowed) {
    // Pick the first section this role can see.
    const first = sections.find(s => access[s]);
    if (!first) return;
    section = first;
  }
  activeSection = section;
  if(section==='paper-stock')void loadPaperStock(client);
  if(section==='pricing' && pricingGridController)pricingGridController.refreshIfClean();
  if(section==='payments') void refreshPaymentConnection();
  // Toggle panel visibility.
  for (const id of sections) {
    const el = document.getElementById(`${id}-panel`);
    if (el && id !== 'tunnel') el.hidden = (id !== section);
  }
  // Reflect selection in the sidebar nav.
  document.querySelectorAll('[data-section]').forEach(item => {
    const selected = item.dataset.section === section;
    item.classList.toggle('active', selected);
    if (selected) item.setAttribute('aria-current', 'page'); else item.removeAttribute('aria-current');
  });
  // Reflect selection in the mobile topbar.
  const topbarSection = document.getElementById('topbar-section');
  if (topbarSection) {
    const activeNavItem = document.querySelector(`[data-section="${section}"]`);
    topbarSection.textContent = activeNavItem ? activeNavItem.textContent.trim() : '';
  }
  // Auto-close the mobile sidebar after navigation.
  dashboardShell.afterNavigate();
  document.querySelector('.more-menu').open = false;
};

// Sidebar nav click handlers — both real <a> clicks and programmatic.
document.querySelectorAll('[data-section]').forEach(item => {
  item.addEventListener('click', event => {
    event.preventDefault();
    setActiveSection(item.dataset.section);
  });
});

// Update sidebar user info (avatar initial, name, role) from the active
// session. Falls back to neutral values while no user is loaded.
const sidebarUserName = () => document.getElementById('sidebar-user-name');
const sidebarUserRole = () => document.getElementById('sidebar-user-role');
const sidebarUserAvatar = () => document.getElementById('sidebar-user-avatar');

const renderUserInfo = (username, roleName) => {
  const name = (username || '').trim() || 'Print Catalyst';
  const safeName = name.replace(/[^\p{L}\p{N}]/gu, '').slice(0, 1).toUpperCase() || 'P';
  if (sidebarUserName()) sidebarUserName().textContent = name;
  if (sidebarUserRole()) {
    const labels = { owner: 'Owner', operator: 'Operator' };
    sidebarUserRole().textContent = labels[roleName] || 'Local server';
  }
  if (sidebarUserAvatar()) sidebarUserAvatar().textContent = safeName;
};

const dashboardShell = initializeDashboardShell();

// Backwards-compatible shim — the older code calls `show('signedIn')` etc.
// Map those calls onto the new shell / section navigation.
const show = (view) => {
  if (view === 'create') showAuth('create');
  else if (view === 'login') showAuth('login');
  else if (view === 'signedIn') showShell(activeSection);
  else showShell(view);
};

const details = form => Object.fromEntries(new FormData(form).entries());
const entriesContainer = () => document.getElementById('pricing-entries');
const operatorsContainer = () => document.getElementById('operators-list');
const businessCurrency = () => document.querySelector('#business-form [name="currency"]').value.trim().toUpperCase() || 'your business currency';

// Decimal precision is not a merchant setting. The local service derives it from
// supported-currency metadata for the saved business currency and reports it;
// this page only ever converts amounts with the value it was given. UNSUPPORTED
// mirrors the server's sentinel for a currency it has no exponent for.
const UNSUPPORTED = -1;
let currencyInfo = null; // { code, minorUnits, supported } from the business profile
let savedBook = null;    // the stored price book, or null for a first-time merchant

// savePrecision is the exponent the next save will be stored under, or
// UNSUPPORTED while pricing cannot be saved at all.
function savePrecision() {
  if (savedBook) return savedBook.currencySupported ? savedBook.derivedMinorUnits : UNSUPPORTED;
  return currencyInfo && currencyInfo.supported ? currencyInfo.minorUnits : UNSUPPORTED;
}

// storedPrecision is the exponent the saved integers were written under. It can
// differ from savePrecision, and rendering a stored amount with the wrong one
// would silently move every price by a power of ten.
const storedPrecision = () => (savedBook ? savedBook.currencyMinorUnits : savePrecision());

const decimalPlaces = places => `${places} decimal place${places === 1 ? '' : 's'}`;

function addEntry({ paperSize = '', colourMode = 'monochrome', sides = 'one-sided', amount = '', tiers = [] } = {}) {
  const row = document.getElementById('pricing-row').content.firstElementChild.cloneNode(true);
  row.querySelector('[name="paperSize"]').value = paperSize;
  row.querySelector('[name="colourMode"]').value = colourMode;
  row.querySelector('[name="sides"]').value = sides;
  row.querySelector('[name="amount"]').value = amount;
  entriesContainer().append(row);
  const list = row.querySelector('[data-tier-list]');
  for (const tier of tiers) addTierRow(list, tier);
  return row;
}

// Adds one tier row to the given list. Tiers never cross combinations and never
// survive the removal of their parent entry: the local server enforces both
// rules, so this helper just renders and lets the server be the source of truth.
function addTierRow(list, { minQuantity = '', unitPriceMinor } = {}) {
  const row = document.getElementById('tier-row').content.firstElementChild.cloneNode(true);
  row.querySelector('[name="tierQuantity"]').value = minQuantity === '' ? '' : String(minQuantity);
  if (unitPriceMinor !== undefined && unitPriceMinor !== null) {
    // Render stored integer minor units with the same exponent the row's base uses.
    row.querySelector('[name="tierAmount"]').value = fromMinorUnits(Number(unitPriceMinor), storedPrecision());
  } else {
    row.querySelector('[name="tierAmount"]').value = '';
  }
  list.append(row);
  return row;
}

// Renders the stored price book, or one empty row for a first-time merchant.
// Amounts are converted from integer minor units with exact decimal string
// arithmetic; no floating-point value is used for money. A stored amount is
// always rendered with the exponent it was stored under, so a precision change
// is surfaced to the merchant instead of quietly moving every price.
async function loadLegacyPricing() {
  entriesContainer().replaceChildren();
  const warning = document.getElementById('pricing-warning');
  const summary = document.getElementById('pricing-currency');
  const precision = document.getElementById('pricing-precision');
  const confirmRow = document.getElementById('pricing-confirm');
  const confirmBox = confirmRow.querySelector('input');
  const confirmText = document.getElementById('pricing-confirm-text');
  const warnings = [];
  warning.textContent = '';
  confirmRow.hidden = true;
  confirmBox.checked = false;
  confirmText.textContent = '';
  savedBook = null;
  try { savedBook = await client.request('GET', '/api/v1/owner/pricing'); }
  catch (error) { if (error.status !== 404) throw error; }

  const next = savePrecision();
  const target = businessCurrency();
  if (next === UNSUPPORTED) {
    precision.textContent = 'Decimal places are unknown for this currency.';
    warnings.push(`Prices cannot be saved in ${target}. The local service has no decimal precision for that currency and will not guess one, because a wrong precision rescales every amount you store. Save business details with a supported currency first.`);
  } else {
    precision.textContent = `Prices are stored in ${target} with ${decimalPlaces(next)}. Decimal places follow your business currency and cannot be changed here.`;
  }

  if (!savedBook) {
    summary.textContent = next === UNSUPPORTED ? 'No prices stored yet.' : `Prices will be stored in ${target}.`;
    addEntry();
    warning.textContent = warnings.join(' ');
    return;
  }

  summary.textContent = `${savedBook.entries.length} price${savedBook.entries.length === 1 ? '' : 's'} stored in ${savedBook.currency} · ${savedBook.priceUnitLabel}.`;
  if (savedBook.precisionMismatch) {
    warnings.push(`These prices were saved with ${decimalPlaces(savedBook.currencyMinorUnits)}, but ${target} uses ${decimalPlaces(next)}. They are shown below exactly as stored and have not been rescaled. Check each amount, then tick the confirmation to store them with ${decimalPlaces(next)}.`);
    confirmText.textContent = `I have reviewed every amount above and want them stored with ${decimalPlaces(next)} for ${target}.`;
    confirmRow.hidden = false;
  }
  if (savedBook.currencyMismatch) {
    warnings.push(`These prices were saved in ${savedBook.currency}, which no longer matches your business currency. Review and save them again to confirm each amount.`);
  }
  warning.textContent = warnings.join(' ');
  for (const entry of savedBook.entries) {
    addEntry({
      paperSize: entry.paperSize, colourMode: entry.colourMode, sides: entry.sides,
      amount: fromMinorUnits(entry.unitPriceMinor, storedPrecision()),
      tiers: (entry.tiers || []).map(tier => ({
        minQuantity: tier.minQuantity, unitPriceMinor: tier.unitPriceMinor,
      })),
    });
  }
}

function collectEntries() {
  // The exponent comes from the service, never from a field on this page.
  const minorUnits = savePrecision();
  if (minorUnits === UNSUPPORTED) {
    throw new Error('Pricing cannot be saved until business details use a currency the local service supports.');
  }
  const rows = [...entriesContainer().querySelectorAll('.entry')];
  if (rows.length === 0) throw new Error('Add at least one price row.');
  const correcting = Boolean(savedBook && savedBook.precisionMismatch);
  const confirmBox = document.querySelector('#pricing-confirm input');
  if (correcting && !confirmBox.checked) {
    throw new Error(`Tick the confirmation to store these prices with ${decimalPlaces(minorUnits)}.`);
  }
  // Mirror the server-side check for free tiers so the merchant sees the
  // warning and the checkbox before the request is sent.
  const confirmFreeBox = document.querySelector('#pricing-confirm-free input');
  const hasFree = rows.some(row => [...row.querySelectorAll('[data-tier-list] .tier')]
    .some(tierRow => {
      const value = tierRow.querySelector('[name="tierAmount"]').value.trim();
      if (!value) return false;
      try { return toMinorUnits(value, minorUnits) === 0; }
      catch { return false; }
    }));
  document.getElementById('pricing-confirm-free').hidden = !hasFree;
  document.getElementById('pricing-free-warning').hidden = !hasFree;
  if (hasFree) {
    document.getElementById('pricing-confirm-free-text').textContent = `One or more quantity tiers on this page charges zero. Tick this box to confirm that is intentional.`;
    document.getElementById('pricing-free-warning').textContent = `At least one tier is free (0.00). The save will be refused unless you confirm this is intentional.`;
  }
  return {
    entries: rows.map(row => {
      // Each tier row collects a min quantity and a discounted per-sheet amount.
      // Quantity must be a whole number ≥ 2; amount is converted to minor units
      // with the same exponent as the parent row's base, so a tier's price
      // always means the same currency precision as the row it belongs to.
      const tierRows = [...row.querySelectorAll('[data-tier-list] .tier')];
      const tiers = tierRows.map(tierRow => ({
        minQuantity: Number(tierRow.querySelector('[name="tierQuantity"]').value),
        unitPriceMinor: toMinorUnits(tierRow.querySelector('[name="tierAmount"]').value, minorUnits),
      })).filter(tier => Number.isFinite(tier.minQuantity) && tier.minQuantity >= 2)
        // Server sorts tiers ascending and rejects duplicates; mirroring that
        // here keeps the merchant's mental model consistent with the response.
        .sort((a, b) => a.minQuantity - b.minQuantity);
      return {
        paperSize: row.querySelector('[name="paperSize"]').value,
        colourMode: row.querySelector('[name="colourMode"]').value,
        sides: row.querySelector('[name="sides"]').value,
        unitPriceMinor: toMinorUnits(row.querySelector('[name="amount"]').value, minorUnits),
        tiers,
      };
    }),
    // Sent only as the merchant's own acknowledgement. The service refuses a
    // precision change without it.
    confirmPrecisionCorrection: correcting && confirmBox.checked,
    // A single flag covers every zero-priced tier in the request; the merchant
    // ticks it once and the server stores the whole book atomically.
    confirmFreePricing: confirmFreeBox.checked,
  };
}

// Renders the operator roster. The server returns operator rows in username
// order; the UI mirrors that ordering so a re-enable and disable cycle does
// not silently shuffle the list. Disabled operators are styled differently so
// the merchant can see at a glance which accounts are currently locked out.
async function loadOperators() {
  const list = operatorsContainer();
  list.replaceChildren();
  const note = document.getElementById('operators-message');
  note.textContent = '';
  const data = await client.request('GET', '/api/v1/owner/operators');
  const operators = data.operators || [];
  if (operators.length === 0) {
    note.textContent = 'No operator accounts yet. Add one above to grant a delegated sign-in.';
    return;
  }
  for (const op of operators) list.append(renderOperatorRow(op));
}

function renderOperatorRow(op) {
  const row = document.getElementById('operator-row').content.firstElementChild.cloneNode(true);
  row.dataset.operatorId = String(op.id);
  row.querySelector('.operator-name').textContent = op.username;
  const status = row.querySelector('.operator-status');
  status.textContent = op.enabled ? 'Enabled' : 'Disabled';
  status.dataset.enabled = op.enabled ? 'true' : 'false';
  const toggle = row.querySelector('.operator-toggle');
  toggle.textContent = op.enabled ? 'Disable' : 'Enable';
  toggle.dataset.next = op.enabled ? 'false' : 'true';
  row.querySelector('.operator-delete').dataset.operatorName = op.username;
  return row;
}

async function refreshSetupProgress() {
  const state = await client.request('GET', '/api/v1/setup/status');
  document.getElementById('setup-progress').textContent = state.ProductionReady ? 'Shop setup is complete. Customer orders are enabled.' : `Setup checks completed: ${state.Completed}. Next: ${state.Next || 'complete'}.`;
}

function displayBusinessName(value) {
  const name=value?.trim() || 'Print Catalyst';
  for(const element of document.querySelectorAll('.auth-brand strong, .sidebar-brand-text strong, .topbar-brand')) { element.textContent=name; element.title=name; }
  document.title=name+' · Dashboard';
}
async function openBusiness() {
  show('signedIn');
  // Operators cannot edit the business profile; the form is hidden by the role
  // map and the server enforces CanEditBusiness independently. The business
  // API call below still succeeds for operators (read-only), so we always
  // populate currencyInfo from the response.
  const form = document.getElementById('business-form');
  currencyInfo = null;
  try {
    const profile = await client.request('GET', '/api/v1/owner/business');
    displayBusinessName(profile.name);
    for (const [key, value] of Object.entries(profile)) if (form.elements.namedItem(key)) form.elements.namedItem(key).value = value;
    currencyInfo = { code: profile.currency, minorUnits: profile.currencyMinorUnits, supported: Boolean(profile.currencySupported) };
  } catch (error) { if (error.status !== 404) throw error; }
  await loadPricing();
  if (role === 'owner') await loadOperators();
  await loadOrders(); // both roles see the queue.
  await loadReports();
  await loadNotificationSettings();
  connectNotificationStream();
  await loadPrinters();
  if (showPhotoStudios) {
    await loadIDCards();
    await loadPassports();
  }
  if (role === 'owner') await loadTunnel();
  if (role === 'owner') await loadShopSettings(client);
  if (role === 'owner') await loadLicence();
  if (role === 'owner') await loadPayments();
  if (role === 'owner') await loadPairing();
  if (role === 'owner') await refreshSetupProgress();
  message.textContent = '';
}

async function start() {
  try {
    const { ownerExists,licenseRequired } = await client.request('GET', '/api/v1/setup/owner');
    document.getElementById('setup-license-label').hidden=!licenseRequired;
    const keyInput=document.querySelector('#create-form [name="licenseKey"]');keyInput.disabled=!licenseRequired;keyInput.required=Boolean(licenseRequired);
    if (!ownerExists) { show('create'); role = null; currentUsername = ''; return; }
    try {
      const session = await client.request('GET', '/api/v1/owner/session');
      role = session.role || null;
      currentUsername = session.username || '';
      await openBusiness();
    } catch (error) {
      if (error.status !== 401) throw error;
      role = null;
      currentUsername = '';
      show('login');
      message.textContent = '';
    }
  } catch (error) { role = null; currentUsername = ''; show('login'); message.textContent = `Open this page directly on the merchant computer using its local address. ${error.message}`; }
}

for (const name of ['create', 'login', 'business']) {
  const form = document.getElementById(`${name}-form`);
  form.addEventListener('submit', async event => {
    event.preventDefault(); const button = form.querySelector('button[type="submit"]');button.disabled = true;message.textContent = '';
    try {
      if (name === 'create') {
        await client.request('POST', '/api/v1/setup/owner', details(form)); form.reset();show('login');message.textContent = 'Owner created. Sign in to configure your business.';
      } else if (name === 'login') {
        const formData = details(form);
        const result = await client.request('POST', '/api/v1/owner/login', formData);
        role = (result && result.role) || null;
        currentUsername = formData.username || '';
        form.reset();
        await openBusiness();
      } else {
        await client.request('PUT', '/api/v1/owner/business', details(form)); await openBusiness();message.textContent = 'Business details saved on this computer.';
      }
    } catch (error) {
      const isAuthError = error.status === 401;
      if (isAuthError && name !== 'business') {
        // Expired or invalid session after a successful login: show the
        // error first so the user knows WHY they are back on the login form.
        message.textContent = error.message || `Session expired or invalid (${error.status}). Sign in again.`;
      } else if (isAuthError && name === 'business') {
        role = null; show('login');
      }
      if (!isAuthError || name !== 'business') {
        message.textContent = error.message || `Request failed (${error.status || 'network'}). Check the browser console for details.`;
      }
    } finally { button.disabled = false; }
  });
}

document.getElementById('pricing-form').addEventListener('submit', async event => {
  event.preventDefault();
  const form = event.currentTarget;
  const button = form.querySelector('button[type="submit"]');
  const note = document.getElementById('pricing-message');
  button.disabled = true; note.textContent = '';
  try {
    const saved = await client.request('PUT', '/api/v1/owner/pricing', collectEntries());
    // Adopt the freshly stored book so the precision state reflects what is now
    // on disk, and a confirmed correction is not demanded twice.
    savedBook = saved;
    document.getElementById('pricing-confirm').hidden = true;
    document.getElementById('pricing-warning').textContent = '';
    entriesContainer().replaceChildren();
    for (const entry of saved.entries) {
      addEntry({
        paperSize: entry.paperSize, colourMode: entry.colourMode, sides: entry.sides,
        amount: fromMinorUnits(entry.unitPriceMinor, saved.currencyMinorUnits),
        tiers: (entry.tiers || []).map(tier => ({
          minQuantity: tier.minQuantity, unitPriceMinor: tier.unitPriceMinor,
        })),
      });
    }
    note.textContent = `${saved.entries.length} price${saved.entries.length === 1 ? '' : 's'} saved locally in ${saved.currency}. The pricing setup check is complete; printer capability checks are still open and customers cannot order yet.`;
    if (role === 'owner') await refreshSetupProgress();
  } catch (error) {
    if (error.status === 401) { role = null; show('login'); message.textContent = error.message; } else if (error.status === 403 && role === 'operator') { note.textContent = 'Operators cannot edit pricing. Sign out, then sign in as the owner to make changes.'; } else { note.textContent = error.message; }
  } finally { button.disabled = false; }
});

document.getElementById('pricing-add').addEventListener('click', () => addEntry());
entriesContainer().addEventListener('click', event => {
  if (event.target.closest('.remove')) {
    const container = entriesContainer();
    event.target.closest('.entry').remove();
    if (container.querySelectorAll('.entry').length === 0) addEntry();
    return;
  }
  if (event.target.closest('.add-tier')) {
    const list = event.target.closest('.entry').querySelector('[data-tier-list]');
    addTierRow(list);
    return;
  }
  if (event.target.closest('.remove-tier')) {
    event.target.closest('.tier').remove();
  }
});

document.getElementById('operators-create-form').addEventListener('submit', async event => {
  event.preventDefault();
  const form = event.currentTarget;
  const button = form.querySelector('button[type="submit"]');
  const note = document.getElementById('operators-message');
  button.disabled = true; note.textContent = '';
  try {
    const payload = details(form);
    payload.username = (payload.username || '').toLowerCase();
    await client.request('POST', '/api/v1/owner/operators', payload);
    form.reset();
    await loadOperators();
    note.textContent = 'Operator added. They can now sign in with their username and password.';
  } catch (error) {
    note.textContent = error.message;
  } finally { button.disabled = false; }
});

document.getElementById('operators-refresh').addEventListener('click', async event => {
  event.target.disabled = true;
  try { await loadOperators(); }
  catch (error) { document.getElementById('operators-message').textContent = error.message; }
  finally { event.target.disabled = false; }
});

document.getElementById('orders-refresh').addEventListener('click', async event => {
  event.target.disabled = true;
  try { await loadOrders(); }
  catch (error) { orderMessage().textContent = error.message; }
  finally { event.target.disabled = false; }
});

operatorsContainer().addEventListener('click', async event => {
  const row = event.target.closest('.operator');
  if (!row) return;
  const id = row.dataset.operatorId;
  if (event.target.closest('.operator-toggle')) {
    const next = event.target.dataset.next === 'true';
    event.target.disabled = true;
    try {
      await client.request('PATCH', `/api/v1/owner/operators/${id}`, { enabled: next });
      await loadOperators();
    } catch (error) {
      document.getElementById('operators-message').textContent = error.message;
    } finally { event.target.disabled = false; }
    return;
  }
  if (event.target.closest('.operator-delete')) {
    const name = event.target.dataset.operatorName || 'this operator';
    if (!confirm(`Delete operator "${name}"? Their active session will be revoked immediately.`)) return;
    event.target.disabled = true;
    try {
      await client.request('DELETE', `/api/v1/owner/operators/${id}`);
      await loadOperators();
    } catch (error) {
      document.getElementById('operators-message').textContent = error.message;
    } finally { event.target.disabled = false; }
  }
});

const handleLogout = async (button) => {
  button.disabled = true;
  try {
    await client.request('POST', '/api/v1/owner/logout', {});
    document.getElementById('business-form').reset();
    savedBook = null;
    currencyInfo = null;
    entriesContainer().replaceChildren();
    operatorsContainer().replaceChildren();
    role = null;
    currentUsername = '';
    show('login');
    message.textContent = 'Signed out.';
  } catch (error) {
    message.textContent = error.message;
  } finally {
    button.disabled = false;
  }
};
document.getElementById('logout').addEventListener('click', event => handleLogout(event.target));
const topbarLogout = document.getElementById('topbar-logout');
if (topbarLogout) topbarLogout.addEventListener('click', event => handleLogout(event.currentTarget));

// ---------- Phase 3F: orders queue, status actions, invoice issuance ----------

const ordersContainer = () => document.getElementById('orders-list');
const orderDetail = () => document.getElementById('order-detail');
const orderMessage = () => document.getElementById('orders-message');

// Status set the owner can move the order into from its current value. Mirrors
// the allowedTransitions graph in the orders service.
const allowedTransitions = {
  pending_payment: ['paid', 'cancelled'],
  paid: ['cancelled'],
  dispatched: ['completed', 'cancelled'],
  completed: [],
  cancelled: [],
  failed: [],
};

async function loadOrders() {
  orderQueueController ||= createOrderQueue(client,()=>role);
  await orderQueueController.refresh();
}

function renderOrderRow(o) {
  const row = document.createElement('div');
  row.className = 'order-row';
  row.dataset.orderId = o.id;
  const left = document.createElement('div');
  left.className = 'order-meta';
  const name = document.createElement('span');
  name.className = 'order-name';
  name.textContent = o.customerName || 'Customer';
  const info = document.createElement('span');
  info.className = 'order-info';
  const money = formatMoney(o.totalMinor, o.currencyMinorUnits) + ' ' + o.currency;
  info.textContent = `${money} · ${humanStatus(o.status)} · ${new Date(o.createdAt * 1000).toLocaleString()}`;
  left.append(name, info);
  const actions = document.createElement('div');
  actions.className = 'order-actions';
  const openBtn = document.createElement('button');
  openBtn.className = 'secondary';
  openBtn.textContent = 'Open';
  openBtn.addEventListener('click', () => openOrder(o.id));
  actions.append(openBtn);
  row.append(left, actions);
  return row;
}

function humanStatus(status) {
  const map = {
    pending_payment: 'Pending payment',
    paid: 'Paid',
    dispatched: 'Dispatched',
    completed: 'Completed',
    cancelled: 'Cancelled',
    failed: 'Failed',
  };
  return map[status] || status;
}

async function openOrder(orderId) {
  const detail = orderDetail();
  detail.replaceChildren();
  const loading = document.createElement('p');
  loading.className = 'help';
  loading.textContent = 'Loading order…';
  detail.append(loading);
  try {
    const o = await client.request('GET', '/api/v1/owner/orders/' + orderId);
    renderOrderDetail(o, detail);
  } catch (error) {
    detail.replaceChildren();
    const err = document.createElement('p');
    err.className = 'error';
    err.textContent = error.message;
    detail.append(err);
  }
}

function renderOrderDetail(o, detail) {
  detail.replaceChildren();
  const card = document.createElement('div');
  card.className = 'order-detail-card';
  const title = document.createElement('h3');
  title.textContent = `Order ${o.id}`;
  card.append(title);
  const meta = document.createElement('p');
  meta.className = 'help';
  meta.textContent = `${o.customerName} · ${o.customerPhone}${o.customerEmail ? ' · ' + o.customerEmail : ''}`;
  card.append(meta);
  const status = document.createElement('p');
  status.innerHTML = `Status: <strong>${humanStatus(o.status)}</strong>`;
  card.append(status);

  // Status action buttons (owners only). Operators see read-only text.
  const next = (allowedTransitions[o.status] || []);
  if (role === 'owner' && next.length > 0) {
    const actions = document.createElement('div');
    actions.className = 'order-detail-actions';
    for (const target of next) {
      const btn = document.createElement('button');
      btn.textContent = humanStatus(target);
      btn.disabled = false;
      btn.addEventListener('click', () => transitionOrder(o.id, target));
      actions.append(btn);
    }
    card.append(actions);
  }

  if (role === 'owner' && o.status === 'paid') {
    const release = document.createElement('button'); release.textContent = 'Print order';
    const retry = document.createElement('button'); retry.textContent = 'Retry failed or interrupted printing';
    const note = document.createElement('p'); note.textContent = 'Before retrying, check the printer queue and output tray. An interrupted job may already have printed.';
    const sendPrint = async (retry) => {
      try { await client.request('POST', '/api/v1/owner/orders/' + o.id + '/print', {retry}); note.textContent = 'Print request queued.'; }
      catch (error) { note.textContent = error.message; }
    };
    release.addEventListener('click', () => sendPrint(false));
    retry.addEventListener('click', () => { if (window.confirm('Have you checked the printer queue and output tray? Retry may print an interrupted document again.')) void sendPrint(true); });
    card.append(release, retry, note);
    void client.request('GET', '/api/v1/owner/orders/' + o.id + '/print').then(lines => {
      for (const line of lines) { const p = document.createElement('p'); p.textContent = [line.state, line.error, line.jobId].filter(Boolean).join(' · '); card.append(p); }
    }).catch(error => { note.textContent = error.message; });
  }

  // Invoice issuance: the owner can issue an invoice when the order is paid.
  if (role === 'owner' && o.status === 'paid') {
    const issue = document.createElement('button');
    issue.textContent = 'Issue invoice';
    issue.addEventListener('click', () => issueInvoice(o.id));
    card.append(issue);
  }

  // Lines.
  if ((o.lines || []).length > 0) {
    const linesHeading = document.createElement('h4');
    linesHeading.textContent = 'Lines';
    card.append(linesHeading);
    const ul = document.createElement('ul');
    ul.className = 'order-lines';
    for (const l of o.lines) {
      const li = document.createElement('li');
      const money = formatMoney(l.lineTotalMinor, o.currencyMinorUnits);
      li.textContent = `${l.paperSize} · ${l.colourMode} · ${l.sides} · ${l.copies} copy · pages ${l.pages?.length ? l.pages.join(', ') : `${l.pageRangeStart}-${l.pageRangeEnd}`} = ${money}`;
      ul.append(li);
    }
    card.append(ul);
  }

  // Existing invoice link.
  const existingInvoice = document.createElement('div');
  existingInvoice.className = 'order-invoice';
  const invoiceLink = document.createElement('a');
  invoiceLink.id = 'order-invoice-link';
  invoiceLink.href = '#';
  invoiceLink.textContent = 'View invoice';
  existingInvoice.append(invoiceLink);
  card.append(existingInvoice);
  loadInvoiceLink(o.id);

  detail.append(card);
}

async function transitionOrder(orderId, target) {
  try {
    await client.request('PUT', '/api/v1/owner/orders/' + orderId + '/status', { status: target });
    orderMessage().textContent = `Order ${orderId} is now ${humanStatus(target)}.`;
    await openOrder(orderId);
    await loadOrders();
  } catch (error) {
    orderMessage().textContent = error.message;
  }
}

async function issueInvoice(orderId) {
  try {
    await client.request('POST', '/api/v1/owner/orders/' + orderId + '/invoice', {});
    orderMessage().textContent = `Invoice issued for order ${orderId}.`;
    await openOrder(orderId);
  } catch (error) {
    orderMessage().textContent = error.message;
  }
}

async function loadInvoiceLink(orderId) {
  const link = document.getElementById('order-invoice-link');
  if (!link) return;
  link.textContent = 'Loading invoice…';
  try {
    const inv = await client.request('GET', '/api/v1/owner/orders/' + orderId + '/invoice');
    link.textContent = `Invoice #${inv.number}`;
    link.onclick = (e) => {
      e.preventDefault();
      window.open('about:blank', '_blank');
      // Phase 3F exposes invoice data via API only; the printable view is a
      // follow-up. For now show the JSON in a new window.
      const w = window.open('about:blank', '_blank');
      if (w) {
        w.document.write('<title>Invoice ' + inv.number + '</title>');
        w.document.write('<pre>' + JSON.stringify(inv, null, 2) + '</pre>');
      }
    };
  } catch (error) {
    link.textContent = 'No invoice yet';
  }
}

// ---- Phase 3G: reports + notifications ----

function reportsMessage() { return document.getElementById('reports-message'); }
function reportsStatus() { return document.getElementById('reports-status'); }
function reportsDaily() { return document.getElementById('reports-daily'); }
function reportsCombinations() { return document.getElementById('reports-combinations'); }

async function loadReports() {
  reportsMessage().textContent = '';
  const fromInput = document.getElementById('reports-from');
  const toInput = document.getElementById('reports-to');
  const limitInput = document.getElementById('reports-limit');
  const from = fromInput.value || Math.floor(Date.now() / 1000) - 30 * 86400;
  const to = toInput.value || Math.floor(Date.now() / 1000);
  const limit = parseInt(limitInput.value || '10', 10);
  try {
    const [summary, combos] = await Promise.all([
      client.request('GET', `/api/v1/owner/reports/summary?from=${from}&to=${to}`),
      client.request('GET', `/api/v1/owner/reports/top-combinations?from=${from}&to=${to}&limit=${limit}`),
    ]);
    renderReportStatus(summary);
    renderReportDaily(summary);
    renderReportCombinations(combos);
    reportsMessage().textContent = 'Report ready.';
  } catch (error) {
    reportsMessage().textContent = error.message;
  }
}

function renderReportStatus(summary) {
  const target = reportsStatus();
  target.innerHTML = '';
  if (!summary.byStatus || summary.byStatus.length === 0) {
    target.textContent = 'No orders in range.';
    return;
  }
  const table = document.createElement('table');
  table.className = 'reports-table';
  const thead = document.createElement('thead');
  thead.innerHTML = '<tr><th>Status</th><th>Count</th><th>Total (minor units)</th></tr>';
  table.append(thead);
  const tbody = document.createElement('tbody');
  for (const row of summary.byStatus) {
    const tr = document.createElement('tr');
    tr.innerHTML = `<td>${row.status}</td><td>${row.count}</td><td>${row.totalMinor}</td>`;
    tbody.append(tr);
  }
  table.append(tbody);
  target.append(table);
}

function renderReportDaily(summary) {
  const target = reportsDaily();
  target.innerHTML = '';
  if (!summary.daily || summary.daily.length === 0) {
    target.textContent = 'No settled revenue in range.';
    return;
  }
  const heading = document.createElement('h3');
  heading.textContent = 'Daily settled revenue';
  target.append(heading);
  const table = document.createElement('table');
  table.className = 'reports-table';
  const thead = document.createElement('thead');
  thead.innerHTML = '<tr><th>Date</th><th>Orders</th><th>Revenue (minor units)</th></tr>';
  table.append(thead);
  const tbody = document.createElement('tbody');
  for (const row of summary.daily) {
    const tr = document.createElement('tr');
    tr.innerHTML = `<td>${row.date}</td><td>${row.orderCount}</td><td>${row.revenueMinor}</td>`;
    tbody.append(tr);
  }
  table.append(tbody);
  target.append(table);
}

function renderReportCombinations(combos) {
  const target = reportsCombinations();
  target.innerHTML = '';
  if (!combos.combinations || combos.combinations.length === 0) {
    target.textContent = 'No settled combinations in range.';
    return;
  }
  const heading = document.createElement('h3');
  heading.textContent = 'Top combinations';
  target.append(heading);
  const table = document.createElement('table');
  table.className = 'reports-table';
  const thead = document.createElement('thead');
  thead.innerHTML = '<tr><th>Paper</th><th>Colour</th><th>Sides</th><th>Lines</th><th>Revenue (minor units)</th></tr>';
  table.append(thead);
  const tbody = document.createElement('tbody');
  for (const row of combos.combinations) {
    const tr = document.createElement('tr');
    tr.innerHTML = `<td>${row.paperSize}</td><td>${row.colourMode}</td><td>${row.sides}</td><td>${row.lineCount}</td><td>${row.revenueMinor}</td>`;
    tbody.append(tr);
  }
  table.append(tbody);
  target.append(table);
}

document.getElementById('reports-run').addEventListener('click', () => { void loadReports(); });
document.getElementById('reports-refresh').addEventListener('click', () => { void loadReports(); });

function notificationsMessage() { return document.getElementById('notifications-message'); }
function notificationsStreamStatus() { return document.getElementById('notifications-stream-status'); }

async function loadNotificationSettings() {
  try {
    const s = await client.request('GET', '/api/v1/owner/notifications/settings');
    const form = document.getElementById('notifications-form');
    form.elements['desktopAlertsEnabled'].checked = !!s.desktopAlertsEnabled;
    form.elements['audioAlertsEnabled'].checked = !!s.audioAlertsEnabled;
    form.elements['emailEnabled'].checked = !!s.emailEnabled;
    form.elements['emailHost'].value = s.emailHost || '';
    form.elements['emailPort'].value = s.emailPort || 587;
    form.elements['emailUsername'].value = s.emailUsername || '';
    form.elements['emailFrom'].value = s.emailFrom || '';
    form.elements['emailToken'].value = '';
    form.elements['whatsappEnabled'].checked = !!s.whatsappEnabled;
    form.elements['whatsappApiUrl'].value = s.whatsappApiUrl || '';
    form.elements['whatsappTo'].value = s.whatsappTo || '';
    form.elements['whatsappToken'].value = '';
    notificationsMessage().textContent = 'Settings loaded.';
  } catch (error) {
    notificationsMessage().textContent = error.message;
  }
  try {
    const deliveries = await client.request('GET', '/api/v1/owner/notifications/deliveries?limit=50');
    renderNotificationDeliveries(deliveries.deliveries || []);
  } catch (error) {
    notificationsDeliveriesMessage().textContent = error.message;
  }
}

function notificationsDeliveriesMessage() { return document.getElementById('notifications-deliveries-message'); }
function notificationsDeliveriesList() { return document.getElementById('notifications-deliveries-list'); }

function renderNotificationDeliveries(deliveries) {
  const list = notificationsDeliveriesList();
  list.replaceChildren();
  if (deliveries.length === 0) {
    notificationsDeliveriesMessage().textContent = 'No transport deliveries recorded yet.';
    return;
  }
  notificationsDeliveriesMessage().textContent = '';
  for (const delivery of deliveries) {
    const row = document.createElement('div');
    row.className = 'notifications-delivery-row';
    const time = document.createElement('span');
    time.className = 'notifications-delivery-time';
    time.textContent = new Date(delivery.occurredAt).toLocaleString();
    const transport = document.createElement('span');
    transport.className = 'notifications-delivery-transport';
    transport.textContent = delivery.transport;
    transport.dataset.transport = delivery.transport;
    const status = document.createElement('span');
    status.className = 'notifications-delivery-status';
    status.dataset.status = delivery.status;
    status.textContent = delivery.status;
    const recipient = document.createElement('span');
    recipient.className = 'notifications-delivery-recipient';
    recipient.textContent = delivery.recipient || '';
    const detail = document.createElement('span');
    detail.className = 'notifications-delivery-detail';
    detail.textContent = delivery.failureDetail || delivery.subject || delivery.body || '';
    row.append(time, transport, status, recipient, detail);
    list.append(row);
  }
}

document.getElementById('notifications-form').addEventListener('submit', async event => {
  event.preventDefault();
  const form = event.currentTarget;
  const button = form.querySelector('button[type="submit"]');
  button.disabled = true;
  notificationsMessage().textContent = '';
  try {
    const payload = {
      desktopAlertsEnabled: form.elements['desktopAlertsEnabled'].checked,
      audioAlertsEnabled: form.elements['audioAlertsEnabled'].checked,
      emailEnabled: form.elements['emailEnabled'].checked,
      emailHost: form.elements['emailHost'].value.trim(),
      emailPort: parseInt(form.elements['emailPort'].value || '587', 10),
      emailUsername: form.elements['emailUsername'].value.trim(),
      emailFrom: form.elements['emailFrom'].value.trim(),
      emailToken: form.elements['emailToken'].value,
      whatsappEnabled: form.elements['whatsappEnabled'].checked,
      whatsappApiUrl: form.elements['whatsappApiUrl'].value.trim(),
      whatsappTo: form.elements['whatsappTo'].value.trim(),
      whatsappToken: form.elements['whatsappToken'].value,
    };
    await client.request('PUT', '/api/v1/owner/notifications/settings', payload);
    notificationsMessage().textContent = 'Notification settings saved.';
    form.elements['emailToken'].value = '';
    form.elements['whatsappToken'].value = '';
  } catch (error) {
    notificationsMessage().textContent = error.message;
  } finally {
    button.disabled = false;
  }
});

// ---- SSE: new-order event stream ----

let sseSource = null;

function playNewOrderSound() {
  try {
    const ctx = new (window.AudioContext || window.webkitAudioContext)();
    const osc = ctx.createOscillator();
    const gain = ctx.createGain();
    osc.connect(gain);
    gain.connect(ctx.destination);
    osc.type = 'sine';
    osc.frequency.value = 880;
    gain.gain.value = 0.1;
    osc.start();
    setTimeout(() => { osc.stop(); ctx.close(); }, 200);
  } catch (_) {
    // WebAudio unavailable; silently skip.
  }
}

function showDesktopNotification(event) {
  if (typeof Notification === 'undefined') return;
  if (Notification.permission === 'default') {
    Notification.requestPermission().then(permission => {
      if (permission === 'granted') {
        try {
          new Notification('New print order', {
            body: `${event.customerName} placed an order for ${event.totalMinor} minor units`,
          });
        } catch (_) { /* sandbox or permission denied */ }
      }
    });
  } else if (Notification.permission === 'granted') {
    try {
      new Notification('New print order', {
        body: `${event.customerName} placed an order for ${event.totalMinor} minor units`,
      });
    } catch (_) { /* sandbox or permission denied */ }
  }
}

function connectNotificationStream() {
  if (sseSource) sseSource.close();
  notificationsStreamStatus().textContent = 'Connecting…';
  sseSource = new EventSource('/api/v1/owner/notifications/stream', { withCredentials: true });
  sseSource.addEventListener('connected', () => {
    notificationsStreamStatus().textContent = 'Live';
  });
  sseSource.addEventListener('new-order', evt => {
    try {
      const event = JSON.parse(evt.data);
      showDesktopNotification(event);
      playNewOrderSound();
      // Refresh orders and reports when a new order arrives.
      void loadOrders();
      void loadReports();
    } catch (_) { /* ignore malformed payload */ }
  });
  sseSource.onerror = () => {
    notificationsStreamStatus().textContent = 'Disconnected';
  };
}

void start();

// ---- Phase 4: printers ----

function printersMessage() { return document.getElementById('printers-message'); }
function printersList() { return document.getElementById('printers-list'); }

async function loadPrinters() {
 if (!printerSetupController) printerSetupController = createPrinterSetup(client, () => role);
 await printerSetupController.refresh();
}

document.getElementById('printers-enroll-form').addEventListener('submit', async event => {
  event.preventDefault();
  const form = event.currentTarget;
  const button = form.querySelector('button[type="submit"]');
  button.disabled = true;
  printersMessage().textContent = '';
  try {
    const payload = {
      backend: form.elements['backend'].value,
      queueName: form.elements['queueName'].value.trim(),
      uri: form.elements['uri'].value.trim(),
      displayName: form.elements['displayName'].value.trim(),
      location: form.elements['location'].value.trim(),
      // Manual enrollment without an actual IPP probe falls back to a fixture
      // attribute bag so the dashboard can show the printer immediately.
      // A future slice will probe the URI on the merchant's behalf.
      attributes: {
        'media-supported': ['A4', 'Letter'],
        'media-source-supported': ['auto'],
        'print-color-mode-supported': ['monochrome'],
        'sides-supported': ['one-sided'],
      },
    };
    await client.request('POST', '/api/v1/owner/printers', payload);
    printersMessage().textContent = 'Printer registered.';
    form.reset();
    await loadPrinters();
  } catch (error) {
    printersMessage().textContent = error.message;
  } finally {
    button.disabled = false;
  }
});

// ---- Phase 5: ID Card Studio ----

function idcardsMessage() { return document.getElementById('idcards-message'); }
function idcardsList() { return document.getElementById('idcards-list'); }
function idcardsDetail() { return document.getElementById('idcards-detail'); }
function idcardsDetailBody() { return document.getElementById('idcards-detail-body'); }
function idcardsCalibrationsList() { return document.getElementById('idcards-calibrations-list'); }
function idcardsCalibrationsMessage() { return document.getElementById('idcards-calibrations-message'); }

// idCardFrontDocs / idCardBackDocs caches the available document list so the
// session form can populate its select without re-querying every time.
let idCardFrontDocs = [];
let idCardBackDocs = [];

async function loadIDCards() {
  idcardsMessage().textContent = '';
  await Promise.all([loadIDCardSessions(), loadIDCardCalibrations(), loadIDCardDocuments()]);
  populateIDCardDocumentSelects();
}

async function loadIDCardSessions() {
  try {
    const { sessions } = await client.request('GET', '/api/v1/owner/id-cards/sessions');
    renderIDCardSessions(sessions || []);
  } catch (error) {
    idcardsMessage().textContent = error.message;
  }
}

function renderIDCardSessions(sessions) {
  const list = idcardsList();
  list.innerHTML = '';
  if (sessions.length === 0) {
    list.textContent = 'No ID card sessions yet. Use "New session" to start one.';
    return;
  }
  for (const s of sessions) list.append(renderIDCardRow(s));
}

function renderIDCardRow(s) {
  const node = document.getElementById('idcard-row').content.firstElementChild.cloneNode(true);
  const meta = node.querySelector('.idcard-meta');
  const title = node.querySelector('.idcard-title');
  title.textContent = `Session ${s.id.slice(0, 8)}`;
  const status = node.querySelector('.idcard-status');
  status.textContent = s.status || '';
  const layout = node.querySelector('.idcard-layout');
  layout.textContent = `${s.card} on ${s.sheet} · ${s.layoutKind}${s.actualSize ? ' · actual size' : ''}`;
  meta.append(title, status, layout);
  node.querySelector('.idcard-select').addEventListener('click', () => openIDCardSession(s.id));
  node.querySelector('.idcard-compose').addEventListener('click', async () => {
    try {
      const updated = await client.request('POST', `/api/v1/owner/id-cards/sessions/${s.id}/compose`, { side: 'front' });
      idcardsMessage().textContent = `Rendered front (${updated.outputs.length} output${updated.outputs.length === 1 ? '' : 's'}).`;
      await loadIDCardSessions();
    } catch (error) {
      idcardsMessage().textContent = error.message;
    }
  });
  const deleteBtn = node.querySelector('.idcard-delete');
  if (role !== 'owner') deleteBtn.disabled = true;
  deleteBtn.addEventListener('click', async () => {
    if (role !== 'owner') return;
    if (!confirm('Remove this session? The customer documents are retained.')) return;
    try {
      await client.request('DELETE', `/api/v1/owner/id-cards/sessions/${s.id}`);
      await loadIDCardSessions();
    } catch (error) {
      idcardsMessage().textContent = error.message;
    }
  });
  return node;
}

async function openIDCardSession(id) {
  try {
    const session = await client.request('GET', `/api/v1/owner/id-cards/sessions/${id}`);
    renderIDCardSessionDetail(session);
    idcardsDetail().hidden = false;
    idcardsDetail().open = true;
  } catch (error) {
    idcardsMessage().textContent = error.message;
  }
}

function renderIDCardSessionDetail(s) {
  const body = idcardsDetailBody();
  body.innerHTML = '';
  const card = document.createElement('div');
  card.className = 'order-detail-card';
  const head = document.createElement('strong');
  head.textContent = `Session ${s.id.slice(0, 8)} — ${s.status}`;
  card.append(head);
  const summary = document.createElement('p');
  summary.className = 'help';
  summary.textContent = `${s.card} on ${s.sheet}, layout ${s.layoutKind}, flip ${s.flipEdge}, dpi ${s.dpi}, actualSize=${s.actualSize}, calibration=${s.calibrationId || '—'}.`;
  card.append(summary);
  if (s.error) {
    const err = document.createElement('p');
    err.className = 'help warning';
    err.textContent = `Detection error: ${s.error}`;
    card.append(err);
  }
  if (s.frontCorners) card.append(renderIDCardCornersTable('Front', s.frontCorners));
  if (s.backCorners) card.append(renderIDCardCornersTable('Back', s.backCorners));
  for (const o of s.outputs || []) {
    const link = document.createElement('a');
    link.href = `/api/v1/owner/id-cards/sessions/${s.id}/outputs/${o.side}/image`;
    link.target = '_blank';
    link.rel = 'noopener';
    link.textContent = `Open rendered ${o.side} sheet (PNG)`;
    card.append(link);
  }
  if (role === 'owner') card.append(renderIDCardManualCornersForm(s));
  body.append(card);
}

function renderIDCardCornersTable(label, c) {
  const wrap = document.createElement('div');
  wrap.className = 'help';
  wrap.style.marginTop = '8px';
  const heading = document.createElement('strong');
  heading.textContent = `${label} corners (confidence ${Number(c.confidence).toFixed(2)}${c.manual ? ', manual' : ', auto'})`;
  wrap.append(heading);
  const table = document.createElement('table');
  table.className = 'reports-table';
  table.innerHTML = '<thead><tr><th>Corner</th><th>X (px)</th><th>Y (px)</th></tr></thead>';
  const tbody = document.createElement('tbody');
  for (const [name, pt] of [['TL', c.tl], ['TR', c.tr], ['BR', c.br], ['BL', c.bl]]) {
    const tr = document.createElement('tr');
    tr.innerHTML = `<td>${name}</td><td>${Number(pt.x).toFixed(1)}</td><td>${Number(pt.y).toFixed(1)}</td>`;
    tbody.append(tr);
  }
  table.append(tbody);
  wrap.append(table);
  return wrap;
}

function renderIDCardManualCornersForm(s) {
  const form = document.createElement('form');
  form.className = 'help';
  form.style.marginTop = '8px';
  form.innerHTML = `
    <strong>Override corners</strong>
    <div class="pair">
      <label>Side<select name="side">
        <option value="front">Front</option>
        <option value="back">Back</option>
      </select></label>
    </div>
    <div class="pair">
      <label>TL<input name="tlx" type="number" step="0.1" required></label>
      <label>TR<input name="trx" type="number" step="0.1" required></label>
    </div>
    <div class="pair">
      <label>BR<input name="brx" type="number" step="0.1" required></label>
      <label>BL<input name="blx" type="number" step="0.1" required></label>
    </div>
    <div class="pair">
      <label>TLY<input name="tly" type="number" step="0.1" required></label>
      <label>TRY<input name="try_" type="number" step="0.1" required></label>
    </div>
    <div class="pair">
      <label>BRY<input name="bry" type="number" step="0.1" required></label>
      <label>BLY<input name="bly" type="number" step="0.1" required></label>
    </div>
    <button type="submit">Save manual corners</button>
  `;
  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    const els = form.elements;
    const payload = {
      side: els['side'].value,
      corners: {
        tl: { x: Number(els['tlx'].value), y: Number(els['tly'].value) },
        tr: { x: Number(els['trx'].value), y: Number(els['try_'].value) },
        br: { x: Number(els['brx'].value), y: Number(els['bry'].value) },
        bl: { x: Number(els['blx'].value), y: Number(els['bly'].value) },
        confidence: 1.0,
        manual: true,
      },
    };
    try {
      const updated = await client.request('PUT', `/api/v1/owner/id-cards/sessions/${s.id}/corners`, payload);
      idcardsMessage().textContent = `Corners saved; status=${updated.status}.`;
      renderIDCardSessionDetail(updated);
    } catch (error) {
      idcardsMessage().textContent = error.message;
    }
  });
  return form;
}

async function loadIDCardDocuments() {
  try {
    const { documents } = await client.request('GET', '/api/v1/owner/id-cards/documents');
    idCardFrontDocs = documents || [];
    idCardBackDocs = documents || [];
  } catch (error) {
    idcardsMessage().textContent = error.message;
  }
}

function populateIDCardDocumentSelects() {
  const front = document.querySelector('#idcards-new-form [name="frontDocId"]');
  const back = document.querySelector('#idcards-new-form [name="backDocId"]');
  if (!front || !back) return;
  const opts = ['<option value="">— select a document —</option>']
    .concat(idCardFrontDocs.map(d => `<option value="${d.id}">${escapeHTML(d.filename || d.id)} (${d.mimeType || 'image'})</option>`))
    .join('');
  front.innerHTML = opts;
  back.innerHTML = ['<option value="">— none —</option>']
    .concat(idCardBackDocs.map(d => `<option value="${d.id}">${escapeHTML(d.filename || d.id)} (${d.mimeType || 'image'})</option>`))
    .join('');
}

function escapeHTML(value) {
  return String(value).replace(/[&<>"']/g, ch => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]));
}

async function loadIDCardCalibrations() {
  try {
    const { calibrations } = await client.request('GET', '/api/v1/owner/id-cards/calibrations');
    renderIDCardCalibrations(calibrations || []);
  } catch (error) {
    idcardsCalibrationsMessage().textContent = error.message;
  }
}

function renderIDCardCalibrations(rows) {
  const list = idcardsCalibrationsList();
  list.innerHTML = '';
  if (rows.length === 0) {
    list.textContent = 'No calibrations saved yet.';
    return;
  }
  for (const c of rows) {
    const item = document.createElement('div');
    item.className = 'printer-card';
    const head = document.createElement('div');
    head.className = 'printer-head';
    const title = document.createElement('strong');
    title.textContent = c.name;
    head.append(title);
    const sub = document.createElement('span');
    sub.className = 'help';
    sub.textContent = `${c.sheet} · ${c.flipEdge} · dx=${Number(c.dxMm).toFixed(2)} mm · dy=${Number(c.dyMm).toFixed(2)} mm`;
    head.append(sub);
    item.append(head);
    if (c.notes) {
      const notes = document.createElement('p');
      notes.className = 'help';
      notes.textContent = c.notes;
      item.append(notes);
    }
    if (role === 'owner') {
      const actions = document.createElement('div');
      actions.className = 'printer-actions';
      const del = document.createElement('button');
      del.type = 'button';
      del.className = 'secondary';
      del.textContent = 'Delete';
      del.addEventListener('click', async () => {
        try {
          await client.request('DELETE', `/api/v1/owner/id-cards/calibrations/${c.id}`);
          await loadIDCardCalibrations();
        } catch (error) {
          idcardsCalibrationsMessage().textContent = error.message;
        }
      });
      actions.append(del);
      item.append(actions);
    }
    list.append(item);
  }
}

document.getElementById('idcards-refresh').addEventListener('click', () => { void loadIDCards(); });

document.getElementById('idcards-new-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  if (role !== 'owner') {
    idcardsMessage().textContent = 'Only the owner can create ID card sessions.';
    return;
  }
  const form = event.currentTarget;
  const button = form.querySelector('button[type="submit"]');
  button.disabled = true;
  idcardsMessage().textContent = '';
  try {
    const frontDocId = form.elements['frontDocId'].value;
    if (!frontDocId) throw new Error('Front document is required.');
    const cardW = Number(form.elements['cardWidthMm'].value || 0);
    const cardH = Number(form.elements['cardHeightMm'].value || 0);
    const payload = {
      frontDocId,
      backDocId: form.elements['backDocId'].value || undefined,
      sheet: form.elements['sheet'].value,
      card: form.elements['card'].value,
      cardWidthMm: cardW || undefined,
      cardHeightMm: cardH || undefined,
      layoutKind: form.elements['layoutKind'].value,
      flipEdge: form.elements['flipEdge'].value,
      actualSize: form.elements['actualSize'].checked,
      dpi: Number(form.elements['dpi'].value || 300),
    };
    const created = await client.request('POST', '/api/v1/owner/id-cards/sessions', payload);
    idcardsMessage().textContent = `Session ${created.id.slice(0, 8)} created (${created.status}).`;
    form.reset();
    await loadIDCardSessions();
    renderIDCardSessionDetail(created);
    idcardsDetail().hidden = false;
    idcardsDetail().open = true;
  } catch (error) {
    idcardsMessage().textContent = error.message;
  } finally {
    button.disabled = false;
  }
});

document.getElementById('idcards-calibration-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  if (role !== 'owner') {
    idcardsCalibrationsMessage().textContent = 'Only the owner can save calibrations.';
    return;
  }
  const form = event.currentTarget;
  const button = form.querySelector('button[type="submit"]');
  button.disabled = true;
  idcardsCalibrationsMessage().textContent = '';
  try {
    const payload = {
      name: form.elements['name'].value.trim(),
      sheet: form.elements['sheet'].value,
      flipEdge: form.elements['flipEdge'].value,
      dxMm: Number(form.elements['dxMm'].value || 0),
      dyMm: Number(form.elements['dyMm'].value || 0),
      notes: form.elements['notes'].value.trim(),
    };
    if (!payload.name) throw new Error('Name is required.');
    await client.request('POST', '/api/v1/owner/id-cards/calibrations', payload);
    idcardsCalibrationsMessage().textContent = 'Calibration saved.';
    form.reset();
    await loadIDCardCalibrations();
  } catch (error) {
    idcardsCalibrationsMessage().textContent = error.message;
  } finally {
    button.disabled = false;
  }
});

// ---- Phase 6: Passport Photo Studio ----

function passportsMessage() { return document.getElementById('passports-message'); }
function passportsList() { return document.getElementById('passports-list'); }
function passportsDetail() { return document.getElementById('passports-detail'); }
function passportsDetailBody() { return document.getElementById('passports-detail-body'); }

// passportDocs caches the document list so the session form can populate its
// <select> without re-querying every time.
let passportDocs = [];

async function loadPassports() {
  passportsMessage().textContent = '';
  await Promise.all([loadPassportSessions(), loadPassportDocuments()]);
  populatePassportDocumentSelects();
}

async function loadPassportSessions() {
  try {
    const { sessions } = await client.request('GET', '/api/v1/owner/passports/sessions');
    renderPassportSessions(sessions || []);
  } catch (error) {
    passportsMessage().textContent = error.message;
  }
}

async function loadPassportDocuments() {
  try {
    const { documents } = await client.request('GET', '/api/v1/owner/passports/documents');
    passportDocs = documents || [];
  } catch (error) {
    passportsMessage().textContent = error.message;
  }
}

function populatePassportDocumentSelects() {
  const sel = document.querySelector('#passports-new-form [name="documentId"]');
  if (!sel) return;
  const opts = ['<option value="">— select a customer photo —</option>']
    .concat(passportDocs.map(d => `<option value="${d.id}">${escapeHTML(d.filename || d.id)} (${d.mimeType || 'image'})</option>`))
    .join('');
  sel.innerHTML = opts;
}

function renderPassportSessions(sessions) {
  const list = passportsList();
  list.innerHTML = '';
  if (sessions.length === 0) {
    list.textContent = 'No passport sessions yet. Use "New session" to start one.';
    return;
  }
  for (const s of sessions) list.append(renderPassportRow(s));
}

function renderPassportRow(s) {
  const node = document.getElementById('passport-row').content.firstElementChild.cloneNode(true);
  const meta = node.querySelector('.passport-meta');
  const title = node.querySelector('.passport-title');
  title.textContent = `Session ${(s.id || '').slice(0, 8)}`;
  const status = node.querySelector('.passport-status');
  status.textContent = s.status || '';
  const layout = node.querySelector('.passport-layout');
  layout.textContent = `${s.presetDisplayName || s.preset || ''} on ${(s.outputs && s.outputs[0]) ? `${s.outputs[0].sheetWidthMm}×${s.outputs[0].sheetHeightMm} mm` : 'A4'}`;
  meta.append(title, status, layout);
  node.querySelector('.passport-select').addEventListener('click', () => openPassportSession(s.id));
  node.querySelector('.passport-compose').addEventListener('click', async () => {
    try {
      const updated = await client.request('POST', `/api/v1/owner/passports/sessions/${s.id}/compose`, {});
      passportsMessage().textContent = `Rendered (${updated.outputs.length} output${updated.outputs.length === 1 ? '' : 's'}).`;
      await loadPassportSessions();
    } catch (error) {
      passportsMessage().textContent = error.message;
    }
  });
  const deleteBtn = node.querySelector('.passport-delete');
  if (role !== 'owner') deleteBtn.disabled = true;
  deleteBtn.addEventListener('click', async () => {
    if (role !== 'owner') return;
    if (!confirm('Remove this session? The customer documents are retained.')) return;
    try {
      await client.request('DELETE', `/api/v1/owner/passports/sessions/${s.id}`);
      await loadPassportSessions();
    } catch (error) {
      passportsMessage().textContent = error.message;
    }
  });
  return node;
}

async function openPassportSession(id) {
  try {
    const session = await client.request('GET', `/api/v1/owner/passports/sessions/${id}`);
    renderPassportSessionDetail(session);
    passportsDetail().hidden = false;
    passportsDetail().open = true;
  } catch (error) {
    passportsMessage().textContent = error.message;
  }
}

function renderPassportSessionDetail(s) {
  const body = passportsDetailBody();
  body.innerHTML = '';
  const card = document.createElement('div');
  card.className = 'order-detail-card';
  const head = document.createElement('strong');
  head.textContent = `Session ${(s.id || '').slice(0, 8)} — ${s.status}`;
  card.append(head);
  const summary = document.createElement('p');
  summary.className = 'help';
  const bgLabel = (s.background === 'white') ? 'replace white' : (s.background === 'light_blue') ? 'replace light blue' : 'keep original';
  summary.textContent = `${s.presetDisplayName || s.preset} (${s.widthMm}×${s.heightMm} mm), background=${bgLabel}, head=${s.headHeightMm} mm, eye line=${s.eyeLineFromBottomMm} mm from bottom.`;
  card.append(summary);
  if (s.complianceNote) {
    const note = document.createElement('p');
    note.className = 'help';
    note.textContent = `Compliance note: ${s.complianceNote}`;
    card.append(note);
  }
  if (s.error) {
    const err = document.createElement('p');
    err.className = 'help warning';
    err.textContent = `Detection error: ${s.error}`;
    card.append(err);
  }
  if (s.faceRegion) card.append(renderPassportFaceRegionTable(s.faceRegion));
  for (const o of s.outputs || []) {
    const link = document.createElement('a');
    link.href = `/api/v1/owner/passports/sessions/${s.id}/outputs/${o.id}/image`;
    link.target = '_blank';
    link.rel = 'noopener';
    link.textContent = `Open rendered sheet (PNG, ${o.pixelWidth}×${o.pixelHeight}, ${o.photoCount} photos)`;
    card.append(link);
    const meta = document.createElement('p');
    meta.className = 'help';
    meta.textContent = `SHA-256 ${o.sha256.slice(0, 16)}…`;
    card.append(meta);
  }
  if (role === 'owner') card.append(renderPassportManualFaceRegionForm(s));
  body.append(card);
}

function renderPassportFaceRegionTable(r) {
  const wrap = document.createElement('div');
  wrap.className = 'help';
  wrap.style.marginTop = '8px';
  const heading = document.createElement('strong');
  heading.textContent = `Auto-detected face region (confidence ${Number(r.confidence).toFixed(2)}${r.manual ? ', manual' : ', needs operator confirmation'})`;
  wrap.append(heading);
  const table = document.createElement('table');
  table.className = 'reports-table';
  table.innerHTML = '<thead><tr><th>Field</th><th>Value (px)</th></tr></thead>';
  const tbody = document.createElement('tbody');
  for (const [name, v] of [['X', r.x], ['Y', r.y], ['Width', r.width], ['Height', r.height]]) {
    const tr = document.createElement('tr');
    tr.innerHTML = `<td>${name}</td><td>${Number(v).toFixed(0)}</td>`;
    tbody.append(tr);
  }
  table.append(tbody);
  wrap.append(table);
  return wrap;
}

function renderPassportManualFaceRegionForm(s) {
  const form = document.createElement('form');
  form.className = 'help';
  form.style.marginTop = '8px';
  const r = s.faceRegion || { x: 0, y: 0, width: 0, height: 0 };
  form.innerHTML = `
    <strong>Override face region</strong>
    <p class="help">Adjust the bounding box (source-image pixels) and Save to mark the region as manually confirmed.</p>
    <div class="pair">
      <label>X<input name="x" type="number" min="0" step="1" required value="${Number(r.x) || 0}"></label>
      <label>Y<input name="y" type="number" min="0" step="1" required value="${Number(r.y) || 0}"></label>
    </div>
    <div class="pair">
      <label>Width<input name="width" type="number" min="1" step="1" required value="${Number(r.width) || 0}"></label>
      <label>Height<input name="height" type="number" min="1" step="1" required value="${Number(r.height) || 0}"></label>
    </div>
    <button type="submit">Save manual region</button>
  `;
  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    const els = form.elements;
    const payload = {
      x: Number(els['x'].value),
      y: Number(els['y'].value),
      width: Number(els['width'].value),
      height: Number(els['height'].value),
      confidence: 1.0,
      manual: true,
    };
    try {
      const updated = await client.request('PUT', `/api/v1/owner/passports/sessions/${s.id}/face-region`, payload);
      passportsMessage().textContent = `Face region saved; status=${updated.status}.`;
      renderPassportSessionDetail(updated);
    } catch (error) {
      passportsMessage().textContent = error.message;
    }
  });
  return form;
}

document.getElementById('passports-refresh').addEventListener('click', () => { void loadPassports(); });

document.getElementById('passports-new-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  if (role !== 'owner') {
    passportsMessage().textContent = 'Only the owner can create passport sessions.';
    return;
  }
  const form = event.currentTarget;
  const button = form.querySelector('button[type="submit"]');
  button.disabled = true;
  passportsMessage().textContent = '';
  try {
    const documentId = form.elements['documentId'].value;
    if (!documentId) throw new Error('Source document is required.');
    const preset = form.elements['preset'].value;
    const payload = {
      documentId,
      preset,
      background: form.elements['background'].value,
      sheet: form.elements['sheet'].value,
      photosPerSheet: Number(form.elements['photosPerSheet'].value || 0),
      dpi: Number(form.elements['dpi'].value || 300),
      complianceNote: form.elements['complianceNote'].value.trim(),
    };
    if (preset === 'custom') {
      payload.widthMm = Number(form.elements['widthMm'].value || 0);
      payload.heightMm = Number(form.elements['heightMm'].value || 0);
      if (!(payload.widthMm > 0) || !(payload.heightMm > 0)) {
        throw new Error('Custom preset needs width_mm and height_mm greater than 0.');
      }
    }
    const created = await client.request('POST', '/api/v1/owner/passports/sessions', payload);
    passportsMessage().textContent = `Session ${created.id.slice(0, 8)} created (${created.status}).`;
    form.reset();
    await loadPassportSessions();
    renderPassportSessionDetail(created);
    passportsDetail().hidden = false;
    passportsDetail().open = true;
  } catch (error) {
    passportsMessage().textContent = error.message;
  } finally {
    button.disabled = false;
  }
});
// ---- Tunnel, custom domain and branded QR ----

const tunnelMessage = () => document.getElementById('tunnel-message');
const tunnelStatusValue = () => document.getElementById('tunnel-status-value');
const tunnelPublicOrigin = () => document.getElementById('tunnel-public-origin');
const tunnelProvider = () => document.getElementById('tunnel-provider');
const tunnelToken = () => document.getElementById('tunnel-token');
const tunnelLastProbe = () => document.getElementById('tunnel-last-probe');
const tunnelQrPreview = () => document.getElementById('tunnel-qr-preview');
const tunnelQrImage = () => document.getElementById('tunnel-qr-image');
const tunnelDownloadQr = () => document.getElementById('tunnel-download-qr');
const tunnelEventsList = () => document.getElementById('tunnel-events-list');
const tunnelEventsMessage = () => document.getElementById('tunnel-events-message');

function tunnelFmtTime(ms) {
  if (!ms) return 'never';
  try { return new Date(ms).toLocaleString(); } catch { return String(ms); }
}

function describeTunnelStatus(snapshot) {
  switch (snapshot.status) {
    case 'online':
      return 'online — public origin responded successfully';
    case 'starting':
      return 'starting — waiting for the external connector';
    case 'verifying':
      return 'verifying — probing the public origin';
    case 'degraded':
      return 'degraded — public origin returned a 5xx';
    case 'offline':
      return 'offline — connection is not verified';
    case 'error':
      return `error — ${snapshot.lastError || 'unknown failure'}`;
    case 'unconfigured':
      return 'unconfigured — enter the shop PC address';
    default:
      return snapshot.status;
  }
}

function renderTunnelSnapshot(snapshot) {
  tunnelStatusValue().textContent = describeTunnelStatus(snapshot);
  tunnelStatusValue().dataset.state = snapshot.status;
  tunnelPublicOrigin().textContent = snapshot.publicOrigin || '(not set)';
  tunnelProvider().textContent = snapshot.provider || 'direct';
  if (tunnelToken()) tunnelToken().textContent = snapshot.hasToken ? `saved · ${snapshot.tunnelTokenFingerprint}` : 'not configured';
  tunnelLastProbe().textContent = `${tunnelFmtTime(snapshot.lastVerifiedAt)} (HTTP ${snapshot.lastVerifiedStatus || '—'})`;
  const form = document.getElementById('tunnel-form');
  if (form && !form.dataset.dirty) {
    form.elements.namedItem('provider').value = snapshot.provider || 'direct';
    form.elements.namedItem('publicOrigin').value = snapshot.publicOrigin || '';
    form.elements.namedItem('qrTargetPath').value = snapshot.qrTargetPath || '/portal/';
    form.elements.namedItem('shopRoute').value = snapshot.shopRoute || '';
  }
  const isOnline = snapshot.status === 'online' || (snapshot.provider === 'direct' && !!snapshot.publicOrigin);
  tunnelQrPreview().hidden = !isOnline;
  tunnelDownloadQr().hidden = !isOnline;
  if (isOnline) {
    tunnelQrImage().src = `/api/v1/owner/tunnel/qr.svg?ts=${Date.now()}`;
    tunnelDownloadQr().onclick = () => {
      const link = document.createElement('a');
      link.href = tunnelQrImage().src;
      link.download = 'print-catalyst-qr.svg';
      link.click();
    };
  }
  if (snapshot.lastVerifiedError && snapshot.status !== 'online') {
    tunnelMessage().textContent = `Last probe: ${snapshot.lastVerifiedError}`;
  } else {
    tunnelMessage().textContent = '';
  }
}

async function loadTunnel() {
  try {
    const snapshot = await client.request('GET', '/api/v1/owner/tunnel');
    renderTunnelSnapshot(snapshot);
  } catch (error) {
    tunnelMessage().textContent = error.message;
  }
  try {
    const events = await client.request('GET', '/api/v1/owner/tunnel/events');
    renderTunnelEvents(events.events || []);
  } catch (error) {
    if (error.status !== 503) tunnelEventsMessage().textContent = error.message;
  }
}

function renderTunnelEvents(events) {
  const list = tunnelEventsList();
  list.replaceChildren();
  if (events.length === 0) {
    tunnelEventsMessage().textContent = 'No transitions recorded yet.';
    return;
  }
  tunnelEventsMessage().textContent = '';
  for (const event of events) {
    const row = document.createElement('div');
    row.className = 'tunnel-event';
    const time = document.createElement('span');
    time.className = 'tunnel-event-time';
    time.textContent = tunnelFmtTime(event.occurredAt);
    const status = document.createElement('span');
    status.className = 'tunnel-event-status';
    status.dataset.state = event.status;
    status.textContent = event.status;
    const detail = document.createElement('span');
    detail.className = 'tunnel-event-detail';
    detail.textContent = event.detail || '';
    if (event.httpStatus) {
      const code = document.createElement('span');
      code.className = 'tunnel-event-http';
      code.textContent = `HTTP ${event.httpStatus}`;
      detail.appendChild(document.createTextNode(' · '));
      detail.appendChild(code);
    }
    row.append(time, status, detail);
    list.append(row);
  }
}

document.getElementById('tunnel-refresh').addEventListener('click', () => { void loadTunnel(); });
document.getElementById('tunnel-verify').addEventListener('click', async (event) => {
  const button = event.currentTarget;
  button.disabled = true;
  try {
    const snap = await client.request('POST', '/api/v1/owner/tunnel/verify', {});
    renderTunnelSnapshot(snap);
    await loadTunnel();
  } catch (error) {
    tunnelMessage().textContent = error.message;
  } finally {
    button.disabled = false;
  }
});
document.getElementById('tunnel-disconnect').addEventListener('click', async (event) => {
  const button = event.currentTarget;
  button.disabled = true;
  try {
    const snap = await client.request('POST', '/api/v1/owner/tunnel/disconnect', {});
    renderTunnelSnapshot(snap);
    await loadTunnel();
  } catch (error) {
    tunnelMessage().textContent = error.message;
  } finally {
    button.disabled = false;
  }
});
const tunnelForm = document.getElementById('tunnel-form');
for (const input of tunnelForm.querySelectorAll('input, select')) {
  input.addEventListener('input', () => { tunnelForm.dataset.dirty = '1'; });
  input.addEventListener('change', () => { tunnelForm.dataset.dirty = '1'; });
}
tunnelForm.addEventListener('submit', async (event) => {
  event.preventDefault();
  const submit = tunnelForm.querySelector('button[type="submit"]');
  submit.disabled = true;
  try {
    const body = Object.fromEntries(new FormData(tunnelForm).entries());
    const snap = await client.request('PUT', '/api/v1/owner/tunnel', body);
    tunnelForm.dataset.dirty = '';
    renderTunnelSnapshot(snap);
    await loadTunnel();
    tunnelMessage().textContent = snap.provider === 'direct' ? 'Shop IP saved. On an existing loopback installation, restart the Windows service once to enable LAN access. Customers must connect to shop Wi-Fi.' : 'Configuration saved. Verify the public origin.';
  } catch (error) {
    tunnelMessage().textContent = error.message;
  } finally {
    submit.disabled = false;
  }
});

// ---- Phase 8a: Licensing and device identity ----

const licenceInstallationId = () => document.getElementById('licence-installation-id');
const licenceFingerprint = () => document.getElementById('licence-fingerprint');
const licencePublicKey = () => document.getElementById('licence-public-key');
const licenceEdition = () => document.getElementById('licence-edition');
const licenceSupportUntil = () => document.getElementById('licence-support-until');
const licenceVerifiedAt = () => document.getElementById('licence-verified-at');
const licenceState = () => document.getElementById('licence-state');
const licenceEntitlementsList = () => document.getElementById('licence-entitlements-list');
const licenceMessage = () => document.getElementById('licence-message');
const licenceEventsMessage = () => document.getElementById('licence-events-message');
const licenceEventsList = () => document.getElementById('licence-events-list');
const licenceRevokeForm = () => document.getElementById('licence-revoke-form');
const licenceRevokeFormElement = () => document.getElementById('licence-revoke-form-element');

function licenceFmtTime(value) {
  if (!value) return '—';
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return '—';
  return parsed.toLocaleString();
}

function describeLicenceState(status) {
  if (status.revoked) return 'revoked';
  if (status.clockRollback) return 'clock rollback detected';
  if (status.unconfigured) return 'unconfigured';
  if (status.offlineGrace) return `verified · offline grace ${status.graceRemaining || ''}`.trim();
  if (status.current) return 'verified';
  return 'unknown';
}

function renderLicence(status) {
  if(status.permanentLicense){
    licenceInstallationId().textContent=status.installationId;licenceFingerprint().textContent=status.licenseId||'Not activated';licencePublicKey().textContent='Publisher signature verified by the service';licenceEdition().textContent='Permanent · all features';licenceSupportUntil().textContent='Lifetime (no subscription expiry)';licenceVerifiedAt().textContent=status.active?'Activated once · no recurring checks':'Not yet activated';licenceState().textContent=status.message;document.getElementById('licence-transfer').hidden=true;document.getElementById('licence-revoke').hidden=true;licenceEntitlementsList().textContent='All software features on one shop computer';return;
  }
  licenceInstallationId().textContent = status.installationId || '(pending)';
  licenceFingerprint().textContent = status.fingerprint || '(pending)';
  licencePublicKey().textContent = status.publicKey ? `${status.publicKey.slice(0, 16)}…` : '(pending)';
  licenceEdition().textContent = status.current?.edition || '—';
  licenceSupportUntil().textContent = licenceFmtTime(status.supportUntil);
  licenceVerifiedAt().textContent = licenceFmtTime(status.verifiedAt);
  licenceState().textContent = describeLicenceState(status);
  const entitlements = status.entitlements || (status.current?.entitlements) || [];
  const list = licenceEntitlementsList();
  list.replaceChildren();
  if (entitlements.length === 0) {
    list.textContent = 'No entitlements recorded yet.';
    return;
  }
  for (const entitlement of entitlements) {
    const row = document.createElement('div');
    row.className = 'licence-entitlement';
    row.textContent = entitlement;
    list.append(row);
  }
}

async function loadLicence() {
  try {
    const status = await client.request('GET', '/api/v1/owner/licence');
    document.getElementById('software-license-banner').hidden=!status.permanentLicense||status.active;
    renderLicence(status);
    licenceMessage().textContent = status.revoked
      ? `Licence revoked: ${status.revokedReason || 'no reason recorded'}.`
      : '';
  } catch (error) {
    licenceMessage().textContent = error.message;
  }
  try {
    const events = await client.request('GET', '/api/v1/owner/licence/events?limit=50');
    renderLicenceEvents(events || []);
  } catch (error) {
    if (error.status !== 503) licenceEventsMessage().textContent = error.message;
  }
}

function renderLicenceEvents(events) {
  const list = licenceEventsList();
  list.replaceChildren();
  if (events.length === 0) {
    licenceEventsMessage().textContent = 'No transitions recorded yet.';
    return;
  }
  licenceEventsMessage().textContent = '';
  for (const event of events) {
    const row = document.createElement('div');
    row.className = 'licence-event';
    const time = document.createElement('span');
    time.className = 'licence-event-time';
    time.textContent = licenceFmtTime(event.occurredAt);
    const type = document.createElement('span');
    type.className = 'licence-event-type';
    type.textContent = event.eventType;
    const detail = document.createElement('span');
    detail.className = 'licence-event-detail';
    detail.textContent = event.detail || '';
    row.append(time, type, detail);
    list.append(row);
  }
}

document.getElementById('licence-refresh').addEventListener('click', () => { void loadLicence(); });
document.getElementById('licence-refresh-now').addEventListener('click', async (event) => {
  const button = event.currentTarget;
  button.disabled = true;
  try {
    const status = await client.request('POST', '/api/v1/owner/licence/refresh', {});
    renderLicence(status);
    licenceMessage().textContent = 'Re-verified successfully.';
  } catch (error) {
    licenceMessage().textContent = error.message;
  } finally {
    button.disabled = false;
  }
});
document.getElementById('licence-activate').addEventListener('click', async (event) => {
  const button = event.currentTarget;
  button.disabled = true;
  try {
    await client.request('POST', '/api/v1/owner/licence/activate', {licenseKey:document.getElementById('permanent-license-key').value.trim()});
    document.getElementById('permanent-license-key').value='';
    licenceMessage().textContent = 'Licence activated.';
    await loadLicence();
  } catch (error) {
    licenceMessage().textContent = error.message;
  } finally {
    button.disabled = false;
  }
});
document.getElementById('licence-transfer').addEventListener('click', async (event) => {
  if (!window.confirm('Transfer the device? This rotates the device key pair and invalidates the current licence; a fresh activation is required.')) {
    return;
  }
  const button = event.currentTarget;
  button.disabled = true;
  try {
    const status = await client.request('POST', '/api/v1/owner/licence/transfer', {});
    renderLicence(status);
    licenceMessage().textContent = 'Device transferred. Re-activate the licence on this machine.';
  } catch (error) {
    licenceMessage().textContent = error.message;
  } finally {
    button.disabled = false;
  }
});
document.getElementById('licence-revoke').addEventListener('click', () => {
  licenceRevokeForm().hidden = !licenceRevokeForm().hidden;
});
licenceRevokeFormElement().addEventListener('submit', async (event) => {
  event.preventDefault();
  const reason = licenceRevokeFormElement().elements.namedItem('reason').value.trim();
  if (!reason) {
    licenceMessage().textContent = 'Revocation reason is required.';
    return;
  }
  try {
    const status = await client.request('POST', '/api/v1/owner/licence/revoke', { reason });
    renderLicence(status);
    licenceRevokeForm().hidden = true;
    licenceMessage().textContent = 'Licence revoked. New commercial orders are blocked; already-paid work continues to print.';
  } catch (error) {
    licenceMessage().textContent = error.message;
  }
});

// ---- Phase 8b: Payments and reconciliation ----

let selectedPaymentProvider = '';
let savedWebhookSecret = '';
let paymentProviderChoices = [];
const paymentsProviders = () => document.getElementById('payments-providers');
const paymentsIntentsList = () => document.getElementById('payments-intents-list');
const paymentsLedgerList = () => document.getElementById('payments-ledger-list');
const paymentsMessage = () => document.getElementById('payments-message');
const paymentsProviderForm = () => document.getElementById('payments-provider-form');

function paymentsFmtTime(value) {
  if (!value) return '—';
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return '—';
  return parsed.toLocaleString();
}

function describeProvider(provider) {
  const flags = [];
  if (provider.enabled) flags.push('enabled');
  else flags.push('disabled');
  if (provider.isDefault) flags.push('default');
  if (provider.hasSecret) flags.push('secret set');
  return flags.join(', ');
}

function renderPaymentsProviders(providers) {
  const container = paymentsProviders();
  container.replaceChildren();
  for (const provider of providers) {
    const card = document.createElement('div');
    card.className = 'payments-provider-card';
    const title = document.createElement('div');
    title.className = 'payments-provider-title';
    title.textContent = provider.displayName;
    const status = document.createElement('div');
    status.className = 'payments-provider-status';
    status.textContent = describeProvider(provider);
    const actions = document.createElement('div');
    actions.className = 'payments-provider-actions';
    const enableBtn = document.createElement('button');
    enableBtn.type = 'button';
    enableBtn.className = 'btn btn--secondary';
    enableBtn.textContent = provider.enabled ? 'Disable' : 'Enable';
    enableBtn.addEventListener('click', async () => {
      enableBtn.disabled = true;
      try { await updateProviderEnabled(provider.id, !provider.enabled); }
      catch (error) { paymentsMessage().textContent = error.message; }
      finally { enableBtn.disabled = false; }
    });
    actions.appendChild(enableBtn);
    card.append(title, status, actions);
    container.append(card);
  }
}

function renderPaymentsIntents(intents) {
  const list = paymentsIntentsList();
  list.replaceChildren();
  if (intents.length === 0) {
    list.textContent = 'No payment intents recorded yet.';
    return;
  }
  for (const intent of intents) {
    const row = document.createElement('div');
    row.className = 'payments-intent-row';
    const head = document.createElement('div');
    head.className = 'payments-intent-head';
    head.textContent = `${intent.idempotencyKey || intent.id} · ${intent.status} · ${intent.amountMinor} ${intent.currency}`;
    const meta = document.createElement('div');
    meta.className = 'payments-intent-meta';
    meta.textContent = `order ${intent.orderId} · provider ${intent.providerId} · ${paymentsFmtTime(intent.createdAt)}`;
    row.append(head, meta);
    list.append(row);
  }
}

function renderPaymentsLedger(entries) {
  const list = paymentsLedgerList();
  list.replaceChildren();
  if (entries.length === 0) {
    list.textContent = 'No ledger entries recorded yet.';
    return;
  }
  for (const entry of entries) {
    const row = document.createElement('div');
    row.className = 'payments-ledger-row';
    const time = document.createElement('span');
    time.className = 'payments-ledger-time';
    time.textContent = paymentsFmtTime(entry.occurredAt);
    const type = document.createElement('span');
    type.className = 'payments-ledger-type';
    type.textContent = entry.eventType;
    const detail = document.createElement('span');
    detail.className = 'payments-ledger-detail';
    detail.textContent = `${entry.actor}: ${entry.detail || ''}`;
    row.append(time, type, detail);
    list.append(row);
  }
}

async function loadPayments() {
  paymentsMessage().textContent = '';
  await refreshPaymentConnection();
  try {
    const providers = await client.request('GET', '/api/v1/owner/payments/providers');
    renderPaymentsProviders(providers.providers || []);
    refreshPaymentProviderChoices(providers.providers || []);
  } catch (error) {
    paymentsMessage().textContent = error.message;
  }
  try {
    const intents = await client.request('GET', '/api/v1/owner/payments/intents?limit=20');
    renderPaymentsIntents(intents.intents || []);
  } catch (error) {
    paymentsMessage().textContent = error.message;
  }
  try {
    const ledger = await client.request('GET', '/api/v1/owner/payments/ledger?limit=50');
    renderPaymentsLedger(ledger.entries || []);
  } catch (error) {
    paymentsMessage().textContent = error.message;
  }
  // Load webhook URL and events for the first Razorpay merchant provider.
  await loadPaymentsWebhookURL();
}

async function updateProviderEnabled(providerID, enabled) {
  const list = await client.request('GET', '/api/v1/owner/payments/providers');
  const provider = (list.providers || []).find((p) => p.id === providerID);
  if (!provider) return;
  const body = {
    kind: provider.kind,
    displayName: provider.displayName,
    enabled,
    isDefault: provider.isDefault,
  };
  await client.request('PUT', `/api/v1/owner/payments/providers/${providerID}`, body);
  await loadPayments();
}

document.getElementById('payments-refresh').addEventListener('click', () => { void loadPayments(); });
const paymentsProviderTest = document.getElementById('payments-provider-test');
if (paymentsProviderTest) {
  paymentsProviderTest.addEventListener('click', async () => {
    const data = new FormData(paymentsProviderForm());
    const keyId = (data.get('razorpayKeyId') || '').toString().trim();
    const keySecret = (data.get('razorpayKeySecret') || '').toString();
    const webhookSecret = (data.get('razorpayWebhookSecret') || '').toString();
    if (!keyId || !keySecret || !webhookSecret) {
      paymentsMessage().textContent = 'Key ID, Key Secret and Webhook Secret are all required to test the connection.';
      return;
    }
    paymentsMessage().textContent = 'Testing Razorpay connection…';
    try {
      const body = {
        kind: 'razorpay_merchant',
        displayName: (data.get('displayName') || 'Test').toString(),
        enabled: data.get('enabled') === 'on',
        isDefault: data.get('isDefault') === 'on',
        secret: JSON.stringify({
          key_id: keyId,
          key_secret: keySecret,
          webhook_secret: webhookSecret,
        }),
      };
      await client.request('POST', '/api/v1/owner/payments/providers/test', body);
      paymentsMessage().textContent = 'Razorpay connection succeeded. The keys authenticate with Razorpay. Complete webhook setup and a Test Mode payment next.';
    } catch (error) {
      paymentsMessage().textContent = `Razorpay connection failed: ${error.message}`;
    }
  });
}
paymentsProviderForm().addEventListener('submit', async (event) => {
  event.preventDefault();
  const form = paymentsProviderForm();
  const data = new FormData(form);
  const id = document.getElementById('payments-edit-provider').value;
  const keyId = (data.get('razorpayKeyId') || '').toString().trim();
  const keySecret = (data.get('razorpayKeySecret') || '').toString();
  const webhookSecret = (data.get('razorpayWebhookSecret') || '').toString();
  const body = {kind: 'razorpay_merchant', displayName: data.get('displayName'), enabled: data.get('enabled') === 'on', isDefault: data.get('isDefault') === 'on'};
  if (!id || keyId || keySecret || webhookSecret) {
    if (!keyId || !keySecret || !webhookSecret) {
      paymentsMessage().textContent = 'Enter Key ID and Key Secret, then generate a webhook secret. For an existing connection, leave all three blank to keep saved credentials.';
      return;
    }
    body.secret = JSON.stringify({key_id: keyId, key_secret: keySecret, webhook_secret: webhookSecret});
  }
  const submit = form.querySelector('[type="submit"]');
  submit.disabled = true;
  try {
    const response = await client.request(id ? 'PUT' : 'POST', id ? `/api/v1/owner/payments/providers/${id}` : '/api/v1/owner/payments/providers', body);
    selectedPaymentProvider = response.id;
    for (const field of ['razorpayKeyId', 'razorpayKeySecret', 'razorpayWebhookSecret']) form.elements.namedItem(field).value = '';
    if (webhookSecret) savedWebhookSecret = webhookSecret;
    await loadPayments();
    paymentsMessage().textContent = 'Connection saved. Step 3: copy the webhook secret and URL into Razorpay, then complete a Test Mode payment.';
  } catch (error) { paymentsMessage().textContent = error.message; }
  finally { submit.disabled = false; }
});

document.getElementById('payments-manual-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const data = new FormData(document.getElementById('payments-manual-form'));
  const intentId = (data.get('intentId') || '').toString().trim();
  if (!intentId) {
    paymentsMessage().textContent = 'Intent id is required.';
    return;
  }
  const amountMinor = parseInt((data.get('amountMinor') || '0').toString(), 10);
  if (!amountMinor || amountMinor <= 0) {
    paymentsMessage().textContent = 'Amount (minor units) must be a positive integer.';
    return;
  }
  try {
    await client.request('POST', `/api/v1/owner/payments/intents/${intentId}/action`, {
      method: (data.get('method') || 'cash').toString(),
      reference: (data.get('reference') || '').toString(),
      amountMinor,
      currency: (data.get('currency') || '').toString().trim(),
      note: (data.get('note') || '').toString(),
    });
    paymentsMessage().textContent = `Manual approval recorded for ${intentId}.`;
    await loadPayments();
  } catch (error) {
    paymentsMessage().textContent = error.message;
  }
});

document.getElementById('payments-manual-reject').addEventListener('click', async () => {
  const data = new FormData(document.getElementById('payments-manual-form'));
  const intentId = (data.get('intentId') || '').toString().trim();
  const note = (data.get('note') || '').toString();
  if (!intentId) {
    paymentsMessage().textContent = 'Intent id is required.';
    return;
  }
  try {
    await client.request('POST', `/api/v1/owner/payments/intents/${intentId}/action`, {
      method: 'cash',
      reference: '',
      amountMinor: 0,
      currency: 'INR',
      note,
      reject: true,
    });
    paymentsMessage().textContent = `Manual rejection recorded for ${intentId}.`;
    await loadPayments();
  } catch (error) {
    paymentsMessage().textContent = error.message;
  }
});

function selectedRazorpayProvider(providers) {
  const id = document.getElementById('payments-edit-provider')?.value;
  return providers.find(p => p.id === id) || providers.find(p => p.kind === 'razorpay_merchant' && p.enabled);
}
async function refreshPaymentConnection() {
  const status = document.getElementById('payments-domain-status');
  try {
    const result = await client.request('GET', '/api/v1/owner/payments/connection');
    status.textContent = result.message;
    for (const el of paymentsProviderForm().querySelectorAll('input, button, select')) el.disabled = !result.ready;
  } catch (error) {
    status.textContent = error.message;
    for (const el of paymentsProviderForm().querySelectorAll('input, button, select')) el.disabled = true;
  }
}
function refreshPaymentProviderChoices(providers) {
  paymentProviderChoices = providers.filter(p => p.kind === 'razorpay_merchant');
  const select = document.getElementById('payments-edit-provider');
  const current = selectedPaymentProvider || select.value;
  select.replaceChildren(new Option('New Razorpay connection', ''));
  for (const p of paymentProviderChoices) select.add(new Option(p.displayName, p.id));
  select.value = current;
}
document.getElementById('payments-edit-provider').addEventListener('change', async event => {
  selectedPaymentProvider = event.target.value;
  savedWebhookSecret = '';
  const p = paymentProviderChoices.find(p => p.id === selectedPaymentProvider);
  const form = paymentsProviderForm();
  form.reset();
  event.target.value = selectedPaymentProvider;
  if (p) {
    form.elements.namedItem('displayName').value = p.displayName;
    form.elements.namedItem('enabled').checked = p.enabled;
    form.elements.namedItem('isDefault').checked = p.isDefault;
  }
  await loadPaymentsWebhookURL();
});
document.getElementById('payments-generate-secret').addEventListener('click', async event => {
  event.target.disabled = true;
  try {
    const result = await client.request('POST', '/api/v1/owner/payments/webhook-secret', {});
    paymentsProviderForm().elements.namedItem('razorpayWebhookSecret').value = result.secret;
    paymentsMessage().textContent = 'Secret generated. Save the connection, then copy the secret into Razorpay.';
  } catch (error) { paymentsMessage().textContent = error.message; }
  finally { event.target.disabled = false; }
});
document.getElementById('payments-copy-secret').addEventListener('click', async () => {
  const value = paymentsProviderForm().elements.namedItem('razorpayWebhookSecret').value || savedWebhookSecret;
  if (!value) { paymentsMessage().textContent = 'No secret in this page. Existing saved secrets are not displayed. Generate a replacement only if updating both this connection and Razorpay.'; return; }
  try { await navigator.clipboard.writeText(value); paymentsMessage().textContent = 'Webhook secret copied. Paste it into Razorpay webhook Secret.'; }
  catch { window.prompt('Copy the webhook secret:', value); }
});

// ---- Webhook URL loader and test handlers ----
//
// loadPaymentsWebhookURL fetches the webhook URL for the first Razorpay
// merchant provider and populates the webhook setup section. It is called
// after every provider save and after loadPayments(). The section is hidden
// when there is no Razorpay provider, and shows a "public origin not
// configured" hint when the tunnel is not set up.

async function loadPaymentsWebhookURL() {
  const urlEl = document.getElementById('payments-webhook-url');
  const hintEl = document.getElementById('payments-webhook-url-hint');
  const eventsEl = document.getElementById('payments-webhook-events');
  const testBtn = document.getElementById('payments-webhook-test');
  if (!urlEl || !hintEl || !eventsEl || !testBtn) return;

  // Find the first Razorpay merchant provider.
  let providers = [];
  try {
    const res = await client.request('GET', '/api/v1/owner/payments/providers');
    providers = res.providers || [];
  } catch {
    // No providers yet — clear and hide.
    urlEl.textContent = '';
    hintEl.textContent = 'Save a Razorpay provider first to see the webhook URL.';
    hintEl.hidden = false;
    eventsEl.replaceChildren();
    testBtn.disabled = true;
    return;
  }

  const razorpay = selectedRazorpayProvider(providers);

  if (!razorpay) {
    urlEl.textContent = '';
    hintEl.textContent = 'No enabled Razorpay provider found. Save and enable one to see the webhook URL.';
    hintEl.hidden = false;
    eventsEl.replaceChildren();
    testBtn.disabled = true;
    return;
  }

  try {
    const info = await client.request(
      'GET',
      `/api/v1/owner/payments/providers/${razorpay.id}/webhook-url`,
    );
    urlEl.textContent = info.webhookURL || '';
    hintEl.hidden = true;
    testBtn.disabled = false;
    // Render the events list.
    eventsEl.replaceChildren();
    if (info.events && info.events.length > 0) {
      for (const evt of info.events) {
        const tag = document.createElement('code');
        tag.textContent = evt;
        tag.style.display = 'inline-block';
        tag.style.margin = '2px 4px 2px 0';
        tag.style.padding = '2px 8px';
        tag.style.background = 'var(--surface-3)';
        tag.style.borderRadius = '4px';
        tag.style.fontSize = '12px';
        eventsEl.append(tag);
      }
    }
  } catch (error) {
    if (error.status === 409) {
      // Public origin not configured.
      urlEl.textContent = '';
      hintEl.textContent =
        'Public origin not configured. Set a public HTTPS origin in Tunnel & QR settings before registering the webhook URL in Razorpay.';
      hintEl.hidden = false;
    } else {
      urlEl.textContent = '';
      hintEl.textContent = `Could not load webhook URL: ${error.message}`;
      hintEl.hidden = false;
    }
    eventsEl.replaceChildren();
    testBtn.disabled = true;
  }
}

// Copy webhook URL button.
document.getElementById('payments-copy-webhook-url')?.addEventListener('click', async () => {
  const urlEl = document.getElementById('payments-webhook-url');
  const url = urlEl?.textContent?.trim() || '';
  if (!url) return;
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(url);
      paymentsMessage().textContent = 'Webhook URL copied to clipboard.';
    } else {
      // Fallback: show in an alert.
      window.prompt('Copy this webhook URL:', url);
    }
  } catch {
    window.prompt('Copy this webhook URL:', url);
  }
});

// Send test webhook button.
document.getElementById('payments-webhook-test')?.addEventListener('click', async () => {
  const resultEl = document.getElementById('payments-webhook-test-result');
  const testBtn = document.getElementById('payments-webhook-test');
  if (!resultEl) return;
  resultEl.textContent = 'Checking API credentials and domain…';
  resultEl.className = 'help';
  testBtn.disabled = true;
  try {
    // Find the first enabled Razorpay provider.
    const res = await client.request('GET', '/api/v1/owner/payments/providers');
    const providers = res.providers || [];
    const razorpay = selectedRazorpayProvider(providers);
    if (!razorpay) {
      resultEl.textContent = 'No enabled Razorpay provider found.';
      resultEl.className = 'help danger';
      return;
    }
    const result = await client.request(
      'POST',
      `/api/v1/owner/payments/providers/${razorpay.id}/webhook-test`,
      {},
    );
    if (result.ok) {
      resultEl.textContent =
        result.message ||
        `API and domain checks passed. Complete a Test Mode payment to verify webhook delivery.`;
      resultEl.className = 'help success';
    } else {
      resultEl.textContent =
        result.hint ||
        result.error ||
        `Connection check failed (HTTP ${result.statusCode}).`;
      resultEl.className = 'help danger';
    }
  } catch (error) {
    resultEl.textContent = `Connection check failed: ${error.message}`;
    resultEl.className = 'help danger';
  } finally {
    testBtn.disabled = false;
  }
});

// ---------------- Companion app pairing ----------------
//
// The companion app panel mints single-use pairing codes, lists the
// recently-issued codes (showing which were consumed by which device)
// and lists the paired devices with one-click revocation. The HTTP
// surface is owned by the pairing package; the dashboard is a thin
// shell over it. All write paths reuse the same owner/CSRF guard.

const pairingMessage = () => document.getElementById('pairing-message');
const pairingActive = () => document.getElementById('pairing-active');
const pairingActiveCode = () => document.getElementById('pairing-active-code');
const pairingActiveLink = () => document.getElementById('pairing-active-link');
const pairingActiveExpires = () => document.getElementById('pairing-active-expires');
const pairingCodesList = () => document.getElementById('pairing-codes-list');
const pairingCodesMessage = () => document.getElementById('pairing-codes-message');
const pairingDevicesList = () => document.getElementById('pairing-devices-list');
const pairingDevicesMessage = () => document.getElementById('pairing-devices-message');
const pairingRevokeDetails = () => document.getElementById('pairing-revoke-details');
const pairingRevokeForm = () => document.getElementById('pairing-revoke-form');
const pairingDeepLink = () => document.getElementById('pairing-deeplink');
const pairingCopy = () => document.getElementById('pairing-copy');

function pairingFmtTime(ms) {
  if (!ms) return '—';
  const date = new Date(typeof ms === 'string' || typeof ms === 'number' ? ms : Date.now());
  if (Number.isNaN(date.getTime())) return String(ms);
  return date.toLocaleString();
}

function describeCodeStatus(code) {
  if (code.consumedAt) return 'consumed';
  if (code.expiresAt && new Date(code.expiresAt).getTime() < Date.now()) return 'expired';
  return 'pending';
}

async function copyActiveCode() {
  const text = pairingActiveCode().textContent;
  if (!text) return;
  try {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(text);
      pairingMessage().textContent = 'Pairing code copied to the clipboard.';
    } else {
      pairingMessage().textContent = `Pairing code: ${text}`;
    }
  } catch (error) {
    pairingMessage().textContent = `Pairing code: ${text}`;
  }
}

function renderPairingCodes(codes) {
  const list = pairingCodesList();
  list.replaceChildren();
  if (!codes || codes.length === 0) {
    pairingCodesMessage().textContent = 'No pairing codes minted yet.';
    return;
  }
  pairingCodesMessage().textContent = '';
  for (const code of codes) {
    const row = document.getElementById('pairing-code-row').content.firstElementChild.cloneNode(true);
    row.querySelector('.pairing-code-status').textContent = describeCodeStatus(code);
    row.querySelector('.pairing-code-status').dataset.state = describeCodeStatus(code);
    row.querySelector('.pairing-code-time').textContent = `minted ${pairingFmtTime(code.createdAt)}`;
    row.querySelector('.pairing-code-expires').textContent = `expires ${pairingFmtTime(code.expiresAt)}`;
    list.append(row);
  }
}

function renderPairingDevices(devices) {
  const list = pairingDevicesList();
  list.replaceChildren();
  if (!devices || devices.length === 0) {
    pairingDevicesMessage().textContent = 'No paired devices yet. Mint a code and open the deep link on your phone to pair the companion app.';
    return;
  }
  pairingDevicesMessage().textContent = '';
  for (const device of devices) {
    const row = document.getElementById('pairing-device-row').content.firstElementChild.cloneNode(true);
    row.dataset.deviceId = device.id;
    row.querySelector('.pairing-device-label').textContent = device.label || '(no label)';
    row.querySelector('.pairing-device-fingerprint').textContent = device.fingerprint;
    row.querySelector('.pairing-device-paired').textContent = `paired ${pairingFmtTime(device.pairedAt)}`;
    row.querySelector('.pairing-device-seen').textContent = `last seen ${pairingFmtTime(device.lastSeenAt)}`;
    const revoked = row.querySelector('.pairing-device-revoked');
    if (device.revokedAt) {
      revoked.textContent = `revoked ${pairingFmtTime(device.revokedAt)} · ${device.revokedReason || 'no reason recorded'}`;
      revoked.dataset.state = 'revoked';
      row.querySelector('.pairing-device-revoke').disabled = true;
    } else {
      revoked.textContent = '';
    }
    list.append(row);
  }
}

async function loadPairing() {
  pairingMessage().textContent = '';
  try {
    const status = await client.request('GET', '/api/v1/owner/pairing');
    renderPairingCodes(status.pendingCodes || []);
    renderPairingDevices(status.devices || []);
  } catch (error) {
    if (error.status === 503) {
      pairingCodesMessage().textContent = 'Companion pairing is not wired on this build.';
      pairingDevicesMessage().textContent = 'Companion pairing is not wired on this build.';
      return;
    }
    pairingMessage().textContent = error.message;
  }
}

async function mintPairingCode() {
  pairingMessage().textContent = '';
  const deepLink = pairingDeepLink().value.trim();
  try {
    const code = await client.request('POST', '/api/v1/owner/pairing/initiate', deepLink ? { deepLinkTemplate: deepLink } : {});
    pairingActive().hidden = false;
    pairingActiveCode().textContent = code.code;
    pairingActiveLink().href = code.deepLink;
    pairingActiveLink().textContent = code.deepLink;
    pairingActiveExpires().textContent = pairingFmtTime(code.expiresAt);
    pairingMessage().textContent = 'Pairing code minted. Open the deep link on the merchant phone within five minutes.';
    await loadPairing();
  } catch (error) {
    pairingMessage().textContent = error.message;
  }
}

async function revokeDevice(deviceId) {
  const reason = window.prompt('Why are you revoking this device? (recorded in the audit trail)');
  if (!reason || !reason.trim()) {
    pairingMessage().textContent = 'Revocation cancelled; a reason is required for the audit trail.';
    return;
  }
  try {
    await client.request('POST', '/api/v1/owner/pairing/revoke', { deviceId, reason: reason.trim() });
    pairingMessage().textContent = `Device ${deviceId} revoked.`;
    await loadPairing();
  } catch (error) {
    pairingMessage().textContent = error.message;
  }
}

document.getElementById('pairing-refresh').addEventListener('click', () => { void loadPairing(); });
document.getElementById('pairing-mint').addEventListener('click', () => { void mintPairingCode(); });
pairingCopy().addEventListener('click', () => { void copyActiveCode(); });
pairingDevicesList().addEventListener('click', (event) => {
  const target = event.target;
  if (!(target instanceof HTMLElement)) return;
  if (!target.classList.contains('pairing-device-revoke')) return;
  const row = target.closest('.pairing-device-row');
  if (!row) return;
  void revokeDevice(row.dataset.deviceId);
});
pairingRevokeForm().addEventListener('submit', async (event) => {
  event.preventDefault();
  const form = event.currentTarget;
  const deviceId = form.elements.namedItem('deviceId').value.trim();
  const reason = form.elements.namedItem('reason').value.trim();
  if (!deviceId || !reason) {
    pairingMessage().textContent = 'Device id and reason are both required.';
    return;
  }
  try {
    await client.request('POST', '/api/v1/owner/pairing/revoke', { deviceId, reason });
    pairingMessage().textContent = `Device ${deviceId} revoked.`;
    form.reset();
    pairingRevokeDetails().hidden = true;
    await loadPairing();
  } catch (error) {
    pairingMessage().textContent = error.message;
  }
});

async function loadPricing(){if(!pricingGridController)pricingGridController=createPricingGrid(client,()=>role);await pricingGridController.refresh();}

fetch('/api/v1/portal/branding',{cache:'no-store'}).then(r=>r.ok?r.json():null).then(b=>{if(b?.businessName)displayBusinessName(b.businessName);}).catch(()=>{});
