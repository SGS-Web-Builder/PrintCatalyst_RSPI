import test from 'node:test';
import assert from 'node:assert/strict';
import {createOwnerClient} from '../runtime/internal/localserver/web/client.mjs';
test('paper stock uses authenticated owner client and sends refill idempotency key',async()=>{
 const calls=[];const client=createOwnerClient(async(path,options)=>{calls.push({path,options});return {ok:true,json:async()=>({csrfToken:'token'})};});
 await client.request('POST','/api/v1/owner/login',{});
 await client.request('GET','/api/v1/owner/paper-stock');
 await client.request('POST','/api/v1/owner/paper-stock',{printerId:'p',sheets:500,requestId:'unique-refill-key'});
 assert.equal(calls[2].options.headers['X-CSRF-Token'],'token');
 assert.equal(JSON.parse(calls[2].options.body).sheets,500);
 assert.equal(JSON.parse(calls[2].options.body).requestId,'unique-refill-key');
});
