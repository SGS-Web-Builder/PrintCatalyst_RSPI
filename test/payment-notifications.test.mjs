import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

const moduleURL=new URL('../runtime/internal/localserver/web/portal/payment-notifications.mjs',import.meta.url);
async function setup(checked=true){
 const box={checked},message={textContent:''};
 globalThis.document={getElementById:id=>id==='payment-notifications'?box:message};
 globalThis.isSecureContext=true;
 globalThis.window={PushManager:function(){},Notification:function(){}};
 let writes=0;
 Object.defineProperty(globalThis,'navigator',{configurable:true,value:{serviceWorker:{register:async()=>({active:true,pushManager:{getSubscription:async()=>({toJSON:()=>({endpoint:'https://fcm.googleapis.com/fcm/send/test',keys:{auth:'a',p256dh:'b'}})})}})}}});
 globalThis.Notification={permission:'granted'};
 globalThis.fetch=async(url,options)=>{if(options?.method==='PUT')writes++;return {ok:true,json:async()=>({publicKey:'YQ'})}};
 const mod=await import(moduleURL.href+'?test='+Math.random());return {mod,box,message,writes:()=>writes};
}
test('notification choice defaults on in portal markup',()=>{assert.match(fs.readFileSync(new URL('../runtime/internal/localserver/web/portal/index.html',import.meta.url),'utf8'),/id="payment-notifications" checked/)});
test('notification subscription binds the order only after permission',async()=>{const s=await setup();await s.mod.prepareNotifications();await s.mod.subscribeOrder('order','token');assert.equal(s.writes(),1)});
test('denied, unsupported, and unchecked notifications do not block checkout',async()=>{
 for(const mode of ['denied','unsupported','unchecked','throws']){const s=await setup(mode!=='unchecked');if(mode==='denied')Notification.permission='denied';if(mode==='unsupported')globalThis.isSecureContext=false;if(mode==='throws'){Notification.permission='default';Notification.requestPermission=()=>{throw Error('blocked')}}await s.mod.prepareNotifications();await s.mod.subscribeOrder('order','token');assert.equal(s.writes(),0,mode)}
});
test('unchecking after setup prevents registration for the order',async()=>{const s=await setup();await s.mod.prepareNotifications();s.box.checked=false;await s.mod.subscribeOrder('order','token');assert.equal(s.writes(),0)});
test('notification receipt recovers token from fragment only',async()=>{const s=await setup();assert.deepEqual(s.mod.notificationReceipt('#order=abc&token=secret'),{orderId:'abc',shareToken:'secret',paymentMethod:'razorpay',files:[]});assert.equal(s.mod.notificationReceipt('#order=abc'),null)});
test('worker notification hides OTP and opens same-origin recovery',async()=>{
 const listeners={};let notice,target,pending;
 const self={addEventListener:(name,fn)=>listeners[name]=fn,registration:{showNotification:async(title,options)=>notice={title,...options}},location:{origin:'https://shop.example'},clients:{openWindow:async url=>target=url}};
 vm.runInNewContext(fs.readFileSync(new URL('../runtime/internal/localserver/web/portal/payment-sw.js',import.meta.url),'utf8'),{self,URL,URLSearchParams,Date});
 const data={orderId:'abc',token:'secret',code:'1234',expires:Math.floor(Date.now()/1000)+600};
 listeners.push({data:{json:()=>data},waitUntil:p=>pending=p});await pending;assert.equal(notice.body,'Tap to view your pickup code.');assert.equal(notice.data.code,undefined);
 listeners.notificationclick({notification:{data:notice.data,close(){}},waitUntil:p=>pending=p});await pending;assert.equal(target,'https://shop.example/portal/#order=abc&token=secret');
 notice=null;listeners.push({data:{json:()=>({...data,expires:1})},waitUntil:p=>pending=p});await pending;assert.equal(notice,null);
});
