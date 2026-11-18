(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const params = new URLSearchParams(location.search);
  const movie = window.PYTHAGO_MOVIE;
  const {scenes, heading, splashHeading, subheading, figure} = {
    introduction: {scenes: window.PYTHAGO_STORY, heading: 'Pythago', subheading: 'Go and Python in one process'},
    internals: {scenes: window.PYTHAGO_INTERNALS_STORY, heading: 'Pythago internals',
      subheading: 'Asyncio, threads, and trampolines', figure: window.drawPythagoInternals},
    native: {scenes: window.PYTHAGO_NATIVE_STORY, heading: 'Pythago native internals',
      splashHeading: 'Pythago\nnative internals', subheading: 'Object editing, hooks, and thunks',
      figure: window.drawPythagoNative},
  }[movie];
  const duration = scenes.at(-1).end;
  const closingStart = duration - 11;
  const creditsStart = duration - 10.3;
  if (figure) document.title = `${heading} · ${subheading}`;
  const splash = params.has('splash');
  const capture = params.has('capture') || splash;
  if (capture) document.body.classList.add('capture');
  if (splash) document.body.classList.add('splash');
  document.body.classList.add(movie);
  const NS = 'http://www.w3.org/2000/svg';
  const colors = {go:'#168b91',python:'#a8790c',muted:'#557976',white:'#345f60',pink:'#b75c4c'};
  const svg = $('diagram');
  const film = $('film');
  const stage = $('presenter-stage');
  const openingTitle = $('opening-title');
  const credits = $('end-credits');
  $('opening-heading').textContent = splash && splashHeading ? splashHeading : heading;
  $('opening-subheading').textContent = subheading;
  const voiceCredit = params.get('voice-credit');
  $('voice-credit').hidden = voiceCredit === null || voiceCredit === 'ryan';
  $('ryan-voice-credit').hidden = voiceCredit !== 'ryan';
  const clampTime = t => Math.max(0, Math.min(duration, Number(t) || 0));
  let time = clampTime(params.get('t') ?? (splash ? 10.5 : 0));
  let playing = false, last = 0;
  const xml = s => String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&apos;'}[c]));
  const smooth = n => { n=Math.max(0,Math.min(1,n)); return n*n*(3-2*n); };
  const clockTime = t => [Math.floor(t / 60), Math.floor(t) % 60]
    .map(part => String(part).padStart(2, '0')).join(':');
  const sceneAt = t => scenes.find(s => t >= s.start && t < s.end) || scenes[scenes.length-1];
  const node = (w,h,label,small,color='go',delay=0) => ({w,h,label,small,color,delay});
  const diagramScale = 1.18;
  const diagramCenter = 1392;
  const diagramRight = 1886;
  const boxGap = 56;
  function layoutDiagram(top, ...rows) {
    const boxHeight = rows.reduce((sum, row) => sum + Math.max(...row.boxes.map(box => box.h)), 0);
    let y = top - boxHeight * (diagramScale - 1) / 2;
    let right = diagramCenter;
    return rows.flatMap(({boxes, alignRight}) => {
      const width = diagramScale * boxes.reduce((sum, box) => sum + box.w, 0) + boxGap * (boxes.length - 1);
      if (!alignRight) right = Math.min(diagramRight, diagramCenter + width / 2);
      let x = right - width;
      const positioned = boxes.map(box => {
        const item = {...box, x, y, w: box.w * diagramScale, h: box.h * diagramScale};
        x += item.w + boxGap;
        return item;
      });
      y += Math.max(...positioned.map(box => box.h)) + boxGap;
      return positioned;
    });
  }
  const configs = {
    intro: layoutDiagram(355,
      {boxes: [node(260,155,'Go','Application'), node(260,155,'Python','Application','python',2)]},
      {boxes: [node(410,145,'One process','CPython + Go','white',7)]}),
    build: layoutDiagram(345,
      {boxes: [node(260,125,'Python','Distribution','python'), node(300,125,'pythago-dist','Target build','go',5), node(215,125,'dist','Package','white',13)]},
      {boxes: [node(270,125,'Go app','Imports dist','go',21), node(320,125,'Executable','Image or DLLs','python',28)]}),
    bridge: layoutDiagram(380,
      {boxes: [node(200,150,'Go app','Caller'), node(190,150,'py','Public API','go',5), node(160,150,'Glue','C boundary','muted',10), node(190,150,'CPython','Interpreter','python',15)]}),
    packages: layoutDiagram(365,
      {boxes: [node(340,145,'Executable','CPython + Go')]},
      {boxes: [node(325,140,'Embedded','Stdlib + app files','python',6), node(340,140,'On disk','Environment + extensions','white',14)]}),
    threads: layoutDiagram(365,
      {boxes: [node(320,125,'Goroutines','Go scheduler'), node(330,125,'Python','Thread states','python',5)]},
      {boxes: [node(350,135,'OS threads','Attach + coordinate','white',10)]}),
    request: layoutDiagram(385,
      {boxes: [node(205,130,'Client','HTTP request','white'), node(205,130,'net/http','Go server','go',5), node(205,130,'asgi','Handler','go',12), node(205,130,'Python','ASGI app','python',20)]},
      {boxes: [node(290,130,'aio','asyncio loop','python',29)], alignRight: true}),
    deploy: layoutDiagram(325,
      {boxes: [node(350,260,'Small app','Embedded files'), node(350,260,'Large app','Disk environment','python',8)]}),
    outro: layoutDiagram(350,
      {boxes: [node(240,160,'Go','Networking'), node(240,160,'py','The bridge','white',3), node(240,160,'Python','Ecosystem','python',6)]})
  };
  const edges = {
    intro:[[0,2],[1,2]],build:[[0,1],[1,2],[2,4],[3,4]],bridge:[[0,1],[1,2],[2,3]],packages:[[0,1],[0,2]],threads:[[0,2],[1,2]],request:[[0,1],[1,2],[2,3],[3,4]],deploy:[],outro:[[0,1],[1,2]]
  };
  function pathBetween(a,b) {
    const ax=a.x+a.w/2, ay=a.y+a.h/2, bx=b.x+b.w/2, by=b.y+b.h/2;
    const dx=bx-ax, dy=by-ay;
    const edge = n => Math.min(dx ? n.w/(2*Math.abs(dx)) : Infinity, dy ? n.h/(2*Math.abs(dy)) : Infinity);
    const start=edge(a), end=edge(b);
    return [ax+dx*start,ay+dy*start,bx-dx*end,by-dy*end];
  }
  function draw(s,t,closing) {
    const elapsed = t - s.start;
    const items = configs[s.diagram].map(box => {
      const opacity = smooth((elapsed - box.delay) / 1.2);
      return {...box, opacity, y: box.y + 16 * (1 - opacity)};
    });
    let out=`<defs><filter id="glow"><feGaussianBlur stdDeviation="8"/></filter><marker id="arrow" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0 0 L10 5 L0 10Z" fill="#168b91"/></marker></defs><g transform="translate(-24 0)">`;
    edges[s.diagram].forEach(([ai,bi],i) => {
      const a=items[ai], b=items[bi], o=smooth((elapsed-Math.max(a.delay,b.delay)) / 1.5);
      const [x1,y1,x2,y2]=pathBetween(a,b); const reverse=(s.diagram==='bridge' && elapsed>=29)||(s.diagram==='request' && elapsed>=42 && elapsed<54); const raw=((elapsed*.12+i*.23)%1+1)%1; const phase=reverse?1-raw:raw;
      const x=x1+(x2-x1)*phase,y=y1+(y2-y1)*phase;
      out+=`<g opacity="${o}"><path d="M${x1} ${y1} L${x2} ${y2}" stroke="#168b91" stroke-opacity=".72" stroke-width="3" fill="none" ${reverse?'marker-start':'marker-end'}="url(#arrow)"/><circle cx="${x}" cy="${y}" r="12" fill="#168b91" opacity=".3" filter="url(#glow)"/><circle cx="${x}" cy="${y}" r="5" fill="#168b91"/></g>`;
    });
    items.forEach(n => {
      const {opacity:o, y} = n, c=colors[n.color];
      const maxSize = n.label.length > 12 ? 31 : n.label.length > 8 ? 35 : 44;
      const size = Math.min(maxSize * diagramScale, (n.w - 28 * diagramScale) / (n.label.length * .62));
      const cx = n.x + n.w / 2, cy = y + n.h / 2;
      out+=`<g opacity="${o}"><rect x="${n.x}" y="${y}" width="${n.w}" height="${n.h}" rx="18" fill="#f5faf9" stroke="${c}" stroke-opacity=".65" stroke-width="2"/><rect x="${n.x+14}" y="${y + 5}" width="${n.w-28}" height="3" rx="2" fill="${c}"/><text x="${cx}" y="${cy + 4 * diagramScale}" text-anchor="middle" class="node-label" fill="#183843" font-size="${size}">${xml(n.label)}</text><text x="${cx}" y="${cy + 38 * diagramScale}" text-anchor="middle" fill="${c}" font-family="monospace" font-size="${15 * diagramScale}" font-weight="700" letter-spacing="2">${xml(n.small)}</text></g>`;
    });
    const noteSize = 19 * diagramScale;
    if(s.diagram==='intro'||s.diagram==='outro') out+=`<text x="1420" y="${800 - 230 * closing}" text-anchor="middle" fill="#597a76" font-family="monospace" font-size="${noteSize}" letter-spacing="4" opacity="${smooth((elapsed-10)/2)}">CGO_ENABLED=0  ·  CPython inside</text>`;
    if(s.diagram==='threads') out+=`<text x="1400" y="790" text-anchor="middle" fill="#597a76" font-family="monospace" font-size="${noteSize}" letter-spacing="2" opacity="${smooth((elapsed-30)/2)}">GIL  /  Free-threaded</text>`;
    if(s.diagram==='request') out+=`<text x="1380" y="790" text-anchor="middle" fill="#597a76" font-family="monospace" font-size="${noteSize}" letter-spacing="2" opacity="${smooth((elapsed-54)/2)}">WSGI handler also available</text>`;
    if(s.diagram==='deploy') out+=`<text x="1392" y="698" text-anchor="middle" fill="#597a76" font-family="monospace" font-size="${noteSize}" letter-spacing="2" opacity="${smooth((elapsed-18)/2)}">Linux · macOS · Windows</text>`;
    svg.innerHTML=`${out}</g>`;
  }
  function render(t) {
    time = clampTime(t);
    const s=sceneAt(time), cue=s.cues.find(c => time>=c[0] && time<c[1]) || s.cues[s.cues.length-1];
    $('eyebrow').textContent=s.kicker; $('heading').innerHTML=xml(s.title).replace(/\n/g,'<br>');
    $('subheading').textContent=s.sub; $('footer-label').textContent=s.footer;
    $('caption').innerHTML=$('captions-toggle').checked?`<span>${xml(cue[2])}</span>`:'';
    $('progress').style.width = `${100 * time / duration}%`; $('seek').value=time; $('chapters').value=String(s.start);
    $('clock').textContent = `${clockTime(time)} / ${clockTime(duration)}`;
    const opening = splash ? 1 : 1 - smooth((time - 4.7) / 1.6);
    const closing = smooth((time - closingStart) / 1.6);
    if (figure) svg.innerHTML = figure(s, time, cue);
    else draw(s,time,closing);
    const cuddle = window.posePythagoPresenters(time, s, cue, duration);
    film.classList.toggle('opening-shot', opening > 0);
    film.style.setProperty('--opening', opening);
    openingTitle.hidden = opening <= 0.5;
    film.classList.toggle('closing-shot', closing > 0);
    film.style.setProperty('--closing', closing);
    credits.hidden = time < creditsStart;
    credits.style.opacity = smooth((time - creditsStart) / 0.9);
    // Frame the pair as one group, shifting the center as the snake moves in.
    const centerX = 405 - 92 * cuddle;
    const closeFrame = Math.max(opening, closing);
    const scale = splash ? 2.25 : 1 + 0.55 * closeFrame;
    const stageX = splash ? 1410 - scale * centerX : closeFrame * (960 - 1.55 * centerX);
    const stageY = splash ? 600 - scale * 640 : 100 + closeFrame * (550 - 1.55 * 640);
    stage.setAttribute('transform', `translate(${stageX} ${stageY}) scale(${scale})`);
    return time;
  }
  function frame(now) { if(!playing)return; if(last)render(time+(now-last)/1000); last=now; if(time>=duration){playing=false;$('play').textContent='▶';$('play').setAttribute('aria-label','Play animation');return;} requestAnimationFrame(frame); }
  $('play').addEventListener('click',()=>{playing=!playing; $('play').textContent=playing?'❚❚':'▶'; $('play').setAttribute('aria-label',playing?'Pause animation':'Play animation');if(playing){if(time>=duration)render(0);last=0;requestAnimationFrame(frame);}});
  $('seek').addEventListener('input',e=>{last=0;render(e.target.value);});
  $('captions-toggle').addEventListener('change',()=>render(time));
  scenes.forEach(s=>{const o=document.createElement('option');o.value=s.start;o.textContent=`${clockTime(s.start)}  ${s.footer}`;$('chapters').appendChild(o);});
  $('chapters').addEventListener('change',e=>{last=0;render(e.target.value);});
  $('seek').max = duration;
  window.pythagoAnimation={render,scenes,duration};
  if(splash || params.get('captions')==='0')$('captions-toggle').checked=false;
  render(time);
})();
