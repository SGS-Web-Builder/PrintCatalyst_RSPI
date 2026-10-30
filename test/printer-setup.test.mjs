import {test} from 'node:test';
import assert from 'node:assert/strict';
import {enabledPapers} from '../runtime/internal/localserver/web/printer-setup.mjs';
test('only current positive paper confirmations enable customer sizes',()=>{
 const rows=[{capabilityType:'paper_size',capabilityKey:'A4',status:'confirmed'},{capabilityType:'paper_size',capabilityKey:'A3',status:'verified',invalidatedAt:123},{capabilityType:'paper_size',capabilityKey:'Letter',status:'failed'},{capabilityType:'duplex',capabilityKey:'long-edge',status:'verified'}];
 assert.deepEqual([...enabledPapers(rows)],['A4']);
});

import {canTestPaper} from '../runtime/internal/localserver/web/printer-setup.mjs';
import {pricingCombinations} from '../runtime/internal/localserver/web/pricing-grid.mjs';
test('CUPS A4 supports owner test and capability-limited printer overrides',()=>{
 assert.equal(canTestPaper({backend:'ipp'},{widthMm:210,heightMm:297}),true);
 assert.equal(canTestPaper({backend:'ipp'},{widthMm:0,heightMm:0}),false);
 assert.equal(canTestPaper({backend:'unknown'},{widthMm:210,heightMm:297}),false);
 assert.deepEqual(pricingCombinations([{id:'canon',paperSizes:['A4'],colourModes:['monochrome'],sidesModes:['one-sided']}],'canon'),[{paperSize:'A4',colourMode:'monochrome',sides:'one-sided'}]);
});
