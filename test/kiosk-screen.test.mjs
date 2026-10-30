import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import fs from 'node:fs';
const source=fs.readFileSync(new URL('../runtime/internal/kiosk/ui/kiosk.js',import.meta.url),'utf8');
function screen(fetch,extra={}){
 const ids=new Map(), timers=new Map();let nextTimer=0;
 const element=id=>{if(!ids.has(id))ids.set(id,{value:'',hidden:true,addEventListener(type,fn){this[type]=fn;},click(){if(!this.disabled)this.onclick?.();}});return ids.get(id);};
 const digits=Array.from({length:10},(_,n)=>({...element('digit'+n),dataset:{digit:String(n)}}));
 const handlers={};
 const document={getElementById:element,querySelectorAll:s=>s==='[data-digit]'?digits:digits.concat([element('clear'),element('back')]),addEventListener(type,fn){handlers[type]=fn;}};
 vm.runInNewContext(source,{document,fetch,AbortController,HTMLInputElement:class{},Date,setTimeout:(fn,ms)=>{timers.set(++nextTimer,{fn,ms});return nextTimer;},clearTimeout:id=>timers.delete(id),setInterval(){},...extra});
 return {element,key(event){handlers.keydown(event);},async tick(ms){const item=[...timers].find(([,v])=>v.ms===ms);assert.ok(item,"timer exists");timers.delete(item[0]);await item[1].fn();},enter(code){for(const n of code)digits[Number(n)].click();}};
}
test('keypad preserves zeroes, blocks double submit and clears successful input',async()=>{
 let resolve,calls=0,body;
 const ui=screen((url,options)=>{calls++;body=JSON.parse(options.body);return new Promise(r=>resolve=r);});
 ui.enter('0007');assert.equal(ui.element('digits').textContent,'0 0 0 7');
 const pending=ui.element('release').onclick();await ui.element('release').onclick();assert.equal(calls,1);assert.equal(body.code,'0007');
 resolve({status:202,json:async()=>({state:'accepted'})});await pending;
 assert.equal(ui.element('digits').textContent,'— — — —');assert.equal(ui.element('release').disabled,true);assert.match(ui.element('status').textContent,/queue/);
 ui.element('next').onclick();ui.enter('1234');assert.equal(ui.element('release').disabled,false);
});
test('lost claim response stops retries and requests attendant review',async()=>{
 let calls=0;const ui=screen(async()=>{calls++;throw Error('offline');});ui.enter('1234');await ui.element('release').onclick();await ui.element('release').onclick();
 assert.equal(calls,1);assert.match(ui.element('status').textContent,/may have been released/);assert.equal(ui.element('release').disabled,true);
});

test('receipt polls completion without submitting another print',async()=>{
 let claims=0,progress=0;
 const ui=screen(async(url)=>{if(url.endsWith('/claim')){claims++;return {status:202,ok:true,json:async()=>({state:'accepted',receipt:'a'.repeat(64)})};}progress++;return {status:200,ok:true,json:async()=>({state:'done'})};});
 ui.enter('0007');await ui.element('release').onclick();await ui.tick(0);
 assert.match(ui.element('status').textContent,/Print Done/);assert.equal(claims,1);assert.equal(progress,1);
 await ui.tick(5000);ui.enter('1234');assert.equal(ui.element('release').disabled,false);
});

test('preparing OTP waits through receipt polling then shows printing without re-entry',async()=>{
 let claims=0,progress=0;
 const ui=screen(async url=>{
  if(url.endsWith('/claim')){claims++;return {status:202,ok:true,json:async()=>({state:'preparing',receipt:'b'.repeat(64)})};}
  progress++;return {status:200,ok:true,json:async()=>({state:progress===1?'preparing':'printing'})};
 });
 ui.enter('0007');await ui.element('release').onclick();
 assert.match(ui.element('status').textContent,/Processing your file/);
 assert.equal(ui.element('release').disabled,true);
 await ui.tick(0);assert.match(ui.element('status').textContent,/Processing your file/);
 await ui.tick(1500);assert.match(ui.element('status').textContent,/Printing/);
 assert.equal(claims,1);assert.equal(progress,2);
});

test('streamed completion paints green and skips visible countdown when disabled',async()=>{
 let claims=0;const ui=screen(async(url)=>{
 if(url.endsWith('/claim')){claims++;return {status:202,ok:true,json:async()=>({state:'accepted',receipt:'c'.repeat(64)})};}
 if(url.endsWith('/events'))return {ok:true,body:new ReadableStream({start(c){c.enqueue(new TextEncoder().encode('data: {"state":"printing","countdownEnabled":false}\n\ndata: {"state":"done","countdownEnabled":false}\n\n'));c.close();}})};
 return {ok:true,status:200,json:async()=>({state:'dismissed'})};
 },{ReadableStream,TextDecoder});
 ui.enter('0007');await ui.element('release').onclick();await ui.tick(0);await new Promise(setImmediate);
 assert.equal(ui.element('progress-ring').className,'progress-ring success');assert.equal(ui.element('success-countdown').hidden,true);assert.equal(claims,1);
 await ui.tick(1500);assert.equal(ui.element('otp-controls').hidden,false);
});
test('automatic mode hides keypad and cannot submit a hidden OTP',async()=>{
 let claims=0;
 const ui=screen(async url=>{if(url.endsWith('/claim'))claims++;return {ok:true,status:200,json:async()=>({otpEnabled:false,countdownEnabled:false})};});
 await ui.tick(1000);assert.equal(ui.element('otp-controls').hidden,true);ui.enter('1234');await ui.element('release').onclick();assert.equal(claims,0);assert.equal(ui.element('release').disabled,true);
});

test('fourth digit submits next event-loop turn without an artificial delay or duplicate claim',async()=>{
 let claims=0;const ui=screen(async()=>{claims++;return {status:202,ok:true,json:async()=>({state:'accepted'})};});
 ui.enter('0007');await ui.tick(0);await new Promise(setImmediate);
 assert.equal(claims,1);assert.equal(ui.element('release').disabled,true);
 await ui.element('release').onclick();assert.equal(claims,1);
});
test('pairing failure is visible before submission and prevents silent input',async()=>{
 let claims=0;const ui=screen(async url=>{if(url.endsWith('/claim'))claims++;return {status:403,ok:false,json:async()=>({state:'pairing_required'})};});
 await ui.tick(1000);assert.match(ui.element('screen-health').textContent,/not paired/);assert.equal(ui.element('pair-screen-link').hidden,false);
 ui.enter('0007');assert.equal(ui.element('release').disabled,true);assert.equal(claims,0);
});
test('Enter submits even when a keypad button has focus',async()=>{
 let claims=0;const ui=screen(async()=>{claims++;return {status:202,ok:true,json:async()=>({state:'accepted'})};});
 ui.enter('0007');let prevented=false;ui.key({key:'Enter',repeat:false,target:{closest:()=>({})},preventDefault(){prevented=true;}});
 await new Promise(setImmediate);assert.equal(prevented,true);assert.equal(claims,1);
});

 test('assistance keeps the order locked and continues checking instead of resetting OTP',async()=>{
 let progress=0;
 const ui=screen(async url=>url.endsWith('/claim')?{status:202,ok:true,json:async()=>({state:'accepted',receipt:'d'.repeat(64)})}:{status:200,ok:true,json:async()=>({state:++progress===1?'assistance':'printing'})});
 ui.enter('0007');await ui.element('release').onclick();await ui.tick(0);
 assert.equal(ui.element('release').disabled,true);
 await ui.tick(1000);await ui.tick(1000);assert.match(ui.element('status').textContent,/Printing/);
 assert.equal(ui.element('otp-controls').hidden,true);
 });
