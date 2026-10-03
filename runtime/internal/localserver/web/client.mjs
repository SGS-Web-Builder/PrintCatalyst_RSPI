// Tokens stay in memory; the session itself is an HttpOnly cookie.
export function createOwnerClient(fetcher = globalThis.fetch.bind(globalThis)) {
  let csrf = '';
  return {
    async request(method, path, body) {
      if (!/^\/api\/v1\/(owner\/(login|session|logout|business(\/(settings|services|discounts)(\/[A-Za-z0-9_-]+)?)?|paper-stock|qr-theme|portal-payment-buttons|config(\/reload)?|pricing(\/grid\/[A-Za-z0-9_-]+)?|operators(\/[A-Za-z0-9_-]+)?|orders(\/[A-Za-z0-9_-]+)?|orders\/[A-Za-z0-9_-]+\/(status|print)|orders\/[A-Za-z0-9_-]+\/invoice|invoices|reports\/|notifications(\/|$)|printers(\/[A-Za-z0-9_-]+)?(\/[A-Za-z0-9_-]+)?|id-cards(\/[A-Za-z0-9_-]+)?(\/[A-Za-z0-9_-]+)?(\/[A-Za-z0-9_-]+)?|passports(\/[A-Za-z0-9_-]+)?(\/[A-Za-z0-9_-]+)?(\/[A-Za-z0-9_-]+)?|tunnel(\/(verify|disconnect|events))?|licence(\/(activate|refresh|revoke|transfer|events))?|payments(\/(providers|providers\/[A-Za-z0-9_-]+(\/(webhook-url|webhook-test))?|connection|webhook-secret|intents|intents\/[A-Za-z0-9_-]+\/action|ledger))?|pairing(\/(initiate|revoke))?)|setup\/(owner|status))$/.test(path.split('?')[0])) throw new Error('Only local owner endpoints are permitted.');
      const headers = { 'Content-Type': 'application/json' };
      if (csrf && method !== 'GET') headers['X-CSRF-Token'] = csrf;
      const response = await fetcher(path, {
        method, headers, credentials: 'same-origin', redirect: 'error', cache: 'no-store',
        ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
      });
      if (!response.ok) {
        if (response.status === 401) csrf = '';
        const error = new Error((await response.text()).trim() || 'The local server could not complete this request.');
        error.status = response.status;
        throw error;
      }
      const result = await response.json();
      if (result.csrfToken) csrf = result.csrfToken;
      if (path.endsWith('/logout')) csrf = '';
      return result;
    },
  };
}