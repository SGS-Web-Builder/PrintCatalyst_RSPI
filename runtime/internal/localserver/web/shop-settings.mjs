import {loadPortalBranding} from './portal-branding.mjs';
import {toMinorUnits,fromMinorUnits} from './money.mjs';
// Shop policies, service-to-printer routing and QR branding.
export async function loadShopSettings(client) {
  void loadPortalBranding(client);
  let section = document.getElementById('shop-policies');
  if (!section) {
    section = document.createElement('section'); section.id = 'shop-policies'; section.className = 'section-card';
    section.innerHTML = `<h2>Printing and file retention</h2>
      <p id="shop-settings-message" role="status"></p>
      <form id="shop-policy-form" class="form-grid">
        <label>Automatic printing<select name="autoPrintMode"><option value="off">Off — print from the orders screen</option><option value="on_payment_captured">After payment approval</option><option value="all_documents">Immediately, including unpaid orders</option></select></label>
        <label>Default printer<select name="primaryPrinterId"></select></label>
        <p>Files are deleted as soon as printing is confirmed complete. Unfinished uploads are deleted after 24 hours. Order details remain available for cross-checking, including previous days.</p><input type="hidden" name="autoDeleteEnabled" value="true">
        <input type="hidden" name="autoDeleteMinutes" value="1">
        <button type="submit">Save printing settings</button>
      </form>
      <h2>Services and assigned printers</h2><div id="shop-services"></div>
      <form id="shop-service-form" class="form-grid">
        <label>Service code<input name="code" maxlength="80" required></label>
        <label>Service name<input name="displayName" maxlength="120" required></label>
        <label>Printers for this service<select name="printerIds" multiple required></select></label>
        <button type="submit">Add service</button>
      </form>
      <section id="shop-qr-settings" class="section-card"><h2>QR theme and logo</h2><p id="shop-qr-message" role="status"></p>
      <form id="shop-qr-form" class="form-grid">
        <label>Dark colour<input name="dark" type="color"></label>
        <label>Background<input name="light" type="color"></label>
        <label>Label<input name="frame" maxlength="64"></label>
        <label>Logo (PNG, up to 512 × 512)<input name="logoFile" type="file" accept="image/png"></label>
        <label><input name="removeLogo" type="checkbox"> Remove saved logo</label>
        <p>The logo sits below the code to keep it easy to scan.</p><button type="submit">Save QR appearance</button>
      </form></section>`;
    document.getElementById('settings-printing').append(section);
    document.getElementById('qr-panel-appearance').append(section.querySelector('#shop-qr-settings'));
  }
  let paymentForm = document.getElementById('qr-payment-buttons-form');
  if (!paymentForm) {
    const card = document.createElement('section');
    card.className = 'section-card';
    card.innerHTML = `<h2>Customer payment buttons</h2>
      <p>Choose the payment buttons customers see. At least one must remain enabled.</p>
      <form id="qr-payment-buttons-form">
        <label><input type="checkbox" name="cashEnabled"> Cash on Counter</label>
        <label><input type="checkbox" name="onlineEnabled"> Confirm &amp; Pay</label>
        <p class="help">Confirm &amp; Pay also needs an enabled Razorpay connection. Set it up before switching off Cash on Counter.</p>
        <button type="submit" class="btn btn--primary">Save payment buttons</button>
        <p id="qr-payment-buttons-message" role="status" aria-live="polite"></p>
      </form>`;
    document.getElementById('qr-panel-payments').append(card);
    paymentForm = card.querySelector('form');
  }
  let detailsForm=document.getElementById('customer-details-settings');
  if(!detailsForm){
    const card=document.createElement('section');card.className='section-card';
    card.innerHTML=`<h2>Customer details</h2><p>Choose what customers provide before placing an order. Switch off to create orders immediately after the payment choice.</p>
      <form id="customer-details-settings"><label><input type="checkbox" name="enabled"> Ask for customer details</label>
      <div class="form-grid">${[['customerName','Name'],['customerPhone','Phone number'],['customerEmail','Email'],['customerNotes','Notes']].map(([key,label])=>`<label>${label}<select name="${key}"><option value="hidden">Hidden</option><option value="optional">Optional</option><option value="required">Required</option></select></label>`).join('')}</div>
      <button type="submit" class="btn btn--primary">Save customer details</button><p role="status" data-message></p></form>`;
    document.getElementById('qr-panel-details').append(card);detailsForm=card.querySelector('form');
  }
  const detailsMessage=detailsForm.querySelector('[data-message]');
  try{
    const policy=await client.request('GET','/api/v1/owner/customer-details');
    detailsForm.elements.enabled.checked=policy.enabled;
    for(const [key,value] of Object.entries(policy.fields))detailsForm.elements[key].value=value;
    const sync=()=>detailsForm.querySelectorAll('select').forEach(el=>el.disabled=!detailsForm.elements.enabled.checked);
    detailsForm.elements.enabled.onchange=sync;sync();
    detailsForm.onsubmit=async event=>{
      event.preventDefault();const button=detailsForm.querySelector('button');button.disabled=true;
      const fields=Object.fromEntries([...detailsForm.querySelectorAll('select')].map(el=>[el.name,el.value]));
      try{await client.request('PUT','/api/v1/owner/customer-details',{enabled:detailsForm.elements.enabled.checked,fields});detailsMessage.textContent='Saved. Customer details settings apply to new checkouts.';}
      catch(error){detailsMessage.textContent=error.message;}finally{button.disabled=false;}
    };
  }catch(error){detailsMessage.textContent=error.message;}
  const paymentMessage = document.getElementById('qr-payment-buttons-message');
  try {
    const buttons = await client.request('GET', '/api/v1/owner/portal-payment-buttons');
    for (const key of ['cashEnabled','onlineEnabled']) paymentForm.elements[key].checked = buttons[key];
    paymentForm.onsubmit = async event => {
      event.preventDefault();
      const body = {cashEnabled:paymentForm.elements.cashEnabled.checked, onlineEnabled:paymentForm.elements.onlineEnabled.checked};
      if (!body.cashEnabled && !body.onlineEnabled) { paymentMessage.textContent = 'Enable at least one payment button.'; return; }
      const submit = paymentForm.querySelector('button'); submit.disabled = true;
      try { await client.request('PUT', '/api/v1/owner/portal-payment-buttons', body); paymentMessage.textContent = 'Saved. Customers will see these options when they load the portal.'; }
      catch (error) { paymentMessage.textContent = error.message; }
      finally { submit.disabled = false; }
    };
  } catch (error) { paymentMessage.textContent = error.message; }
  const message = section.querySelector('#shop-settings-message');
  try {
    const [settings, fleet, catalogue, theme, priceBook, discountList] = await Promise.all([
      client.request('GET', '/api/v1/owner/business/settings'), client.request('GET', '/api/v1/owner/printers'),
      client.request('GET', '/api/v1/owner/business/services'), client.request('GET', '/api/v1/owner/qr-theme'),client.request('GET','/api/v1/owner/pricing').catch(error=>{if(error.status===404)return {entries:[],currency:'not configured',currencyMinorUnits:2,unconfigured:true};throw error;}),client.request('GET','/api/v1/owner/business/discounts'),
    ]);
    const policy = section.querySelector('#shop-policy-form');
    const serviceForm = section.querySelector('#shop-service-form');
    const qr = document.getElementById('shop-qr-form');
    const qrMessage = document.getElementById('shop-qr-message');
    const printers = (fleet.printers || []).filter(p => p.enabled && !p.removedAt);
    for (const select of [policy.elements.primaryPrinterId, serviceForm.elements.printerIds]) {
      select.replaceChildren();
      if (!select.multiple) select.add(new Option('Use Windows default', ''));
      for (const p of printers) select.add(new Option(p.displayName || p.queueName, p.id));
    }
    policy.elements.autoPrintMode.value = settings.autoPrintMode;
    policy.elements.primaryPrinterId.value = settings.primaryPrinterId;
    policy.elements.autoDeleteEnabled.checked = settings.autoDeleteEnabled;
    policy.elements.autoDeleteMinutes.value = settings.autoDeleteMinutes;
    policy.onsubmit = async event => {
      event.preventDefault();
      try {
        await client.request('PUT', '/api/v1/owner/business/settings', {...settings,
          autoPrintMode: policy.elements.autoPrintMode.value, primaryPrinterId: policy.elements.primaryPrinterId.value,
          autoDeleteEnabled: true, autoDeleteMinutes: 1, updatedAt: undefined,
        }); message.textContent = 'Printing settings saved.';
      } catch (error) { message.textContent = error.message; }
    };
    const list = section.querySelector('#shop-services'); list.replaceChildren();
    for (const svc of catalogue.services || []) {
      const row = document.createElement('div'); const name = document.createElement('p');
      name.textContent = `${svc.displayName} (${svc.enabled ? 'enabled' : 'disabled'})`; row.append(name);
      const assignments = document.createElement('select'); assignments.multiple = true;
      for (const p of printers) assignments.add(new Option(p.displayName || p.queueName, p.id, false, svc.printerIds.includes(p.id)));
      const rateNote=document.createElement('p');rateNote.textContent='Prices come from the Pricing Grid: shared rates with optional printer overrides. Legacy service prices no longer replace these rates.';row.append(rateNote);
      const save = document.createElement('button'); save.type = 'button'; save.textContent = 'Save service printers';
      const toggle = document.createElement('button'); toggle.type = 'button'; toggle.textContent = svc.enabled ? 'Disable' : 'Enable';
      const update = async enabled => {
        try {
          await client.request('PUT', '/api/v1/owner/business/services/' + svc.id, {displayName: svc.displayName, description: svc.description, enabled, priceBookId: svc.priceBookId, printerIds: [...assignments.selectedOptions].map(o => o.value)}); await loadShopSettings(client); }
        catch (error) { message.textContent = error.message; }
      };
      save.onclick = () => update(svc.enabled); toggle.onclick = () => update(!svc.enabled); row.append(assignments, save, toggle); list.append(row);
    }
    serviceForm.onsubmit = async event => {
      event.preventDefault();
      try { await client.request('POST', '/api/v1/owner/business/services', {code: serviceForm.elements.code.value, displayName: serviceForm.elements.displayName.value, enabled: true, printerIds: [...serviceForm.elements.printerIds.selectedOptions].map(o => o.value)}); serviceForm.reset(); await loadShopSettings(client); }
      catch (error) { message.textContent = error.message; }
    };
    let discounts=section.querySelector('#discount-editor');if(!discounts){discounts=document.createElement('section');discounts.id='discount-editor';section.append(discounts);}
    discounts.innerHTML=`<h2>Discount codes</h2><div id="discount-list"></div><form id="discount-form" class="form-grid">
      <label>Code<input name="code" required maxlength="80"></label><label>Description<input name="displayName" required maxlength="120"></label>
      <label>Type<select name="type"><option value="percent">Percent</option><option value="flat">Fixed amount</option></select></label>
      <label>Value (percent or amount in ${priceBook.currency})<input name="value" inputmode="decimal" required></label>
      <label>Minimum order amount<input name="minimum" inputmode="decimal" value="0" required></label>
      <label>Maximum uses (0 means unlimited)<input name="maxUses" type="number" min="0" step="1" value="0" required></label>
      <label>Valid until (optional)<input name="validUntil" type="datetime-local"></label><button type="submit">Create discount</button></form>`;
    for(const discount of discountList.discounts || []){
      const row=document.createElement('p');row.textContent=`${discount.code}: ${discount.type==='percent' ? discount.value+'%' : fromMinorUnits(discount.value,priceBook.currencyMinorUnits)+' '+priceBook.currency} (${discount.timesUsed} uses) `;
      const remove=document.createElement('button');remove.type='button';remove.textContent='Remove';remove.onclick=async()=>{try{await client.request('DELETE','/api/v1/owner/business/discounts/'+discount.id);await loadShopSettings(client);}catch(err){message.textContent=err.message;}};row.append(remove);discounts.querySelector('#discount-list').append(row);
    }
    if(priceBook.unconfigured){message.textContent='Configure standard pricing to enable service prices and discounts.';for(const input of discounts.querySelectorAll('input,select,button'))input.disabled=true;}
    discounts.querySelector('form').onsubmit=async event=>{event.preventDefault();try{
      const f=event.target.elements;const value=f.type.value==='percent' ? Number(f.value.value) : toMinorUnits(f.value.value,priceBook.currencyMinorUnits);
      await client.request('POST','/api/v1/owner/business/discounts',{code:f.code.value,displayName:f.displayName.value,type:f.type.value,value,minOrderMinor:toMinorUnits(f.minimum.value,priceBook.currencyMinorUnits),maxUses:Number(f.maxUses.value),validUntil:f.validUntil.value ? Math.floor(new Date(f.validUntil.value).getTime()/1000) : 0,enabled:true});await loadShopSettings(client);
    }catch(err){message.textContent=err.message;}};
    for (const key of ['dark', 'light', 'frame']) qr.elements[key].value = theme[key] || '';
    qr.onsubmit = async event => {
      event.preventDefault();
      try {
        let logo = qr.elements.removeLogo.checked ? null : theme.logo;
        const file = qr.elements.logoFile.files[0];
        if (file) {
          if (file.size > 1024 * 1024) throw new Error('Logo must be at most 1 MiB.');
          const bytes = new Uint8Array(await file.arrayBuffer()); let binary = ''; for (const byte of bytes) binary += String.fromCharCode(byte); logo = btoa(binary);
        }
        const saved = await client.request('PUT', '/api/v1/owner/qr-theme', {dark: qr.elements.dark.value, light: qr.elements.light.value, frame: qr.elements.frame.value, logo});
        theme.logo = saved.logo;
        qrMessage.textContent = 'QR theme saved. Refresh the QR code above to preview or download it.';
      } catch (error) { qrMessage.textContent = error.message; }
    };
  } catch (error) { message.textContent = error.message; }
}
