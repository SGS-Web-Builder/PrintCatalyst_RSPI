import test from 'node:test';
import assert from 'node:assert/strict';
import { createOwnerClient } from '../runtime/internal/localserver/web/client.mjs';

// The owner client is the same module the dashboard loads; these tests
// verify the operator CRUD surface is reachable via the local allowlist,
// that CSRF and credentials behave identically to the existing endpoints,
// and that out-of-scope paths are still refused before any fetch.
test('owner client permits operator CRUD paths and refuses everything else', async () => {
  const calls = [];
  const client = createOwnerClient(async (path, options) => {
    calls.push({ path, options });
    if (path.endsWith('/login')) return { ok: true, status: 200, json: async () => ({ csrfToken: 'session-csrf', role: 'owner' }) };
    if (path.endsWith('/operators') && options.method === 'GET') return { ok: true, status: 200, json: async () => ({ operators: [{ id: 1, username: 'alice', role: 'operator', enabled: true, createdAt: 0, updatedAt: 0, lastLoginAt: 0 }] }) };
    return { ok: true, status: 200, json: async () => ({ id: 1, username: 'alice', role: 'operator', enabled: true, createdAt: 0, updatedAt: 0, lastLoginAt: 0 }) };
  });

  await client.request('POST', '/api/v1/owner/login', { username: 'owner', password: 'irrelevant' });
  const roster = await client.request('GET', '/api/v1/owner/operators');
  assert.equal(roster.operators.length, 1);
  assert.equal(roster.operators[0].username, 'alice');

  await client.request('POST', '/api/v1/owner/operators', { username: 'bob', password: 'a sufficiently long password' });
  const created = calls[2];
  assert.equal(created.path, '/api/v1/owner/operators');
  assert.equal(created.options.headers['X-CSRF-Token'], 'session-csrf');

  await client.request('PATCH', '/api/v1/owner/operators/7', { enabled: false });
  const patched = calls[3];
  assert.equal(patched.path, '/api/v1/owner/operators/7');
  assert.equal(patched.options.method, 'PATCH');
  assert.equal(JSON.parse(patched.options.body).enabled, false);

  await client.request('DELETE', '/api/v1/owner/operators/7');
  assert.equal(calls[4].options.method, 'DELETE');

  // Reads never carry the CSRF header.
  assert.equal(calls[1].options.headers['X-CSRF-Token'], undefined);
});

test('operator CRUD paths reject traversal and external hosts', async () => {
  const client = createOwnerClient(async () => ({ ok: true, status: 200, json: async () => ({}) }));
  await assert.rejects(client.request('GET', '/api/v1/owner/operators/../../../etc/passwd'), /local owner endpoints/i);
  await assert.rejects(client.request('POST', '/api/v1/owner/operators/../other'), /local owner endpoints/i);
  await assert.rejects(client.request('GET', '/api/v1/orders/operators'), /local owner endpoints/i);
  await assert.rejects(client.request('GET', 'https://attacker.example/api/v1/owner/operators'), /local/i);
});

test('operator write failures surface the local server message', async () => {
  const client = createOwnerClient(async () => ({ ok: false, status: 409, text: async () => 'an operator with that username already exists' }));
  await assert.rejects(
    client.request('POST', '/api/v1/owner/operators', { username: 'alice', password: 'a sufficiently long password' }),
    /already exists/,
  );
  await assert.rejects(
    client.request('PATCH', '/api/v1/owner/operators/1', { enabled: false }),
    /already exists/,
  );
});