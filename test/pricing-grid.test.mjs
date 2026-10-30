import {test} from 'node:test';
import assert from 'node:assert/strict';
import {pricingCombinations} from '../runtime/internal/localserver/web/pricing-grid.mjs';
test('pricing grid keeps paper, colour and duplex capabilities tied to each printer',()=>{
 const fleet=[{id:'mono',paperSizes:['A4'],colourModes:['monochrome'],sidesModes:['one-sided']},{id:'colour',paperSizes:['A3'],colourModes:['colour'],sidesModes:['two-sided-long-edge']}];
 assert.equal(pricingCombinations(fleet).length,12);
 assert.equal(pricingCombinations(fleet).filter(e=>e.paperSize==='A4').length,6);
 assert.deepEqual(pricingCombinations(fleet,'mono'),[{paperSize:'A4',colourMode:'monochrome',sides:'one-sided'}]);
 assert.deepEqual(pricingCombinations(fleet,'missing'),[]);
});

import {hasSharedPrices} from '../runtime/internal/localserver/web/pricing-grid.mjs';
test('first-time setup requires shared prices before optional overrides',()=>{
 assert.equal(hasSharedPrices({shared:{entries:[]}}),false);
 assert.equal(hasSharedPrices({shared:{entries:[{unitPriceMinor:400}]}}),true);
});
