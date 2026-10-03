import test from 'node:test';
import assert from 'node:assert/strict';
import { createOwnerClient } from '../runtime/internal/localserver/web/client.mjs';

const stubFetch = () => async (path, options) => {
  void options;
  return { ok: true, status: 200, json: async () => ({ ok: true, path }) };
};

test('owner client permits pairing endpoints behind the local allow-list', async () => {
  const client = createOwnerClient(stubFetch());
  for (const path of [
    '/api/v1/owner/pairing',
    '/api/v1/owner/pairing/initiate',
    '/api/v1/owner/pairing/revoke',
  ]) {
    const result = await client.request('GET', path);
    assert.equal(result.ok, true, `${path} should be permitted`);
    assert.equal(result.path, path);
  }
});

test('owner client still rejects non-owner paths and cross-origin targets', async () => {
  const client = createOwnerClient(stubFetch());
  await assert.rejects(client.request('GET', '/api/v1/portal/orders'), /local/i);
  await assert.rejects(client.request('GET', 'https://attacker.example.com/'), /local/i);
  // The public pair/exchange endpoint is intentionally NOT routed
  // through the owner client — it is unauthenticated and the mobile
  // companion application calls it directly with its own fetcher.
  await assert.rejects(client.request('POST', '/api/v1/pair/exchange'), /local/i);
});
