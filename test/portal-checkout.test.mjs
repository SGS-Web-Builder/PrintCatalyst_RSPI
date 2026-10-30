import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {createImageComposer} from '../runtime/internal/localserver/web/portal/image-composer.mjs';
import {readFileSync} from 'node:fs';

const source=readFileSync(new URL('../runtime/internal/localserver/web/portal/portal.mjs',import.meta.url),'utf8');
test('pickup display preserves leading zeros and clears secrets after claim or reset',()=>{
 const {api,element}=portal();
 api.renderPickup({state:'active',code:'0007',preparation:'pending'});
 assert.equal(element('#pickup-code').textContent,'0007');
 assert.match(element('#pickup-message').textContent,/being prepared/);
 api.renderPickup({state:'active',code:'0007',preparation:'ready'});
 assert.match(element('#pickup-message').textContent,/shop touchscreen/);
 api.renderPickup({state:'claimed'});
 assert.equal(element('#pickup-code').textContent,'');
 assert.match(element('#pickup-message').textContent,/queued/);
 api.renderPickup({state:'expired'});
 assert.match(element('#pickup-message').textContent,/paid order is saved/);
 api.renderPickup(null);
 assert.equal(element('#pickup-panel').hidden,true);
 assert.equal(element('#pickup-code').textContent,'');
});
test('pickup display rejects malformed codes and never renders markup',()=>{
 const {api,element}=portal();
 api.renderPickup({state:'active',code:'<img src=x>',preparation:'ready'});
 assert.equal(element('#pickup-code').textContent,'');
 api.renderPickup({state:'waiting_payment'});
 assert.equal(element('#pickup-panel').hidden,true);
});
function portal(fetchFn=async()=>new Response('{}'), extras={}) {
 const elements=new Map();
 const element=selector=>{if(!elements.has(selector))elements.set(selector,{classList:{toggle(){}},disabled:false,hidden:false,value:'',textContent:'',parentElement:{hidden:false,querySelectorAll:()=>[]},before(){},focus(){this.focused=true}});return elements.get(selector);};
 const ctx=vm.createContext({prepareNotifications:()=>Promise.resolve(),subscribeOrder:()=>Promise.resolve(),notificationReceipt:()=>null,createImageComposer,window:{addEventListener(){}},document:{createElement:()=>({textContent:''}),querySelector:element,getElementById:id=>element('#'+id),querySelectorAll:()=>[]},fetch:fetchFn,Response,JSON,console,URL,setTimeout,clearTimeout,...extras});
 vm.runInContext(source.replace(/^import .*payment-notifications.mjs';\r?\n/, '').replace(/^import .*image-composer.mjs[^']*';\r?\n/, '')+'\n globalThis.api={uploadFiles,prepareUpload(send){uploadWithProgress=send;renderUploadedFiles=()=>{};loadPricing=async()=>{};renderConfig=()=>{}},retryPayment,renderPickup,handleStatusUpdate,restoreCustomerPhone,rememberCustomerPhone,initializePortal,persistOrderReceipt,prepareRecovery(callback){loadPricing=async()=>{};showConfirmation=callback},uploadWithProgress,state,beginCheckout,configureCustomerDetails,mockSubmit(fn){placeOrder=fn},pagesForMode,readResponse,requestQuote,updateCheckoutButtons,setReady(value){quoteReady=value},setPlacing(value){placingOrder=value}};',ctx);
 return {api:ctx.api,element};
}
test('real portal displays readiness and non-JSON server errors',async()=>{
 const {api}=portal();
 await assert.rejects(api.readResponse(new Response(JSON.stringify({reason:'setup incomplete: printer'}),{status:503})),/setup incomplete: printer/);
 await assert.rejects(api.readResponse(new Response('same-origin access required',{status:403})),/same-origin access required/);
});
test('checkout offers cash and conditionally online; pending checkout disables both',()=>{
 const {api,element}=portal();api.setReady(true);api.state.razorpayReady=false;api.updateCheckoutButtons();
 assert.equal(element('#place-order-btn').disabled,false);assert.equal(element('#pay-online-btn').hidden,true);
 api.state.razorpayReady=true;api.updateCheckoutButtons();assert.equal(element('#pay-online-btn').hidden,false);
 api.setPlacing(true);api.updateCheckoutButtons();assert.equal(element('#place-order-btn').disabled,true);assert.equal(element('#pay-online-btn').disabled,true);
});
test('quote sends service without a customer discount and disables payment until current quote arrives',async()=>{
 let finish,body;const {api,element}=portal(async(_url,options)=>{body=JSON.parse(options.body);return new Promise(resolve=>{finish=()=>resolve(new Response(JSON.stringify({totalMinor:270,discountMinor:30,currency:'INR',currencyMinorUnits:2})));});});
 element('#service-choice').value='service-a';
 api.setReady(true);const pending=api.requestQuote();
 assert.equal(element('#place-order-btn').disabled,true);assert.equal(body.serviceId,'service-a');assert.equal(body.discountCode,undefined);
 finish();await pending;assert.equal(element('#place-order-btn').disabled,false);assert.equal(element('#quote-total').textContent,'2.70 INR');
});

test('page choices support ranges, exclusions, odd/even and reject empty or invalid selections',()=>{
 const {api}=portal();const select=(mode,value)=>Array.from(api.pagesForMode(8,mode,value));
 assert.deepEqual(select('all'),[1,2,3,4,5,6,7,8]);
 assert.deepEqual(select('selected','1,3,5-8'),[1,3,5,6,7,8]);
 assert.deepEqual(select('selected','3,1,1-3'),[1,2,3]);
 assert.deepEqual(select('skip','1,3,5-8'),[2,4]);
 assert.deepEqual(select('odd'),[1,3,5,7]);assert.deepEqual(select('even'),[2,4,6,8]);
 for(const value of ['', '0','9','4-2','one','1,'])assert.throws(()=>select('selected',value));
 assert.throws(()=>select('skip','1-8'),/at least one/);
});


test('payment choice opens customer details without creating an order',()=>{
 let requests=0;const {api,element}=portal(async()=>{requests++;return new Response('{}');});
 element('#details-section').hidden=true;
 api.beginCheckout('cash_on_counter');assert.equal(element('#details-section').hidden,true);
 api.setReady(true);element('#quote-total').textContent='12.00 INR';
 api.beginCheckout('cash_on_counter');
 assert.equal(element('#config-section').hidden,true);
 assert.equal(element('#details-section').hidden,false);
 assert.equal(element('#confirm-order-btn').textContent,'Confirm cash order');
 assert.equal(api.state.paymentMethod,'cash_on_counter');
 assert.equal(element('#order-form input:not([disabled]), #order-form textarea:not([disabled])').focused,true);
 api.beginCheckout('razorpay');assert.equal(api.state.paymentMethod,'cash_on_counter');
 api.state.razorpayReady=true;api.beginCheckout('razorpay');
 assert.equal(api.state.paymentMethod,'razorpay');
 assert.equal(element('#confirm-order-btn').textContent,'Continue to payment');
 assert.match(element('#details-summary').textContent,/12\.00 INR.*Pay Online/);
 assert.match(element('#details-summary').textContent,/return to this page for your 4-digit pickup code/);
 assert.equal(requests,0);
});

test('disabled details immediately submits chosen payment without opening the form',()=>{
 const {api,element}=portal();api.setReady(true);api.state.razorpayReady=true;
 api.state.customerDetails={enabled:false,fields:{customerName:'required',customerPhone:'required',customerEmail:'optional',customerNotes:'optional'}};
 element('#details-section').hidden=true;
 const methods=[];api.mockSubmit((_form,method)=>methods.push(method));
 api.beginCheckout('cash_on_counter');api.beginCheckout('razorpay');
 assert.deepEqual(methods,['cash_on_counter','razorpay']);
 assert.equal(element('#details-section').hidden,true);
 assert.equal(element('#order-form [name="customerName"]').required,false);
 assert.equal(element('#order-form [name="customerName"]').disabled,true);
});
test('field policy hides omitted fields and marks only selected fields required',()=>{
 const {api,element}=portal();
 api.state.customerDetails={enabled:true,fields:{customerName:'hidden',customerPhone:'optional',customerEmail:'required',customerNotes:'hidden'}};
 api.configureCustomerDetails();
 assert.equal(element('#order-form [name="customerName"]').parentElement.hidden,true);
 assert.equal(element('#order-form [name="customerPhone"]').required,false);
 assert.equal(element('#order-form [name="customerEmail"]').required,true);
 assert.equal(element('#order-form [name="customerEmail"]').disabled,false);
});

test('upload reports actual progress and resolves the HTTP response', async()=>{
 let xhr;
 class FakeXHR {constructor(){xhr=this;this.upload={};}open(){}setRequestHeader(){}send(){}getResponseHeader(){return 'application/json';}}
 const {api,element}=portal(undefined,{XMLHttpRequest:FakeXHR});
 const pending=api.uploadWithProgress('/upload',{},'token');
 xhr.upload.onprogress({lengthComputable:true,loaded:25,total:100});
 assert.equal(element('#upload-percent').textContent,'25% uploaded');
 xhr.upload.onprogress({lengthComputable:true,loaded:100,total:100});
 assert.match(element('#upload-percent').textContent,/Preparing/);
 xhr.status=200;xhr.responseText='{"files":[]}';xhr.onload();
 assert.deepEqual(await (await pending).json(),{files:[]});
 assert.equal(xhr.timeout,600000);
});
test('upload rejects timeout, network errors and status zero instead of hanging', async()=>{
 for(const failure of ['timeout','network','zero']) {
  let xhr;
  class FakeXHR {constructor(){xhr=this;this.upload={};}open(){}setRequestHeader(){}send(){}}
  const {api}=portal(undefined,{XMLHttpRequest:FakeXHR});
  const pending=api.uploadWithProgress('/upload',{},'token');
  if(failure==='timeout')xhr.ontimeout();else if(failure==='network')xhr.onerror();else{xhr.status=0;xhr.onload();}
  await assert.rejects(pending,/Upload (timed out|interrupted)/);
 }
});

test('submitted draft still shows receipt when browser storage is full',async()=>{
 let confirmed,removed=false;
 const draft={orderId:'draft',uploadToken:'token',files:[]};
 const result={orderId:'draft',submitted:true,shareToken:'token',paymentMethod:'cash_on_counter',files:[]};
 const {api}=portal(async()=>new Response(JSON.stringify(result)),{sessionStorage:{getItem:()=>JSON.stringify(draft),setItem(){throw new Error('Quota exceeded')},removeItem(){removed=true}}});
 api.prepareRecovery(value=>{confirmed=value});await api.initializePortal();
 assert.equal(confirmed.orderId,'draft');assert.equal(removed,false);
});
test('receipt persistence saves the receipt before deleting its recovery draft',()=>{
 const calls=[];
 const {api}=portal(undefined,{sessionStorage:{setItem(key){calls.push(key)},removeItem(key){calls.push(key)}}});
 assert.equal(api.persistOrderReceipt({orderId:'saved'}),true);
 assert.deepEqual(calls,['pc-last-order','pc-draft']);
});


test('receipt only says Print Done once printing is done, not merely paid or dispatched',()=>{
 const {api,element}=portal();
 for(const status of ['pending_payment','paid','dispatched']){
  api.handleStatusUpdate({status,printStatus:'pending'});
  assert.equal(element('#receipt-title').textContent,'Order received');
 }
 api.handleStatusUpdate({status:'dispatched',printStatus:'done'});
 assert.equal(element('#receipt-title').textContent,'Print Done');
 assert.match(element('#order-summary').textContent,/print is completed/);
 api.handleStatusUpdate({status:'dispatched',printStatus:'failed'});
 assert.equal(element('#receipt-title').textContent,'Order received');
});
test('phone cache requires consent, expires, and does not bypass disabled customer details',()=>{
 const values=new Map();const localStorage={getItem:k=>values.get(k),setItem:(k,v)=>values.set(k,v),removeItem:k=>values.delete(k)};
 const {api,element}=portal(undefined,{localStorage});
 api.rememberCustomerPhone('9876543210');assert.equal(values.size,0);
 element('#remember-phone').checked=true;api.rememberCustomerPhone('9876543210');
 api.restoreCustomerPhone();assert.equal(element('#order-form [name="customerPhone"]').value,'9876543210');
 element('#order-form [name="customerPhone"]').disabled=true;
 api.restoreCustomerPhone();assert.equal(element('#remember-phone-label').hidden,true);
 element('#order-form [name="customerPhone"]').disabled=false;
 values.set('pc-customer-phone',JSON.stringify({phone:'old',expires:1}));
 api.restoreCustomerPhone();assert.equal(values.size,0);
});

test('receipt recovery falls back to compact receipt without losing OTP authorization',()=>{
 let saved;const {api}=portal(undefined,{sessionStorage:{setItem(k,v){if(JSON.parse(v).files.length)throw new Error('quota');saved=JSON.parse(v)},removeItem(){}}});
 assert.equal(api.persistOrderReceipt({orderId:'order',shareToken:'private-token',paymentMethod:'razorpay',files:[{large:'preview'}]}),true);
 assert.equal(saved.shareToken,'private-token');assert.equal(saved.orderId,'order');assert.equal(saved.paymentMethod,'razorpay');
});
test('receipt persistence reports unavailable storage before payment navigation',()=>{
 const {api}=portal(undefined,{sessionStorage:{setItem(){throw new Error('disabled')},removeItem(){throw new Error('must not delete draft')}}});
 assert.equal(api.persistOrderReceipt({orderId:'order',shareToken:'private-token'}),false);
});

test('payment retry does not navigate or call gateway when receipt cannot survive return',async()=>{
 let calls=0;
 const {api,element}=portal(async()=>{calls++;throw new Error('unexpected payment request')},{sessionStorage:{setItem(){throw new Error('disabled')},removeItem(){}}});
 api.state.orderId='order';api.state.shareToken='private';api.state.paymentMethod='razorpay';
 await api.retryPayment();
 assert.equal(calls,0);
 assert.match(element('#payment-error').textContent,/Allow site storage/);
 assert.equal(element('#payment-error').hidden,false);
});


test('multiple large files use separate requests and preserve earlier uploads on failure',async()=>{
 const stored=new Map();const sent=[];
 class UploadForm {constructor(){this.items=[]}append(...args){this.items.push(args)}}
 const {api}=portal(undefined,{FormData:UploadForm,sessionStorage:{setItem:(k,v)=>stored.set(k,v)}});
 api.state.orderId='draft';api.state.uploadToken='token';
 api.prepareUpload(async(url,form,token)=>{sent.push(form.items);assert.equal(token,'token');assert.equal(form.items.length,1);if(sent.length===2)throw Error('connection lost');return new Response(JSON.stringify({files:[{documentId:'first',originalFilename:'one.pdf'}],uploadToken:'token'}),{status:201})});
 await api.uploadFiles([{name:'one.pdf',type:'application/pdf',size:50*1024*1024},{name:'two.pdf',type:'application/pdf',size:50*1024*1024}]);
 assert.equal(sent.length,2);assert.equal(api.state.uploadedFiles[0].documentId,'first');assert.equal(JSON.parse(stored.get('pc-draft')).files[0].documentId,'first');
});

test('cash checkout stays visible without Razorpay and unavailable methods explain the blocker',()=>{
 const {api,element}=portal();api.state.cashOnCounterReady=true;api.state.razorpayReady=false;api.setReady(true);api.updateCheckoutButtons();
 assert.equal(element('#place-order-btn').hidden,false);assert.equal(element('#place-order-btn').disabled,false);
 assert.equal(element('#pay-online-btn').hidden,true);assert.equal(element('#payment-notification-options').hidden,true);
 api.state.cashOnCounterReady=false;api.updateCheckoutButtons();assert.equal(element('#payment-unavailable').hidden,false);
});

test('unpaid cash pickup is visible without claiming payment was confirmed',()=>{
 const {api,element}=portal();api.renderPickup({state:'active',code:'0012',preparation:'pending',paymentPending:true});
 assert.equal(element('#pickup-code').textContent,'0012');assert.match(element('#pickup-message').textContent,/Payment is still pending/);assert.doesNotMatch(element('#pickup-message').textContent,/Payment confirmed/);
 api.renderPickup({state:'automatic',paymentPending:true});assert.equal(element('#pickup-code').textContent,'');assert.match(element('#pickup-message').textContent,/Payment is still pending/);
});
