import test from 'node:test';
import assert from 'node:assert/strict';
import { createOwnerClient } from '../runtime/internal/localserver/web/client.mjs';

test('owner client sends session-bound CSRF on same-origin writes, and clears it on logout',async()=>{
 const calls=[];
 const client=createOwnerClient(async(path,options)=>{calls.push({path,options});return {ok:true,status:200,json:async()=>path.endsWith('/login')?{csrfToken:'session-csrf'}:{signedOut:true}};});
 await client.request('POST','/api/v1/owner/login',{username:'owner',password:'not-persisted'});
 await client.request('PUT','/api/v1/owner/business',{name:'Campus'});
 assert.equal(calls[1].options.headers['X-CSRF-Token'],'session-csrf');
 assert.equal(calls[1].options.credentials,'same-origin');
 assert.equal(calls[1].options.redirect,'error');
 await client.request('POST','/api/v1/owner/logout',{});
 await client.request('POST','/api/v1/owner/login',{});
 assert.equal(calls[3].options.headers['X-CSRF-Token'],undefined);
 await assert.rejects(client.request('GET','https://example.com'),/local/i);
});
test('owner client exposes a failed local save and never treats it as success',async()=>{
 const client=createOwnerClient(async()=>({ok:false,status:403,text:async()=> 'invalid CSRF token'}));
 await assert.rejects(client.request('PUT','/api/v1/owner/business',{}),/invalid CSRF token/);
});
