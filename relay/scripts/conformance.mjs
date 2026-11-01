// Fresh local Worker; the shared Go suite never touches a deployed relay.
import { spawn } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import net from 'node:net';
const root=dirname(dirname(dirname(fileURLToPath(import.meta.url))));
const port=await new Promise(resolve=>{const server=net.createServer().listen(0,'127.0.0.1',()=>{const value=server.address().port;server.close(()=>resolve(value));});});
const data=await mkdtemp(join(tmpdir(),'ptln-worker-contract-'));
const worker=spawn(process.execPath,['node_modules/wrangler/bin/wrangler.js','dev','--local','--ip','127.0.0.1','--port',String(port),'--inspector-port','0','--compatibility-date','2026-06-02','--persist-to',data,'--var','INITIAL_MACHINE:11111111111111111111111111111111','--var','INITIAL_HASH:4f2e8d65483c641648cdb374ae9d8abd368d269e4ddffe092a8237b8162cddd6'],{cwd:join(root,'relay'),detached:true,stdio:['ignore','pipe','pipe']});
let output='';let failure;worker.stdout.on('data',b=>output=(output+b).slice(-12000));worker.stderr.on('data',b=>output=(output+b).slice(-12000));worker.on('error',e=>failure=e);
const origin=`http://127.0.0.1:${port}`;
let stopped=false;
function stop(){if(stopped)return;stopped=true;try{process.kill(-worker.pid,'SIGTERM')}catch{}}
process.once('SIGINT',stop);process.once('SIGTERM',stop);
try {
 const deadline=Date.now()+45000;
 for(;;){if(failure||worker.exitCode!==null)throw failure??new Error(output);try{const r=await fetch(origin,{signal:AbortSignal.timeout(1000)});if(r.ok)break;}catch{}if(Date.now()>deadline)throw new Error('Local Worker startup timed out: '+output);await new Promise(r=>setTimeout(r,150));}
 const code=await new Promise((resolve,reject)=>{const test=spawn('go',['test','./relay/conformance','-count=1','-v','-relay-origin',origin],{cwd:root,stdio:'inherit'});test.on('error',reject);test.on('exit',resolve);});
 if(code!==0)throw new Error('Worker protocol conformance failed');
} finally {
 stop();
 await new Promise(resolve=>{if(worker.exitCode!==null)return resolve();const timer=setTimeout(()=>{try{process.kill(-worker.pid,'SIGKILL')}catch{}resolve();},3000);worker.once('exit',()=>{clearTimeout(timer);resolve()});});
 await rm(data,{recursive:true,force:true});
}
