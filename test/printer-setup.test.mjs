import {test} from 'node:test';
import assert from 'node:assert/strict';
import {enabledPapers} from '../runtime/internal/localserver/web/printer-setup.mjs';
test('only current positive paper confirmations enable customer sizes',()=>{
 const rows=[{capabilityType:'paper_size',capabilityKey:'A4',status:'confirmed'},{capabilityType:'paper_size',capabilityKey:'A3',status:'verified',invalidatedAt:123},{capabilityType:'paper_size',capabilityKey:'Letter',status:'failed'},{capabilityType:'duplex',capabilityKey:'long-edge',status:'verified'}];
 assert.deepEqual([...enabledPapers(rows)],['A4']);
});
