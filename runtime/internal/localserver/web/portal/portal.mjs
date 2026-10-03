import {createImageComposer} from './image-composer.mjs?v=0.1.19';

// Customer portal client. Manages file upload, per-file print options,

// server-authoritative quote, order placement, payment (cash on counter

// or Razorpay) and a tight status-polling loop that drives the

// "Print released - collect your prints" UI in real time.

//

// The portal is deliberately small: no framework, no bundler, no service

// worker. Every byte we ship here is parsed by the customer's browser on

// the merchant's PC; the smaller the file the faster the page first

// paints after a QR scan.



const $ = (sel) => document.querySelector(sel);

const $$ = (sel) => [...document.querySelectorAll(sel)];

const sections = ['upload', 'config', 'details', 'confirm'];

const showSection = (name) => {
  document.querySelector('main')?.classList.toggle('upload-view',name==='upload');
  const step=name==='details'?'config':name;
  for(const item of document.querySelectorAll('[data-step]')){if(item.dataset.step===step)item.setAttribute('aria-current','step');else item.removeAttribute('aria-current');item.classList.toggle('complete',sections.indexOf(item.dataset.step)<sections.indexOf(step));}
  sections.forEach((s) => {

  const el = document.getElementById(`${s}-section`);

  if (el) el.hidden = s !== name;

});
};



const MAX_FILE_SIZE = 50 << 20;

const ACCEPTED_TYPES = new Set(['application/pdf', 'image/jpeg', 'image/png']);

const ACCEPTED_EXT = new Set(['.pdf', '.jpg', '.jpeg', '.png']);



const state = {

  uploadedFiles: [],     // { documentId, originalFilename, mimeType, sizeBytes, pageCount }

  orderId: null,

  shareToken: null,

  pricing: null,         // pricing book fetched for paper-size select options

  paymentMethod: 'cash_on_counter',  // 'cash_on_counter' or 'razorpay'

  statusPollHandle: null,

  lastKnownStatus: null,

};



// -------------- helpers --------------



function formatBytes(bytes) {

  if (bytes < 1024) return `${bytes} B`;

  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;

  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;

}



function titleCase(value) {

  if (!value) return '';

  return value

    .split(' ')

    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))

    .join(' ');

}



function formatMoney(minor, mu) {

  if (!Number.isFinite(minor) || !Number.isInteger(minor)) return '—';

  if (mu === 0) return String(minor);

  const value = String(minor).padStart(mu + 1, '0');

  const whole = value.slice(0, value.length - mu);

  const frac = value.slice(value.length - mu);

  return frac ? `${whole}.${frac}` : whole;

}



function validateFile(file) {

  if (file.size > MAX_FILE_SIZE) return `File "${file.name}" exceeds 50 MB limit.`;

  const ext = (file.name.match(/\.[^.]+$/) || [''])[0].toLowerCase();

  if (file.type && !ACCEPTED_TYPES.has(file.type)) {

    return `File "${file.name}" has unsupported type "${file.type}".`;

  }

  if (!file.type && !ACCEPTED_EXT.has(ext)) {

    return `File "${file.name}" has unsupported extension.`;

  }

  return null;

}



async function readResponse(response) {

  const text = await response.text();

  let data;

  try { data = text ? JSON.parse(text) : null; }

  catch { if (response.ok) throw new Error('The shop returned an unreadable response. Please retry.'); }

  if (!response.ok) {

    const err = new Error(data?.error || data?.reason || text?.slice(0, 240) || `Server returned ${response.status}`);

    err.status = response.status;

    throw err;

  }

  return data;

}

async function postJSON(url, body) {

  return readResponse(await fetch(url, {method:'POST', headers:{'Content-Type':'application/json'}, credentials:'same-origin',cache:'no-store',body:JSON.stringify(body)}));

}

async function getJSON(url) {

  return readResponse(await fetch(url,{credentials:'same-origin',cache:'no-store'}));

}

const previewURLs = new Map();

let uploadBusy = false;

let placingOrder = false;

let quoteReady = false;

const imageComposer = createImageComposer({

  files:()=>state.uploadedFiles,

  settings:()=>{rememberSettings();return documentSettings.get(selectedDocument);},

  applySettings:ids=>{

    if(uploadBusy || placingOrder)return;

    rememberSettings();const settings=documentSettings.get(selectedDocument);

    if(!settings)return;

    for(const id of ids)documentSettings.set(id,{...settings,pageMode:'all',pageSelection:''});

    for(const row of $$('.config-row'))row.disposePreview?.();

    $('#config-list').replaceChildren();renderConfig();saveDraft();

  },

  paperSizes:()=>state.paperSizes || [...new Set((state.combinations || []).map(c=>c.paperSize))],

  image:async file=>{

    const response=await fetch(`/api/v1/portal/documents/${encodeURIComponent(file.documentId)}?orderId=${encodeURIComponent(state.orderId)}`,{headers:{'X-Upload-Token':state.uploadToken},cache:'no-store'});

    if(!response.ok)throw new Error('Could not load an image. Please retry.');return response.blob();

  },

  apply:async options=>{

    if(uploadBusy || placingOrder)throw new Error('Please wait for the current action to finish.');

    rememberSettings();uploadBusy=true;quoteReady=false;updateCheckoutButtons();

    try {

      const response=await fetch('/api/v1/portal/compose-images',{method:'POST',headers:{'Content-Type':'application/json','X-Upload-Token':state.uploadToken},body:JSON.stringify({orderId:state.orderId,...options}),cache:'no-store'});

      const output=await readResponse(response);

      const selected=new Set(options.documentIds);const next=[];let inserted=false;

      for(const file of state.uploadedFiles){if(selected.has(file.documentId)){if(!inserted){next.push(output);inserted=true;}documentSettings.delete(file.documentId);}else next.push(file);}

      state.uploadedFiles=next;selectedDocument=output.documentId;

      documentSettings.set(output.documentId,{paperSize:options.paperSize,orientation:options.orientation,pagesPerSheet:'1',copies:'1'});

      for(const row of $$('.config-row'))row.disposePreview?.();

      $('#config-list').replaceChildren();

      saveDraft();renderUploadedFiles();renderConfig();

      $('#config-list').scrollIntoView({behavior:'smooth',block:'start'});

    } finally {uploadBusy=false;updateCheckoutButtons();}

  }

});

function updateCheckoutButtons() {

  const disabled = !quoteReady || uploadBusy || placingOrder;

  $('#place-order-btn').disabled = disabled;

  $('#place-order-btn').hidden = state.cashOnCounterReady === false;

  $('#pay-online-btn').disabled = disabled;

  $('#pay-online-btn').hidden = !state.razorpayReady;

  $('#confirm-order-btn').disabled=disabled;

  $('#back-options-btn').disabled=placingOrder;

}

function saveDraft() {

  sessionStorage.setItem('pc-draft', JSON.stringify({orderId:state.orderId,uploadToken:state.uploadToken,files:state.uploadedFiles,settings:Object.fromEntries(documentSettings),selectedDocument}));

}

async function uploadFiles(files) {

  const error = $('#upload-error');

  error.hidden = true;

  if (uploadBusy || files.length === 0) return;

  if (files.length > 10) {error.textContent='Choose at most 10 files.';error.hidden=false;return;}

  for (const file of files) { const problem=validateFile(file);if(problem){error.textContent=problem;error.hidden=false;return;} }

  uploadBusy=true;quoteReady=false;updateCheckoutButtons();

  showSection('upload');

  const button=$('#continue-btn');button.disabled=true;button.textContent='Uploading...';
  $('#drop-zone').hidden=true;$('#upload-progress').hidden=false;
  $('#upload-filename').textContent=files.map(file=>file.name).join(', ');
  $('#upload-meter').value=0;$('#upload-percent').textContent='0% uploaded';

  try {

    if (!state.orderId) {

      const draft=await postJSON('/api/v1/portal/drafts',{});

      state.orderId=draft.orderId;state.uploadToken=draft.uploadToken;

      saveDraft();

    }

    const formData=new FormData();for(const file of files)formData.append('files',file,file.name);

    const response=await uploadWithProgress('/api/v1/portal/uploads?orderId='+encodeURIComponent(state.orderId),formData,state.uploadToken);

    const data=await readResponse(response);

    state.uploadedFiles=data.files;state.uploadToken=data.uploadToken;saveDraft();

    renderUploadedFiles();

    await loadPricing();

    showSection('config');renderConfig();

  } catch(err) {error.textContent=err.message;error.hidden=false;}

  finally {$('#upload-progress').hidden=true;$('#drop-zone').hidden=false;uploadBusy=false;button.disabled=false;button.textContent='Continue to print options';$('#file-input').value='';updateCheckoutButtons();}

}



function loadPreview(file,container,row) {

  const img=document.createElement('img');img.className='document-preview';img.alt='Print preview of '+file.originalFilename;

  const message=document.createElement('p');message.className='preview-message';message.setAttribute('role','status');

  const controls=document.createElement('div');controls.className='preview-navigation';

  const previous=document.createElement('button'),next=document.createElement('button'),label=document.createElement('span');

  previous.type=next.type='button';previous.textContent='‹';next.textContent='›';previous.setAttribute('aria-label','Previous sheet side');next.setAttribute('aria-label','Next sheet side');controls.append(previous,label,next);container.append(img,message,controls);

  const pages=document.createElement('div');pages.className='preview-pages';container.append(pages);

  let side=1,generation=0,controller,timer;

  let thumbnailURLs=[];

  const clearThumbnails=()=>{thumbnailURLs.forEach(URL.revokeObjectURL);thumbnailURLs=[];pages.replaceChildren();};

  const render=async()=>{

    const current=++generation;controller?.abort();controller=new AbortController();clearThumbnails();

    const values=Object.fromEntries([...row.querySelectorAll('input,select')].map(el=>[el.name,el.value]));

    let selected;

    try{selected=selectedRowPages(row);}catch(err){message.textContent=err.message;message.hidden=false;img.hidden=true;previous.disabled=next.disabled=true;label.textContent='No valid pages selected';return;}

    const total=Math.ceil(selected.length/Number(values.pagesPerSheet));controls.hidden=total<=1;side=Math.max(1,Math.min(side,total || 1));

    label.textContent=values.sides==='one-sided' ? `Side ${side} of ${total}` : `Sheet ${Math.ceil(side/2)} · ${side%2 ? 'front' : 'back'} · ${side} of ${total} sides`;

    label.textContent+=` · Source pages ${selected.slice((side-1)*Number(values.pagesPerSheet),side*Number(values.pagesPerSheet)).join(", ")}`;

    previous.disabled=side<=1;next.disabled=side>=total;message.textContent='Updating print preview…';message.hidden=false;img.hidden=true;

    try{

      const params=new URLSearchParams({orderId:state.orderId,preview:'sheet',side:String(side),from:String(selected[0]),to:String(selected[selected.length-1]),pages:JSON.stringify(selected),pagesPerSheet:values.pagesPerSheet,paperSize:values.paperSize,orientation:values.orientation,colourMode:values.colourMode,sides:values.sides});

      const response=await fetch(`/api/v1/portal/documents/${encodeURIComponent(file.documentId)}?${params}`,{headers:{'X-Upload-Token':state.uploadToken || state.shareToken},cache:'no-store',signal:controller.signal});

      if(!response.ok){await readResponse(response);return;}

      const blob=await response.blob();if(current!==generation || !container.isConnected)return;

      const url=URL.createObjectURL(blob),key=file.documentId+':sheet',old=previewURLs.get(key);

      img.onload=()=>{if(current===generation){message.hidden=true;img.hidden=false;}};img.onerror=()=>{if(current===generation)message.textContent='Unable to display preview. Change an option to retry.';};

      previewURLs.set(key,url);img.src=url;if(old)URL.revokeObjectURL(old);

      if(total>1){

        // Show a bounded page strip, using the same rendered sheets as the main preview.

        const start=Math.max(1,Math.min(side-2,total-5));

        for(let page=start;page<=Math.min(total,start+5);page++){

          const button=document.createElement('button');button.type='button';button.setAttribute('aria-pressed',String(page===side));

          const thumb=document.createElement('img');thumb.alt='';const title=document.createElement('span');title.textContent=`Page ${page}`;button.append(thumb,title);pages.append(button);

          button.onclick=()=>{side=page;void render();};

          if(page===side){thumb.src=url;continue;}

          const query=new URLSearchParams(params);query.set('side',String(page));

          void fetch(`/api/v1/portal/documents/${encodeURIComponent(file.documentId)}?${query}`,{headers:{'X-Upload-Token':state.uploadToken || state.shareToken},cache:'no-store',signal:controller.signal})

            .then(response=>{if(!response.ok)throw new Error('Preview unavailable');return response.blob();})

            .then(blob=>{if(current!==generation||!container.isConnected)return;const address=URL.createObjectURL(blob);thumbnailURLs.push(address);thumb.src=address;})

            .catch(()=>{thumb.hidden=true;});

        }

      }

    }catch(err){if(current===generation && err.name!=='AbortError')message.textContent='Preview unavailable: '+err.message;}

  };

  row.refreshPreview=()=>{++generation;controller?.abort();clearTimeout(timer);img.hidden=true;message.hidden=false;message.textContent='Updating print preview…';timer=setTimeout(render,120);};

  previous.onclick=()=>{side--;void render();};next.onclick=()=>{side++;void render();};row.disposePreview=()=>{++generation;controller?.abort();clearTimeout(timer);clearThumbnails();};void render();

}



function renderUploadedFiles() {

  const list = $('#file-list');

  list.replaceChildren();

  $('#upload-actions').hidden=state.uploadedFiles.length===0;

  for (const f of state.uploadedFiles) {

    const item = document.createElement('div');

    item.className = 'file-item';

    const meta = document.createElement('div');

    meta.className = 'file-meta';

    const name = document.createElement('span');

    name.className = 'file-name';

    name.textContent = f.originalFilename;

    const info = document.createElement('span');

    info.className = 'file-info';

    info.textContent = `${formatBytes(f.sizeBytes)} · ${f.pageCount} page${f.pageCount === 1 ? '' : 's'}`;

    meta.append(name, info);

    item.append(meta);

    list.append(item);

  }

}



let optionsRequest = 0;

async function loadPricing() {

  const request=++optionsRequest;

  ++quoteRequestId;

  const service=$('#service-choice');

  quoteReady=false;updateCheckoutButtons();

  try {

    const data=await getJSON('/api/v1/portal/options?serviceId='+encodeURIComponent(service.value)+'&printerId='+encodeURIComponent($('#printer-choice').value));

    if(request!==optionsRequest)return;

    const selected=service.value;

    service.replaceChildren(new Option('Standard document printing',''));

    for(const entry of data.services || [])service.add(new Option(entry.displayName,entry.id));

    service.value=selected;

    const printer=$('#printer-choice');printer.replaceChildren();

    if(!data.printerSelectionRequired)printer.add(new Option('Automatic printer',''));

    for(const p of data.printers||[])printer.add(new Option(p.name,p.id));

    printer.value=data.selectedPrinterId||'';

    $('#printer-choice-label').hidden=!data.printerSelectionRequired;

    state.combinations=data.combinations || [];

    state.razorpayReady=Boolean(data.razorpayReady);

    state.cashOnCounterReady=data.cashOnCounterReady !== false;
    state.customerDetails=data.customerDetails;
    configureCustomerDetails();
  restoreCustomerPhone();

    if(data.docxAvailable){ACCEPTED_TYPES.add('application/vnd.openxmlformats-officedocument.wordprocessingml.document');ACCEPTED_EXT.add('.docx');$('#file-input').accept='.pdf,.jpg,.jpeg,.png,.docx';$('#source-file-types').textContent='PDF, JPG, PNG and Word (DOCX) supported. Up to 10 files per order.';}

    $('#upload-error').hidden=true;

  } catch(err) {

    state.combinations=[];state.razorpayReady=false;

    $('#upload-error').textContent='The shop cannot accept prints yet: '+err.message;$('#upload-error').hidden=false;

    $('#quote-error').textContent=err.message;$('#quote-error').hidden=false;

    throw err;

  } finally {updateCheckoutButtons();}

}



const documentSettings=new Map();

let selectedDocument=null;

function rememberSettings(){for(const row of $$('.config-row'))documentSettings.set(row.dataset.documentId,Object.fromEntries([...row.querySelectorAll('input,select')].map(el=>[el.name,el.value])));}

function selectDocument(id){

  selectedDocument=id;for(const row of $$('.config-row'))row.hidden=row.dataset.documentId!==id;

  for(const button of $$('#document-tabs .document-tab'))button.setAttribute('aria-pressed',String(button.dataset.documentId===id));updateCopies();

}

function updateCopies(){

  const row=$$('.config-row').find(row=>row.dataset.documentId===selectedDocument);const copies=Number(row?.querySelector('[name="copies"]').value || 1);

  $('#copies-value').textContent=String(copies);$('#copies-less').disabled=copies<=1;$('#copies-more').disabled=copies>=100;

}

function renderConfig() {

  rememberSettings();for(const row of $$('.config-row'))row.disposePreview?.();

  $('#document-count').textContent=`(${state.uploadedFiles.length})`;
  const list=$('#config-list');list.replaceChildren();$('#document-tabs').replaceChildren();

  if(!state.uploadedFiles.some(f=>f.documentId===selectedDocument))selectedDocument=state.uploadedFiles[0]?.documentId;

  for(const [index,file] of state.uploadedFiles.entries()){

    const card=document.createElement('button');card.type='button';card.className='document-tab';card.dataset.documentId=file.documentId;

    const number=document.createElement('span');number.className='document-number';number.textContent=`▤  Doc ${index+1}`;

    const name=document.createElement('strong');name.title=file.originalFilename;name.textContent=file.originalFilename.length>21?file.originalFilename.slice(0,18)+'…':file.originalFilename;const summary=document.createElement('small');summary.textContent=`${file.pageCount} pages · Original`;

    card.append(number,name,summary);card.onclick=()=>selectDocument(file.documentId);

    const wrapper=document.createElement('div');wrapper.className='document-card';

    const pick=document.createElement('input');pick.type='checkbox';pick.dataset.documentId=file.documentId;

    pick.setAttribute('aria-label','Select '+file.originalFilename);pick.hidden=state.uploadedFiles.length<2;

    const remove=document.createElement('button');remove.type='button';remove.className='document-delete';remove.title='Delete document';remove.setAttribute('aria-label','Delete '+file.originalFilename);
    remove.innerHTML='<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><path d="M3 6h18M9 6V3h6v3M6 6l1 15h10l1-15M10 10v7M14 10v7"/></svg>';
    remove.onclick=async()=>{remove.disabled=true;try{
      await readResponse(await fetch(`/api/v1/portal/documents/${encodeURIComponent(file.documentId)}?orderId=${encodeURIComponent(state.orderId)}`,{method:'DELETE',headers:{'X-Upload-Token':state.uploadToken}}));
      rememberSettings();state.uploadedFiles=state.uploadedFiles.filter(item=>item.documentId!==file.documentId);documentSettings.delete(file.documentId);
      for(const [key,url] of previewURLs){if(key.startsWith(file.documentId+':')){URL.revokeObjectURL(url);previewURLs.delete(key);}}
      renderConfig();documentSettings.delete(file.documentId);saveDraft();if(!state.uploadedFiles.length)showSection('upload');
    }catch(err){$('#quote-error').textContent=err.message;$('#quote-error').hidden=false;}finally{remove.disabled=false;}};
    wrapper.append(card,pick,remove);$('#document-tabs').append(wrapper);list.append(renderConfigRow(file,card));

  }

  imageComposer.render();selectDocument(selectedDocument);requestQuote();

}

function renderConfigRow(file,card) {

  const row=document.createElement('article');row.className='config-row';row.dataset.documentId=file.documentId;

  const heading=document.createElement('div');heading.className='preview-title';heading.textContent=file.originalFilename;row.append(heading);

  const preview=document.createElement('div');preview.className='preview-container';row.append(preview);

  const badges=document.createElement('div');badges.className='preview-badges';row.append(badges);

  const toolbar=document.createElement('div');toolbar.className='preview-toolbar';const note=document.createElement('small');note.textContent='Print preview · Fit to page';

  toolbar.append(note);row.append(toolbar);

  const panel=document.createElement('div');panel.className='edit-panel';const grid=document.createElement('div');grid.className='config-grid';

  row.dataset.pageCount=String(file.pageCount);

  const combinations=state.combinations || [];const options=(key,choices=combinations)=>[...new Set(choices.map(c=>c[key]))].map(value=>[titleCase(value.replace(/-/g,' ')),value]);

  grid.append(selectField('Paper size','paperSize',options('paperSize')),selectField('Colour mode','colourMode',[]),selectField('Print style','sides',[]),selectField('Paper orientation','orientation',[['Auto orientation','auto'],['Portrait','portrait'],['Landscape','landscape']]),selectField('Pages per side','pagesPerSheet',[['1','1'],['2','2'],['4','4']]),numberField('Copies','copies',1,100,1));

  grid.querySelector('[name="copies"]').parentElement.hidden=true;

  const optionsTitle=document.createElement('h2');optionsTitle.textContent='Print options';panel.append(optionsTitle,grid);

  row.append(panel);

  const pageField=selectField('Pages to print','pageMode',[[`All ${file.pageCount} pages`,'all'],['Selected pages','selected'],['Skip pages','skip'],['Odd pages only','odd'],['Even pages only','even']]);pageField.className='page-selection';pageField.hidden=file.pageCount<=1;

  const pageInput=document.createElement('input');pageInput.name='pageSelection';pageInput.placeholder='e.g. 1,3,5-8';pageInput.setAttribute('aria-label','Page numbers or ranges');pageInput.maxLength=2000;pageInput.autocomplete='off';

  const pageCountLabel=document.createElement('small');pageCountLabel.className='selection-count';pageCountLabel.setAttribute('role','status');pageField.append(pageInput,pageCountLabel);grid.append(pageField);

  const paper=grid.querySelector('[name="paperSize"]'),colour=grid.querySelector('[name="colourMode"]'),sides=grid.querySelector('[name="sides"]');

  const replace=(select,entries)=>{const previous=select.value;select.replaceChildren(...entries.map(([label,value])=>new Option(label,value)));if(entries.some(([,value])=>value===previous))select.value=previous;};

  const sync=()=>{const choices=combinations.filter(c=>c.paperSize===paper.value);replace(colour,options('colourMode',choices));replace(sides,options('sides',choices.filter(c=>c.colourMode===colour.value && (file.pageCount>1 || c.sides==='one-sided'))));};

  const saved=documentSettings.get(file.documentId);if(saved?.paperSize && [...paper.options].some(o=>o.value===saved.paperSize))paper.value=saved.paperSize;

  sync();if(saved?.colourMode && [...colour.options].some(o=>o.value===saved.colourMode))colour.value=saved.colourMode;else if([...colour.options].some(o=>o.value==='monochrome'))colour.value='monochrome';sync();

  for(const el of grid.querySelectorAll('input,select'))if(saved?.[el.name] && !['paperSize','colourMode'].includes(el.name)){if(el.tagName!=='SELECT' || [...el.options].some(o=>o.value===saved[el.name]))el.value=saved[el.name];}

  const labels={monochrome:'B&W',colour:'Colour','one-sided':'Single-sided','two-sided-long-edge':'Back-to-back · long edge','two-sided-short-edge':'Back-to-back · short edge',auto:'Auto orientation',portrait:'Portrait',landscape:'Landscape'};

  const groups=new Map();

  for(const name of ['colourMode','sides','paperSize','orientation','pagesPerSheet']){

    const select=grid.querySelector(`[name="${name}"]`);select.className='option-source';select.tabIndex=-1;select.setAttribute('aria-hidden','true');

    const group=document.createElement('div');group.className='option-pills';group.setAttribute('role','group');group.setAttribute('aria-label',select.parentElement.firstChild.textContent);select.parentElement.append(group);groups.set(name,group);

  }

  const update=()=>{
    const orientation=grid.querySelector('[name="orientation"]').value;
    const preferred=orientation==='landscape'?'two-sided-short-edge':'two-sided-long-edge';
    if(sides.value!=='one-sided' && [...sides.options].some(o=>o.value===preferred))sides.value=preferred;

    const mode=grid.querySelector('[name="pageMode"]').value;pageInput.hidden=!['selected','skip'].includes(mode);

    pageInput.setAttribute('aria-label',mode==='skip'?'Pages to skip':'Pages to include');

    try{const pages=selectedRowPages(row);pageCountLabel.textContent=`${pages.length} page${pages.length===1?'':'s'} selected · Source document stays unchanged`;pageInput.removeAttribute('aria-invalid');}

    catch(err){pageCountLabel.textContent=err.message;pageInput.setAttribute('aria-invalid','true');}



    for(const [name,group] of groups){const select=grid.querySelector(`[name="${name}"]`);group.replaceChildren();for(const option of select.options){if(name==='sides' && option.value!=='one-sided' && option.value!==([...select.options].some(o=>o.value===preferred)?preferred:[...select.options].find(o=>o.value!=='one-sided')?.value))continue;const button=document.createElement('button');button.type='button';button.textContent=name==='sides' && option.value!=='one-sided'?(orientation==='auto'?'Back-to-back · automatic flip':orientation==='landscape'?'Back-to-back · short edge':'Back-to-back · long edge'):labels[option.value] || option.textContent;const icon=printOptionIcon(option.value);if(icon)button.prepend(icon);button.setAttribute('aria-pressed',String(option.selected));button.onclick=()=>{select.value=option.value;select.dispatchEvent(new Event('change',{bubbles:true}));};group.append(button);}}

    badges.replaceChildren();for(const value of [labels[colour.value],labels[sides.value],labels[grid.querySelector('[name="orientation"]').value],paper.value,'Fit to page']){const badge=document.createElement('span');badge.textContent=value || 'Unavailable';badges.append(badge);}

    const copies=grid.querySelector('[name="copies"]').value;card.querySelector('small').textContent=`${file.pageCount} page${file.pageCount===1?'':'s'} · ${copies} ${copies==='1'?'copy':'copies'}`;updateCopies();

  };

  for(const el of grid.querySelectorAll('input,select'))el.addEventListener('change',()=>{sync();update();rememberSettings();saveDraft();row.refreshPreview?.();requestQuote();});

  pageInput.addEventListener('input',()=>{update();rememberSettings();saveDraft();row.refreshPreview?.();requestQuote();});

  update();queueMicrotask(()=>loadPreview(file,preview,row));return row;

}



function selectField(label, name, options) {

  const wrap = document.createElement('label');

  wrap.textContent = label;

  const sel = document.createElement('select');

  sel.name = name;

  for (const [text, value] of options) {

    const opt = document.createElement('option');

    opt.value = value;

    opt.textContent = text;

    sel.append(opt);

  }

  wrap.append(sel);

  return wrap;

}



function numberField(label, name, min, max, value) {

  const wrap = document.createElement('label');

  wrap.textContent = label;

  const input = document.createElement('input');

  input.name = name;

  input.type = 'number';

  input.min = String(min);

  input.max = String(max);

  input.value = String(value);

  wrap.append(input);

  return wrap;

}



function pagesForMode(count,mode,expression='') {

  const all=Array.from({length:count},(_,i)=>i+1);

  if(mode==='all')return all;

  if(mode==='odd')return all.filter(page=>page%2===1);

  if(mode==='even'){const pages=all.filter(page=>page%2===0);if(!pages.length)throw new Error('No even pages in this document.');return pages;}

  if(!['selected','skip'].includes(mode))throw new Error('Choose a valid page selection.');

  if(!expression.trim())throw new Error(mode==='skip'?'Enter the pages to skip.':'Enter the pages to print.');

  const chosen=new Set();

  for(const part of expression.split(',')){

    const match=part.trim().match(/^(\d+)(?:\s*-\s*(\d+))?$/);

    if(!match)throw new Error('Use page numbers or ranges, e.g. 1,3,5-8.');

    const start=Number(match[1]),end=Number(match[2] || match[1]);

    if(start<1 || end<start || end>count)throw new Error(`Pages must be between 1 and ${count}.`);

    for(let page=start;page<=end;page++)chosen.add(page);

  }

  const pages=all.filter(page=>mode==='skip'?!chosen.has(page):chosen.has(page));

  if(!pages.length)throw new Error('Select at least one page to print.');return pages;

}

function selectedRowPages(row){return pagesForMode(Number(row.dataset.pageCount),row.querySelector('[name="pageMode"]').value,row.querySelector('[name="pageSelection"]').value);}



function collectLines() {

  const lines = [];

  for (const row of $$('.config-row')) {

    const paperSize = row.querySelector('[name="paperSize"]').value;

    const colourMode = row.querySelector('[name="colourMode"]').value;

    const sides = row.querySelector('[name="sides"]').value;

    const copies = Number(row.querySelector('[name="copies"]').value) || 1;

    const pages=selectedRowPages(row);

    const start=pages[0],end=pages[pages.length-1];

    lines.push({

      documentId: row.dataset.documentId,

      orientation: row.querySelector('[name="orientation"]').value,

      pagesPerSheet: Number(row.querySelector('[name="pagesPerSheet"]').value),

      paperSize,

      colourMode,

      sides,

      copies,

      pages,

      pageRangeStart: start,

      pageRangeEnd: end,

    });

  }

  return lines;

}



let quoteRequestId = 0;

async function requestQuote() {

  const error = $('#quote-error');

  const result = $('#quote-result');

  const loading = $('#quote-loading');

  error.hidden = true;

  result.hidden = true;

  loading.hidden = false;

  const myRequest = ++quoteRequestId;

  quoteReady=false;updateCheckoutButtons();

  try {

    const lines = collectLines();

    const quote = await postJSON('/api/v1/portal/quote', { lines,serviceId:$('#service-choice').value,primaryPrinterId:$('#printer-choice').value });

    if (myRequest !== quoteRequestId) return;

    $('#quote-total').textContent = `${formatMoney(quote.totalMinor, quote.currencyMinorUnits)} ${quote.currency}`;

    $('#quote-label').textContent=quote.discountMinor ? `Estimated total (discount ${formatMoney(quote.discountMinor,quote.currencyMinorUnits)} ${quote.currency})` : 'Estimated total';

    result.hidden = false;

    quoteReady=true;updateCheckoutButtons();

  } catch (err) {

    if (myRequest !== quoteRequestId) return;

    error.textContent = err.message;

    error.hidden = false;

    quoteReady=false;updateCheckoutButtons();

  } finally {

    if (myRequest === quoteRequestId) loading.hidden = true;

  }

}



function configureCustomerDetails(){
  const policy=state.customerDetails;
  if(!policy)return;
  for(const [key,mode] of Object.entries(policy.fields)){
    const input=$(`#order-form [name="${key}"]`);if(!input)continue;
    const visible=policy.enabled && mode!=='hidden';
    input.disabled=!visible;input.required=visible && mode==='required';input.parentElement.hidden=!visible;
    if(!visible)input.value='';
    input.parentElement.querySelectorAll('small,.required').forEach(el=>el.remove());
    const hint=document.createElement('small');hint.textContent=input.required?' *':' (optional)';input.before(hint);
  }
}

function beginCheckout(method) {

  if (!quoteReady || placingOrder || uploadBusy) return;

  if ((method === 'cash_on_counter' && state.cashOnCounterReady === false) || !['cash_on_counter', 'razorpay'].includes(method) || (method === 'razorpay' && !state.razorpayReady)) return;

  state.paymentMethod = method;
  configureCustomerDetails();
  restoreCustomerPhone();
  if(state.customerDetails && (!state.customerDetails.enabled || Object.values(state.customerDetails.fields).every(mode=>mode==='hidden'))){
    void placeOrder($('#order-form'),method);return;
  }

  $('#details-summary').textContent = `${$('#quote-total').textContent} · ${method === 'razorpay' ? 'Pay Online' : 'Cash On Counter'}`;

  $('#confirm-order-btn').textContent = method === 'razorpay' ? 'Continue to payment' : 'Confirm cash order';

  $('#order-error').hidden = true;

  showSection('details');

  ($('#order-form input:not([disabled]), #order-form textarea:not([disabled])') || $('#confirm-order-btn')).focus();

}



async function placeOrder(form,method) {

  if(placingOrder || !quoteReady)return;

  placingOrder=true;updateCheckoutButtons();

  const errEl = $('#order-error');

  errEl.hidden = true;

  try {

  const formData = new FormData(form);

  const payload = {

    orderId: state.orderId,

    primaryPrinterId: $('#printer-choice').value,

    serviceId: document.getElementById('service-choice')?.value || '',

    lines: collectLines(),

    customerName: formData.get('customerName') || '',

    customerPhone: formData.get('customerPhone') || '',

    customerEmail: formData.get('customerEmail') || '',

    customerNotes: formData.get('customerNotes') || '',

    paymentMethod: method,

  };

    const result = await postJSON('/api/v1/portal/orders', payload);
    rememberCustomerPhone(payload.customerPhone);
    result.receiptLines=payload.lines.map(line=>({...line,filename:state.uploadedFiles.find(f=>f.documentId===line.documentId)?.originalFilename || 'Document'}));

    // Keep draft recovery until the receipt has actually been persisted.
    persistOrderReceipt({...result, files:state.uploadedFiles});

    state.shareToken = result.shareToken;

    state.paymentMethod=result.paymentMethod;

    state.orderId = result.orderId;

    if (result.paymentMethod === 'razorpay' && result.redirectUrl) {

      // Open the hosted checkout directly, without an intermediate screen.
      window.location.href = result.redirectUrl;

      return;

    }

    // Cash on counter flow: show confirmation and start status polling.

    showConfirmation(result);

  } catch (err) {

    errEl.textContent = err.message;

    errEl.hidden = false;
    if($('#details-section').hidden){$('#quote-error').textContent=err.message;$('#quote-error').hidden=false;}

  } finally {placingOrder=false;updateCheckoutButtons();}

}



function renderReceipt(lines=[]){
  const root=$('#receipt-documents');root.replaceChildren();
  const labels={monochrome:'Black & white',colour:'Colour','one-sided':'Single sided','two-sided-long-edge':'Double sided · long edge','two-sided-short-edge':'Double sided · short edge',portrait:'Portrait',landscape:'Landscape',auto:'Automatic'};
  for(const line of lines){
    const section=document.createElement('section');section.className='receipt-document';
    const title=document.createElement('h3');title.textContent=line.filename || 'Document';section.append(title);
    const list=document.createElement('dl');const count=line.pages?.length || (line.pageRangeEnd-line.pageRangeStart+1);
    for(const [label,value] of [['Colour mode',labels[line.colourMode] || line.colourMode],['Print style',labels[line.sides] || line.sides],['Orientation',labels[line.orientation] || 'Automatic'],['Paper',line.paperSize],['Pages per side',line.pagesPerSheet || 1],['Copies',line.copies],['Selected pages',count]]){
      const term=document.createElement('dt'),detail=document.createElement('dd');term.textContent=label;detail.textContent=String(value ?? '—');list.append(term,detail);
    }
    section.append(list);root.append(section);
  }
}

function showConfirmation(result) {
  renderPickup(null);
  renderReceipt(result.receiptLines || []);

  $('#receipt-title').textContent = 'Order received';
  $('#conf-order-id').textContent = result.orderId;

  $('#conf-total').textContent = `${formatMoney(result.totalMinor, result.currencyMU)} ${result.currency}`;

  $('#conf-files').textContent = state.uploadedFiles.map((f) => f.originalFilename).join(', ');

  setStatusBadge('pending_payment');

  $('#order-summary').textContent = `Order ${result.orderId} received. ${result.paymentMethod === 'razorpay' ? 'Awaiting payment confirmation.' : 'Please pay at the counter to start printing.'}`;

  $('#payment-error').textContent=result.paymentError || '';

  $('#payment-error').hidden=!result.paymentError;

  $('#retry-payment-btn').hidden=result.paymentMethod!=='razorpay';

  showSection('confirm');

  // Start polling the moment we land on the confirmation screen — the

  // payment may have already been confirmed by the time the page paints.

  startStatusPolling(result.orderId);

}



// startStatusPolling hits the order status endpoint on a tight loop

// until the order reaches a terminal state (completed/failed/cancelled)

// or the user clicks "Start a new order". The poll interval is fast

// enough that the "Print released" UI appears within a fraction of a

// second of the dispatcher actually submitting the bytes.

//

// Poll cadence:

//   - 250 ms for the first 5 seconds (covers the webhook → paid →

//     dispatched pipeline in real time)

//   - 1000 ms afterwards (the operator has had time to react; the

//     customer is reading the "Print released" message)

function startStatusPolling(orderID) {

  if (state.statusPollHandle) {

    clearTimeout(state.statusPollHandle);

  }

  state.lastKnownStatus = null;

  const startedAt = Date.now();

  let cancelled = false;

  const stop = () => {

    cancelled = true;

    if (state.statusPollHandle) {

      clearTimeout(state.statusPollHandle);

      state.statusPollHandle = null;

    }

  };

  // Expose stop on the cancel button.

  $('#new-order-btn').onclick = () => {

    stop();

    resetState();

    showSection('upload');

  };



  const poll = async () => {

    if (cancelled) return;

    try {

      const data = await getJSON(`/api/v1/portal/orders/${encodeURIComponent(orderID)}`);
      if (cancelled) return;
      handleStatusUpdate(data);

      if (state.shareToken) {
        try {
          const response = await fetch(`/api/v1/portal/orders/${encodeURIComponent(orderID)}/pickup`, {
            headers: {'X-Order-Token': state.shareToken}, cache: 'no-store'
          });
          const pickup = response.ok ? await response.json() : {state:'unavailable'};
          if (cancelled) return;
          renderPickup(pickup);
        } catch {
          if (!cancelled) renderPickup({state:'unavailable'});
        }
      }

      if (data.status === 'cancelled' || (data.printStatus === 'done' && data.status !== 'pending_payment')) {

        stop();

        return;

      }

    } catch (err) {

      console.warn('status poll failed:', err);

    }

    const elapsed = Date.now() - startedAt;

    const next = elapsed < 5000 ? 250 : 1000;

    state.statusPollHandle = setTimeout(poll, next);

  };

  // Kick off immediately so the first response is in flight before the

  // next event loop tick.

  state.statusPollHandle = setTimeout(poll, 0);

}



function renderPickup(view) {
  const panel=$('#pickup-panel'), code=$('#pickup-code'), message=$('#pickup-message');
  if (!panel || !code || !message) return;
  code.textContent='';
  panel.hidden=!view || view.state==='waiting_payment' || view.state==='completed';
  if (panel.hidden) { message.textContent=''; return; }
  if (view.state==='active' && /^\d{4}$/.test(view.code || '')) {
    code.textContent=view.code;
    message.textContent=view.preparation==='ready'
      ? 'Enter this code on the shop touchscreen to release your prints. Keep it private.'
      : view.preparation==='failed'
        ? 'Your payment is confirmed, but preparation needs attention. Please ask the merchant.'
        : 'Payment confirmed. Your files are being prepared. Keep this code for the shop touchscreen.';
  } else {
    const messages={assistance:'Payment verified, but pickup setup needs attention. Please ask the merchant.',preparing_code:'Payment verified. Preparing your pickup code…',claimed:'Code accepted. Your order is queued for printing.',expired:'This pickup code has expired. Your paid order is saved; ask the merchant for assistance.',unavailable:'Pickup information is unavailable. Refresh or ask the merchant for assistance.'};
    message.textContent=messages[view.state] || messages.unavailable;
  }
}

function isTerminalStatus(status) {

  return status === 'completed' || status === 'failed' || status === 'cancelled';

}



function handleStatusUpdate(data) {

  if (!data || !data.status) return;

  const status = data.status;

  $('#retry-payment-btn').hidden=state.paymentMethod!=='razorpay' || status!=='pending_payment';

  const progress = data.printStatus;
  const done = progress === 'done' || (!progress && status === 'completed');
  $('#receipt-title').textContent = done ? 'Print Done' : (status === 'cancelled' ? 'Order cancelled' : 'Order received');

  const key = status + ':' + progress;

  if (key === state.lastKnownStatus) return;

  state.lastKnownStatus = key;

  if (progress) {

    setStatusBadge(progress);

    $('#conf-status').textContent = progress;

    const messages = {pending:'Waiting for pickup-code release at the shop touchscreen.',processing:'Processing your print job. The shop may need to confirm output.',printing:'Your job is being delivered to the printer.',failed:'Printer needs attention. Please check with the shop.',done:'Your document print is completed. Please collect your documents.',rejected:'Order cancelled.'};

    $('#order-summary').textContent = (messages[progress] || progress) + (status === 'pending_payment' ? ' Payment is still pending.' : '');

    return;

  }

  setStatusBadge(status);

  $('#conf-status').textContent = status;

  if (status === 'paid') {

    $('#order-summary').textContent = `Payment confirmed. Use your pickup code at the shop touchscreen once preparation is complete.`;

  } else if (status === 'dispatched') {

    $('#order-summary').textContent = `Print job sent to the printer. Please check at the counter for completion.`;

  } else if (status === 'completed') {

    $('#order-summary').textContent = `Your document print is completed. Please collect your documents.`;

  } else if (status === 'failed') {

    $('#order-summary').textContent = `Something went wrong with your order. Please ask the merchant for help.`;

  } else if (status === 'cancelled') {

    $('#order-summary').textContent = `Order cancelled.`;

  }

}



function setStatusBadge(status) {

  const badge = $('#conf-status-badge');

  if (!badge) return;

  badge.textContent = status;

  badge.className = `status-badge status-${status}`;

}



// resetState clears the in-memory state and DOM elements so a new

// order starts from a clean slate. The same handler is wired to the

// "Start a new order" button so the operator can keep accepting

// customers without reloading the page.

function resetState() {
  renderPickup(null);

  try { sessionStorage.removeItem('pc-last-order'); } catch {}

  for(const url of previewURLs.values())URL.revokeObjectURL(url);previewURLs.clear();

  sessionStorage.removeItem("pc-draft");state.uploadToken=null;quoteReady=false;

  state.uploadedFiles = [];

  documentSettings.clear();selectedDocument=null;

  for(const row of $$('.config-row'))row.disposePreview?.();

  $('#config-list').replaceChildren();

  state.orderId = null;

  state.shareToken = null;

  state.lastKnownStatus = null;

  $('#file-input').value = '';

  $('#file-list').replaceChildren();

  $('#upload-error').hidden = true;

  $('#order-error').hidden = true;

  $('#quote-error').hidden = true;

  $('#order-form').reset();

}



// -------------- binding --------------



window.addEventListener('DOMContentLoaded', () => {

  void loadHeaderBranding();
  void initializePortal();

  try {

    const previous = JSON.parse(sessionStorage.getItem('pc-last-order') || 'null');

    if (previous?.orderId) { state.orderId = previous.orderId; state.shareToken = previous.shareToken; state.uploadedFiles = previous.files || []; state.paymentMethod=previous.paymentMethod; showConfirmation(previous); }

  } catch {}



  $('#remember-phone').addEventListener('change',()=>{if(!$('#remember-phone').checked){try{localStorage.removeItem('pc-customer-phone');}catch{}}});
  const fileInput = $('#file-input');

  fileInput.addEventListener('click',event=>event.stopPropagation());
  const dropZone = $('#drop-zone');

  const sourceDialog=$('#upload-source-dialog');
  const openSources=()=>{if(!uploadBusy&&!placingOrder)sourceDialog.showModal();};
  $('#close-upload-source').onclick=()=>sourceDialog.close();
  $('#choose-source-files').onclick=()=>{sourceDialog.close();fileInput.click();};
  $('#choose-source-camera').onclick=()=>{sourceDialog.close();$('#camera-input').click();};
  $('#camera-input').onchange=event=>{void uploadFiles([...event.target.files]);event.target.value='';};
  dropZone.addEventListener('click', openSources);

  dropZone.addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); openSources(); } });

  fileInput.addEventListener('change', (e) => uploadFiles([...e.target.files]));

  dropZone.addEventListener('dragover', (e) => { e.preventDefault(); dropZone.classList.add('dragover'); });

  dropZone.addEventListener('dragleave', () => dropZone.classList.remove('dragover'));

  dropZone.addEventListener('drop', (e) => {

    e.preventDefault();

    dropZone.classList.remove('dragover');

    uploadFiles([...(e.dataTransfer.files || [])]);

  });

  $('#continue-btn').addEventListener('click', () => {

    if (state.uploadedFiles.length === 0) {

      $('#upload-error').textContent = 'Please upload at least one file first.';

      $('#upload-error').hidden = false;

      return;

    }

    renderConfig();

    showSection('config');

  });

  $('#printer-choice').addEventListener('change',async()=>{try{await loadPricing();renderConfig();}catch{}});

  $('#service-choice').addEventListener('change',async()=>{try{$('#printer-choice').value='';await loadPricing();renderConfig();}catch{}});

  for(const id of ['place-order-btn','pay-online-btn'])$('#'+id).addEventListener('click',()=>beginCheckout($('#'+id).value));

  $('#back-options-btn').addEventListener('click',()=>{if(!placingOrder)showSection('config');});

  for(const [id,delta] of [['copies-less',-1],['copies-more',1]])$('#'+id).addEventListener('click',()=>{

    const row=$$('.config-row').find(row=>row.dataset.documentId===selectedDocument);if(!row)return;const input=row.querySelector('[name="copies"]');input.value=String(Math.min(100,Math.max(1,Number(input.value)+delta)));input.dispatchEvent(new Event('change',{bubbles:true}));

  });

  $('#add-files-btn').addEventListener('click',openSources);

  $('#discard-draft-btn').addEventListener('click',async()=>{

    try{await readResponse(await fetch('/api/v1/portal/drafts/'+encodeURIComponent(state.orderId),{method:'DELETE',headers:{'X-Upload-Token':state.uploadToken}}));resetState();showSection('upload');}

    catch(err){$('#quote-error').textContent=err.message;$('#quote-error').hidden=false;}

  });

  $('#retry-payment-btn').addEventListener('click',retryPayment);

  $('#order-form').addEventListener('submit', (e) => {

    e.preventDefault();

    placeOrder(e.target,state.paymentMethod);

  });

});



async function retryPayment(){

 const button=$('#retry-payment-btn');button.disabled=true;$('#payment-error').hidden=true;

 try{const data=await postJSON(`/api/v1/portal/orders/${encodeURIComponent(state.orderId)}/pay`,{});if(!data.redirectUrl)throw new Error('Payment link is unavailable. Please retry.');window.location.href=data.redirectUrl;}

 catch(err){$('#payment-error').textContent=err.message;$('#payment-error').hidden=false;}

 finally{button.disabled=false;}

}



async function initializePortal(){

 try{

  await loadPricing();

  if(state.shareToken)return;

  const draft=JSON.parse(sessionStorage.getItem('pc-draft') || 'null');

  if(!draft?.orderId)return;

  const response=await fetch('/api/v1/portal/drafts/'+encodeURIComponent(draft.orderId),{headers:{'X-Upload-Token':draft.uploadToken},cache:'no-store'});

  if(response.status===404){sessionStorage.removeItem('pc-draft');return;}

  const data=await readResponse(response);

  state.orderId=data.orderId;state.uploadToken=data.uploadToken;state.uploadedFiles=data.files;

  for(const [id,settings] of Object.entries(draft.settings || {}))documentSettings.set(id,settings);

  selectedDocument=draft.selectedDocument;

  if(data.submitted){state.shareToken=data.shareToken;state.paymentMethod=data.paymentMethod;persistOrderReceipt(data);showConfirmation(data);}

  else if(data.files.length){renderUploadedFiles();showSection('config');renderConfig();}

 }catch(err){$('#upload-error').textContent=err.message;$('#upload-error').hidden=false;}

}


async function loadHeaderBranding(){
 try{
  const branding=await getJSON('/api/v1/portal/branding');
  const businessName=branding.businessName?.trim() || branding.title || 'Print Catalyst';
  const title=$('.brand strong');title.replaceChildren();title.textContent=businessName;title.hidden=false;
  const subtitle=$('.brand small');subtitle.textContent=branding.subtitle;subtitle.hidden=!branding.subtitle;
  if(branding.logo){const img=document.createElement('img');img.className='portal-header-logo';img.src=branding.logo;img.alt=businessName;img.onerror=()=>{img.remove();title.hidden=false;};title.before(img);title.hidden=true;}
  if(branding.icon){const img=document.createElement('img');img.src=branding.icon;img.alt='';const icon=$('.brand-icon');icon.replaceChildren(img);img.onerror=()=>{icon.textContent='▤';};}
  document.title=businessName+' · Upload & Print';
 }catch(error){console.warn('Portal branding unavailable');}
}

// XMLHttpRequest exposes actual bytes transferred; fetch does not expose upload progress.
function uploadWithProgress(url, body, token) {
  return new Promise((resolve,reject)=>{
    const xhr=new XMLHttpRequest();
    xhr.open('POST',url);xhr.timeout=10*60*1000;xhr.setRequestHeader('X-Upload-Token',token);
    xhr.upload.onprogress=event=>{
      if(!event.lengthComputable){$('#upload-meter').removeAttribute('value');$('#upload-percent').textContent='Uploading…';return;}
      const percent=Math.min(100,Math.floor(event.loaded/event.total*100));
      $('#upload-meter').value=percent;
      $('#upload-percent').textContent=percent===100?'100% uploaded · Preparing your documents…':`${percent}% uploaded`;
    };
    xhr.onload=()=>{
      if(xhr.status===0){reject(new Error('Upload interrupted. Check your connection and try again.'));return;}
      try{resolve(new Response(xhr.responseText,{status:xhr.status,headers:{'Content-Type':xhr.getResponseHeader('Content-Type')||'text/plain'}}));}catch(error){reject(error);}
    };
    xhr.ontimeout=()=>reject(new Error('Upload timed out. Refresh to recover your uploaded files before trying again.'));
    xhr.onerror=()=>reject(new Error('Upload interrupted. Check your connection and try again.'));
    xhr.onabort=()=>reject(new Error('Upload cancelled. Please try again.'));
    xhr.send(body);
  });
}

function printOptionIcon(value) {
  const drawings={
    monochrome:'<circle cx="12" cy="12" r="8" fill="white" stroke="currentColor"/><path d="M12 4a8 8 0 0 0 0 16Z" fill="currentColor"/>',
    colour:'<circle cx="9" cy="9" r="5" fill="#e94465"/><circle cx="15" cy="9" r="5" fill="#f5bc32" fill-opacity=".85"/><circle cx="12" cy="15" r="5" fill="#398ae9" fill-opacity=".85"/>',
    'one-sided':'<rect x="5" y="3" width="14" height="18" rx="2" fill="none" stroke="currentColor"/><path d="M9 8h6M9 12h6M9 16h4" stroke="currentColor"/>',
    'two-sided-long-edge':'<rect x="3" y="3" width="12" height="16" rx="2" fill="none" stroke="currentColor"/><path d="M18 6h3v15H9v-1M7 8h4M7 12h4" fill="none" stroke="currentColor"/>',
    'two-sided-short-edge':'<rect x="3" y="3" width="16" height="12" rx="2" fill="none" stroke="currentColor"/><path d="M6 18v3h15V9h-1M8 7v4M12 7v4" fill="none" stroke="currentColor"/>'
  };
  if(!drawings[value])return null;
  const icon=document.createElementNS('http://www.w3.org/2000/svg','svg');
  icon.setAttribute('viewBox','0 0 24 24');icon.setAttribute('aria-hidden','true');icon.setAttribute('focusable','false');icon.classList.add('print-option-icon');icon.innerHTML=drawings[value];return icon;
}

// Keep the recoverable draft if browser storage is unavailable or full.
// Receipt display and payment recovery must not depend on storage writes.
function persistOrderReceipt(receipt){
  try{sessionStorage.setItem('pc-last-order',JSON.stringify(receipt));sessionStorage.removeItem('pc-draft');return true;}
  catch{return false;}
}

// Store only the phone, with explicit consent, scoped to this browser origin.
function restoreCustomerPhone(){
 const input=$('#order-form [name="customerPhone"]');
 const label=$('#remember-phone-label'),check=$('#remember-phone');
 label.hidden=input.disabled;check.checked=false;
 if(input.disabled)return;
 try{
  const saved=JSON.parse(localStorage.getItem('pc-customer-phone') || 'null');
  if(saved && saved.expires>Date.now() && typeof saved.phone==='string' && saved.phone.length<=40){
   if(!input.value)input.value=saved.phone;
   check.checked=true;
  }else localStorage.removeItem('pc-customer-phone');
 }catch{}
}
function rememberCustomerPhone(phone){
 try{
  if($('#remember-phone').checked && phone){
   localStorage.setItem('pc-customer-phone',JSON.stringify({phone,expires:Date.now()+30*86400000}));
  }else localStorage.removeItem('pc-customer-phone');
 }catch{} // Private browsing or full storage must never block checkout.
}
