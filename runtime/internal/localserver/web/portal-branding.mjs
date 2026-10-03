export async function loadPortalBranding(client){
 const host=document.getElementById('qr-panel-branding');if(!host||host.dataset.loaded)return;host.dataset.loaded='true';
 host.innerHTML=`<section class="section-card"><h2>Portal header branding</h2><p>Customize the header customers see after scanning your QR code. Without a logo, the saved Business name is displayed. Change it under Business details. A logo replaces the name visually.</p>
 <form class="form-grid" id="portal-branding-form">
 <label>Fallback title (used before a business name is saved)<input name="title" maxlength="80" required></label><label>Subtitle<input name="subtitle" maxlength="160"></label>
 <label>Header logo<input name="logoFile" type="file" accept="image/png,image/jpeg"><small>Recommended: 600 × 160 px. Maximum: 2048 × 512 px, 512 KB. PNG or JPG; transparent PNG works best.</small><span><input name="removeLogo" type="checkbox"> Remove logo and use text</span></label>
 <label>Header icon<input name="iconFile" type="file" accept="image/png,image/jpeg"><small>Recommended: 128 × 128 px. Must be square; maximum 512 × 512 px, 512 KB. PNG or JPG.</small><span><input name="removeIcon" type="checkbox"> Use default icon</span></label>
 <div style="grid-column:1/-1"><h3>Header preview</h3><div data-preview style="display:flex;align-items:center;gap:12px;padding:18px;background:linear-gradient(115deg,#4c5df4,#7b89fc);border-radius:16px;color:white;overflow:hidden"><span data-icon style="flex:0 0 44px;width:44px;height:44px;display:grid;place-items:center;border-radius:14px;background:white;color:#26334d;overflow:hidden"></span><div style="min-width:0"><img data-logo style="max-width:100%;width:auto;height:42px;object-fit:contain" hidden><strong data-title style="overflow-wrap:anywhere"></strong><small data-subtitle style="display:block;margin-top:6px;overflow-wrap:anywhere"></small></div></div></div>
 <button type="submit" class="btn btn--primary">Save portal branding</button><p data-message role="status"></p></form></section>`;
 const form=host.querySelector('form'),message=form.querySelector('[data-message]');let saved={logo:'',icon:''},uploads={};let pending=0;
 const draw=()=>{
  const logo=form.elements.removeLogo.checked?'':uploads.logo??saved.logo;
  const icon=form.elements.removeIcon.checked?'':uploads.icon??saved.icon;
  const image=form.querySelector('[data-logo]');image.hidden=!logo;if(logo)image.src=logo;else image.removeAttribute('src');image.alt=form.elements.title.value;
  const title=form.querySelector('[data-title]');title.textContent=saved.businessName || form.elements.title.value;title.hidden=!!logo;
  form.querySelector('[data-subtitle]').textContent=form.elements.subtitle.value;
  const badge=form.querySelector('[data-icon]');badge.replaceChildren();if(icon){const img=document.createElement('img');img.src=icon;img.alt='';img.style.cssText='width:100%;height:100%;object-fit:contain';badge.append(img);}else badge.textContent='▤';
 };
 form.addEventListener('input',draw);
 for(const key of ['logo','icon'])form.elements[key+'File'].onchange=async event=>{
  const file=event.target.files[0];if(!file)return;
  if(!['image/png','image/jpeg'].includes(file.type)||file.size>512*1024){message.textContent='Choose a PNG or JPG up to 512 KB.';event.target.value='';return;}
  const button=form.querySelector('button');pending++;button.disabled=true;
  try{const data=await new Promise((resolve,reject)=>{const reader=new FileReader();reader.onload=()=>resolve(reader.result);reader.onerror=()=>reject(new Error('Could not read image'));reader.readAsDataURL(file);});uploads[key]=data;form.elements['remove'+key[0].toUpperCase()+key.slice(1)].checked=false;message.textContent='Preview updated. Save to apply.';draw();}
  catch(error){message.textContent=error.message;}finally{pending--;button.disabled=pending>0;}
 };
 try{const value=await client.request('GET','/api/v1/owner/portal-branding');saved=value;form.elements.title.value=value.title;form.elements.subtitle.value=value.subtitle;draw();}
 catch(error){message.textContent=error.message;delete host.dataset.loaded;}
 form.onsubmit=async event=>{
  event.preventDefault();if(pending)return;const button=form.querySelector('button');button.disabled=true;
  try{saved={...await client.request('PUT','/api/v1/owner/portal-branding',{title:form.elements.title.value,subtitle:form.elements.subtitle.value,logo:form.elements.removeLogo.checked?'':uploads.logo??saved.logo,icon:form.elements.removeIcon.checked?'':uploads.icon??saved.icon}),businessName:saved.businessName};uploads={};message.textContent='Saved. Customers see the new header when they reopen or refresh the portal.';draw();}
  catch(error){message.textContent=error.message;}finally{button.disabled=false;}
 };
}
