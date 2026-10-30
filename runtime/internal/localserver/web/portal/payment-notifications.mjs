/* Browser push is optional: this module never authorizes payment or printing. */
let registration, subscription, pending;
const status=text=>{const el=document.getElementById('notification-status');if(el)el.textContent=text;};
const selected=()=>document.getElementById('payment-notifications')?.checked===true;
const supported=()=>globalThis.isSecureContext && 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window;
export function prepareNotifications(){
 if(!selected())return Promise.resolve();
 if(!supported()){status('Notifications are unavailable in this browser. Return here after payment for your code.');return Promise.resolve();}
 // Request permission directly within the customer's payment-button gesture.
 let permission;
 try{permission=Notification.permission==='default'?Notification.requestPermission():Promise.resolve(Notification.permission);}catch{status('Notifications could not be enabled. Return here after payment for your pickup code.');return Promise.resolve();}
 pending=(async()=>{
  if(await permission!=='granted'){status('Notifications are not allowed. Your pickup code will still appear on this website.');return;}
  registration=await navigator.serviceWorker.register('/portal/payment-sw.js',{scope:'/portal/'});
  if(!registration.active)await navigator.serviceWorker.ready;
  const response=await fetch('/api/v1/portal/notifications/key',{cache:'no-store'});if(!response.ok)throw new Error('key');
  const {publicKey}=await response.json();const raw=atob(publicKey.replace(/-/g,'+').replace(/_/g,'/'));
  subscription=await registration.pushManager.getSubscription()||await registration.pushManager.subscribe({userVisibleOnly:true,applicationServerKey:Uint8Array.from(raw,c=>c.charCodeAt(0))});
  status('Notifications allowed. Payment alerts will be enabled for this order.');
 })().catch(()=>status('Notifications could not be enabled. Return here after payment for your pickup code.'));
 return pending;
}
export async function subscribeOrder(orderId,token){
 if(!selected())return;
 // Do not hold payment hostage to an unanswered permission prompt/network failure.
 await Promise.race([pending||Promise.resolve(),new Promise(r=>setTimeout(r,3000))]);
 if(!selected())return;
 if(!subscription){status('Notification setup is incomplete. Return here after payment to see your code.');return;}
 const sub=subscription.toJSON();const controller=new AbortController();const timeout=setTimeout(()=>controller.abort(),5000);
 try{const response=await fetch(`/api/v1/portal/orders/${encodeURIComponent(orderId)}/notifications`,{method:'PUT',headers:{'Content-Type':'application/json','X-Order-Token':token},body:JSON.stringify({endpoint:sub.endpoint,keys:sub.keys}),signal:controller.signal});if(!response.ok)throw new Error('subscription');status('Payment notification enabled.');}
 catch{status('Notification setup failed. Return here after payment for your code.');}finally{clearTimeout(timeout);}
}
export function notificationReceipt(hash){
 const args=new URLSearchParams(hash.replace(/^#/,''));const orderId=args.get('order'),shareToken=args.get('token');
 if(!orderId||!shareToken||orderId.length>128||shareToken.length>256)return null;
 return {orderId,shareToken,paymentMethod:'razorpay',files:[]};
}
