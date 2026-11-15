import test from 'node:test';
import assert from 'node:assert/strict';
import { previousRange, outcomeCount, change, dealComparisons, billingPage } from '../src/pages/sales-reporting/results.ts';

test('comparison uses the adjacent equal UTC period, including a whole year', () => {
  assert.deepEqual(previousRange('2026-11-11T10:00:00Z','2026-11-12T10:00:00Z'), {from:'2026-11-10T10:00:00.000Z',to:'2026-11-11T10:00:00.000Z'});
  assert.deepEqual(previousRange('2025-11-12T00:00:00Z','2026-11-12T00:00:00Z'), {from:'2024-11-12T00:00:00.000Z',to:'2025-11-12T00:00:00.000Z'});
  assert.throws(() => previousRange('bad','bad'));
});
test('growth handles declines and a zero baseline without inventing a percentage', () => {
  assert.equal(change(12,8).label,'+50% vs previous period');
  assert.equal(change(0,8).label,'−100% vs previous period');
  assert.equal(change(2,0).label,'Up from 0 · no percentage baseline');
  assert.equal(change(0,0).label,'No change');
  assert.equal(change(2,null).label,'Comparison unavailable');
});
test('an unavailable source or comparison is distinct from a recorded zero', () => {
  const data={source:{status:'missing'},totals:{sdr:2}};
  assert.equal(outcomeCount(data,'replied'),null);
  assert.equal(outcomeCount(data,'closed'),0);
  assert.equal(outcomeCount(undefined,'sdr'),null);
  assert.equal(outcomeCount({...data,source:{status:'connected'}},'replied'),0);
});
test('deal values preserve currencies, missing amounts and unavailable history', () => {
  const row=(currency,value)=>({currency,amount_minor:value,valued:value===null?0:1,unvalued:value===null?1:0});
  const current={results:{amounts:[row('EUR',1500),row('USD',null)]}};
  const prior={results:{amounts:[row('EUR',1000),row('GBP',700)]}};
  const values=dealComparisons(current,prior);
  assert.deepEqual(values.map(v=>v.currency),['EUR','GBP','USD']);
  assert.equal(values[0].current.amount_minor,1500);
  assert.equal(values[1].current.amount_minor,0);
  assert.equal(values[2].current.amount_minor,null);
  assert.equal(dealComparisons(current)[0].previous,undefined);
});
test('funding links follow the paying wallet and reject unknown targets', () => {
  assert.equal(billingPage('treg').url,'https://treg.to/app');
  assert.equal(billingPage('openrouter').url,'https://openrouter.ai/settings/credits');
  assert.equal(billingPage('blitz').action,'Manage plan');
  for(const value of [null,'unknown','__proto__','https://example.com'])assert.equal(billingPage(value),null);
});
