import {createRemoteOperations} from './remote-operations.mjs';
import {createPricingGrid} from './pricing-grid.mjs';
import {renderPaperStock} from './paper-stock.mjs';
import {matchesMonitorDate, localDateValue} from './monitor-filters.mjs';
import {queueState} from './order-queue.mjs';
const $=id=>document.getElementById(id);
let csrf='',orders=[],timer,busy=false,signedIn=false,generation=0,orderSnapshot='';
async function request(path,body){const response=await fetch('/api/v1/remote/'+path,{method:body?'POST':'GET',credentials:'same-origin',cache:'no-store',signal:AbortSignal.timeout(20000),headers:body?{'Content-Type':'application/json','X-CSRF-Token':csrf}:{},body:body?JSON.stringify(body):undefined});if(!response.ok){const error=new Error(await response.text());error.status=response.status;throw error;}return response.json();}
function node(tag,text,cls){const n=document.createElement(tag);n.textContent=text;if(cls)n.className=cls;return n;}
function showLogin(){generation++;signedIn=false;clearTimeout(timer);orders=[];orderSnapshot='';$('orders').replaceChildren();$('workspace').hidden=true;$('pricing-panel').hidden=true;operations.reset();$('logout').hidden=true;$('login').hidden=false;}
function render(){const host=$('orders');host.replaceChildren();const term=$('search').value.toLowerCase();for(const order of orders){if(!matchesMonitorDate(order.createdAt,$('period').value,$('specific-date').value))continue;const status=queueState(order);if($('filter').value!=='all'&&$('filter').value!==status)continue;if(![order.id,order.customerName,order.customerPhone].join(' ').toLowerCase().includes(term))continue;const card=node('article',''),row=node('div','','row');row.append(node('h2',order.customerName||'Customer'),node('span',status[0].toUpperCase()+status.slice(1),'badge '+status));card.append(row,node('p',new Date(order.createdAt*1000).toLocaleString()+' · #'+order.id.slice(0,8)),node('p',order.customerPhone||''));const money=new Intl.NumberFormat(undefined,{style:'currency',currency:order.currency}).format(order.totalMinor/10**order.currencyMinorUnits);card.append(node('strong',money),node('p',order.status==='pending_payment'?'Payment pending':['paid','dispatched','completed'].includes(order.status)?'Payment recorded':'Payment/order status: '+order.status));for(const d of order.documents)card.append(node('p',d.filename+(d.purged?' · File deleted':'')+(d.printError?' · '+d.printError:'')));host.append(card);}$('result-count').textContent=host.children.length+' matching '+(host.children.length===1?'order':'orders');if(!host.children.length)host.append(node('p','No matching orders.'));}
async function refresh(){if(busy)return;busy=true;const current=generation;try{const data=await request('orders');if(current!==generation)return;csrf=data.csrfToken;if(data.paperStockUnavailable)$('monitor-paper-stock').textContent='Paper stock is temporarily unavailable.';else if(data.paperStock)renderPaperStock($('monitor-paper-stock'),data.paperStock);const snapshot=JSON.stringify(data.orders);const changed=snapshot!==orderSnapshot;orders=data.orders;orderSnapshot=snapshot;renderOverview();const firstSignIn=!signedIn;signedIn=true;if(firstSignIn&&location.hash==='#pricing')void pricing.refresh();$('business').textContent=data.businessName;document.title=data.businessName+' · Kiosk dashboard';$('login').hidden=true;$('workspace').hidden=false;showPage(false);$('logout').hidden=false;$('message').textContent='';$('updated').textContent='Updated '+new Date().toLocaleTimeString();if(changed)render();void operations.refresh();}catch(error){if(current!==generation)return;if(error.status===401)showLogin();$('message').textContent=error.status===401?'Sign in to manage your kiosk.':error.message+' Displayed orders may be out of date.';}finally{busy=false;clearTimeout(timer);if(signedIn&&!document.hidden)timer=setTimeout(refresh,5000);}}
$('login').onsubmit=async event=>{event.preventDefault();const button=event.target.querySelector('button');button.disabled=true;try{const data=await request('login',{username:event.target.elements.username.value,password:event.target.elements.password.value});csrf=data.csrfToken;event.target.elements.password.value='';await refresh();}catch(error){$('message').textContent=error.message;}finally{button.disabled=false;}};
$('logout').onclick=async()=>{try{await request('logout',{});showLogin();csrf='';$('message').textContent='Signed out.';}catch(error){$('message').textContent=error.message;}};
$('specific-date').value=localDateValue(new Date());
$('period').onchange=()=>{$('date-field').hidden=$('period').value!=='date';render();};
$('specific-date').onchange=render;
$('filter').onchange=render;$('search').oninput=render;
document.addEventListener('visibilitychange',()=>{clearTimeout(timer);if(!document.hidden&&signedIn)void refresh();});
void refresh();

const pricing=createPricingGrid({request:async(method,path,body)=>{
 if(method==='PUT'&&document.getElementById('price-due').value){const dueAt=Math.floor(new Date(document.getElementById('price-due').value).getTime()/1000);return remoteRequest('POST','schedules',{scope:path.split('/').pop(),dueAt,input:body});}
 const response=await fetch(path.replace('/api/v1/owner/','/api/v1/remote/'),{method,credentials:'same-origin',cache:'no-store',signal:AbortSignal.timeout(20000),headers:{'Content-Type':'application/json','X-CSRF-Token':csrf},body:body?JSON.stringify(body):undefined});
 if(!response.ok){if(response.status===401)showLogin();throw new Error(await response.text());}return response.json();
}},()=>signedIn?'owner':'');
$('open-pricing').onclick=()=>{if(location.hash==='#pricing')void pricing.refresh();};

function renderOverview(){
 const host=$('overview');host.replaceChildren();
 const counts=[['Submitted orders',orders.length],['Preparing / queued',orders.filter(o=>['pending','processing'].includes(queueState(o))).length],['Printing',orders.filter(o=>queueState(o)==='printing').length],['Needs attention',orders.filter(o=>queueState(o)==='failed'||o.preparationFailed||o.pickupExpired||[...(o.documents||[]),...(o.separators||[])].some(d=>d.printState==='review')).length]];
 for(const [label,value] of counts){const card=node('article','');card.append(node('small',label),node('h2',String(value)));host.append(card);}
}

async function remoteRequest(method,path,body){const upload=method==='UPLOAD';const response=await fetch('/api/v1/remote/'+path,{method:upload?'POST':method,credentials:'same-origin',cache:'no-store',signal:AbortSignal.timeout(upload?600000:35000),headers:{'Content-Type':upload?'application/octet-stream':'application/json','X-CSRF-Token':csrf,...(upload?{'X-Update-Signature':body.signature}:{})},body:upload?body.file:body?JSON.stringify(body):undefined});if(!response.ok){if(response.status===401)showLogin();throw new Error(await response.text());}return response.json();}
const operations=createRemoteOperations(remoteRequest);
// Keep scheduling beside pricing, and operation errors visible across sections.
const schedulePanel=$('price-due').closest('section');
schedulePanel.dataset.page='pricing';schedulePanel.classList.add('surface');
$('operations').append(schedulePanel);
document.querySelector('.page-heading').after($('ops-message'));


const pages={overview:['Overview','Monitor orders, collections and your kiosk.'],orders:['Orders','Track customer orders from payment to print release.'],printer:['Printer & stock','Check device health and estimated paper availability.'],pricing:['Pricing','Manage shared rates and printer-specific pricing.'],settings:['Kiosk settings','Control print release, the customer screen and maintenance.']};
let currentPage='';
function showPage(focus=true){
 const requested=location.hash.slice(1),page=Object.hasOwn(pages,requested)?requested:'overview';
 document.querySelectorAll('[data-page]').forEach(el=>{el.hidden=el.dataset.page!==page;});
 document.querySelectorAll('[data-view]').forEach(el=>{if(el.dataset.view===page)el.setAttribute('aria-current','page');else el.removeAttribute('aria-current');});
 $('page-title').textContent=pages[page][0];$('page-description').textContent=pages[page][1];
 if(currentPage!==page){currentPage=page;if(page==='pricing'&&signedIn)void pricing.refresh();if(focus)$('page-title').focus();}
}
window.addEventListener('hashchange',()=>showPage());
showPage(false);
