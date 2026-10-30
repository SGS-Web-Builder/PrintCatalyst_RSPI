// Development-only UI preview. No database, device adapters, network clients or workers.
import http from 'node:http';
import {readFile} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
const assets=new URL('../runtime/internal/localserver/web/',import.meta.url);
const origin='http://127.0.0.1:18086';
const flow={otpEnabled:true,countdownEnabled:true}, maintenance={paused:false,reason:''};
const printer={id:'demo-epson',displayName:'Epson EM-C800 · simulated',paperSizes:['A4'],colourModes:['monochrome','colour'],sidesModes:['one-sided','two-sided-long-edge','two-sided-short-edge']};
const entries=printer.colourModes.flatMap(colourMode=>printer.sidesModes.map(sides=>({paperSize:'A4',colourMode,sides,unitPriceMinor:colourMode==='colour'?1500:250,tiers:[]})));
let revision=1;
const books={all:{currency:'INR',currencyMinorUnits:2,derivedMinorUnits:2,currencySupported:true,entries}};
const audit=[];
const json=(res,data,status=200)=>{res.writeHead(status,{'Content-Type':'application/json','Cache-Control':'no-store'});res.end(JSON.stringify(data));};
const server=http.createServer(async(req,res)=>{
 try {
  if(req.headers.host!=='127.0.0.1:18086'||req.headers['sec-fetch-site']==='cross-site'||(req.headers.origin&&req.headers.origin!==origin)){res.writeHead(403);res.end('Loopback same-origin preview only');return;}
  const path=new URL(req.url,origin).pathname;
  if(path.startsWith('/api/')){
   let raw='';for await(const chunk of req){raw+=chunk;if(raw.length>65536){json(res,{error:'Uploads disabled in this preview'},413);return;}}
   const body=raw?JSON.parse(raw):{};
   const route=path.replace('/api/v1/remote/','');
   if(['login','logout'].includes(route))return json(res,{csrfToken:'local-demo'});
   if(route==='orders')return json(res,{csrfToken:'local-demo',businessName:'PrintCatalyst · Local demo',paperStock:[{name:printer.displayName,tracked:true,inserted:500,printed:124,remaining:376}],orders:['paid','dispatched','completed'].map((status,i)=>({id:'demo-order-'+(i+1),customerName:'Sample customer '+(i+1),customerPhone:'',currency:'INR',currencyMinorUnits:2,totalMinor:1500+i*250,createdAt:Math.floor(Date.now()/1000)-i*600,status,kioskMode:true,documents:[{filename:'Sample document '+(i+1)+'.pdf',printState:['pending','printing','done'][i]}],separators:[]}))});
   if(route==='kiosk-flow'){if(req.method==='PUT')for(const k of Object.keys(flow))if(typeof body[k]==='boolean')flow[k]=body[k];return json(res,flow);}
   if(route==='maintenance'){if(req.method==='PUT')Object.assign(maintenance,{paused:!!body.paused,reason:String(body.reason||'').slice(0,240)});return json(res,maintenance);}
   if(route==='status')return json(res,{...maintenance,storage:{availableBytes:24*1073741824,low:false},printers:[printer],telemetry:{[printer.id]:{state:'Idle · simulated',model:'Epson WorkForce Pro EM-C800 (sample)',checkedAt:Math.floor(Date.now()/1000),queued:0,reasons:[],supplies:[]}}});
   if(route.startsWith('pricing/grid/')){const scope=route.split('/').pop();if(req.method==='PUT'){books[scope]={...books.all,entries:body.entries};revision++;audit.unshift({action:'Demo pricing saved',scope});}return json(res,{revision,book:books[scope]||{...books.all,entries:[]},shared:books.all,fleet:[printer],printers:[printer],overrides:{}});}
   if(route==='revenue')return json(res,[{currency:'INR',currency_minor_units:2,gross_minor:5250,orders:3}]);
   if(route==='ads/campaigns')return json(res,{csrfToken:'local-demo',campaigns:[]});
   if(route==='audit')return json(res,audit);
   if(['commands','schedules','providers','refunds'].includes(route)&&req.method==='GET')return json(res,[]);
   res.writeHead(409,{'Content-Type':'text/plain'});res.end('Preview only: this operation needs a configured Pi. No action was performed.');return;
  }
  const name=path==='/'||path==='/remote/'?'remote.html':path==='/remote/ads/'?'ads-admin.html':path.slice(1);
  if(!/^[a-z0-9-]+\.(html|css|mjs)$/.test(name)){res.writeHead(404);res.end();return;}
  let content=await readFile(new URL(name,assets));
  if(name.endsWith('.html'))content=Buffer.from(content.toString().replace('<main>','<main><p class="notice"><strong>LOCAL PREVIEW — SAMPLE DATA ONLY.</strong> No Pi, printer, payments or ERS connected. OTP, countdown and prices change only this temporary demo. <a href="/remote/">Dashboard</a></p>').replace('Live connection to this Pi.','Simulated connection for local preview.'));
  res.writeHead(200,{'Content-Type':name.endsWith('.mjs')?'text/javascript':name.endsWith('.css')?'text/css':'text/html','Cache-Control':'no-store','X-Content-Type-Options':'nosniff'});res.end(content);
 }catch(error){res.writeHead(error.code==='ENOENT'?404:400);res.end('Preview request unavailable');}
});
server.listen(18086,'127.0.0.1',()=>console.log('Remote dashboard preview: '+origin+'/remote/'));
