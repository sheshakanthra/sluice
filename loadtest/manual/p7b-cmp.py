# usage: python loadtest/manual/p7b-cmp.py <run_id>... - one row per loadtest/results/<run_id>/summary.json (P7b Step 0)
import json,sys,os
cols=[('git','git_sha'),('req','load.requests'),('peakrps','load.peak_rps'),('err%','errors.rate'),('5xx','errors.5xx'),('to','errors.timeouts'),('ck50','latency_ms.checkout.p50'),('ck95','latency_ms.checkout.p95'),('hold95','latency_ms.hold.p95'),('ord95','latency_ms.order.p95'),('pay95','latency_ms.pay.p95'),('st95','latency_ms.status.p95'),('join95','latency_ms.join.p95'),('sess95','latency_ms.session.p95'),('sold_s','timing.sold_out_s'),('drain','timing.drain_s'),('lag99','generator.lag_p99_ms'),('TICKET','outcomes.TICKET'),('unres','outcomes.UNRESOLVED')]
def g(d,p):
  for k in p.split('.'): d=d.get(k,{}) if isinstance(d,dict) else None
  return d
print('run|'+'|'.join(c for c,_ in cols))
for r in sys.argv[1:]:
  f=os.path.join('loadtest/results',r,'summary.json')
  if not os.path.exists(f): print(r,'missing');continue
  d=json.load(open(f)); v=[g(d,p) for _,p in cols]
  v[3]=f'{v[3]*100:.4f}' if isinstance(v[3],float) else v[3]
  print(r+'|'+'|'.join(str(x) for x in v))
