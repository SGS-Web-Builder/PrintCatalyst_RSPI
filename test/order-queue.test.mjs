import test from 'node:test';
import assert from 'node:assert/strict';
import {queueState,selectedPages,orderTotals,matchesOrder} from '../runtime/internal/localserver/web/order-queue.mjs';

const doc={filename:'Attendance.pdf',pagesJSON:'[1,3,5]',pageStart:1,pageEnd:5,copies:2,pagesPerSheet:2,sides:'two-sided-long-edge',printState:''};
test('queue separates print progress from payment and exposes failures',()=>{
 const order={status:'pending_payment',documents:[{...doc}]};
 assert.equal(queueState(order),'pending');
 order.printRequested=true;assert.equal(queueState(order),'processing');
 order.documents[0].printState='failed';assert.equal(queueState(order),'failed');
 order.documents[0].printState='submitting';assert.equal(queueState(order),'processing');
 order.printRequested=false;order.documents[0].printState='submitted';assert.equal(queueState(order),'printing');
 assert.equal(order.status,'pending_payment');
 order.status='cancelled';assert.equal(queueState(order),'rejected');
 order.status='completed';assert.equal(queueState(order),'done');
});
test('queue counts sparse selections, N-up, duplex and separate copies',()=>{
 assert.deepEqual(selectedPages(doc),[1,3,5]);
 assert.deepEqual(orderTotals({documents:[doc]}),{pages:6,sheets:2});
 assert.deepEqual(orderTotals({documents:[{...doc,pagesPerSheet:1}]}),{pages:6,sheets:4});
 assert.deepEqual(selectedPages({...doc,pagesJSON:'null',pageStart:2,pageEnd:4}),[2,3,4]);
});
test('search supports file, customer, phone and order; dates use local day boundaries',()=>{
 const now=new Date(2026,8,24,15);
 const order={id:'abcd0541',customerName:'Ravi',customerPhone:'98765',createdAt:new Date(2026,8,24,0,1).getTime()/1000,documents:[doc]};
 for(const search of ['0541','ravi','987','attendance'])assert.ok(matchesOrder(order,search,'today',now));
 assert.equal(matchesOrder(order,'missing','all',now),false);
 order.createdAt=new Date(2026,8,23,23,59).getTime()/1000;
 assert.equal(matchesOrder(order,'','today',now),false);
 assert.ok(matchesOrder(order,'','week',now));
});

test('Windows progress updates independently of payment, including recovery and done',()=>{
 const o={status:'pending_payment',documents:[{...doc}]};
 for(const [state,want] of [['pending','pending'],['processing','processing'],['printing','printing'],['blocked','failed'],['printing','printing'],['review','processing'],['completed','done']]){
  o.documents[0].printState=state;assert.equal(queueState(o),want);assert.equal(o.status,'pending_payment');
 }
 o.documents.push({...doc,printState:'printing'});assert.equal(queueState(o),'printing');
 o.documents[1].printState='failed';assert.equal(queueState(o),'failed');
});
