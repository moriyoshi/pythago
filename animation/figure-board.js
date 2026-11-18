(() => {
  'use strict';

  const ink = '#183843';
  const muted = '#46636b';
  const palette = {
    teal: { fill: '#e4f4f3', line: '#147982' },
    gold: { fill: '#fff3d5', line: '#946511' },
    blue: { fill: '#eaf0fa', line: '#475e96' },
    gray: { fill: '#f1f5f6', line: '#57747b' },
  };
  const escape = value => String(value).replace(/[&<>]/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;'}[c]));

  function label(x, y, words, {size = 22, color = ink, anchor = 'start', weight = 550, mono = false, display = false} = {}) {
    const family = mono ? 'monospace' : 'system-ui,sans-serif';
    return `<text x="${x}" y="${y}" fill="${color}" text-anchor="${anchor}" font-size="${size}"
      font-family="${family}" font-weight="${weight}" ${display ? 'class="node-label"' : ''}>${escape(words)}</text>`;
  }

  function card(x, y, width, height, title, lines, {tone = 'gray', active = false, mono = false, size = 31} = {}) {
    const {fill, line} = palette[tone];
    const center = x + width / 2;
    const left = x + 14;
    const titleY = y + (height - (lines.length - 1) * 25) / 2 - 9;
    return `<g data-card>
      <rect x="${x}" y="${y}" width="${width}" height="${height}" rx="16" fill="${fill}" stroke="${line}"
        stroke-width="${active ? 4 : 2}" stroke-opacity="${active ? 1 : 0.52}"/>
      <path d="M${left} ${y + 10} H${x + width - 14}" stroke="${line}" stroke-width="3" opacity="${active ? 0.9 : 0.35}"/>
      ${label(center, titleY, title, {size, mono, display: !mono, anchor: 'middle', weight: 700})}
      ${lines.map((line, i) => label(center, titleY + 30 + i * 25, line, {size: 20, color: muted, anchor: 'middle'})).join('')}
    </g>`;
  }

  function note(x, y, title, detail, tone = 'gray') {
    const {fill, line} = palette[tone];
    return `<g><rect x="${x}" y="${y}" width="1018" height="73" rx="12" fill="${fill}"/>
      <rect x="${x}" y="${y + 11}" width="4" height="51" rx="2" fill="${line}"/>
      ${label(x + 21, y + 29, title, {size: 20, weight: 730})}
      ${label(x + 21, y + 55, detail, {size: 19, color: muted})}</g>`;
  }

  function arrow(x1, y1, x2, y2, {tone = 'teal', active = false, elapsed = 0} = {}) {
    const {line} = palette[tone];
    const phase = (elapsed % 2.1) / 2.1;
    return `<g><path d="M${x1} ${y1} L${x2} ${y2}" fill="none" stroke="${line}"
      stroke-width="${active ? 4 : 2.5}" stroke-opacity="${active ? 1 : 0.62}"
      marker-end="url(#internals-${tone})"/>
      ${active ? `<circle cx="${x1 + (x2 - x1) * phase}" cy="${y1 + (y2 - y1) * phase}" r="5.5" fill="${line}"/>` : ''}
    </g>`;
  }

  function board(title, detail, parts) {
    const markers = Object.entries(palette).map(([name,{line}]) => `<marker id="internals-${name}"
      viewBox="0 0 10 10" markerWidth="11" markerHeight="11" markerUnits="userSpaceOnUse" refX="9" refY="5"
      orient="auto"><path d="M0 0 L10 5 L0 10Z" fill="${line}"/></marker>`).join('');
    return `<defs>${markers}</defs><g transform="translate(754 164)" id="runtime-figure">
      <rect width="1100" height="674" rx="26" fill="#fafcfc" stroke="#c5d9da" stroke-width="2"/>
      ${label(41, 53, title, {size: 34, display: true})}
      ${label(41, 84, detail, {size: 20, color: muted})}
      <path d="M41 103 H1059" stroke="#dbe7e8" stroke-width="2"/>
      ${parts}</g>`;
  }

  const codeTokens = /\/\/.*|\/\*.*?\*\/|#(?:ifdef|else|endif)\b|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|\b(?:async|await|case|default|def|defer|else|for|func|go|if|in|nil|None|NULL|range|return|select|struct|switch|var)\b|\b\d+\b/g;

  function highlightedCode(line) {
    let out = '', offset = 0;
    for (const match of line.matchAll(codeTokens)) {
      const token = match[0];
      const first = token[0];
      const color = first === '/' ? '#aec5c9' : first === '"' || first === "'" ? '#f2cc91'
        : /\d/.test(first) ? '#a9e0d5' : '#b6c9ff';
      out += escape(line.slice(offset, match.index));
      out += `<tspan fill="${color}">${escape(token)}</tspan>`;
      offset = match.index + token.length;
    }
    return out + escape(line.slice(offset));
  }

  function codePanel({file, symbol, language, rows}, active, heading, detail) {
    const start = rows[0][0];
    const end = rows.at(-1)[0];
    const columns = 74;
    const displayRows = rows.flatMap(([number, line]) => {
      if (line.length <= columns) return [{number, line}];
      const comment = line.indexOf(' //');
      const gap = comment > 0 && comment < columns ? comment : line.lastIndexOf(' ', columns);
      const split = gap > 0 ? gap : columns;
      return [{number, line: line.slice(0, split)}, {number, line: `        ${line.slice(split).trimStart()}`, continuation: true}];
    });
    const spacing = displayRows.length > 15 ? 22 : 25;
    const longest = Math.max(...displayRows.map(row => row.line.length));
    const fontSize = Math.min(spacing - 2, 930 / (longest * .62));
    const height = Math.min(438, Math.max(218, 60 + spacing * displayRows.length));
    const lines = displayRows.map(({number, line, continuation}, i) => {
      const y = 163 + i * spacing;
      const focus = active.includes(number);
      return `<g data-source-line="${number ?? ''}">
        ${focus ? `<rect x="54" y="${y - spacing + 4}" width="992" height="${spacing}" rx="4" fill="#29545c"/>
          <rect x="54" y="${y - spacing + 4}" width="3" height="${spacing}" rx="1.5" fill="#78d8cc"/>` : ''}
        ${label(101, y, continuation ? '' : number ?? '', {mono: true, size: 17, color: '#afc5cd', anchor: 'end', weight: 400})}
        <text x="119" y="${y}" fill="#f1f6f7" font-family="monospace" font-size="${fontSize}" xml:space="preserve">${highlightedCode(line)}</text>
      </g>`;
    }).join('');
    return board(symbol, `${file}:${start}–${end}  ·  ${language} source excerpt`,
      `<g data-source-panel><rect x="41" y="126" width="1018" height="${height}" rx="15" fill="#17323e"/>
        <path d="M109 143 V${110 + height}" stroke="#43606a" stroke-width="1"/>${lines}</g>`
      + note(41, Math.min(578, 148 + height), heading, detail, 'teal'));
  }

  window.PythagoFigureBoard = {label, card, note, arrow, board, codePanel, ink, muted, palette};
})();
