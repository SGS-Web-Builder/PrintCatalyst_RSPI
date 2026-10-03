// Choose the smallest supported grid that keeps every selected image on one sheet.
export function singlePageLayout(count) {
  if(!Number.isInteger(count) || count<2 || count>10)throw new Error('Select 2–10 images.');
  return [2,4,6,9,12].find(size=>size>=count);
}
export function createImageComposer(api) {

  const selected = new Set();

  let urls = [], generation = 0;

  const clear = () => { generation++; urls.forEach(URL.revokeObjectURL); urls=[]; };

  function render() {

    const root=document.getElementById('image-composer');

    if(!root)return;

    clear();root.replaceChildren();

    const files=api.files();

    const images=files.filter(f=>['image/jpeg','image/png'].includes(f.mimeType));

    for(const id of selected)if(!files.some(f=>f.documentId===id))selected.delete(id);

    if(files.length<2)return;

    root.innerHTML=`<section class="image-tools">

      <div class="image-pick-actions"><button type="button" data-all>Select all</button><span data-count aria-live="polite"></span><button type="button" data-merge hidden>Merge images</button><button type="button" data-settings hidden title="Apply the current document�s print settings to all documents">Apply settings</button></div>

      <details data-layout-options hidden><summary>More layout options</summary><button type="button" data-collage>Photo collage</button></details>

      <div class="composition-editor" hidden></div><p class="composition-message" role="status" aria-live="polite"></p></section>`;

    const editor=root.querySelector('.composition-editor'),message=root.querySelector('.composition-message');

    const checks=[...document.querySelectorAll('#document-tabs input[type="checkbox"]')];

    function update(){

      root.querySelector('[data-all]').textContent=selected.size===files.length?'Clear selection':'Select all';

      root.querySelector('[data-count]').textContent=selected.size?`${selected.size} of ${files.length} selected`:'Select images to merge';

      const canMerge=selected.size>=2 && images.filter(f=>selected.has(f.documentId)).length===selected.size;

      root.querySelectorAll('[data-merge],[data-collage]').forEach(b=>b.disabled=!canMerge);

      root.querySelector('[data-merge]').hidden=!canMerge;
      root.querySelector('[data-layout-options]').hidden=!canMerge;
      root.querySelector('[data-settings]').hidden=selected.size<2;
      root.querySelector('[data-settings]').disabled=selected.size<2;

      checks.forEach(input=>input.checked=selected.has(input.dataset.documentId));editor.hidden=true;clear();

    }

    checks.forEach(input=>input.onchange=()=>{input.checked?selected.add(input.dataset.documentId):selected.delete(input.dataset.documentId);update();});

    root.querySelector('[data-all]').onclick=()=>{const all=selected.size===files.length;files.forEach(f=>all?selected.delete(f.documentId):selected.add(f.documentId));update();};

    root.querySelector('[data-settings]').onclick=()=>{api.applySettings(files.map(file=>file.documentId));};

    update();

    function open(collage){

      clear();message.textContent='';editor.hidden=false;

      editor.innerHTML=`<h3>${collage?'Photo collage':'Merge into one document'}</h3>

        <p>${collage?'Fit several photos on each page. Photos keep their proportions without cropping.':'One image on each page, in the order listed above.'}</p>

        <div class="composition-options"><label>Paper size<select data-paper></select></label><label>Page orientation<select data-orientation><option value="portrait">Portrait</option><option value="landscape">Landscape</option></select></label></div>

        <label ${collage?'':'hidden'}>Photos per page<select data-layout>${(collage?[2,4,6,9,12,16]:[1]).map(n=>`<option value="${n}">${n} per page</option>`).join('')}</select></label>

        <label data-split-label ${collage?'':'hidden'}>Two-photo split: <output>50 / 50</output><input data-split type="range" min="20" max="80" value="50" step="5"></label>

        <div class="composition-layout-preview" aria-label="Layout preview"></div><p data-summary></p>

        <p class="help">Applying replaces the selected images in this order with one PDF. Originals on your device are unchanged. Review the print preview and updated price before confirming.</p>

        <div class="image-tool-actions"><button type="button" data-apply>Apply ${collage?'collage':'merge'}</button><button type="button" data-cancel>Cancel</button></div>`;

      const paper=editor.querySelector('[data-paper]');

      const supported=new Set(['A4','A3','A5','LETTER','LEGAL']);

      const available=(api.paperSizes()||[]).filter(p=>supported.has(p.toUpperCase()));

      for(const p of available)paper.add(new Option(p,p));

      const apply=editor.querySelector('[data-apply]');apply.disabled=!available.length;

      if(!available.length)message.textContent='The shop needs an enabled A4, A3, A5, Letter or Legal paper size for this feature.';

      const files=images.filter(f=>selected.has(f.documentId));

      const layout=editor.querySelector('[data-layout]'),orientation=editor.querySelector('[data-orientation]'),split=editor.querySelector('[data-split]'),preview=editor.querySelector('.composition-layout-preview');

      const thumbs=[];

      function draw(){

        const n=Number(layout.value),landscape=orientation.value==='landscape';

        let [cols,rows]=({1:[1,1],2:[1,2],4:[2,2],6:[2,3],9:[3,3],12:[3,4],16:[4,4]})[n];if(landscape)[cols,rows]=[rows,cols];

        const sizes={A4:[210,297],A3:[297,420],A5:[148,210],LETTER:[216,279],LEGAL:[216,356]};const size=sizes[paper.value.toUpperCase()]||sizes.A4;

        preview.style.aspectRatio=landscape?`${size[1]} / ${size[0]}`:`${size[0]} / ${size[1]}`;

        preview.style.gridTemplateColumns=`repeat(${cols},minmax(0,1fr))`;preview.style.gridTemplateRows=`repeat(${rows},minmax(0,1fr))`;

        if(n===2)preview.style[landscape?'gridTemplateColumns':'gridTemplateRows']=`${split.value}fr ${100-Number(split.value)}fr`;

        editor.querySelector('[data-split-label]').hidden=n!==2;editor.querySelector('output').textContent=`${split.value} / ${100-Number(split.value)}`;

        preview.replaceChildren();

        for(let i=0;i<Math.min(n,files.length);i++){const cell=document.createElement('div');if(thumbs[i]){const image=document.createElement('img');image.src=thumbs[i];image.alt=files[i].originalFilename;cell.append(image);}else cell.textContent=`Photo ${i+1}`;preview.append(cell);}

        const pages=Math.ceil(files.length/n);editor.querySelector('[data-summary]').textContent=`${files.length} images → ${pages} print ${pages===1?'page':'pages'}. ${pages>1?'First page shown. Remaining images continue on the next page.':''}`;

      }

      for(const control of [paper,layout,orientation,split])control.oninput=draw;

      draw();const current=generation;

      files.forEach(async(file,i)=>{try{const blob=await api.image(file);if(current!==generation)return;const url=URL.createObjectURL(blob);urls.push(url);thumbs[i]=url;draw();}catch{if(current===generation)message.textContent='Some thumbnails could not load. You can retry by reopening the layout.';}});

      editor.querySelector('[data-cancel]').onclick=()=>{editor.hidden=true;clear();};

      apply.onclick=async()=>{

        const controls=[...root.querySelectorAll('button,input,select'),...checks];controls.forEach(el=>el.disabled=true);message.textContent='Creating your document…';root.setAttribute('aria-busy','true');

        try{await api.apply({documentIds:files.map(f=>f.documentId),perPage:Number(layout.value),paperSize:paper.value,orientation:orientation.value,split:Number(split.value)});selected.clear();}

        catch(error){message.textContent=error.message;controls.forEach(el=>el.disabled=false);}

        finally{root.removeAttribute('aria-busy');}

      };

    }

    root.querySelector('[data-merge]').onclick=async()=>{

      const defaults=api.settings?.() || {};

      const paper=(api.paperSizes()||[]).find(p=>p===defaults.paperSize) || (api.paperSizes()||[])[0];

      if(!paper){message.textContent='No paper size is available.';return;}

      const controls=[...root.querySelectorAll('button,input,select'),...checks];

      controls.forEach(el=>el.disabled=true);message.textContent='Merging images…';

      try{await api.apply({documentIds:images.filter(f=>selected.has(f.documentId)).map(f=>f.documentId),perPage:singlePageLayout(selected.size),paperSize:paper,orientation:defaults.orientation==='landscape'?'landscape':'portrait',split:50});selected.clear();}

      catch(error){controls.forEach(el=>el.disabled=false);update();message.textContent=error.message;}

    };

    root.querySelector('[data-collage]').onclick=()=>open(true);

  }

  return {render};

}
