export function initializeDashboardShell() {
  const shell = document.getElementById('app-shell');
  const toggle = document.getElementById('sidebar-open');
  const close = document.getElementById('sidebar-close');
  const backdrop = document.getElementById('sidebar-backdrop');
  const narrow = matchMedia('(max-width: 1099px)');
  let desktopCollapsed = false;
  try { desktopCollapsed = localStorage.getItem('dashboard.sidebar.collapsed') === 'true'; } catch {}
  const setCollapsed = (collapsed, persist = false) => {
    shell.classList.toggle('sidebar-collapsed', collapsed);
    toggle.setAttribute('aria-expanded', String(!collapsed));
    toggle.setAttribute('aria-controls', 'sidebar');
    toggle.setAttribute('aria-label', collapsed ? 'Expand navigation' : 'Collapse navigation');
    backdrop.hidden = !narrow.matches || collapsed;
    if (persist && !narrow.matches) {
      desktopCollapsed = collapsed;
      try { localStorage.setItem('dashboard.sidebar.collapsed', String(collapsed)); } catch {}
    }
  };
  toggle.addEventListener('click', () => setCollapsed(!shell.classList.contains('sidebar-collapsed'), true));
  close.addEventListener('click', () => { setCollapsed(true, true); toggle.focus(); });
  backdrop.addEventListener('click', () => setCollapsed(true));
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape' && narrow.matches) { setCollapsed(true); toggle.focus(); }
  });
  narrow.addEventListener('change', () => setCollapsed(narrow.matches || desktopCollapsed));
  setCollapsed(narrow.matches || desktopCollapsed);

  const tunnel = document.getElementById('tunnel-panel');
  document.getElementById('qr-panel-domain').append(tunnel);
  tunnel.hidden = false;
  const qrTabs = [...document.querySelectorAll('[data-qr-tab]')];
  const selectQRTab = name => {
    for (const tab of qrTabs) {
      const selected = tab.dataset.qrTab === name;
      tab.setAttribute('aria-selected', String(selected));
      tab.tabIndex = selected ? 0 : -1;
      document.getElementById(tab.getAttribute('aria-controls')).hidden = !selected;
    }
  };
  qrTabs.forEach((tab, index) => {
    tab.addEventListener('click', () => selectQRTab(tab.dataset.qrTab));
    tab.addEventListener('keydown', event => {
      let next;
      if (event.key === 'ArrowRight') next = (index + 1) % qrTabs.length;
      if (event.key === 'ArrowLeft') next = (index + qrTabs.length - 1) % qrTabs.length;
      if (event.key === 'Home') next = 0;
      if (event.key === 'End') next = qrTabs.length - 1;
      if (next === undefined) return;
      event.preventDefault();
      selectQRTab(qrTabs[next].dataset.qrTab);
      qrTabs[next].focus();
    });
  });
  const tabs = [...document.querySelectorAll('[data-settings-tab]')];
  const selectSettingsTab = name => {
    for (const tab of tabs) {
      const selected = tab.dataset.settingsTab === name;
      tab.setAttribute('aria-selected', String(selected));
      tab.tabIndex = selected ? 0 : -1;
      document.getElementById(tab.getAttribute('aria-controls')).hidden = !selected;
    }
  };
  tabs.forEach((tab, index) => {
    tab.addEventListener('click', () => selectSettingsTab(tab.dataset.settingsTab));
    tab.addEventListener('keydown', event => {
      let next;
      if (event.key === 'ArrowRight') next = (index + 1) % tabs.length;
      if (event.key === 'ArrowLeft') next = (index + tabs.length - 1) % tabs.length;
      if (event.key === 'Home') next = 0;
      if (event.key === 'End') next = tabs.length - 1;
      if (next === undefined) return;
      event.preventDefault();
      selectSettingsTab(tabs[next].dataset.settingsTab);
      tabs[next].focus();
    });
  });
  return { selectQRTab, selectSettingsTab, afterNavigate() { if (narrow.matches) setCollapsed(true); } };
}
