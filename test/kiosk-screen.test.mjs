import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import fs from 'node:fs';
const source=fs.readFileSync(new URL('../runtime/internal/kiosk/ui/kiosk.js',import.meta.url),'utf8');
function screen(fetch){
 const ids=new Map(), timers=new Map();let nextTimer=0;
 const element=id=>{if(!ids.has(id))ids.set(id,{value:'',hidden:true,addEventListener(type,fn){this[type]=fn;},click(){if(!this.disabled)this.onclick?.();}});return ids.get(id);};
 const digits=Array.from({length:10},(_,n)=>({...element('digit'+n),dataset:{digit:String(n)}}));
 const document={getElementById:element,querySelectorAll:s=>s==='[data-digit]'?digits:digits.concat([element('clear'),element('back')]),addEventListener(){}};
 vm.runInNewContext(source,{document,fetch,AbortController,HTMLInputElement:class{},Date,setTimeout:(fn,ms)=>{timers.set(++nextTimer,{fn,ms});return nextTimer;},clearTimeout:id=>timers.delete(id),setInterval(){}});
 return {element,async tick(ms){const item=[...timers].find(([,v])=>v.ms===ms);assert.ok(item,"timer exists");timers.delete(item[0]);await item[1].fn();},enter(code){for(const n of code)digits[Number(n)].click();}};
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
 ui.enter('0007');await ui.element('release').onclick();await ui.tick(500);
 assert.match(ui.element('status').textContent,/Print Done/);assert.equal(claims,1);assert.equal(progress,1);
 await ui.tick(15000);ui.enter('1234');assert.equal(ui.element('release').disabled,false);
});
