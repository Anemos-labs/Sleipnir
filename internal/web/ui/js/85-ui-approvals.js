/* 85-ui-approvals.js: SL.ui.approvals, the approval question and the cross-session inbox.
 *
 * The question box names the agent, the operation and its scope, shows the command and why it asks, and offers the three real choices.
 * Security rule (product requirement): an answer is accepted only after the keyboard has been QUIET for ~0.8 s since the question was shown
 * or since the last key; until then the buttons are inert and the meter fills. Text typed ahead goes to the prompt and never answers.
 * The same rule governs the inbox, which can answer a question of a background session. Several questions are asked one at a time.
 * The server keeps its own floor (350 ms, `409 too_soon`): the meter re-arms when it refuses. The header says what the question is
 * about (a command, an edit, a fetch, project files or a tool server). A question that can offer it gets the TUI's fourth answer,
 * builds and tests for the session (on screen 3, on the wire choice 4; No is 4 and Esc). An edit, a write or a patch shows the change
 * it asks to make.
 * What a question shows is shown whole and stays in reach: the command, the change and the reason each scroll on their own with every
 * character wrapped, and a long one says how many lines and bytes it has and offers its end in one press (a dangerous last line is never
 * below the fold unannounced). Answer 2 names the exact rules it remembers. A key answers only a question that is on screen: with the
 * rail folded away, a question opens it, and a key press opens it instead of answering. The command, the rule, the path, the scope
 * and the reason are drawn left to right in the order they are written (CSS bidi override: a right-to-left word cannot trade places
 * with its neighbour), and every character that is not ASCII is marked with its code point (a look-alike letter is noticed). */
(function (SL) {
  'use strict';
  const U = SL.u, { $, $$, esc, clip1 } = U, calc = SL.calc, ui = SL.ui = SL.ui || {};
  const QUIET = 0.8, shown = Object.create(null);          // wall ms at which a question's answer UI was last seen to appear

  /* ---------- long text: bounded, wrapped, counted, its end one press away ---------- */
  const LONG_LINES = 4, LONG_BYTES = 280;
  /** The UTF-8 length of s in bytes. */
  function bytesOf(s) { s = String(s == null ? '' : s); let n = 0; for (let i = 0; i < s.length; i++) { const c = s.charCodeAt(i); if (c < 0x80) n++; else if (c < 0x800) n += 2; else if (c >= 0xd800 && c < 0xdc00 && i + 1 < s.length) { n += 4; i++; } else n += 3; } return n; }
  const sizeTxt = b => b < 1024 ? b + ' byte' + (b === 1 ? '' : 's') : b < 1048576 ? (b / 1024).toFixed(1) + ' KB' : (b / 1048576).toFixed(1) + ' MB';
  /** "N lines, M bytes" of a text. */
  const lenTxt = s => { s = String(s == null ? '' : s); const n = s.split('\n').length; return n + ' line' + (n === 1 ? '' : 's') + ', ' + sizeTxt(bytesOf(s)); };
  /** The line above a long block (more than LONG_LINES lines or LONG_BYTES bytes, or always with force): how long it is, and a button
   *  that scrolls the block that follows to its end (and back). Empty for a short text. */
  function lenLine(text, what, force) {
    const s = String(text == null ? '' : text), n = s.split('\n').length, b = bytesOf(s); if (!force && n <= LONG_LINES && b <= LONG_BYTES) return '';
    return '<div class="qlen"><span>' + (what ? esc(what) + ': ' : '') + lenTxt(s) + '</span><button class="btn sm" type="button" data-end aria-label="Show the end of ' + esc(what || 'the text') + '">Show the end ↓</button></div>';
  }
  /** Click handler for a container of long blocks: [data-end] scrolls the block after its line to the end, and from the end back to the start. */
  function endClick(e) {
    const b = e.target && e.target.closest ? e.target.closest('[data-end]') : null; if (!b) return; const ln = b.closest('.qlen'), blk = ln && ln.nextElementSibling; if (!blk) return;
    const atEnd = blk.scrollTop + blk.clientHeight >= blk.scrollHeight - 2; blk.scrollTop = atEnd ? 0 : blk.scrollHeight; b.textContent = atEnd ? 'Show the end ↓' : 'Show the start ↑';
  }
  /* ---------- characters that are not ASCII: marked, each with its code point, so a look-alike letter or an invisible one is noticed ---------- */
  const SCRIPT = [[0x80, 0xFF, 'Latin-1'], [0x100, 0x24F, 'Latin'], [0x250, 0x2FF, 'phonetic or modifier'], [0x300, 0x36F, 'combining mark'], [0x370, 0x3FF, 'Greek'], [0x400, 0x52F, 'Cyrillic'],
    [0x530, 0x58F, 'Armenian'], [0x590, 0x5FF, 'Hebrew'], [0x600, 0x6FF, 'Arabic'], [0x700, 0x8FF, 'right-to-left script'], [0x1AB0, 0x1AFF, 'combining mark'], [0x1D00, 0x1DBF, 'phonetic'],
    [0x1DC0, 0x1DFF, 'combining mark'], [0x1E00, 0x1EFF, 'Latin'], [0x1F00, 0x1FFF, 'Greek'], [0x2000, 0x206F, 'space, format or punctuation'], [0x2070, 0x209F, 'superscript or subscript'],
    [0x20D0, 0x20FF, 'combining mark'], [0x2100, 0x214F, 'letter-like symbol'], [0x2150, 0x218F, 'number form'], [0x2190, 0x2BFF, 'symbol'], [0x2C00, 0x2DFF, 'letter'], [0x2E00, 0x2E7F, 'punctuation'],
    [0x3000, 0x9FFF, 'CJK'], [0xA640, 0xA69F, 'Cyrillic'], [0xAB30, 0xAB6F, 'Latin'], [0xFB00, 0xFB4F, 'presentation form'], [0xFE00, 0xFE0F, 'variation selector (invisible)'], [0xFE20, 0xFE2F, 'combining mark'],
    [0xFE70, 0xFEFF, 'Arabic form or byte-order mark'], [0xFF00, 0xFFEF, 'fullwidth or halfwidth form'], [0x1D400, 0x1D7FF, 'mathematical letter'], [0xE0000, 0xE007F, 'tag (invisible)'], [0xE0100, 0xE01EF, 'variation selector (invisible)']];
  const NAMED = { 0xA0: 'no-break space', 0xAD: 'soft hyphen (invisible)', 0x115F: 'Hangul filler (invisible)', 0x1160: 'Hangul filler (invisible)', 0x180E: 'Mongolian vowel separator (invisible)', 0x200B: 'zero width space', 0x200C: 'zero width non-joiner', 0x200D: 'zero width joiner',
    0x200E: 'left-to-right mark', 0x200F: 'right-to-left mark', 0x2028: 'line separator', 0x2029: 'paragraph separator', 0x202A: 'left-to-right embedding', 0x202B: 'right-to-left embedding', 0x202C: 'pop directional formatting',
    0x202D: 'left-to-right override', 0x202E: 'right-to-left override', 0x2060: 'word joiner', 0x2066: 'left-to-right isolate', 0x2067: 'right-to-left isolate', 0x2068: 'first strong isolate', 0x2069: 'pop directional isolate',
    0x3164: 'Hangul filler (invisible)', 0xFEFF: 'zero width no-break space', 0xFFA0: 'halfwidth Hangul filler (invisible)' };
  /** What a code point is, in a few words: a name for the invisible and directional ones, else its script or block. */
  function cpWhat(cp) { if (cp < 0x20 || cp === 0x7F) return 'control character'; if (cp >= 0x80 && cp < 0xA0) return 'control character'; if (Object.prototype.hasOwnProperty.call(NAMED, cp)) return NAMED[cp]; for (const r of SCRIPT) if (cp >= r[0] && cp <= r[1]) return r[2]; return cp >= 0xD800 && cp <= 0xDFFF ? 'lone surrogate' : 'not ASCII'; }
  const cpHex = cp => 'U+' + cp.toString(16).toUpperCase().padStart(4, '0');
  /** A character that is marked: anything outside printable ASCII, except a newline and a tab. */
  const marked = cp => cp > 0x7E || (cp < 0x20 && cp !== 0x0A && cp !== 0x09);
  const MARK_RUNS = 4000;
  /** A directional formatting character: marked on its own, so its mark (an isolate, see 60-rail.css) holds its effect to nothing but itself. */
  const BIDI = cp => cp === 0x061C || cp === 0x200E || cp === 0x200F || (cp >= 0x202A && cp <= 0x202E) || (cp >= 0x2066 && cp <= 0x2069);
  /**
   * The markup of a text with every character outside printable ASCII marked: a run of them is one <mark class="nca"> whose title names
   * each code point ("U+0430 Cyrillic", at most 12, then …), and a directional formatting character is a mark of its own. The text itself is unchanged (the marks add no character, so it copies as
   * written) and everything is escaped. Returns {html, n (code points beyond ASCII), ctl (ASCII control characters other than a newline
   * or a tab, and DEL), cut (runs left unmarked past MARK_RUNS)}.
   */
  function markNonAscii(text) {
    const s = String(text == null ? '' : text); let html = '', plain = '', run = [], runs = 0, n = 0, ctl = 0, cut = 0;
    const flushPlain = () => { if (plain) { html += esc(plain); plain = ''; } };
    const flushRun = () => { if (!run.length) return; const str = run.map(cp => String.fromCodePoint(cp)).join('');
      if (runs >= MARK_RUNS) { cut++; html += esc(str); } else { runs++; const t = run.slice(0, 12).map(cp => cpHex(cp) + ' ' + cpWhat(cp)).join(', ') + (run.length > 12 ? ', … (' + run.length + ' in all)' : ''); html += '<mark class="nca" title="' + esc(t) + '">' + esc(str) + '</mark>'; }
      run = []; };
    for (const ch of s) { const cp = ch.codePointAt(0); if (marked(cp)) { flushPlain(); if (BIDI(cp)) flushRun(); run.push(cp); if (BIDI(cp)) flushRun(); if (cp > 0x7F) n++; else ctl++; } else { flushRun(); plain += ch; } }
    flushRun(); flushPlain(); return { html, n, ctl, cut };
  }
  const mark = t => markNonAscii(t).html;
  /** The note under a block whose text has marked characters (empty when it has none). */
  function markNote(m) { if (!m.n && !m.ctl) return ''; const p = []; if (m.n) p.push(m.n + ' non-ASCII character' + (m.n === 1 ? '' : 's')); if (m.ctl) p.push(m.ctl + ' control character' + (m.ctl === 1 ? '' : 's')); return '<div class="qnote">contains ' + p.join(' and ') + ' (marked' + (m.cut ? '; the first ' + MARK_RUNS + ' runs' : '') + ')</div>'; }
  ui.longText = { line: lenLine, click: endClick, bytes: bytesOf, len: lenTxt, size: sizeTxt, mark, markNonAscii, markNote };
  /** The rules a "don't ask again" remembers: the server joins several with ", " (outside parentheses); each is shown whole on its own line. */
  function ruleList(rule) {
    const s = String(rule == null ? '' : rule), out = []; if (!s) return out; let depth = 0, cur = '';
    for (let i = 0; i < s.length; i++) { const c = s[i]; if (c === '(') depth++; else if (c === ')' && depth > 0) depth--; if (depth === 0 && c === ',' && s[i + 1] === ' ') { out.push(cur); cur = ''; i++; continue; } cur += c; }
    out.push(cur); return out.filter(x => x.trim() !== '');
  }
  /** Under answer 2: the exact rule or rules it remembers, in small print. */
  function ruleHtml(q) { const rs = ruleList(q.rule); return rs.length ? '<div class="qrule"><span>2 remembers ' + (rs.length > 1 ? 'these ' + rs.length + ' rules' : 'this rule') + ':</span>' + rs.map(r => '<code>' + mark(r) + '</code>').join('') + '</div>' : ''; }

  const TESTS = 'Yes, and allow builds and tests (go, npm, cargo, pytest, make…) for this session';
  const remembers = q => q.kind === 'trust' || q.kind === 'mcp';
  /** Does the question take answer 2? Only where it remembers something (the server's approvals.OffersRemember): a project's tool server
   *  or its own files, or a question whose rule names the rules the permission engine adds. Without it the option is not shown, the key 2
   *  does nothing, and 3 and 4 keep their numbers. */
  const offersTwo = q => remembers(q) || !!q.rule;
  /** The words of a choice (the wire's number): 1 yes, 2 yes and remember, 3 no, 4 the tests preset. */
  const choiceText = (q, c) => ({ 1: 'Yes', 2: remembers(q) ? 'Yes, and remember until they change' : 'Yes, and don\'t ask again for ' + (q.what || 'this command') + ' this session', 3: 'No, and tell Sleipnir what to do instead', 4: TESTS })[c] || '';
  /** The key a choice has on screen: with the fourth answer, the tests preset is 3 and No is 4. */
  const keyOf = (q, c) => q.offersTests ? ({ 1: 1, 2: 2, 4: 3, 3: 4 })[c] : c;
  const choiceOfKey = (q, k) => q.offersTests ? ({ 1: 1, 2: 2, 3: 4, 4: 3 })[k] : k;
  /** What the agent wants, by the question's kind (the header of a question depends on it; writes, patches and web searches are named too). */
  function wants(q) {
    switch (q.kind) {
      case 'edit': return q.tool === 'write' ? 'wants to write a file' : q.tool === 'apply_patch' ? 'wants to apply a patch' : 'wants to edit a file';
      case 'web': return q.tool === 'web_search' ? 'wants to search the web' : 'wants to fetch a web page';
      case 'trust': return 'wants to use this project\'s files';
      case 'mcp': return 'wants to start a tool server';
      case 'read': return 'wants to read a file';
      case 'other': return 'wants to use a tool';
      default: return 'wants to run a command';
    }
  }
  /**
   * The lines of a unified diff with their kinds: outside a hunk, a file header (`diff`, `index`, `--- `, `+++ `) or a note; a hunk's head
   * (`@@ -o,ol +n,nl @@`); inside a hunk, for as many lines as its head counts, a context, removed or added line with its line number
   * (a removed line whose text begins with "-- " is a removed line, never a header), and a "\ No newline" note.
   */
  function parseDiff(text) {
    const out = [], lines = String(text || '').split('\n'); let ro = 0, rn = 0, o = 0, n = 0;
    if (lines.length && lines[lines.length - 1] === '') lines.pop();
    for (const l of lines) {
      const c = l.charAt(0);
      if (ro > 0 || rn > 0) {
        if (c === '-' && ro > 0) { out.push({ k: 'del', s: l.slice(1), o: o++ }); ro--; continue; }
        if (c === '+' && rn > 0) { out.push({ k: 'add', s: l.slice(1), n: n++ }); rn--; continue; }
        if ((c === ' ' || l === '') && ro > 0 && rn > 0) { out.push({ k: 'ctx', s: l.slice(1), o: o++, n: n++ }); ro--; rn--; continue; }
        if (c !== '\\') { ro = 0; rn = 0; }   /* a hunk shorter than its head says: what follows is read afresh */
      }
      if (c === '\\') { out.push({ k: 'note', s: l }); continue; }
      const m = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/.exec(l);
      if (m) { o = +m[1]; ro = m[2] == null ? 1 : +m[2]; n = +m[3]; rn = m[4] == null ? 1 : +m[4]; out.push({ k: 'hunk', s: l }); continue; }
      out.push({ k: 'head', s: l });
    }
    return out;
  }
  /** The unified text of a change as diff lines (the Workspace's diff body, without the attribution gutter). */
  function diffLines(text) {
    return parseDiff(text).map(x => x.k === 'add' ? '<div class="ln add"><i>' + x.n + '</i><s>+</s><span>' + esc(x.s) + '</span></div>' : x.k === 'del' ? '<div class="ln del"><i>' + x.o + '</i><s>−</s><span>' + esc(x.s) + '</span></div>'
      : x.k === 'ctx' ? '<div class="ln ctx"><i>' + x.n + '</i><s></s><span>' + esc(x.s) + '</span></div>' : '<div class="ln hunk"><i></i><s></s><span>' + esc(x.s) + '</span></div>').join('');
  }
  /** [added, removed] lines of a change: only lines inside a hunk count (a file header never does, a removed "-- " line always does). */
  const counts = text => { let a = 0, d = 0; parseDiff(text).forEach(x => { if (x.k === 'add') a++; else if (x.k === 'del') d++; }); return [a, d]; };
  /** The change a question asks to make: a head with `Open it whole`, its length when long, and the diff in a block of its own scroll
   *  (at most 30% of the window's height) whose long lines wrap. */
  function changeHtml(q) {
    if (!q.change) return ''; const [a, d] = counts(q.change);
    return '<div class="qscope"><b>change</b>the change it asks to make' + (q.path ? ' · <span class="mono qpath">' + mark(q.path) + '</span>' : '') + ' · <span class="ok">+' + a + '</span> <span class="err">−' + d + '</span> <button class="btn sm" type="button" data-whole="' + esc(q.id) + '">Open it whole</button></div>' + lenLine(q.change, 'the change') + '<div class="diff qdiff" style="max-height:min(30vh,240px);margin:0 0 6px">' + diffLines(q.change) + '</div>';
  }
  function openWhole(q) { ui.modal({ title: 'The change', kicker: String(q.agent) + (q.task ? ' · ' + q.task : ''), desc: (q.path || '') + ' · as the agent asks to make it', wide: true, color: 'var(--warm)', body: '<div class="diff">' + diffLines(q.change) + '</div>' }); }
  const roTitle = S => S && S.readOnly ? (S.follow ? 'watching ' + S.sid + ' · read-only · the run belongs to another process' : 'a recorded session is read-only') : '';
  /** The words of the meter once the keys answer: the keys this question takes. */
  const readyText = q => { const k = ['1'].concat(offersTwo(q) ? ['2'] : [], ['3'], q.offersTests ? ['4'] : []); return 'ready: press ' + k.slice(0, -1).join(', ') + ' or ' + k[k.length - 1] + ' (esc is ' + (q.offersTests ? 4 : 3) + ')'; };
  const opts = (q, pre, S) => { const ro = roTitle(S), dis = ro ? ' disabled title="' + esc(ro) + '"' : '', four = !!q.offersTests, two = offersTwo(q);
    return '<div class="qopts" data-q="' + esc(q.id) + '">' +
    '<button class="qopt" type="button" data-choice="1" aria-disabled="true"' + dis + '><kbd>1</kbd><span>Yes</span></button>' +
    (two ? '<button class="qopt" type="button" data-choice="2" aria-disabled="true"' + dis + '><kbd>2</kbd><span>' + esc(choiceText(q, 2)) + '</span></button>' + ruleHtml(q) : '') +
    (four ? '<button class="qopt" type="button" data-choice="4" aria-disabled="true"' + dis + '><kbd>3</kbd><span>' + esc(TESTS) + '</span></button>' : '') +
    '<button class="qopt no" type="button" data-choice="3" aria-disabled="true"' + dis + '><kbd>' + (four ? 4 : 3) + '</kbd><span>No, and tell Sleipnir what to do instead <small>(esc)</small></span></button></div>' +
    '<div class="qmeter" data-m="' + esc(q.id) + '" data-ready="' + esc(readyText(q)) + '"><span class="bar"><i></i></span><span class="qmt">your typing goes to the prompt until you pause</span></div>'; };

  /** Is the answer UI of question qid armed (keyboard quiet for QUIET s since it appeared)? Returns {armed, frac}. */
  function arm(qid) {
    const T = SL.time.T, t0 = Math.max(shown[qid] == null ? T.wall : shown[qid], T.quietSince), frac = Math.min(1, (T.wall - t0) / 1000 / QUIET);
    return { armed: frac >= 1, frac };
  }
  /** Is el laid out and visible (not inside a folded rail, a hidden pane or anything else that is display:none or hidden)? */
  function onScreen(el) { if (!el || !el.isConnected || !el.getClientRects().length) return false; const cs = getComputedStyle(el); return cs.visibility !== 'hidden' && cs.display !== 'none'; }
  /** Bring the rail and its question on screen (the shell's own expand; on a phone, its pane). */
  function openRail(pane) { if (!ui.rail) return; if (pane && ui.rail.open) ui.rail.open(); else if (ui.rail.expand) ui.rail.expand(); }
  /** The question strip of the active session, when one is in the DOM. */
  const stripEl = () => $('#qSlot .qstrip');
  function paintMeters(scopeEl) {
    $$('.qmeter', scopeEl).forEach(mt => {
      const qid = mt.dataset.m; if (shown[qid] == null || !onScreen(mt)) shown[qid] = SL.time.T.wall; const a = arm(qid), S = SL.sessions.active, rep = S && S.replay;
      const ok = a.armed && !rep; mt.classList.toggle('ready', ok); $('.bar i', mt).style.width = (a.frac * 100) + '%';
      const t = rep ? 'replay: go live to answer' : ok ? (mt.dataset.ready || 'ready: press 1, 2 or 3 (esc is 3)') : 'your typing goes to the prompt until you pause'; const el = $('.qmt', mt); if (el.textContent !== t) el.textContent = t;
      const box = mt.previousElementSibling; if (box && box.classList.contains('qopts')) $$('.qopt', box).forEach(b => b.setAttribute('aria-disabled', ok ? 'false' : 'true'));
    });
  }
  /** Answer if armed; otherwise explain. choice 3 asks for the instruction first (in the strip or the inbox row). */
  function answer(S, q, choice, boxEl) {
    const rep = S === SL.sessions.active && S.replay; if (rep) { ui.toast('go live to answer: a replay never changes the session', 'warm'); return false; }
    const a = arm(q.id); if (!a.armed) { ui.toast('the buttons wake up when the keyboard has been quiet for a moment', 'warm'); return false; }
    if (S.readOnly) { ui.toast(roTitle(S), 'warm'); return false; }
    if (choice === 2 && !offersTwo(q)) return false;   /* nothing would be remembered: the question takes no answer 2 */
    if (choice === 3 && boxEl) { tellInstead(S, q, boxEl); return true; }
    const r = SL.act.answerQuestion(q.id, choice, undefined, S.id); if (r && r.ok === false) { if (r.why) ui.toast(r.why, 'warm'); return false; } delete shown[q.id];
    ui.toast(choice === 3 ? 'told ' + q.agent + ' what to do instead' : 'answered ' + keyOf(q, choice) + ': ' + q.agent + (q.kind && q.kind !== 'command' ? ' goes ahead' : ' runs the command'), 'ok'); return true;
  }
  function tellInstead(S, q, boxEl) {
    boxEl.innerHTML = '<div class="field" style="margin:0"><input class="tellIn" type="text" placeholder="tell Sleipnir what to do instead" aria-label="Tell Sleipnir what to do instead"></div><div class="row2"><button class="btn pri tellSend" type="button">Send to ' + esc(q.agent) + ' <kbd>⏎</kbd></button><button class="btn tellBack" type="button">Back <kbd>esc</kbd></button></div>';
    const inp = $('.tellIn', boxEl); inp.focus();
    const send = () => { delete boxEl.dataset.tell; SL.act.answerQuestion(q.id, 3, inp.value.trim() || 'don’t do that: find another way', S.id); delete shown[q.id]; ui.toast('told ' + q.agent + ' what to do instead', 'ok'); if (boxEl._afterTell) boxEl._afterTell(); };
    $('.tellSend', boxEl).addEventListener('click', send);
    inp.addEventListener('keydown', e => { SL.time.noteKey(); if (e.key === 'Enter') { e.preventDefault(); send(); } else if (e.key === 'Escape') { e.stopPropagation(); back(); } });
    const back = () => { shown[q.id] = SL.time.T.wall; delete boxEl.dataset.tell; if (boxEl._back) boxEl._back(); if (boxEl._afterTell) boxEl._afterTell(); };
    $('.tellBack', boxEl).addEventListener('click', back); boxEl.dataset.tell = '1';
  }

  /* ---------- the strip in the rail (active session) ---------- */
  /** The "+N waiting" line of a strip that stays as it is. */
  const moreText = n => n > 0 ? '+' + n + ' waiting: asked one at a time' : '';
  /** Say a new question once (role=alert), never the ticking parts of the strip. */
  function announce(text) { let st = $('#qStatus'); if (!st) { st = document.createElement('div'); st.id = 'qStatus'; st.className = 'sr'; st.setAttribute('role', 'alert'); document.body.appendChild(st); } st.textContent = text; }
  /**
   * Draw the front question of S from m, which is S's world model: a question is never held back by the hold, a pin, a pause or a scrub
   * of the view. The strip is built once per question; another question arriving behind it changes only its "+N waiting" line, so a
   * half-typed instruction (3: tell Sleipnir what to do instead), its focus and the blocks' scroll stay as they are.
   */
  function render(S, m) {
    const slot = $('#qSlot'), q = m ? calc.openQuestion(m) : null, done = m ? m.qs.filter(x => x.answered).slice(-1)[0] : null;
    if (!q) { const key = done ? 'd' + done.id + done.answered : ''; if (slot._k !== key) { slot._k = key; slot.innerHTML = done && SL.time.T.wall - (shown['_d' + done.id] || (shown['_d' + done.id] = SL.time.T.wall)) < 8000 ? '<div class="qdone"><b>' + (done.by && done.by !== 'you' ? '⊘' : '✓') + '</b><span><b style="color:var(--fg)">' + (done.by && done.by !== 'you' ? esc(BY[done.by] || 'refused') : keyOf(done, done.answered) + ' ' + esc(choiceText(done, done.answered))) + '</b><br><span class="dim">' + esc(done.agent) + (done.task ? ' · ' + esc(done.task) : '') + ' · <span class="mono">' + esc(clip1(done.cmd, 160)) + '</span></span></span></div>' : ''; } return; }
    const key = 'q' + q.id + '|' + S.id, waiting = calc.waiting(m) - 1, cur = $('.qstrip', slot);
    if (slot._k === key && cur) { if (slot._w !== waiting) { slot._w = waiting; let mo = $('.qmore', cur); if (!mo && waiting > 0) { mo = document.createElement('div'); mo.className = 'qmore'; $('.qh', cur).after(mo); } if (mo) { mo.textContent = moreText(waiting); mo.hidden = !(waiting > 0); } } return; }
    slot._k = key; slot._w = waiting;
    /* a question that arrives while the rail is folded away opens it: the full question is what the person answers */
    const app = $('#app'); if (app && app.dataset.rail === 'min' && !S.replay) openRail(false);
    announce(q.agent + ' asks: ' + clip1(q.cmd, 200) + (waiting > 0 ? ' (' + waiting + ' more waiting)' : ''));
    const A = m.ag[q.agent];
    slot.innerHTML = '<section class="qstrip" role="group" aria-label="Approval question from ' + esc(q.agent) + '" aria-describedby="qWhy"><div class="qh"><div><span class="qk">' + esc(q.agent) + ' · ' + esc(A ? A.role : '') + (q.task ? ' · ' + esc(q.task) : '') + '</span><b>' + wants(q) + '</b></div><span class="qw">waiting for you: 0s</span></div>' + (waiting > 0 ? '<div class="qmore">' + moreText(waiting) + '</div>' : '') +
      cmdHtml(q) + changeHtml(q) + (q.scope ? '<div class="qscope"><b>scope</b><span class="qsv">' + mark(q.scope) + '</span></div>' : '') + whyHtml(q, 'qWhy') + '<div class="qbox">' + opts(q, null, S) + '</div></section>';
    const strip = $('.qstrip', slot), box = $('.qbox', strip), render2 = () => { box.innerHTML = opts(q, null, S); wire(); }; box._back = render2;
    const wb = $('[data-whole]', strip); if (wb) wb.addEventListener('click', () => openWhole(q));
    function wire() { $$('.qopt', box).forEach(b => b.addEventListener('click', () => answer(S, q, +b.dataset.choice, box))); }
    wire(); shown[q.id] = shown[q.id] == null ? SL.time.T.wall : shown[q.id];
  }
  /** Who closed a question when it was not the person (the `by` of an answer event). */
  const BY = { timeout: 'refused: nobody answered in time', canceled: 'refused: canceled (the turn stopped, or the question could not be shown)', closed: 'refused: the session closed', nobody: 'refused: no page was open to answer' };
  /** The command: every line and every character of it, in a block of its own scroll; a long one has its length and its end above it. */
  const cmdHtml = q => { const m = markNonAscii(q.cmd); return lenLine(q.cmd, 'the command') + '<div class="qcmd qblk"><i>' + mark(q.cwd || '.') + ' $</i> ' + m.html + '</div>' + markNote(m); };
  /** Why it asks, in a block of its own scroll (a reason can be as long as the command it explains). */
  const whyHtml = (q, id) => lenLine(q.why, 'the reason') + '<div class="qwhy qblk"' + (id ? ' id="' + id + '"' : '') + '><b>why it asks</b>' + mark(q.why) + '</div>';
  ui.approvals = { render, arm, answer, paintMeters, shown, wants, choiceText, keyOf, choiceOfKey, offersTwo, diffLines, parseDiff, counts, ruleList, html: { cmd: cmdHtml, why: whyHtml, change: changeHtml, opts } };
  /**
   * Keys 1/2/3 (4 with the fourth answer) and Esc answer the front question of the active session: only when it is on screen and armed.
   * Returns true when the key answered, 'opened' when the question was not on screen (the rail was folded away or another pane was in
   * front): the key then opens the rail and starts the quiet period again instead of answering; false when the key is not for a question.
   */
  ui.approvals.tryKey = function (key) {
    const S = SL.sessions.active, q = S && S.wm ? calc.openQuestion(S.wm) : null; if (!q) return false;
    if (!(key === '1' || (key === '2' && offersTwo(q)) || key === '3' || key === 'Escape' || (key === '4' && q.offersTests))) return false;
    if (S.replay) return false;
    if (!onScreen(stripEl())) { openRail(true); shown[q.id] = SL.time.T.wall; return 'opened'; }
    const a = arm(q.id); if (!a.armed) return false; const slot = $('#qSlot'), box = $('.qbox', slot); if (box && box.dataset.tell) return false;
    if (key === '1' || key === '2' || key === '3' || (key === '4' && q.offersTests)) { answer(S, q, choiceOfKey(q, +key), box); return true; } if (key === 'Escape') { answer(S, q, 3, box); return true; } return false;
  };
  ui.approvals.pending = () => { const S = SL.sessions.active; return S && S.wm ? calc.openQuestion(S.wm) : null; };

  /* ---------- the inbox: open questions of every session ---------- */
  function mount(sc) {
    const app = $('#app'); let pop = null;
    function close() { if (pop) { pop.remove(); pop = null; } const b = $('#sInbox'); if (b) b.setAttribute('aria-expanded', 'false'); }
    function html() {
      const nd = SL.sessions.needs();
      return '<div class="ibx-h"><b>Needs you</b><span class="dim">' + nd.length + ' open question' + (nd.length === 1 ? '' : 's') + ' · answering follows the same quiet-period rule</span><button class="btn sm" type="button" data-close>esc</button></div>' +
        (nd.length ? nd.map(({ S, q, waiting }) => '<div class="ibx" data-sid="' + esc(S.id) + '" data-qid="' + esc(q.id) + '"><div class="ibx-t"><b style="color:var(--c-mgr)">' + esc(S.name) + '</b> <span class="dim">› ' + esc(q.agent) + (q.task ? ' · ' + esc(q.task) : '') + '</span>' + (waiting > 1 ? '<span class="tag warm">+' + (waiting - 1) + ' waiting</span>' : '') + '<button class="btn sm" type="button" data-open>Open session</button></div>' + cmdHtml(q) + changeHtml(q) + (q.scope ? '<div class="qscope"><b>scope</b><span class="qsv">' + mark(q.scope) + '</span></div>' : '') + whyHtml(q) + '<div class="qbox">' + opts(q, null, S) + '</div></div>').join('') : '<p class="stubnote" style="padding:14px 12px;margin:0">Nothing waits for you. A question in any session, background ones included, shows up here with a badge on its tab.</p>') +
        '<p class="ibx-f">Headless runs never ask: an action that needs approval is refused, with --ask-timeout shown.</p>';
    }
    /** Redraw the inbox, unless an instruction is being written in it: then only once that is sent or put back (needs-changed meanwhile
     *  marks it dirty), so another question arriving does not take the text or the focus. */
    function draw() {
      if (!pop) return; if (pop.querySelector('.qbox[data-tell]')) { pop._dirty = true; return; } pop._dirty = false; pop.innerHTML = html();
      $$('.ibx', pop).forEach(row => { const S = SL.sessions.get(row.dataset.sid), q = S && S.wm.qs.find(x => x.id === row.dataset.qid && !x.answered); if (!q) return; const box = $('.qbox', row);
        const rr = () => { box.innerHTML = opts(q, null, S); w(); }; box._back = rr; box._afterTell = () => { if (pop && pop._dirty) sc.timeout(draw, 0); }; const wb = $('[data-whole]', row); if (wb) wb.addEventListener('click', () => openWhole(q)); const w = () => $$('.qopt', box).forEach(b => b.addEventListener('click', () => { const ok = answer(S, q, +b.dataset.choice, box); if (ok && +b.dataset.choice !== 3) sc.timeout(draw, 60); })); w(); shown[q.id] = SL.time.T.wall;
        $('[data-open]', row).addEventListener('click', () => { close(); SL.act.switchSession(S.id); }); });
      $('[data-close]', pop).addEventListener('click', close);
    }
    /** Open or close the inbox popover under `btn` (default: the `Needs you` button of the session strip). */
    function toggle(btn) {
      if (pop) { close(); return; }
      btn = btn || $('#sInbox'); if (!btn) return;
      pop = mk_('div', 'popover ibxpop'); pop.addEventListener('click', endClick); pop.setAttribute('role', 'dialog'); pop.setAttribute('aria-label', 'Needs you: open questions'); app.appendChild(pop); btn.setAttribute('aria-expanded', 'true'); draw();
      const r = btn.getBoundingClientRect(), ar = app.getBoundingClientRect(); pop.style.top = (r.bottom - ar.top + 4) + 'px'; pop.style.right = Math.max(8, ar.right - r.right) + 'px';
    }
    const mk_ = (t, c) => { const e = document.createElement(t); e.className = c; return e; };
    const slot = $('#qSlot'); if (slot) sc.listen(slot, 'click', endClick);
    sc.on('needs-changed', () => { if (pop) { const nd = SL.sessions.needs().length; draw(); if (!nd) { /* keep open: shows the empty state */ } } });
    sc.listen(document, 'pointerdown', e => { if (pop && !pop.contains(e.target) && !e.target.closest('#sInbox')) close(); });
    /* key 4: the fourth answer's No (94-keys.js routes 1, 2 and 3); the same rule as the other keys */
    sc.listen(document, 'keydown', e => { if (e.key !== '4' || e.ctrlKey || e.metaKey || e.altKey || ui.hasModal()) return; const t = e.target, inField = t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable); if (inField && !(t.id === 'input' && t.value === '')) return; const q = ui.approvals.pending(); if (q && q.offersTests && ui.approvals.tryKey('4')) { e.preventDefault(); e.stopPropagation(); } }, true);
    sc.frame(() => { const SA = SL.sessions.active; if (SA && SA.wm) render(SA, SA.wm); if (pop) paintMeters(pop); paintMeters($('#qSlot')); const q = ui.approvals.pending(); const w = $('#qSlot .qw'); if (q && w) { const S = SL.sessions.active, s = Math.max(0, Math.floor(S.wt - q.t0)) + 1, t = 'waiting for you: ' + (s < 60 ? s + 's' : Math.floor(s / 60) + 'm ' + (s % 60) + 's'); if (w.textContent !== t) w.textContent = t; } });
    ui.inbox = { toggle, close, isOpen: () => !!pop };
  }
  ui.approvals.mount = mount;
})(SL);
