import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import {readFileSync} from 'node:fs';
import {matchesMonitorDate,localDateValue} from '../runtime/internal/localserver/web/monitor-filters.mjs';

test('monitor avoids rebuilding unchanged orders and pauses background polling',async()=>{
  const elements=new Map();let scheduled=0,rebuilds=0;
  const element=id=>{if(!elements.has(id))elements.set(id,{value:id==='period'?'all':id==='filter'?'all':'',children:[],replaceChildren(){this.children=[];if(id==='orders')rebuilds++;},append(child){this.children.push(child);}});return elements.get(id);};
  const document={hidden:false,getElementById:element,createElement:()=>({}),addEventListener(){}};
  const context=vm.createContext({document,matchesMonitorDate,localDateValue,queueState:()=>'',AbortSignal,fetch:async()=>({ok:true,json:async()=>({csrfToken:'token',orders:[],businessName:'Test shop'})}),setTimeout:()=>{scheduled++;},clearTimeout(){},Date,JSON,Intl});
  const source=readFileSync(new URL('../runtime/internal/localserver/web/monitor.mjs',import.meta.url),'utf8').replace(/^import .*;\r?\n/gm,'').replace(/^void refresh\(\);\r?$/m,'');
  vm.runInContext(source+'\nglobalThis.refreshMonitor=refresh;',context);
  await context.refreshMonitor();await context.refreshMonitor();
  assert.equal(rebuilds,1);
  assert.equal(scheduled,2);
  document.hidden=true;await context.refreshMonitor();
  assert.equal(scheduled,2);
});
