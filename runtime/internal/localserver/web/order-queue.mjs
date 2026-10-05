const tabs = [['pending','Pending'],['processing','Processing'],['printing','Printing'],['rejected','Rejected'],['failed','Print Failed'],['done','Done'],['all','All Orders']];
export function queueState(order) {
  if(order.status==='cancelled')return 'rejected';
  if(order.status==='completed')return 'done';
  const docs=[...(order.documents || []),...(order.separators || [])];
  if(docs.length && docs.every(d=>d.printState==='completed'))return 'done';
  if(order.status==='failed' || docs.some(d=>['failed','blocked'].includes(d.printState)))return 'failed';
  if(docs.some(d=>['waiting','submitting','processing','review'].includes(d.printState)) || order.printRequested)return 'processing';
  if(docs.some(d=>['printing','submitted'].includes(d.printState)))return 'printing';
  if(!docs.length && order.status==='dispatched')return 'printing';
  return 'pending';
}
export function selectedPages(doc) {
  try {const pages=JSON.parse(doc.pagesJSON || 'null');if(Array.isArray(pages))return pages;}catch{}
  return Array.from({length:Math.max(0,doc.pageEnd-doc.pageStart+1)},(_,i)=>doc.pageStart+i);
}
export function orderTotals(order) {
  return (order.documents || []).reduce((sum,d)=>{
    const pages=selectedPages(d).length,copies=d.copies || 1,n=d.pagesPerSheet || 1;
    sum.pages+=pages*copies;sum.sheets+=Math.ceil(pages/(n*(d.sides==='one-sided'?1:2)))*copies;return sum;
  },{pages:0,sheets:0});
}
export function matchesOrder(order,search,date,now=new Date()) {
  if(search && ![order.id,order.customerName,order.customerPhone,...(order.documents || []).map(d=>d.filename)].join(' ').toLowerCase().includes(search.toLowerCase()))return false;
  if(date==='all')return true;
  const start=new Date(now.getFullYear(),now.getMonth(),now.getDate());
  if(date==='week')start.setDate(start.getDate()-6);
  return order.createdAt*1000>=start.getTime();
}
const el=(tag,cls,text)=>{const e=document.createElement(tag);if(cls)e.className=cls;if(text!==undefined)e.textContent=text;return e;};
const button=(text,fn,cls='oq-button')=>{const b=el('button',cls,text);b.type='button';b.onclick=fn;return b;};
const money=(value,order)=>new Intl.NumberFormat(undefined,{style:'currency',currency:order.currency || 'INR',minimumFractionDigits:order.currencyMinorUnits ?? 2}).format(value/10**(order.currencyMinorUnits ?? 2));
const time=o=>new Date(o.createdAt*1000).toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'});
const paymentText=o=>o.status==='pending_payment'?'Payment pending':['paid','dispatched','completed'].includes(o.status)?'Payment recorded':'Check payment status';
const statusText={pending:'Awaiting release',processing:'Processing / checking output',printing:'Printing',rejected:'Rejected',failed:'Print failed',done:'Done'};
const pill=(text,kind)=>el('span','oq-pill '+kind,text);

export function createOrderQueue(client,getRole) {
  const root=document.getElementById('orders-panel');
  let orders=[],active=null,filter='pending',search='',date='today',busy=new Set(),loading=false,timer,snapshot;
  root.classList.add('order-workspace');
  root.innerHTML=`<div class="oq-top"><div class="oq-title"><span class="oq-clock" aria-hidden="true">◷</span><div><small>ORDER QUEUE</small><h1 id="oq-heading">Pending Orders</h1><p>Customer-configured documents awaiting merchant action</p></div></div><div id="oq-metrics"></div></div><div class="oq-toolbar"><nav id="oq-tabs" aria-label="Order status"></nav><div class="oq-filters"><label class="oq-search"><span aria-hidden="true">⌕</span><input id="oq-search" type="search" aria-label="Search orders" placeholder="Search order ID, name, phone or file…"></label><select id="oq-date" aria-label="Order date"><option value="today">Today</option><option value="week">Last 7 days</option><option value="all">All dates</option></select><button id="orders-refresh" type="button" class="oq-button">↻ Refresh</button><button id="oq-back" type="button" class="oq-button" hidden>← All orders</button></div><p id="orders-message" role="status" aria-live="polite"></p></div><div class="oq-body"><aside id="oq-rail" hidden></aside><div id="oq-main"></div></div>`;
  const $=id=>root.querySelector('#'+id);
  const announce=text=>{$('orders-message').textContent=text;};
  for(const nav of document.querySelectorAll('.nav-item')){nav.title=nav.textContent.trim();nav.setAttribute('aria-label',nav.title);}
  $('oq-search').oninput=e=>{search=e.target.value;render();};
  $('oq-date').onchange=e=>{date=e.target.value;render();};
  $('orders-refresh').onclick=()=>refresh();
  $('oq-back').onclick=()=>{active=null;render();};
  function metric(label,value){const box=el('div','oq-metric');box.append(el('strong','',value),el('small','',label));return box;}
  function actionButtons(o) {
    const group=el('div','oq-actions'),state=queueState(o),jobs=[...o.documents,...(o.separators||[])];
    if(getRole()==='owner' && o.kioskMode && o.preparationFailed && o.status==='paid'){const b=button('Retry document preparation',()=>retryPreparation(o),'oq-button oq-preview');b.disabled=busy.has(o.id);group.append(b);}
    if(getRole()==='owner' && o.kioskMode && o.pickupExpired && o.status==='paid'){
      const b=button('Replace expired pickup code',()=>reissue(o),'oq-button oq-preview');b.disabled=busy.has(o.id);group.append(b);
    }
    if(getRole()==='owner' && !o.kioskMode && state==='pending' && !o.documents.some(d=>d.printState)){
      const b=button(busy.has(o.id)?'Releasing…':'One-Click Print',()=>release(o), 'oq-button oq-release');b.disabled=busy.has(o.id);group.append(b);
    }
    if(getRole()==='owner' && ((state==='failed' && jobs.some(d=>d.printState==='failed')) || (state==='processing' && jobs.some(d=>d.printState==='submitting')))){
      const b=button('Review & retry',()=>release(o,true),'oq-button oq-release');b.disabled=busy.has(o.id);group.append(b);
    }
    if(getRole()==='owner' && state!=='done' && state!=='rejected' && jobs.length && jobs.every(d=>d.printState && !['waiting','failed','submitting'].includes(d.printState))){
      const done=button('Confirm printed · Mark done',()=>transition(o,'print_completed'),'oq-button oq-preview');done.disabled=busy.has(o.id);group.append(done);
    }
    return group;
  }
  async function retryPreparation(o){
    if(busy.has(o.id)||!window.confirm('Check printer configuration, renderer dependencies and the document first. Retry preparation? This does not release printing.'))return;
    busy.add(o.id);render();
    try{await client.request('POST',`/api/v1/owner/orders/${o.id}/preparation/retry`,{});await refresh();announce('Preparation queued. The customer still needs to enter their pickup code.');}
    catch(error){announce(error.message);}finally{busy.delete(o.id);render();}
  }
  async function reissue(o) {
    if(busy.has(o.id) || !window.confirm('Replace the expired pickup code? The customer must still enter the new code at this kiosk to print.'))return;
    busy.add(o.id);render();
    try{await client.request('POST',`/api/v1/owner/orders/${o.id}/pickup/reissue`,{});await refresh();announce('Pickup code replaced. Ask the customer to refresh their order receipt. Printing still requires entry at the kiosk.');}
    catch(error){announce(error.message);}finally{busy.delete(o.id);render();}
  }
  async function release(o,retry=false) {
    if(busy.has(o.id))return;
    if(retry && !window.confirm('Check the printer queue and output tray first. An interrupted submission may have printed. Retry only failed or interrupted documents and separator invoices?'))return;
    busy.add(o.id);render();
    try{await client.request('POST',`/api/v1/owner/orders/${o.id}/print`,{retry});announce('Print release accepted. Customer settings preserved. Payment status is unchanged.');await refresh();}
    catch(error){announce(error.message);}
    finally{busy.delete(o.id);render();}
  }
  async function transition(o,target) {
    if(busy.has(o.id))return;
    if(target==='print_completed' && !window.confirm('Confirm that all documents and any separator invoices physically printed. This does not record payment.'))return;
    if(target==='cancelled' && !window.confirm('Reject this order? Jobs already sent to the printer must be cancelled in the printer queue.'))return;
    busy.add(o.id);render();
    try{await client.request('PUT',`/api/v1/owner/orders/${o.id}/status`,{status:target});await refresh();announce(target==='print_completed'?'Print marked done. Payment status is unchanged.':'Order rejected.');}
    catch(error){announce(error.message);}finally{busy.delete(o.id);render();}
  }
  function customer(o) {
    const box=el('div','oq-customer');box.append(el('span','oq-avatar','O#'));
    const text=el('div');text.append(el('strong','',o.customerName || 'Customer'),el('small','',time(o)),el('small','oq-id','#'+o.id.slice(0,8)),el('small','',o.customerPhone));box.append(text);return box;
  }
  function documentNames(o) {
    const box=el('div','oq-files');for(const d of o.documents){const line=el('div');line.append(pill(d.mime==='application/pdf'?'PDF':'IMG','file'),el('span','',d.filename),el('small','',selectedPages(d).length+'p'));box.append(line);}return box;
  }
  function open(o){active=o.id;render();}
  function render() {
    const filtered=orders.filter(o=>matchesOrder(o,search,date));
    const counts={};for(const o of filtered){const key=queueState(o);counts[key]=(counts[key]||0)+1;}
    $('oq-tabs').replaceChildren();for(const [key,label] of tabs){const b=button(`${label}  ${key==='all'?filtered.length:counts[key]||0}`,()=>{filter=key;active=null;render();},'oq-tab');b.setAttribute('aria-pressed',String(filter===key));$('oq-tabs').append(b);}
    $('oq-heading').textContent=(tabs.find(t=>t[0]===filter)?.[1] || 'All')+(filter==='all'?'':' Orders');
    const visible=filtered.filter(o=>filter==='all' || queueState(o)===filter);
    $('oq-metrics').replaceChildren(metric('ORDERS',String(visible.length)),metric('PAGES',String(visible.reduce((n,o)=>n+orderTotals(o).pages,0))));
    const currencies=new Map();for(const o of visible){if(!currencies.has(o.currency))currencies.set(o.currency,0);if(['paid','dispatched','completed'].includes(o.status))currencies.set(o.currency,currencies.get(o.currency)+o.totalMinor);}
    for(const [currency,total] of currencies){const o=visible.find(o=>o.currency===currency);$('oq-metrics').append(metric('PAID TOTAL',money(total,o)));}
    root.classList.toggle('oq-detail-mode',Boolean(active));$('oq-back').hidden=!active;$('oq-rail').hidden=!active;
    const main=$('oq-main'),scroll=main.scrollTop;main.replaceChildren();
    if(active){
      const order=orders.find(o=>o.id===active);$('oq-rail').replaceChildren(el('h2','','Order Queue'),el('p','oq-muted',`${filtered.length} orders`));
      for(const o of filtered){const item=button('',()=>open(o),'oq-rail-item'+(o.id===active?' selected':''));item.append(customer(o),pill(statusText[queueState(o)],queueState(o)),el('strong','oq-price',money(o.totalMinor,o)));$('oq-rail').append(item);}
      if(order)renderDetail(order,main);else main.append(el('p','oq-empty','This order is no longer available.'));
      main.scrollTop=scroll;return;
    }
    if(!visible.length){main.append(el('div','oq-empty','No orders match this view. New customer orders appear here automatically.'));return;}
    const table=el('table','oq-table'),head=el('thead'),header=el('tr');for(const name of ['CUSTOMER & CONTACT','DOCUMENTS','STATUS','PRICE','ACTIONS'])header.append(el('th','',name));head.append(header);table.append(head);
    const body=el('tbody');for(const o of visible){const tr=el('tr');const cells=Array.from({length:5},()=>el('td'));cells[0].append(customer(o));cells[1].append(documentNames(o));cells[2].append(pill(statusText[queueState(o)],queueState(o)),pill(paymentText(o),o.status==='pending_payment'?'payment':''));cells[3].append(el('strong','oq-price',money(o.totalMinor,o)),el('small','',`${orderTotals(o).sheets} sheets · ${orderTotals(o).pages} pages`));const actions=actionButtons(o);actions.append(button('View',()=>open(o),'oq-button oq-primary'));cells[4].append(actions);tr.append(...cells);body.append(tr);}table.append(body);main.append(table);
  }
  function renderDetail(o,main){
    const totals=orderTotals(o),heading=el('div','oq-detail-heading');heading.append(customer(o),pill(statusText[queueState(o)],queueState(o)),pill(paymentText(o),o.status==='pending_payment'?'payment':''));main.append(heading);
    const stats=el('div','oq-stats');stats.append(metric('DOCUMENTS',String(o.documents.length)),metric('PAGES',String(totals.pages)),metric('SHEETS',String(totals.sheets)));main.append(stats);
    for(const d of o.documents){
      const card=el('article','oq-document');const header=el('div','oq-document-header'),name=el('div');name.append(pill(d.mime==='application/pdf'?'PDF':'IMG','file'),el('h3','',d.filename),el('small','',`${(d.sizeBytes/1024).toFixed(1)} KB · ${d.pageCount} source pages`));header.append(name,el('strong','oq-price',money(d.totalMinor,o)));card.append(header);
      const tools=el('div','oq-preview-actions');const preview=button(d.purged?'File deleted':'◉ Preview',()=>window.open(`/api/v1/owner/orders/${o.id}/documents/${d.id}`,'_blank','noopener'),'oq-button oq-preview');preview.disabled=d.purged;tools.append(preview,el('small','oq-muted',`${orderTotals({documents:[d]}).sheets} sheets × ${money(d.unitPriceMinor,o)} / sheet · saved order rate`));card.append(tools);
      const config=el('div','oq-config');config.append(el('small','oq-eyebrow','CUSTOMER PRINT SETTINGS'));
      const grid=el('div','oq-config-grid');for(const [label,value] of [['Colour',d.colourMode==='colour'?'Colour':'Black & White'],['Print style',d.sides==='one-sided'?'Single-Sided':d.sides==='two-sided-long-edge'?'Duplex · long edge':'Duplex · short edge'],['Paper',d.paperSize],['Orientation',d.orientation || 'Auto'],['Copies',String(d.copies)]]){const field=el('div','oq-setting');field.append(el('small','',label),el('strong','',value));grid.append(field);}config.append(grid);
      const advanced=el('div','oq-advanced');advanced.append(el('small','oq-eyebrow','ADVANCED CONFIGURATION'),el('p','',`Pages to print: ${selectedPages(d).join(', ')}`),el('p','',`Document pages per side: ${d.pagesPerSheet || 1} · Scaling: Fit to page`));config.append(advanced);card.append(config);
      if(d.printError)card.append(el('p','oq-error',d.printError));main.append(card);
    }
    for(const invoice of o.separators||[]){const card=el('article','oq-document');card.append(el('h3','','Invoice separator'),el('p','',`${invoice.queue} · ${invoice.paper} · ${invoice.tray}`),el('p','',`Invoice status: ${invoice.printState}`));if(invoice.printError)card.append(el('p','oq-error',invoice.printError));main.append(card);}
    const footer=el('div','oq-detail-footer');const total=el('div');total.append(el('small','oq-eyebrow','TOTAL PRICE'),el('strong','oq-grand-total',money(o.totalMinor,o)),el('small','',`${totals.pages} pages · ${o.documents.length} source files`));footer.append(total);
    const actions=actionButtons(o);
    if(getRole()==='owner'){
      if(['pending_payment','paid','dispatched'].includes(o.status)){const reject=button('✕ Reject',()=>transition(o,'cancelled'),'oq-button oq-danger');reject.disabled=busy.has(o.id);actions.append(reject);}
    }
    footer.append(actions);main.append(footer,el('p','oq-footnote','One-click release uses the customer’s saved settings. Status follows Windows every second. If Windows removes a job without reporting completion, confirm the output and mark done. Payment is tracked separately.'));
  }
  async function refresh(){
    if(loading)return;loading=true;
    try{const data=await client.request('GET','/api/v1/owner/orders');const next=(data.orders || []).filter(o=>(o.documents || []).length);const signature=JSON.stringify(next);if(signature!==snapshot){snapshot=signature;orders=next;render();}}
    catch(error){announce(error.message);}finally{loading=false;}
    clearTimeout(timer);timer=setTimeout(()=>{if(!root.hidden && !document.getElementById('app-shell').hidden)void refresh();else timer=setTimeout(()=>refreshIfVisible(),750);},750);
  }
  function refreshIfVisible(){if(!root.hidden && !document.getElementById('app-shell').hidden)void refresh();else timer=setTimeout(refreshIfVisible,750);}
  return {refresh};
}
