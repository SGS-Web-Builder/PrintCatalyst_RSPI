import test from 'node:test';
import assert from 'node:assert/strict';
import {matchesMonitorDate,localDateValue} from '../runtime/internal/localserver/web/monitor-filters.mjs';
const now=new Date(2026,8,29,14);
const stamp=(day,hour=12)=>new Date(2026,8,day,hour).getTime()/1000;
test('monitor today uses local calendar boundaries',()=>{
  assert.equal(matchesMonitorDate(stamp(29,0),'today','',now),true);
  assert.equal(matchesMonitorDate(stamp(28,23),'today','',now),false);
  assert.equal(matchesMonitorDate(stamp(30,0),'today','',now),false);
});
test('monitor week starts Monday and excludes next week',()=>{
  assert.equal(matchesMonitorDate(stamp(28),'week','',now),true);
  assert.equal(matchesMonitorDate(stamp(27),'week','',now),false);
  assert.equal(matchesMonitorDate(new Date(2026,9,5).getTime()/1000,'week','',now),false);
});
test('monitor specific date and all orders',()=>{
  assert.equal(localDateValue(now),'2026-09-29');
  assert.equal(matchesMonitorDate(stamp(10),'date','2026-09-10',now),true);
  assert.equal(matchesMonitorDate(stamp(10),'date','',now),false);
  assert.equal(matchesMonitorDate(stamp(10),'date','2026-09-11',now),false);
  assert.equal(matchesMonitorDate(stamp(10),'all','',now),true);
});
