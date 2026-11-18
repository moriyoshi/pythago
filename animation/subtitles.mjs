import fs from 'node:fs';
import vm from 'node:vm';
import { fileURLToPath } from 'node:url';
const root = fileURLToPath(new URL('.', import.meta.url));
function readStory(file, key) {
  const context = { window: {} };
  vm.runInNewContext(fs.readFileSync(root + file, 'utf8'), context);
  return context.window[key];
}

const stories = new Map([
  ['introduction', readStory('story.js', 'PYTHAGO_STORY')],
  ['internals', readStory('internals/story.js', 'PYTHAGO_INTERNALS_STORY')],
  ['native', readStory('native/story.js', 'PYTHAGO_NATIVE_STORY')],
]);
export function movieFromArgs(args) {
  const index = args.indexOf('--movie');
  const movie = index === -1 ? (args.includes('--internals') ? 'internals' : 'introduction') : args[index + 1];
  if (!stories.has(movie)) throw new Error('--movie must be introduction, internals, or native.');
  return movie;
}
export const movieDuration = (movie = 'introduction') => stories.get(movie).at(-1).end;
const stamp = s => { const ms=Math.round(s*1000); return `${String(Math.floor(ms/3600000)).padStart(2,'0')}:${String(Math.floor(ms/60000)%60).padStart(2,'0')}:${String(Math.floor(ms/1000)%60).padStart(2,'0')},${String(ms%1000).padStart(3,'0')}`; };
const format = scenes => scenes.flatMap(s=>s.cues).map((c,i)=>`${i+1}\n${stamp(c[0])} --> ${stamp(c[1])}\n${c[2]}\n`).join('\n');
export const movieSrt = movie => format(stories.get(movie));
if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  process.stdout.write(movieSrt(movieFromArgs(process.argv.slice(2))));
}
