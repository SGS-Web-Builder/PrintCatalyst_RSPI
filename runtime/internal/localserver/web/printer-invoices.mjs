export const invoiceExample=threshold=>threshold===0?'Every order gets an invoice, even when only one order is waiting.':`With “above ${threshold}”, ${threshold+1} waiting orders each get an invoice. ${threshold} or fewer print without invoices.`;

export function invoiceSettingsCard(client,printer,owner,busy=false){
 const card=document.createElement('section');card.className='ps-paper-card ps-invoice-card';
 card.innerHTML=`<h3>Invoice separators</h3><p class="ps-note">Print your business logo, name, order details and print settings after the documents. Off by default. Documents keep their own paper settings.</p>
 <form><label class="ps-invoice-toggle"><input type="checkbox" name="enabled"> Enable invoice printing for this printer</label>
 <fieldset disabled><div class="ps-invoice-grid"><label>Waiting-order threshold (0 = every order)<input type="number" name="threshold" min="0" max="10000" step="1" value="4" required></label>
 <label>Invoice paper size<select name="paper" required><option value="">Choose paper size</option></select></label>
 <label>Invoice paper tray<select name="tray" required><option value="">Choose a tray</option></select></label></div></fieldset>
 <p data-example class="ps-note"></p><p class="ps-note">Example: A4 documents → A6 invoice → next order. Load the selected invoice paper in its tray. Long invoices continue on extra sheets.</p>
 <p class="ps-note">Use 0 to print an invoice after every order. A higher threshold counts orders waiting together, not all orders created today. Orders released separately may not qualify. Saving affects future groups.</p>
 <button type="submit" class="oq-button oq-primary" disabled>Save invoice settings</button><p data-message role="status" aria-live="polite">Loading settings…</p></form>`;
 const form=card.querySelector('form'),f=form.elements,message=card.querySelector('[data-message]'),save=form.querySelector('button'),fieldset=form.querySelector('fieldset');
 for(const paper of printer.capabilities?.paperSizes||[])f.paper.add(new Option(paper.key,paper.key));
 for(const tray of printer.capabilities?.trays||[])f.tray.add(new Option(tray.rawLabel,tray.rawLabel));
 const supported=printer.backend==='windows'&&f.paper.options.length>1&&f.tray.options.length>1;
 let loaded=false,saving=false;
 const update=()=>{f.enabled.disabled=!loaded||saving||busy||!owner||(!supported&&!f.enabled.checked);fieldset.disabled=!loaded||saving||busy||!owner||!f.enabled.checked;save.disabled=!loaded||saving||busy||!owner;card.querySelector('[data-example]').textContent=invoiceExample(Number(f.threshold.value)||0);};
 f.enabled.onchange=update;f.threshold.oninput=update;update();
 const path=`/api/v1/owner/printers/${printer.id}/invoice-settings`;
 client.request('GET',path).then(v=>{f.enabled.checked=v.enabled;f.threshold.value=v.threshold;for(const key of ['paper','tray']){if(v[key]&&![...f[key].options].some(o=>o.value===v[key]))f[key].add(new Option(`${v[key]} (unavailable — choose again)`,v[key]));f[key].value=v[key];}loaded=true;message.textContent=supported?'Saved settings apply to this printer only.':'Install a Windows driver that reports paper sizes and trays to enable invoices.';update();}).catch(e=>{message.textContent=e.message;});
 form.onsubmit=async event=>{event.preventDefault();if(!loaded||saving||!owner)return;const payload={enabled:f.enabled.checked,threshold:Number(f.threshold.value),paper:f.paper.value,tray:f.tray.value};saving=true;update();try{await client.request('PUT',path,payload);message.textContent=payload.enabled?'Invoice printing enabled. '+invoiceExample(payload.threshold):'Invoice printing is off for future groups.';}catch(e){message.textContent=e.message;}finally{saving=false;update();}};
 return card;
}
