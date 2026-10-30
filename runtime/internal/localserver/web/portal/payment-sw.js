/* No document caching: this worker only displays payment notifications. */
self.addEventListener('push',event=>{
 event.waitUntil((async()=>{
  let data;try{data=event.data.json();}catch{return;}
  if(!data.orderId||!data.token||data.expires*1000<=Date.now())return;
  await self.registration.showNotification('Payment received',{body:'Tap to view your pickup code.',tag:'payment-'+data.orderId,data:{orderId:data.orderId,token:data.token,expires:data.expires}});
 })());
});
self.addEventListener('notificationclick',event=>{
 event.notification.close();const d=event.notification.data;
 if(!d||d.expires*1000<=Date.now())return;
 const target=new URL('/portal/',self.location.origin);target.hash=new URLSearchParams({order:d.orderId,token:d.token}).toString();
 // Open a fresh page so recovery runs even when an old receipt tab exists.
 event.waitUntil(self.clients.openWindow(target.href));
});
