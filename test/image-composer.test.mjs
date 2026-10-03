import test from 'node:test';
import assert from 'node:assert/strict';
import {singlePageLayout} from '../runtime/internal/localserver/web/portal/image-composer.mjs';

test('Merge fits every supported selection on one page',()=>{
 for(let count=2;count<=10;count++){
  const layout=singlePageLayout(count);
  assert.equal(Math.ceil(count/layout),1);
  assert.ok([2,4,6,9,12].includes(layout));
 }
 assert.equal(singlePageLayout(2),2);
 for(const count of [0,1,11,2.5])assert.throws(()=>singlePageLayout(count));
});
