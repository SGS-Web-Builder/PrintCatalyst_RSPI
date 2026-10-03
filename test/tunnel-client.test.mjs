import test from 'node:test';
import assert from 'node:assert/strict';
import { createOwnerClient } from '../runtime/internal/localserver/web/client.mjs';

const stubFetch = () => async (path, options) => {
  void options;
  return { ok: true, status: 200, json: async () => ({ ok: true, path }) };
};

test('owner client permits tunnel endpoints behind the local allow-list', async () => {
  const client = createOwnerClient(stubFetch());
  for (const path of [
    '/api/v1/owner/tunnel',
    '/api/v1/owner/tunnel/verify',
    '/api/v1/owner/tunnel/disconnect',
    '/api/v1/owner/tunnel/events',
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
  await assert.rejects(client.request('GET', '/api/v1/owner/tunnel/qr.svg'), /local/i);
});
