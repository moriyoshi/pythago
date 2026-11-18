import { chromium } from 'playwright';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { pathToFileURL, fileURLToPath } from 'node:url';
import path from 'node:path';
import fs from 'node:fs';
import { movieDuration, movieFromArgs, movieSrt } from './subtitles.mjs';
import { speechTrack } from './speech.mjs';
const args = process.argv.slice(2);
function option(name, fallback) { const i=args.indexOf(name); if(i<0)return fallback; if(i+1>=args.length || args[i+1].startsWith('--')) throw Error(`Missing value for ${name}`); return args[i+1]; }
if(args.includes('--help')) { console.log('Usage: node render.mjs --output movie.mp4 [--movie introduction|internals|native] [--audio narration.wav] [--fps 30] [--width 1920] [--crf 18] [--duration seconds] [--start 0] [--no-captions] [--voice-credit | --voice-credit=ryan]'); process.exit(0); }
const movie = movieFromArgs(args);
const end = movieDuration(movie);
const out=option('--output', movie === 'introduction' ? 'pythago-architecture.mp4' : `pythago-${movie}.mp4`), audio=option('--audio',null);
const fps=Number(option('--fps','30')), width=Number(option('--width','1920')), start=Number(option('--start','0')), duration=Number(option('--duration',String(end-start)));
if(!Number.isInteger(fps)||fps<1||fps>60||!Number.isInteger(width)||width<320||width%32||!Number.isFinite(start)||start<0||!Number.isFinite(duration)||duration<=0||start+duration>end) throw Error(`Require fps 1–60, width divisible by 32 and >= 320, and a clip within 0–${end} seconds.`);
const crf = Number(option('--crf', '18'));
if (!Number.isInteger(crf) || crf < 0 || crf > 51) throw Error('CRF must be an integer from 0 to 51.');
if(!out.toLowerCase().endsWith('.mp4')) throw Error('Output must have an .mp4 extension.');
if(Math.round(duration*fps)<1) throw Error('Clip is shorter than one frame.');
if(audio && !fs.existsSync(audio)) throw Error(`Audio file not found: ${audio}`);
const height=width*9/16;
const ffargs=['-y','-loglevel','warning','-f','image2pipe','-vcodec','png','-framerate',String(fps),'-i','pipe:0'];
if(audio) ffargs.push('-ss',String(start),'-i',audio,'-map','0:v:0','-map','1:a:0','-af','apad','-t',String(duration),'-c:a','aac','-b:a','192k');
const voiceCredit = args.includes('--voice-credit=ryan') ? 'ryan' : args.includes('--voice-credit') ? '1' : null;
if (voiceCredit) {
  const attribution = voiceCredit === 'ryan'
    ? 'Synthesized voice: Piper en_US-ryan-high, trained on RyanSpeech (Zandie et al.). Dataset CC BY-NC-SA 4.0 per model card: https://huggingface.co/rhasspy/piper-voices/blob/main/en/en_US/ryan/high/MODEL_CARD'
    : 'Synthesized voice from Cori Samuel / Hi-Fi TTS, NVIDIA; CC BY 4.0 https://creativecommons.org/licenses/by/4.0/';
  ffargs.push('-metadata', `comment=${attribution}`);
}
ffargs.push('-c:v','libx264','-preset','medium','-crf',String(crf),'-pix_fmt','yuv420p','-movflags','+faststart',out);
const root=fileURLToPath(new URL('.',import.meta.url));
const speech = audio ? speechTrack(audio, end) : null;
const browser=await chromium.launch({headless:true, ...(process.env.PYTHAGO_CHROMIUM ? {executablePath:process.env.PYTHAGO_CHROMIUM} : {})});
let ff;
try {
  const page=await browser.newPage({viewport:{width,height},deviceScaleFactor:1});
  if (speech) await page.addInitScript(track => { window.PYTHAGO_SPEECH = track; }, speech);
  const url = pathToFileURL(path.join(root, 'index.html'));
  url.searchParams.set('capture', '1');
  url.searchParams.set('movie', movie);
  if (args.includes('--no-captions')) url.searchParams.set('captions', '0');
  if (voiceCredit) url.searchParams.set('voice-credit', voiceCredit);
  await page.goto(url.href); await page.waitForFunction(()=>!!window.pythagoAnimation);
  await page.evaluate(async () => { await document.fonts.ready; });
  ff=spawn('ffmpeg',ffargs,{stdio:['pipe','inherit','inherit']});
  let ffError; ff.on('error',e=>{ffError=e;}); ff.stdin.on('error',e=>{ffError=e;});
  const ended=once(ff,'close');
  const frames=Math.round(duration*fps);
  for(let i=0;i<frames;i++) {
    if(ffError) throw ffError;
    await page.evaluate(t=>window.pythagoAnimation.render(t),start+i/fps);
    const frame=await page.screenshot({type:'png',animations:'disabled'});
    if(!ff.stdin.write(frame)) await Promise.race([once(ff.stdin,'drain'), ended.then(([code])=>{throw Error(`ffmpeg exited early (${code})`);})]);
    if(i%Math.max(1,fps*10)===0) process.stderr.write(`\rRendering ${i}/${frames} frames`);
  }
  ff.stdin.end(); const [code]=await ended;
  if(code!==0) throw Error(`ffmpeg exited with status ${code}`);
  if(start===0 && duration===end) fs.writeFileSync(out.replace(/\.[^.]+$/, '')+'.srt', movieSrt(movie));
  process.stderr.write(`\nWrote ${out}\n`);
} finally { if(ff && ff.exitCode===null) ff.kill(); await browser.close(); }
