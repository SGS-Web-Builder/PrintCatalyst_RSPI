const $ = id => document.getElementById(id);
let code = '', busy = false, locked = false, until = 0, idle;
let receipt='',pollTimer,resetTimer,submitTimer;
let screenReady=true;
let otpEnabled=true,countdownEnabled=true,streamController,screenTimer,countdownTimer,phase='',screenGeneration=0,resetting=false;
function resetScreen(dismiss=true){clearTimeout(submitTimer);screenGeneration++;const old=dismiss?receipt:'';resetting=!!old;if(old)void post('dismiss',{receipt:old}).catch(()=>{}).finally(()=>{resetting=false;});streamController?.abort();phase='';$('progress-ring').hidden=true;$('success-countdown').hidden=true;if(typeof clearInterval==='function')clearInterval(countdownTimer);clearTimeout(pollTimer);clearTimeout(resetTimer);receipt='';try{sessionStorage.removeItem('pc-kiosk-receipt');}catch{} locked=false;code='';$('next').hidden=true;status('Enter the pickup code shown on your phone.');render();}
async function pollProgress(){
 const current=receipt;if(!current)return;if(typeof ReadableStream!=='undefined'){void streamProgress(current);return;}
 try{const {response,data}=await post('progress',{receipt:current});if(receipt!==current)return;
 const messages={preparing:'Processing your file…',pending:'Your file is ready. Waiting for the printer…',processing:'Printing your documents…',printing:'Printing your documents…',done:'Print Done. Please collect your documents.',assistance:'Print status needs review. Please ask the attendant.',failed:'Printer needs assistance. Please ask the attendant.',rejected:'Order cancelled. Please ask the attendant.'};
 showPhase(data.state,messages[data.state] || 'Unable to confirm printer progress. Please ask the attendant.');
 if(data.state==='done')return;
 if(!response.ok || ['failed','rejected','assistance','unavailable'].includes(data.state)){pollTimer=setTimeout(pollProgress,1000);return;}
 }catch{if(receipt!==current)return;status('Connection interrupted. Checking print progress—do not enter the code again.');}
 pollTimer=setTimeout(pollProgress,1500);
}
const status = message => { $('status').textContent = message; };
function render() {
 $('otp-controls').hidden=locked||!otpEnabled;
 if(!locked){$('pickup-heading').textContent=otpEnabled?'Enter your pickup OTP':'Automatic printing';$('pickup-intro').textContent=otpEnabled?'Enter the 4-digit code from your order receipt. It submits automatically.':'Scan the kiosk QR and follow the order instructions. Eligible orders print automatically when ready.';}
 $('digits').textContent = (code.padEnd(4, '—')).split('').join(' ');
 const disabled = busy || locked || !screenReady || !otpEnabled || Date.now() < until;
 document.querySelectorAll('#keypad button').forEach(b => b.disabled = disabled);
 $('release').disabled = disabled || code.length !== 4;

}
function edit(value) {
 if (busy || locked || !screenReady || !otpEnabled || Date.now() < until) return;
 clearTimeout(submitTimer);
 code = value.slice(0, 4); render();
 if(code.length===4){const entered=code;submitTimer=setTimeout(()=>{if(code===entered&&!busy&&!locked&&screenReady)void $('release').onclick();},0);}
 clearTimeout(idle); idle = setTimeout(() => { code = ''; render(); }, 30000);
}
async function post(path, body) {
 const controller = new AbortController();
 const timer = setTimeout(() => controller.abort(), 10000);
 try {
  const response = await fetch('/api/v1/kiosk/' + path, {method:'POST', credentials:'same-origin', cache:'no-store', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body), signal:controller.signal});
  if (response.status === 429) until = Date.now() + Math.max(1, Number(response.headers.get('Retry-After')) || 30) * 1000;
  return {response, data:await response.json()};
 } finally { clearTimeout(timer); }
}
document.querySelectorAll('[data-digit]').forEach(b => b.addEventListener('click', () => edit(code + b.dataset.digit)));
$('clear').onclick = () => edit('');
$('back').onclick = () => edit(code.slice(0,-1));
$('release').onclick = async () => {
 if (busy || locked || !screenReady || !otpEnabled || code.length !== 4 || Date.now() < until) return;
 clearTimeout(submitTimer);busy = true;showPhase('checking','Checking your order…');clearTimeout(idle); const submitted = code; code = ''; render(); status('Checking your order…');
 try {
  const {response, data} = await post('claim', {code:submitted});
  if (response.status === 202 && ['accepted','preparing'].includes(data.state)) {
   locked = true; $('next').hidden = false; showPhase(data.state,data.state==='preparing'?'Processing your file…':'Order released to the print queue. Please wait for your print.');
   if(typeof data.receipt==='string' && /^[a-f0-9]{64}$/.test(data.receipt)){receipt=data.receipt;try{sessionStorage.setItem('pc-kiosk-receipt',receipt);}catch{}pollTimer=setTimeout(pollProgress,0);}
  } else if (response.status === 403) {screenReady=false;$('screen-health').hidden=false;$('screen-health').textContent='Screen not paired. Ask the attendant to pair this browser before printing.';$('pair-screen-link').hidden=false;status('Your order was not released. This screen needs pairing.');}
  else if (response.status === 429) status('Too many attempts. Please wait for the countdown.');
  else if (data.state === 'invalid_code') status('Code unavailable. Check the code on your phone, or ask the attendant if already submitted.');
  else { locked = true; $('next').hidden = false; showPhase('assistance','We could not confirm release. Ask the attendant to check your order before trying again.'); }
 } catch {
  locked = true; $('next').hidden = false; showPhase('assistance','Connection interrupted. Your order may have been released. Ask the attendant to check; do not submit it again.');
 } finally { busy = false;if(!locked){$('progress-ring').hidden=true;phase='';}render(); }
};
$('next').onclick = resetScreen;

document.addEventListener('keydown', e => {
 if (e.target instanceof HTMLInputElement || e.ctrlKey || e.metaKey || e.altKey) return;
 if (/^[0-9]$/.test(e.key)) { e.preventDefault(); edit(code + e.key); }
 else if (e.key === 'Backspace') { e.preventDefault(); edit(code.slice(0,-1)); }
 else if (e.key === 'Escape') edit('');
 else if (e.key === 'Enter' && !e.repeat) { e.preventDefault(); $('release').click(); }
});
setInterval(() => {
 const remaining = Math.ceil((until - Date.now()) / 1000);
 $('release').textContent = remaining > 0 ? `Please wait ${remaining}s` : 'Print my order';
 render();
}, 500);
render();

try{const saved=sessionStorage.getItem('pc-kiosk-receipt');if(/^[a-f0-9]{64}$/.test(saved||'')){receipt=saved;locked=true;$('next').hidden=false;render();pollTimer=setTimeout(pollProgress,0);}}catch{}

function showPhase(state,message){
 status(message);const ring=$('progress-ring');ring.hidden=false;ring.className='progress-ring'+(state==='done'?' success':['assistance','failed','rejected','unavailable'].includes(state)?' attention':'');
 $('pickup-heading').textContent=state==='done'?'Success!':state==='preparing'?'Processing your file':state==='checking'?'Checking your order':['assistance','failed','rejected','unavailable'].includes(state)?'Please ask for assistance':'Printing your order';
 if(state==='done'&&phase!=='done'){
  const seconds=countdownEnabled?5:1.5;let left=5;$('success-countdown').hidden=!countdownEnabled;$('success-countdown').textContent='Next customer in '+left+' seconds';
  if(countdownEnabled)countdownTimer=setInterval(()=>{left=Math.max(0,left-1);$('success-countdown').textContent='Next customer in '+left+' seconds';},1000);
  resetTimer=setTimeout(resetScreen,seconds*1000);
 }
 phase=state;
}
async function streamProgress(current){
 streamController?.abort();const controller=new AbortController();streamController=controller;
 try{
  const response=await fetch('/api/v1/kiosk/events',{method:'POST',credentials:'same-origin',cache:'no-store',headers:{'Content-Type':'application/json'},body:JSON.stringify({receipt:current}),signal:controller.signal});
  if(!response.ok||!response.body)throw Error('Progress unavailable');const reader=response.body.getReader();const decoder=new TextDecoder();let buffer='';
  while(receipt===current){const part=await reader.read();if(part.done)break;buffer+=decoder.decode(part.value,{stream:true});let end;while((end=buffer.indexOf('\n\n'))>=0){const event=buffer.slice(0,end);buffer=buffer.slice(end+2);if(!event.startsWith('data: '))continue;const data=JSON.parse(event.slice(6));if(receipt!==current)return;countdownEnabled=data.countdownEnabled;if(data.state==='otp_required'){resetScreen(false);controller.abort();return;}
   const messages={preparing:'Processing your file…',pending:'Your file is ready. Waiting for the printer…',processing:'Printing your order…',printing:'Printing your order…',done:'Print Done. Please collect your documents.',assistance:'Print status needs review. Please ask the attendant.',failed:'Printer needs assistance. Please ask the attendant.',rejected:'Order cancelled. Please ask the attendant.'};
   showPhase(data.state,messages[data.state]||'Checking your print status…');
   if(data.state==='done'){controller.abort();return;}
   if(['assistance','failed','rejected'].includes(data.state)){controller.abort();pollTimer=setTimeout(pollProgress,1000);return;}
  }}
 }catch(e){if(receipt!==current||controller.signal.aborted)return;status('Connection interrupted. Checking print progress—do not enter the code again.');}
 if(receipt===current)pollTimer=setTimeout(pollProgress,250);
}
async function screenSettings(){
 try{const current=receipt,generation=screenGeneration;const {response,data}=await post('screen',{watching:!!current||resetting});
 if(!response.ok){if(response.status===403){screenReady=false;$('screen-health').hidden=false;$('screen-health').textContent='Screen not paired. Ask the attendant to pair this browser before printing.';$('pair-screen-link').hidden=false;render();}else {$('screen-health').hidden=false;$('screen-health').textContent='Kiosk connection unavailable. Please ask the attendant.';}return;}
 screenReady=true;$('screen-health').hidden=true;$('pair-screen-link').hidden=true;
 if(response.ok&&receipt===current&&generation===screenGeneration){
 if(typeof data.otpEnabled==='boolean'){otpEnabled=data.otpEnabled;countdownEnabled=data.countdownEnabled;render();if(!locked&&!otpEnabled)status('Eligible orders print automatically. Please collect your documents.');}
 if(!current&&!busy&&!locked&&!resetting&&data.state==='automatic'&&/^[a-f0-9]{64}$/.test(data.receipt||'')){receipt=data.receipt;locked=true;try{sessionStorage.setItem('pc-kiosk-receipt',receipt);}catch{}$('next').hidden=false;showPhase('preparing','Processing your file…');render();pollTimer=setTimeout(pollProgress,0);}
 }}catch{$('screen-health').hidden=false;$('screen-health').textContent='Cannot reach the kiosk service. Please ask the attendant.';}finally{screenTimer=setTimeout(screenSettings,1000);}
}
screenTimer=setTimeout(screenSettings,1000);
