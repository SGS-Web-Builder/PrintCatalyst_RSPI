import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import fs from 'node:fs';
const source=fs.readFileSync(new URL('../runtime/internal/kiosk/ui/ads.js',import.meta.url),'utf8');
async function player(items){
 const el=()=>({hidden:true,textContent:'',setAttribute(){},removeAttribute(){},addEventListener(type,fn){this[type]=fn;},pause(){},load(){},play(){return Promise.resolve();}});
 const image=el(),video=el(),fallback=el(),pause=el(),timers=new Map();let id=0,refresh,now=0;
 const document={hidden:false,getElementById:key=>({'ad-image':image,'ad-video':video,'ad-fallback':fallback,'ad-pause':pause}[key]),addEventListener(type,fn){this[type]=fn;}};
 vm.runInNewContext(source,{Date:{now:()=>now},document,matchMedia:()=>({matches:false}),fetch:async()=>({ok:true,json:async()=>items}),AbortSignal:{timeout(){}},setTimeout:(fn,ms)=>{timers.set(++id,{fn,ms});return id;},clearTimeout:id=>timers.delete(id),setInterval(fn){refresh=fn;}});
 await new Promise(resolve=>setImmediate(resolve));
 return {image,video,fallback,document,async refreshAfter(ms){now+=ms;await refresh();},tick(ms){const found=[...timers].find(([,v])=>v.ms===ms);assert.ok(found);timers.delete(found[0]);found[1].fn();},timers};
}
test('image followed by muted video, ended advances without print calls',async()=>{
 const ui=await player([{url:'/ads/1.png',type:'image/png'},{url:'/ads/2.mp4',type:'video/mp4'}]);
 ui.image.onload();assert.equal(ui.image.hidden,false);ui.tick(10000);
 assert.equal(ui.video.src,'/ads/2.mp4');assert.equal(ui.video.muted,true);ui.video.onloadeddata();assert.equal(ui.video.hidden,false);
 ui.video.onended();assert.equal(ui.image.src,'/ads/1.png');
});
test('unsupported or external media ignored; broken playlist stops on fallback',async()=>{
 const ui=await player([{url:'https://example.com/ad.mp4',type:'video/mp4'},{url:'/ads/bad.mp4',type:'video/mp4'}]);
 ui.video.onerror();assert.equal(ui.fallback.hidden,false);assert.equal(ui.timers.size,0);
});
test('hidden screen stops image advance and resumes when visible',async()=>{
 const ui=await player([{url:'/ads/1.png',type:'image/png'}]);ui.image.onload();ui.document.hidden=true;ui.document.visibilitychange();assert.equal(ui.timers.size,0);
 ui.document.hidden=false;ui.document.visibilitychange();ui.image.onload();assert.ok(ui.timers.size>0);
});

test('default ad is used only outside the scheduled rotation, or after active media fails',async()=>{
 const ui=await player([{url:'/ads/broken.mp4',type:'video/mp4'},{url:'/ads/default.png',type:'image/png',default:true}]);
 assert.equal(ui.video.src,'/ads/broken.mp4');ui.video.onerror();ui.tick(1000);assert.equal(ui.image.src,'/ads/default.png');ui.image.onload();assert.equal(ui.fallback.hidden,true);
 const fallbackOnly=await player([{url:'/ads/default.png',type:'image/png',default:true,seconds:20}]);fallbackOnly.image.onload();fallbackOnly.tick(20000);assert.equal(fallbackOnly.image.src,'/ads/default.png');
});

test('scheduled media retries after fallback without interrupting every poll',async()=>{
 const ui=await player([{url:'/ads/retry.mp4',type:'video/mp4'},{url:'/ads/default.png',type:'image/png',default:true}]);
 ui.video.onerror();ui.tick(1000);ui.image.onload();
 ui.video.src='not-retried';await ui.refreshAfter(5000);assert.equal(ui.video.src,'not-retried');
 await ui.refreshAfter(25000);assert.equal(ui.video.src,'/ads/retry.mp4');ui.video.onloadeddata();
 await ui.refreshAfter(30000);assert.equal(ui.video.hidden,false);
});
