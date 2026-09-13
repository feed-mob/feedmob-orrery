// Orrery's web UI. Plain DOM against the same JSON API the CLI uses — no
// framework and no CDN, because a control plane that cannot render its own
// dashboard without reaching the internet is a dependency nobody asked for.
'use strict';

const app = document.getElementById('app');
const crumb = document.getElementById('crumb');
const tick = document.getElementById('tick');

let timer = null;

async function api(path, opts) {
  const res = await fetch(path, opts);
  const text = await res.text();
  if (!res.ok) {
    let msg = text;
    try { msg = JSON.parse(text).error || text; } catch (_) {}
    throw new Error(msg || res.statusText);
  }
  return text ? JSON.parse(text) : null;
}

function el(tag, attrs, ...kids) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k === 'class') n.className = v;
    else if (k.startsWith('on')) n.addEventListener(k.slice(2), v);
    else n.setAttribute(k, v);
  }
  for (const kid of kids.flat()) {
    if (kid === null || kid === undefined || kid === false) continue;
    n.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }
  return n;
}

// Colour follows the verdict, not the status: a run that is still going is
// neutral, and "cancelled" is deliberately not red — we stopped looking, the
// code is not necessarily bad.
function toneOf(status, result) {
  if (status !== 'done') return status === 'pending' ? 'idle' : 'warn';
  switch (result) {
    case 'success': return 'ok';
    case 'failure': return 'fail';
    case 'cancelled': case 'skipped': return 'idle';
    default: return 'warn';
  }
}

function verdict(status, result) {
  if (status === 'pending') return '等待并发组';
  if (status !== 'done') return status;
  return result || 'done';
}

function ms(a, b) {
  if (!a || !b) return '';
  const d = new Date(b) - new Date(a);
  if (!isFinite(d) || d < 0) return '';
  if (d < 1000) return d + 'ms';
  if (d < 60000) return (d / 1000).toFixed(1) + 's';
  return Math.floor(d / 60000) + 'm' + Math.round((d % 60000) / 1000) + 's';
}

function ago(iso) {
  if (!iso) return '';
  const d = (Date.now() - new Date(iso)) / 1000;
  if (!isFinite(d)) return '';
  if (d < 60) return Math.max(0, Math.round(d)) + ' 秒前';
  if (d < 3600) return Math.round(d / 60) + ' 分钟前';
  if (d < 86400) return Math.round(d / 3600) + ' 小时前';
  return Math.round(d / 86400) + ' 天前';
}

function shortRef(ref) {
  return (ref || '').replace(/^refs\/heads\//, '').replace(/^refs\/tags\//, '');
}

// ------------------------------------------------------------- runs list --

async function renderRuns() {
  crumb.textContent = '';
  const runs = await api('/api/runs?limit=50');
  if (!runs || !runs.length) {
    app.replaceChildren(el('div', { class: 'card' },
      el('div', { class: 'empty' }, '还没有任何 run。推一次代码，或者 orrery submit。')));
    return runs;
  }
  const rows = runs.map(r => el('tr', {},
    el('td', { class: 'num mono' },
      el('a', { href: '#/runs/' + r.ID }, '#' + r.ID)),
    el('td', {},
      el('a', { href: '#/runs/' + r.ID }, r.WorkflowName || r.WorkflowFile),
      r.RunAttempt > 1 ? el('span', { class: 'dim' }, ' · 第 ' + r.RunAttempt + ' 次') : null),
    el('td', { class: 'dim mono secondary' }, r.Repo),
    el('td', { class: 'mono' }, shortRef(r.Ref)),
    el('td', { class: 'dim secondary' }, r.Event),
    el('td', { class: 'dim secondary' }, r.Actor),
    el('td', {}, el('span', { class: 'badge ' + toneOf(r.Status, r.Result) },
      verdict(r.Status, r.Result))),
    el('td', { class: 'dim' }, ago(r.CreatedAt)),
  ));
  app.replaceChildren(el('div', { class: 'card' },
    el('h2', {}, 'RUNS', el('span', { class: 'spacer' }), el('span', { class: 'dim' }, runs.length + ' 条')),
    el('div', { class: 'scroll' }, el('table', {},
      el('thead', {}, el('tr', {},
        el('th', { class: 'num' }, 'RUN'), el('th', {}, 'WORKFLOW'),
        el('th', { class: 'secondary' }, '仓库'), el('th', {}, 'REF'),
        el('th', { class: 'secondary' }, '事件'), el('th', { class: 'secondary' }, '触发者'),
        el('th', {}, '结果'), el('th', {}, '时间'))),
      el('tbody', {}, rows)))));
  return runs;
}

// ------------------------------------------------------------ run detail --

const openLogs = new Set();

async function renderRun(id) {
  crumb.textContent = 'run #' + id;
  const sum = await api('/api/runs/' + id);
  const run = sum.Run, jobs = sum.Jobs || [];
  const live = run.Status !== 'done';

  const head = el('div', { class: 'card' },
    el('h2', {},
      'RUN #' + run.ID,
      run.RunAttempt > 1 ? el('span', { class: 'dim' }, '第 ' + run.RunAttempt + ' 次') : null,
      el('span', { class: 'spacer' }),
      el('button', {
        onclick: () => act('/api/runs/' + id + '/rerun?failed_only=true', '重跑失败的'),
        disabled: live,
      }, '重跑失败的'),
      el('button', {
        onclick: () => act('/api/runs/' + id + '/rerun', '全部重跑'),
        disabled: live,
      }, '全部重跑')),
    el('div', { style: 'padding:12px 14px' },
      el('div', { class: 'run-line' },
        el('strong', {}, run.WorkflowName || run.WorkflowFile),
        el('span', { class: 'badge ' + toneOf(run.Status, run.Result) }, verdict(run.Status, run.Result))),
      el('div', { class: 'run-line dim mono', style: 'margin-top:6px' },
        // Wrapped individually: flex gap applies between elements, and bare
        // text nodes would run together into one unreadable word.
        [run.Repo, shortRef(run.Ref), (run.SHA || '').slice(0, 7), run.Event, run.Actor]
          .filter(Boolean).map(v => el('span', {}, v)))));

  const cards = jobs.map(j => jobCard(j, run));
  app.replaceChildren(head, ...cards);
  return run;
}

function jobCard(j, run) {
  const steps = j.Steps || [];
  const note = j.ForceTerminated
    ? '强杀，cleanup_ran=' + (j.CleanupRan ? 'true' : 'false')
    : (j.StopReason || '');

  const body = steps.length
    ? el('ul', { class: 'steps' }, steps.map(st => el('li', {},
        el('span', { class: 'badge ' + toneOf('done', st.Result) }, st.Result || '—'),
        el('span', { class: 'name' }, st.Name || ('步骤 ' + st.Index)),
        el('span', { class: 'dim mono' }, ms(st.StartedAt, st.StoppedAt)),
        st.LogLength > 0
          ? el('span', { class: 'dim mono' }, '行 ' + st.LogIndex + '–' + (st.LogIndex + st.LogLength - 1))
          : null)))
    : el('div', { class: 'empty' }, emptyReason(j));

  const logBox = el('pre', { class: 'log', id: 'log-' + j.ID, hidden: openLogs.has(j.ID) ? null : 'hidden' });
  if (openLogs.has(j.ID)) loadLog(j, logBox);

  return el('div', { class: 'card' },
    el('h2', {},
      j.Name || j.Key,
      el('span', { class: 'badge ' + toneOf(j.Status, j.Result) }, verdict(j.Status, j.Result)),
      note ? el('span', { class: 'dim' }, note) : null,
      el('span', { class: 'spacer' }),
      j.Attempt > 1 ? el('span', { class: 'dim' }, '第 ' + j.Attempt + ' 次') : null,
      el('button', {
        onclick: (e) => {
          if (openLogs.has(j.ID)) { openLogs.delete(j.ID); logBox.hidden = true; e.target.textContent = '日志'; }
          else { openLogs.add(j.ID); logBox.hidden = false; e.target.textContent = '收起'; loadLog(j, logBox); }
        },
      }, openLogs.has(j.ID) ? '收起' : '日志'),
      el('button', {
        onclick: () => act('/api/jobs/' + j.ID + '/stop', '停止', { reason: 'human' }),
        disabled: j.Status !== 'running',
      }, '停止')),
    body, logBox);
}

function emptyReason(j) {
  if (j.Result === 'skipped') {
    return j.StopReason === 'if_false' ? '`if:` 判为假，未运行' : '上游未成功，未运行';
  }
  if (j.Status === 'blocked') return '等待上游';
  if (j.Status === 'queued') return '等待 runner';
  return '还没有步骤';
}

async function loadLog(j, box) {
  try {
    const res = await fetch('/api/jobs/' + j.ID + '/logs');
    const text = await res.text();
    box.replaceChildren(...colourise(text));
    // Only pin to the bottom while the job is still producing output;
    // yanking the view down under someone reading a finished log is rude.
    if (j.Status === 'running') box.scrollTop = box.scrollHeight;
  } catch (err) {
    box.replaceChildren(el('span', { class: 'err' }, '读日志失败：' + err.message));
  }
}

// colourise keeps the workflow-command markers legible instead of stripping
// them: `::error::` is how a step says what went wrong.
function colourise(text) {
  const out = [];
  for (const line of text.split('\n')) {
    if (!line) { out.push(document.createTextNode('\n')); continue; }
    const m = line.match(/^(\S+Z)\s([\s\S]*)$/);
    const stamp = m ? m[1] : '';
    const rest = m ? m[2] : line;
    if (stamp) out.push(el('span', { class: 't' }, stamp.slice(11, 19) + ' '));
    let cls = null;
    if (/^::(error|warning)::/.test(rest)) cls = 'err';
    else if (/^::(group|endgroup)::/.test(rest)) cls = 'grp';
    out.push(cls ? el('span', { class: cls }, rest) : document.createTextNode(rest));
    out.push(document.createTextNode('\n'));
  }
  return out;
}

async function act(url, label, body) {
  try {
    await api(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body || {}),
    });
    route();
  } catch (err) {
    app.prepend(el('div', { class: 'card' },
      el('div', { class: 'err-banner' }, label + '失败：' + err.message)));
  }
}

// ------------------------------------------------------------------ route --

async function route() {
  if (timer) { clearTimeout(timer); timer = null; }
  const m = location.hash.match(/^#\/runs\/(\d+)/);
  try {
    const live = m ? await renderRun(Number(m[1])) : await renderRuns();
    tick.textContent = '刷新于 ' + new Date().toLocaleTimeString();
    // Poll only while something is actually moving. A dashboard that hammers
    // its own control plane while nobody is looking is its own outage.
    const moving = Array.isArray(live)
      ? live.some(r => r.Status !== 'done')
      : live && live.Status !== 'done';
    if (moving) timer = setTimeout(route, 2000);
  } catch (err) {
    app.replaceChildren(el('div', { class: 'card' },
      el('div', { class: 'err-banner' }, '加载失败：' + err.message)));
    timer = setTimeout(route, 5000);
  }
}

window.addEventListener('hashchange', route);
route();
