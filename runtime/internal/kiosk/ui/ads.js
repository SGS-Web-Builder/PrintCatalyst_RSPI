// Advertising is independent of payment and print release. Local files only.
const image = document.getElementById('ad-image');
const video = document.getElementById('ad-video');
const fallback = document.getElementById('ad-fallback');

let signature = "", defaults = [], playlist = [], index = 0, timer, generation = 0, failures = 0;
let retryAt = 0;
function schedule(ms, callback) { clearTimeout(timer); if (!document.hidden) timer = setTimeout(callback, ms); }
function stopMedia() {
 clearTimeout(timer); generation++;
 video.onended = video.onerror = video.onloadeddata = null;
 image.onload = image.onerror = null;
 video.pause(); video.removeAttribute('src'); video.load();
 image.removeAttribute('src'); video.hidden = image.hidden = true;
}
function next() { if (playlist.length) { index = (index + 1) % playlist.length; show(); } }
function show() {
 stopMedia(); fallback.hidden = false;
 if (!playlist.length) return;
 const ad = playlist[index], current = generation;
 const failed = () => {
  if (current !== generation) return;
  failures++; retryAt = Date.now() + 30000; stopMedia(); fallback.hidden = false;
  // A broken playlist must not spin or consume CPU on the print appliance.
  if (failures < playlist.length) schedule(1000, next);
  else if (defaults.length && !playlist.every(ad => ad.default)) { playlist = defaults; index = 0; failures = 0; schedule(1000, show); }
 };
 const loaded = () => { if (current !== generation) return false; failures = 0; fallback.hidden = true; return true; };
 if (ad.type.startsWith('image/')) {
  image.onload = () => { if (loaded()) { image.hidden = false; schedule(Math.max(3, Math.min(300, Number(ad.seconds) || 10)) * 1000, next); } };
  image.onerror = failed; image.src = ad.url;
  schedule(15000, failed);
 } else {
  video.muted = true;
  video.onloadeddata = () => {
   if (!loaded()) return;
   video.hidden = false;
   if (!document.hidden) video.play().catch(failed);
   schedule(600000, next);
  };
  video.onerror = failed; video.onended = next; video.src = ad.url;
  schedule(15000, failed);
 }
}
async function refresh() {
 try {
  const response = await fetch('/ads/playlist', {cache:'no-store', signal:AbortSignal.timeout(5000)});
  if (!response.ok) return;
  const data = await response.json();
  if (!Array.isArray(data)) return;
  const safe = data.slice(0,50).filter(ad => ad && typeof ad.url === 'string' && /^\/ads\/[^/?#]+$/.test(ad.url) && /^(image\/(jpeg|png|webp)|video\/(mp4|webm))$/.test(ad.type));
  if (JSON.stringify(safe) !== signature || (retryAt && Date.now() >= retryAt)) { retryAt = 0; signature = JSON.stringify(safe); defaults = safe.filter(ad => ad.default); const active = safe.filter(ad => !ad.default); playlist = active.length ? active : defaults; index = 0; failures = 0; show(); }
 } catch { /* Keep playing cached playlist when the local server is unavailable. */ }
}

document.addEventListener('visibilitychange', () => {
 if (document.hidden) { clearTimeout(timer); video.pause(); }
 else show();
});
refresh(); setInterval(refresh, 5000);
