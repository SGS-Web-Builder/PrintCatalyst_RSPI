export function renderPaperStock(host,items){
 host.replaceChildren();
 if(!items.length){host.textContent='No printers available. Add a printer first.';return;}
 for(const item of items){
  const card=document.createElement('article');card.className='paper-stock-card';
  const heading=document.createElement('h3');heading.textContent=item.name;card.append(heading);
  if(!item.tracked){const note=document.createElement('p');note.textContent='Not tracked yet. Record the sheets currently loaded to start.';card.append(note);}
  const grid=document.createElement('dl');
  for(const [label,value] of [['Inserted',item.inserted],['Printed',item.printed],['Remaining',item.remaining]]){const pair=document.createElement('div'),dt=document.createElement('dt'),dd=document.createElement('dd');dt.textContent=label;dd.textContent=item.tracked?Number(value).toLocaleString():'—';pair.append(dt,dd);grid.append(pair);}
  card.append(grid);if(item.tracked&&item.remaining<=0){card.classList.add('paper-low');const note=document.createElement('p');note.textContent='Recorded stock is empty. Check the tray and record refills. Printing is not blocked.';card.append(note);}host.append(card);
 }
}
let refreshTimer,requestID='',loading=false;
export async function loadPaperStock(client){
 const panel=document.getElementById('paper-stock-panel');
 if(!panel.dataset.ready){
  panel.dataset.ready='true';panel.innerHTML=`<div class="section-header"><div><h1>Paper stock</h1><p>Track physical sheets separately from print orders.</p></div><button type="button" data-refresh class="btn">Refresh</button></div>
   <section class="section-card"><h2>Record a refill</h2><form class="paper-stock-form"><label>Select printer<select name="printerId" required></select></label><label>Sheets inserted<input name="sheets" type="number" min="1" max="1000000" step="1" required placeholder="e.g. 500"></label><button type="submit" class="btn btn--primary">Add sheets</button></form><p data-message role="status"></p><p>For your first entry, enter the sheets currently in the tray. Later entries add to that stock.</p></section>
   <div class="paper-stock-grid" data-stock></div><p class="help">Estimated stock from completed jobs in this app. Duplex and copies are included. External printing, jams and wasted sheets are not detected. Counts never block printing.</p>`;
  const form=panel.querySelector('form'),message=panel.querySelector('[data-message]');
  form.oninput=()=>{requestID='';};
  form.onsubmit=async event=>{event.preventDefault();if(form.dataset.saving)return;form.dataset.saving='true';const controls=[...form.elements];controls.forEach(control=>control.disabled=true);requestID ||= crypto.randomUUID();
   try{await client.request('POST','/api/v1/owner/paper-stock',{printerId:form.elements.printerId.value,sheets:Number(form.elements.sheets.value),requestId:requestID});requestID='';form.elements.sheets.value='';message.textContent='Sheets added.';await loadPaperStock(client);}
   catch(error){message.textContent=error.message;}finally{controls.forEach(control=>control.disabled=false);delete form.dataset.saving;}
  };
  panel.querySelector('[data-refresh]').onclick=()=>loadPaperStock(client);
  document.addEventListener('visibilitychange',()=>{if(!document.hidden&&!panel.hidden)void loadPaperStock(client);});
 }
 if(loading)return;loading=true;clearTimeout(refreshTimer);
 try{
  const items=await client.request('GET','/api/v1/owner/paper-stock');const select=panel.querySelector('select'),selected=select.value;select.replaceChildren();
  for(const item of items)select.add(new Option(item.name,item.printerId));if(items.some(item=>item.printerId===selected))select.value=selected;
  renderPaperStock(panel.querySelector('[data-stock]'),items);
 }catch(error){panel.querySelector('[data-message]').textContent=error.message;}
 finally{loading=false;refreshTimer=setTimeout(()=>{if(!panel.hidden&&!document.hidden)void loadPaperStock(client);},15000);}
}
