import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {readFileSync} from 'node:fs';
import {formatMoney} from '../runtime/internal/localserver/web/money.mjs';

test('order totals preserve zero, two and three decimal currencies and large totals',()=>{
 assert.equal(formatMoney(250,2),'2.50');
 assert.equal(formatMoney(250,0),'250');
 assert.equal(formatMoney(1005,3),'1.005');
 assert.equal(formatMoney(100000001,2),'1000000.01');
});
test('dashboard order rows render returned orders with currency precision',()=>{
 const source=readFileSync(new URL('../runtime/internal/localserver/web/setup.js',import.meta.url),'utf8');
 assert.match(source,/import \{[^}]*formatMoney[^}]*\} from '.\/money.mjs'/);
 const body=source.slice(source.indexOf('function renderOrderRow('),source.indexOf('async function openOrder('));
 const element=()=>({dataset:{},children:[],append(...items){this.children.push(...items)},addEventListener(){}});
 const context=vm.createContext({document:{createElement:element},formatMoney,Date});
 vm.runInContext(body,context);
 for(const [precision,total] of [[2,'2.50'],[0,'250'],[3,'0.250']]){
  const row=context.renderOrderRow({id:'order-1',customerName:'Test customer',totalMinor:250,currencyMinorUnits:precision,currency:'TEST',status:'pending_payment',createdAt:1});
  assert.equal(row.dataset.orderId,'order-1');
  assert.ok(row.children[0].children[1].textContent.startsWith(total+' TEST'));
 }
});
