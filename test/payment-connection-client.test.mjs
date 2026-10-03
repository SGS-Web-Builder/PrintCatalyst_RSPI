import test from 'node:test';
import assert from 'node:assert/strict';
import { createOwnerClient } from '../runtime/internal/localserver/web/client.mjs';

test('connection wizard permits only local setup and provider webhook endpoints', async () => {
  const calls = [];
  const client = createOwnerClient(async (path, options) => {
    calls.push({path, options});
    return {ok: true, json: async () => path.endsWith('/login') ? {csrfToken:'test-csrf'} : {}};
  });
  await client.request('POST', '/api/v1/owner/login', {});
  for (const path of ['/api/v1/owner/payments/connection', '/api/v1/owner/payments/providers/provider-1/webhook-url', '/api/v1/owner/payments/intents?limit=20', '/api/v1/owner/payments/ledger?limit=50']) await client.request('GET', path);
  for (const path of ['/api/v1/owner/payments/webhook-secret', '/api/v1/owner/payments/providers/provider-1/webhook-test']) {
    await client.request('POST', path, {});
    assert.equal(calls.at(-1).options.headers['X-CSRF-Token'], 'test-csrf');
    assert.equal(calls.at(-1).options.cache, 'no-store');
  }
  await assert.rejects(client.request('POST', 'https://example.com/api/v1/owner/payments/webhook-secret', {}));
});
