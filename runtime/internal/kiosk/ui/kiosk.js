const $ = id => document.getElementById(id);
let code = '', busy = false, locked = false, until = 0, idle;
const status = message => { $('status').textContent = message; };
function render() {
 $('digits').textContent = (code.padEnd(4, '—')).split('').join(' ');
 const disabled = busy || locked || Date.now() < until;
 document.querySelectorAll('#keypad button').forEach(b => b.disabled = disabled);
 $('release').disabled = disabled || code.length !== 4;
 $('pair').disabled = busy || Date.now() < until;
}
function edit(value) {
 if (busy || locked || Date.now() < until) return;
 code = value.slice(0, 4); render();
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
 if (busy || locked || code.length !== 4 || Date.now() < until) return;
 busy = true; clearTimeout(idle); const submitted = code; code = ''; render(); status('Checking your order…');
 try {
  const {response, data} = await post('claim', {code:submitted});
  if (response.status === 202 && data.state === 'accepted') {
   locked = true; $('next').hidden = false; status('Order released to the print queue. Please wait for your print.');
  } else if (data.state === 'preparing') status('Your files are still being prepared. Please wait, then enter your code again.');
  else if (response.status === 403) { status('This screen needs merchant pairing. Please ask the attendant.'); $('setup').open = true; }
  else if (response.status === 429) status('Too many attempts. Please wait for the countdown.');
  else if (data.state === 'invalid_code') status('Code unavailable. Check the code on your phone, or ask the attendant if already submitted.');
  else { locked = true; $('next').hidden = false; status('We could not confirm release. Ask the attendant to check your order before trying again.'); }
 } catch {
  locked = true; $('next').hidden = false; status('Connection interrupted. Your order may have been released. Ask the attendant to check; do not submit it again.');
 } finally { busy = false; render(); }
};
$('next').onclick = () => { locked = false; code = ''; $('next').hidden = true; status('Enter the pickup code shown on your phone.'); render(); };
$('pair').onclick = async () => {
 if (busy || Date.now() < until) return;
 const ticket = $('ticket').value.trim().toLowerCase(); $('ticket').value = '';
 if (!/^[a-f0-9]{12}$/.test(ticket)) { $('pair-status').textContent = 'Enter the 12-character pairing code from the local administrator.'; return; }
 busy = true; render();
 try {
  const {response, data} = await post('session', {ticket});
  $('pair-status').textContent = response.ok && data.state === 'paired' ? 'Screen paired for 12 hours.' : 'Pairing failed or expired. Check the code with the administrator.';
  if (response.ok && data.state === 'paired') { $('setup').open = false; status('Screen ready. Enter your pickup code.'); }
 } catch { $('pair-status').textContent = 'Connection interrupted. Request a new pairing code.'; }
 finally { busy = false; render(); }
};
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
