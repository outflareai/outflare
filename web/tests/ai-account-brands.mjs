import { chromium } from 'playwright';
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {createServer} from 'node:net';
import {fileURLToPath} from 'node:url';
const a='10000000-0000-0000-0000-000000000001',b='10000000-0000-0000-0000-000000000002',c='20000000-0000-0000-0000-000000000001';
const web = fileURLToPath(new URL('../', import.meta.url));
const port = await new Promise(resolve => {
 const listener = createServer(); listener.listen(0,'127.0.0.1',()=>{const port=listener.address().port;listener.close(()=>resolve(port));});
});
const origin = `http://127.0.0.1:${port}`;
const server = spawn(process.execPath,[fileURLToPath(new URL('../node_modules/vite/bin/vite.js',import.meta.url)),'--host','127.0.0.1','--port',String(port),'--strictPort'],{cwd:web,stdio:'pipe'});
let output='';server.stdout.on('data',b=>{output+=b});server.stderr.on('data',b=>{output+=b});
let browser;
try {
 let ready=false;
 for(let n=0;n<100;n++) {try {ready=(await fetch(origin)).ok;if(ready)break;}catch{}await new Promise(r=>setTimeout(r,100));}
 assert(ready,`Vite did not start: ${output}`);
 browser=await chromium.launch({headless:true,args:['--no-sandbox']});
 const page=await browser.newPage({viewport:{width:1280,height:900}});
 const errors=[];page.on('pageerror',e=>errors.push(e.message));
 const queries=[];const writes=[];let assigned=[];let approvedPath;
 const account=(ws)=>({id:ws==='workspace-a'?a:c,provider:'claude',name:'Subscription',account_email:'subscription@example.com',plan:'Pro',seats:null,paid_by:'company',billing_cycle:'monthly',renews_on:null,price_cents:2000,price_hidden:false,resets_available:null,cancelled:false,owner_left:false,brand_ids:ws==='workspace-a'?assigned:[],members:[{user_id:'user-a',name:'Owner',email:'owner@example.com',role:'owner',left:false}],usage:[],reporting_devices:[],created_at:'2026-01-01T00:00:00Z',updated_at:'2026-01-01T00:00:00Z'});
 await page.addInitScript(()=>localStorage.setItem('outflare.brand.workspace-a','acme'));
 await page.route('**/v1/**',async route=>{
  const req=route.request();const u=new URL(req.url());const ws=u.pathname.includes('workspace-b')?'workspace-b':'workspace-a';
  let body={};let status=200;
  if(u.pathname.endsWith('/brands')) {
   if(ws==='workspace-b') await new Promise(r=>setTimeout(r,750));
   body={data:ws==='workspace-a'?[{id:a,name:'Acme',handle:'acme',status:'active'},{id:b,name:'Northpeak',handle:'northpeak',status:'active'}]:[{id:c,name:'Other brand',handle:'other',status:'active'}]};
  } else if(req.method()==='POST' && u.pathname.endsWith('/ai-devices')) {approvedPath=u.pathname;body={name:'Test computer',platform:'macos'};status=201;}
  else if(req.method()==='PATCH') { const input=req.postDataJSON();writes.push(input);assigned=input.brand_ids;body=account(ws); }
  else if(u.pathname.endsWith('/ai-accounts')) {queries.push(u.searchParams.get('brand'));body={data:[account(ws)]};}
  await route.fulfill({status,contentType:'application/json',body:JSON.stringify(body)});
 });
 await page.goto(`${origin}/tests/fixtures/ai-account-brands.html`);
 await page.getByRole('heading',{name:'Assign Subscription',exact:true}).waitFor();
 assert.equal(await page.getByRole('checkbox',{name:'Acme',exact:true}).isChecked(),false);
 assert.equal(await page.getByRole('checkbox',{name:'Northpeak',exact:true}).isChecked(),false);
 assert.equal(await page.getByRole('button',{name:'Save brands',exact:true}).isDisabled(),true);
 await page.getByRole('checkbox',{name:'Northpeak',exact:true}).check();
 await page.getByRole('button',{name:'Save brands',exact:true}).click();
 await page.getByRole('heading',{name:'Assign Subscription',exact:true}).waitFor({state:'hidden'});
 assert.deepEqual(writes,[{brand_ids:[b]}]);
 await page.getByRole('cell',{name:'Northpeak',exact:true}).waitFor();
 await page.waitForTimeout(2200);
 assert(queries.length>=2);assert(queries.every(q=>q==='all'));
 await page.evaluate(()=>window.switchWorkspace('workspace-b'));
 await page.waitForFunction(()=>window.firstFrame?.['workspace-b']!==undefined);
 const first=await page.evaluate(()=>window.firstFrame['workspace-b']);
 assert(!first.includes('Acme')&&!first.includes('Northpeak'),'previous workspace brands appeared in first committed frame');
 await page.getByRole('checkbox',{name:'Other brand',exact:true}).waitFor();
 assert.equal(await page.getByRole('checkbox',{name:'Other brand',exact:true}).isChecked(),false);
 await page.getByRole('button',{name:'Assign later',exact:true}).click();
 await page.waitForTimeout(2200);
 assert.equal(await page.getByRole('heading',{name:'Assign Subscription',exact:true}).count(),0,'deferred assignment reopened during polling');
 await page.evaluate(()=>{delete window.firstFrame['workspace-a'];window.switchWorkspace('workspace-a');});
 await page.waitForFunction(()=>window.firstFrame?.['workspace-a']!==undefined);
 const returning=await page.evaluate(()=>window.firstFrame['workspace-a']);
 assert(!returning.includes('Other brand'),'previous workspace brand appeared when returning');
 await page.route('**/acme/ai-accounts',route=>route.fulfill({status:200,contentType:'text/html',body:'<h1>Subscription assignment</h1>'}));
 await page.goto(`${origin}/tests/fixtures/ai-account-brands.html?connect=1&code=TEST-CODE`);
 await page.getByRole('button',{name:'Link computer',exact:true}).click();
 await page.waitForURL('**/acme/ai-accounts');
 assert.equal(approvedPath,'/v1/workspaces/workspace-a/ai-devices');
 assert.equal(new URL(page.url()).searchParams.has('brand'),false);
 assert.deepEqual(errors,[]);
 console.log('PASS: explicit brand choice, brand-only saves, workspace reporting, deferred prompts, workspace switching and onboarding redirect.');
} finally {await browser?.close();server.kill();}
