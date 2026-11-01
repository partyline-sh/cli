import { chromium } from '../../relay/node_modules/playwright/index.mjs';
import { readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
const documentSource = readFileSync(new URL('../src/ui/live-terminal/bundle.ts', import.meta.url), 'utf8');
const terminalHTML = JSON.parse(documentSource.split('export const terminalHTML = ')[1].trim().replace(/;$/, ''));
const screenshot = process.argv[2] || join(tmpdir(), 'partyline-live-terminal-prediction.png');
(async()=>{
 const browser=await chromium.launch({headless:true});
 try {
 const page=await browser.newPage({viewport:{width:390,height:600},deviceScaleFactor:2});
 const errors=[];page.on('pageerror',e=>errors.push(String(e)));
 await page.evaluate(()=>{window.events=[];window.ReactNativeWebView={postMessage:s=>window.events.push(JSON.parse(s))}});
 await page.setContent(terminalHTML);
 await page.waitForFunction(()=>window.events.some(e=>e.type==='loaded'));
 const screen={typing:true,foreground:'zsh',ansi:'\x1b[38;5;208m$ \x1b[0m',columns:40,rows:24,x:2,y:0,cursor:true,alternate:false,copy:false,acceptedSeq:0,echoSeq:0};
 const push=m=>page.evaluate(m=>window.liveReceive(m),m);
 await push({type:'ready'});await push({type:'screen',screen,revision:1});
 await page.waitForTimeout(80);await push({type:'focus'});
 await page.keyboard.type('a');await page.waitForTimeout(150);
 let events=await page.evaluate(()=>window.events.filter(e=>e.type==='input'));
 if(events.length!==1 || events[0].data!=='a')throw Error('input bridge failed '+JSON.stringify(events));
 await push({type:'screen',screen:{...screen,ansi:'$ a',x:3,acceptedSeq:1,echoSeq:1},revision:2});
 await page.waitForTimeout(50);await page.keyboard.type('b');await page.waitForTimeout(30);
 const predicted=await page.locator('.xterm-rows').first().innerText().then(s=>s.replace(/\u00a0/g,' '));
 if(!predicted.includes('$ ab'))throw Error('prediction not rendered '+JSON.stringify(predicted));
 await page.screenshot({path:screenshot});
 await page.waitForTimeout(150);
 await push({type:'screen',screen:{...screen,ansi:'$ ab',x:4,acceptedSeq:2,echoSeq:2},revision:3});
 await page.waitForTimeout(80);
 const echoed=await page.locator('.xterm-rows').first().innerText().then(s=>s.replace(/\u00a0/g,' '));
 if(echoed.includes('abb') || !echoed.includes('$ ab'))throw Error('double or missing echo '+echoed);
 await page.keyboard.type(' ');await page.waitForTimeout(150);
 await push({type:'screen',screen:{...screen,ansi:'$ ab',x:5,acceptedSeq:3,echoSeq:3},revision:4});
 await page.waitForTimeout(50);await page.keyboard.type('c');await page.waitForTimeout(30);
 const withSpace=await page.locator('.xterm-rows').first().innerText().then(s=>s.replace(/\u00a0/g,' '));
 if(!withSpace.includes('$ ab c'))throw Error('trailing blank cell did not confirm '+JSON.stringify(withSpace));
 await page.waitForTimeout(150);
 await push({type:'closed'});await page.keyboard.type('lost');await page.waitForTimeout(150);
 events=await page.evaluate(()=>window.events.filter(e=>e.type==='input'));
 if(events.length!==4)throw Error('closed terminal sent input '+JSON.stringify(events));
 // An old asynchronous parse must not win when a new session also starts at revision 1.
 await page.evaluate(screen => {
   const old = { ...screen, ansi: 'OLD SESSION '.repeat(5000), x: 2 };
   window.liveReceive({type:'ready'});
   window.liveReceive({type:'screen',screen:old,revision:1});
   window.liveReceive({type:'closed'});
   window.liveReceive({type:'ready'});
   window.liveReceive({type:'screen',screen:{...screen,ansi:'NEW SESSION',x:11},revision:1});
 },screen);
 await page.waitForTimeout(150);
 const reopened=await page.locator('.xterm-rows').first().innerText();
 if(!reopened.includes('NEW') || reopened.includes('OLD'))throw Error('old parse crossed reconnect generation '+reopened);
 if(errors.length)throw Error(errors.join('\n'));
 console.log(JSON.stringify({inputEvents:events,predictionVisible:true,doubleEcho:false,closedInputDiscarded:true,reconnectGeneration:true,screenshot:screenshot}));
 } finally { await browser.close(); }
})().catch(e=>{console.error(e);process.exit(1)});

await import('./check-stream-terminal.mjs');
