import {test} from 'node:test';
import assert from 'node:assert/strict';
import {invoiceExample} from '../runtime/internal/localserver/web/printer-invoices.mjs';
import {queueState,orderTotals} from '../runtime/internal/localserver/web/order-queue.mjs';
import {createOwnerClient} from '../runtime/internal/localserver/web/client.mjs';

test('invoice threshold explains strict above and zero',()=>{
 assert.match(invoiceExample(4),/5 waiting orders/);assert.match(invoiceExample(4),/4 or fewer/);assert.match(invoiceExample(0),/Every order/);
});
test('invoice affects progress without adding chargeable document sheets',()=>{
 const order={status:'dispatched',documents:[{printState:'completed',pageStart:1,pageEnd:1,copies:1,sides:'one-sided'}],separators:[{printState:'waiting'}]};
 assert.equal(queueState(order),'processing');order.separators[0].printState='failed';assert.equal(queueState(order),'failed');order.separators[0].printState='submitted';assert.equal(queueState(order),'printing');order.separators[0].printState='completed';assert.equal(queueState(order),'done');assert.deepEqual(orderTotals(order),{pages:1,sheets:1});
});
test('owner client permits per-printer invoice configuration',async()=>{
 const calls=[];const client=createOwnerClient(async(path,options)=>{calls.push([path,options]);return {ok:true,json:async()=>({enabled:false,threshold:4})};});
 await client.request('GET','/api/v1/owner/printers/abc/invoice-settings');await client.request('PUT','/api/v1/owner/printers/abc/invoice-settings',{enabled:true,threshold:4,paper:'A6',tray:'Tray 2'});assert.equal(calls.length,2);
});
