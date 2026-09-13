// Orrery's dashboard. Plain DOM against the same JSON API the CLI uses — no
// framework and no CDN, because a control plane that cannot render its own
// dashboard without reaching the internet is a dependency nobody asked for.
'use strict';

import {
  toneOf, verdict, duration, ago, shortRef, classify, splitStamp, groupLines, countMatches,
  machineTime, share,
} from './format.js';

const app = document.getElementById('app');
const crumb = document.getElementById('crumb');
const tick = document.getElementById('tick');
const nav = document.getElementById('nav');

let timer = null;
let config = { can_dispatch: false, repos: [] };

class Unauthorized extends Error {}

async function api(path, opts) {
  const res = await fetch(path, opts);
  const text = await res.text();
  if (res.status === 401) throw new Unauthorized('需要令牌');
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
    else if (k === 'text') n.textContent = v;
    else if (k.startsWith('on')) n.addEventListener(k.slice(2), v);
    else n.setAttribute(k, v);
  }
  for (const kid of kids.flat()) {
    if (kid === null || kid === undefined || kid === false) continue;
    n.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }
  return n;
}

function card(title, ...body) {
  return el('div', { class: 'card' }, title ? el('h2', {}, title) : null, ...body);
}

function banner(msg, tone = 'err-banner') {
  return el('div', { class: 'card' }, el('div', { class: tone }, msg));
}

// table builds a scrollable table from plain data. Written as a helper because
// the alternative — eight levels of nested el() calls per view — is how a
// missing parenthesis got shipped, and it reads no better than this does.
//
// A header may be a string, or [label, className] to mark it secondary so the
// phone layout can drop it.
function table(headers, rows) {
  const th = headers.map(h => {
    const [label, cls] = Array.isArray(h) ? h : [h, null];
    return el('th', { class: cls }, label);
  });
  return el('div', { class: 'scroll' },
    el('table', {},
      el('thead', {}, el('tr', {}, th)),
      el('tbody', {}, rows)));
}

// ------------------------------------------------------------------ auth --

// The token is traded for an HttpOnly cookie rather than kept in localStorage:
// anything in localStorage is readable by every script on the page, and this
// token can start a workflow with the server's secrets.
function renderSignIn(message) {
  const input = el('input', { type: 'password', placeholder: 'API token', class: 'text' });
  const submit = async () => {
    try {
      await api('/api/session', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token: input.value }),
      });
      route();
    } catch (err) {
      renderSignIn(err instanceof Unauthorized ? '令牌不对' : err.message);
    }
  };
  input.addEventListener('keydown', e => { if (e.key === 'Enter') submit(); });
  nav.replaceChildren();
  app.replaceChildren(card('需要登录',
    el('div', { class: 'row pad' },
      input,
      el('button', { onclick: submit }, '进入'),
      message ? el('span', { class: 'fail' }, message) : null),
    el('div', { class: 'dim pad-x pad-b' },
      '服务端 -api-token 的值。这个令牌能启动 workflow 并用到服务端注入的密钥。')));
  input.focus();
}

// ------------------------------------------------------------------- nav --

function renderNav(active) {
  const items = [
    ['#/', 'RUNS'],
    ['#/deployments', '部署'],
    ['#/usage', '用量'],
    ['#/runners', 'RUNNERS'],
    ['#/schedules', '定时'],
  ];
  if (config.can_dispatch) items.splice(1, 0, ['#/dispatch', '手动触发']);
  nav.replaceChildren(...items.map(([href, label]) =>
    el('a', { href, class: href === active ? 'on' : null }, label)));
}

// ------------------------------------------------------------ runs list --

// Filters live in the URL so a filtered view is a link someone can paste into
// chat, and so reloading does not throw the question away.
function filtersFromHash() {
  const q = location.hash.split('?')[1] || '';
  return Object.fromEntries(new URLSearchParams(q));
}

function setFilters(next) {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(next)) if (v) params.set(k, v);
  const q = params.toString();
  location.hash = '#/' + (q ? '?' + q : '');
}

const FILTERS = [
  ['workflow', 'workflow'],
  ['repo', '仓库'],
  ['branch', '分支'],
  ['status', '状态'],
  ['result', '结果'],
  ['event', '事件'],
  ['actor', '触发者'],
];

async function renderRuns() {
  renderNav('#/');
  crumb.textContent = '';
  const f = filtersFromHash();
  const params = new URLSearchParams(f);
  params.set('limit', '50');

  const [page, facets] = await Promise.all([
    api('/api/runs?' + params.toString()),
    api('/api/facets').catch(() => ({})),
  ]);
  const runs = page.Runs || [];

  // A disclosure rather than an always-open row: seven stacked controls on a
  // phone push the runs themselves off the screen. Open on a wide viewport, and
  // open regardless when a filter is already applied — a hidden filter that is
  // silently narrowing the list is worse than a tall page.
  const active = Object.keys(f).filter(k => k !== 'before' && f[k]);
  const controls = el('details', {
    class: 'filters',
    open: (window.matchMedia('(min-width: 721px)').matches || active.length) ? 'open' : null,
  },
    el('summary', {}, '筛选', active.length ? el('span', { class: 'dim' }, `（${active.length} 项）`) : null),
    el('div', { class: 'row pad wrap' },
      ...FILTERS.map(([key, label]) => filterControl(key, label, f, facets)),
      active.length ? el('button', { onclick: () => setFilters({}) }, '清除') : null));

  const body = runs.length
    ? table(
        [['RUN', 'num'], 'WORKFLOW', ['仓库', 'secondary'], 'REF',
         ['事件', 'secondary'], ['触发者', 'secondary'], '结果', '时间'],
        runs.map(runRow))
    : el('div', { class: 'empty' },
        Object.keys(f).length ? '没有匹配的 run。' : '还没有任何 run。推一次代码，或者 orrery submit。');

  const footer = page.NextBefore
    ? el('div', { class: 'row pad' },
        el('button', {
          onclick: () => { setFilters({ ...f, before: page.NextBefore }); },
        }, '看更早的'),
        f.before ? el('button', { onclick: () => setFilters({ ...f, before: '' }) }, '回到最新') : null)
    : (f.before ? el('div', { class: 'row pad' },
        el('button', { onclick: () => setFilters({ ...f, before: '' }) }, '回到最新')) : null);

  app.replaceChildren(el('div', { class: 'card' },
    el('h2', {}, 'RUNS', el('span', { class: 'spacer' }),
      el('span', { class: 'dim' }, `${runs.length} / ${page.Total} 条`)),
    controls, body, footer));
  return runs;
}

function filterControl(key, label, f, facets) {
  const options = {
    repo: facets.Repos, event: facets.Events, actor: facets.Actors,
    status: ['queued', 'pending', 'running', 'done'],
    result: ['success', 'failure', 'cancelled', 'skipped'],
  }[key];
  if (options && options.length) {
    const sel = el('select', {
      class: 'text',
      onchange: e => setFilters({ ...f, before: '', [key]: e.target.value }),
    }, el('option', { value: '' }, label),
       ...options.map(v => el('option', { value: v, selected: f[key] === v ? 'selected' : null }, v)));
    return sel;
  }
  // Free text for the things a facet list cannot usefully enumerate.
  const input = el('input', {
    class: 'text narrow', type: 'search', placeholder: label, value: f[key] || '',
    onchange: e => setFilters({ ...f, before: '', [key]: e.target.value.trim() }),
  });
  return input;
}

function runRow(r) {
  return el('tr', {},
    el('td', { class: 'num mono' }, el('a', { href: '#/runs/' + r.ID }, '#' + r.ID)),
    el('td', {},
      el('a', { href: '#/runs/' + r.ID }, r.WorkflowName || r.WorkflowFile),
      r.RunAttempt > 1 ? el('span', { class: 'dim' }, ' · 第 ' + r.RunAttempt + ' 次') : null),
    el('td', { class: 'dim mono secondary' }, r.Repo),
    el('td', { class: 'mono' }, shortRef(r.Ref)),
    el('td', { class: 'dim secondary' }, r.Event),
    el('td', { class: 'dim secondary' }, r.Actor),
    el('td', {}, el('span', { class: 'badge ' + toneOf(r.Status, r.Result) }, verdict(r.Status, r.Result))),
    el('td', { class: 'dim' }, ago(r.CreatedAt)));
}

// ------------------------------------------------------------ run detail --

// Per-job view state survives the two-second refresh: which logs are open, how
// far each has been read, and what someone typed into the find box.
const logState = new Map(); // jobID -> {open, from, lines, find, collapsed:Set}

function stateFor(id) {
  if (!logState.has(id)) {
    logState.set(id, { open: false, from: 0, lines: [], find: '', collapsed: new Set() });
  }
  return logState.get(id);
}

async function renderRun(id) {
  renderNav(null);
  crumb.textContent = 'run #' + id;
  const sum = await api('/api/runs/' + id);
  const run = sum.Run, jobs = sum.Jobs || [];
  const live = run.Status !== 'done';

  const head = el('div', { class: 'card' },
    el('h2', {},
      'RUN #' + run.ID,
      run.RunAttempt > 1 ? el('span', { class: 'dim' }, '第 ' + run.RunAttempt + ' 次') : null,
      el('span', { class: 'spacer' }),
      live
        ? el('button', {
            class: 'danger',
            onclick: () => act('/api/runs/' + id + '/cancel?by=ui', '取消'),
          }, '取消整个 run')
        : [
            el('button', { onclick: () => act('/api/runs/' + id + '/rerun?failed_only=true', '重跑失败的') },
              '重跑失败的'),
            el('button', { onclick: () => act('/api/runs/' + id + '/rerun', '全部重跑') }, '全部重跑'),
          ]),
    el('div', { class: 'pad' },
      el('div', { class: 'row' },
        el('strong', {}, run.WorkflowName || run.WorkflowFile),
        el('span', { class: 'badge ' + toneOf(run.Status, run.Result) }, verdict(run.Status, run.Result))),
      el('div', { class: 'row dim mono tight' },
        [run.Repo, shortRef(run.Ref), (run.SHA || '').slice(0, 7), run.Event, run.Actor]
          .filter(Boolean).map(v => el('span', {}, v)))));

  app.replaceChildren(head, ...jobs.map(j => jobCard(j)));
  return run;
}

function jobCard(j) {
  const st = stateFor(j.ID);
  const steps = j.Steps || [];
  const note = j.ForceTerminated
    ? '强杀，cleanup_ran=' + (j.CleanupRan ? 'true' : 'false')
    : (j.StopReason || '');

  const body = steps.length
    ? el('ul', { class: 'steps' }, steps.map(step => el('li', {},
        el('span', { class: 'badge ' + toneOf('done', step.Result) }, step.Result || '—'),
        el('span', { class: 'name' }, step.Name || ('步骤 ' + step.Index)),
        el('span', { class: 'dim mono' }, duration(step.StartedAt, step.StoppedAt)),
        step.LogLength > 0
          ? el('span', { class: 'dim mono' }, '行 ' + step.LogIndex + '–' + (step.LogIndex + step.LogLength - 1))
          : null)))
    : el('div', { class: 'empty' }, emptyReason(j));

  const logBox = el('div', { class: 'logwrap', hidden: st.open ? null : 'hidden' });
  if (st.open) paintLog(j, logBox);

  return el('div', { class: 'card' },
    el('h2', {},
      j.Name || j.Key,
      el('span', { class: 'badge ' + toneOf(j.Status, j.Result) }, verdict(j.Status, j.Result)),
      note ? el('span', { class: 'dim' }, note) : null,
      el('span', { class: 'spacer' }),
      j.Attempt > 1 ? el('span', { class: 'dim' }, '第 ' + j.Attempt + ' 次') : null,
      el('button', {
        onclick: async (e) => {
          st.open = !st.open;
          e.target.textContent = st.open ? '收起' : '日志';
          logBox.hidden = !st.open;
          if (st.open) { await fetchLog(j); paintLog(j, logBox); }
        },
      }, st.open ? '收起' : '日志'),
      el('button', {
        onclick: () => act('/api/jobs/' + j.ID + '/stop?by=ui', '停止'),
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

// ------------------------------------------------------------------ logs --

// fetchLog pulls only what arrived since last time. Refetching the whole log
// every two seconds costs a few megabytes per tick on a job that prints a few
// megabytes, for one open browser tab.
async function fetchLog(j) {
  const st = stateFor(j.ID);
  const res = await fetch(`/api/jobs/${j.ID}/logs?from=${st.from}`);
  if (res.status === 401) throw new Unauthorized('需要令牌');
  if (!res.ok) throw new Error(await res.text());
  const text = await res.text();
  const next = Number(res.headers.get('X-Orrery-Next') || st.from);
  if (text) {
    const lines = text.split('\n');
    if (lines.length && lines[lines.length - 1] === '') lines.pop();
    st.lines.push(...lines);
  }
  st.from = next;
}

const MAX_RENDERED_LINES = 5000;

function paintLog(j, box) {
  const st = stateFor(j.ID);
  const find = st.find;
  const matches = countMatches(st.lines, find);

  const findBox = el('input', {
    class: 'text narrow', type: 'search', placeholder: '在日志中查找', value: find,
    oninput: e => {
      st.find = e.target.value;
      paintLog(j, box);
      box.querySelector('input[type=search]')?.focus();
    },
  });

  const bar = el('div', { class: 'row logbar' },
    findBox,
    find ? el('span', { class: 'dim' }, matches + ' 处') : null,
    el('span', { class: 'spacer' }),
    el('span', { class: 'dim mono' }, st.lines.length + ' 行'),
    el('a', { href: `/api/jobs/${j.ID}/logs`, target: '_blank', class: 'dim' }, '纯文本'));

  // The tail is what anyone opening a log wants; the whole thing is one click
  // away above. Rendering all of it put ~240k nodes in the DOM and threw.
  const shown = st.lines.length > MAX_RENDERED_LINES
    ? st.lines.slice(-MAX_RENDERED_LINES)
    : st.lines;

  const pre = el('pre', { class: 'log' });
  const frag = document.createDocumentFragment();
  if (shown.length < st.lines.length) {
    frag.append(el('div', { class: 'dim' },
      `… 前 ${st.lines.length - shown.length} 行已省略，点上面的「纯文本」看完整日志`));
  }
  for (const node of renderBlocks(groupLines(shown), j.ID, find)) frag.append(node);
  pre.replaceChildren(frag);

  box.replaceChildren(bar, pre);
  // Only pin to the bottom while the job is still producing output; yanking the
  // view down under someone reading a finished log is rude.
  if (j.Status === 'running' && !find) pre.scrollTop = pre.scrollHeight;
}

function renderBlocks(blocks, jobID, find) {
  const st = stateFor(jobID);
  const out = [];
  for (const b of blocks) {
    if (b.kind === 'line') {
      out.push(lineNode(b, find));
      continue;
    }
    const key = b.title;
    // Groups start folded: a log with twenty setup groups should open as twenty
    // headings, not a wall. A group containing a search hit opens itself.
    const hit = find && b.lines.some(l => l.rest.toLowerCase().includes(find.toLowerCase()));
    const open = hit || st.collapsed.has(key) === false && st.expanded?.has(key);
    const inner = el('div', { class: 'groupbody', hidden: open ? null : 'hidden' });
    for (const l of b.lines) inner.append(lineNode(l, find));
    const head = el('div', {
      class: 'grouphead',
      onclick: () => {
        st.expanded = st.expanded || new Set();
        if (st.expanded.has(key)) st.expanded.delete(key); else st.expanded.add(key);
        inner.hidden = !st.expanded.has(key);
        head.firstChild.textContent = st.expanded.has(key) ? '▾ ' : '▸ ';
      },
    }, el('span', {}, open ? '▾ ' : '▸ '), el('span', { class: 'grp' }, b.title),
       el('span', { class: 'dim' }, ` (${b.lines.length} 行)`));
    out.push(head, inner);
  }
  return out;
}

function lineNode(l, find) {
  const cls = classify(l.rest);
  const row = el('div', { class: 'logline' + (cls ? ' ' + cls : '') });
  if (l.stamp) row.append(el('span', { class: 't' }, l.stamp.slice(11, 19) + ' '));
  if (find && l.rest.toLowerCase().includes(find.toLowerCase())) {
    // Split on the needle and mark the hits, without innerHTML: log content is
    // whatever a step printed, and that is not ours to trust as markup.
    const lower = l.rest.toLowerCase(), needle = find.toLowerCase();
    let i = 0;
    while (i < l.rest.length) {
      const at = lower.indexOf(needle, i);
      if (at < 0) { row.append(l.rest.slice(i)); break; }
      if (at > i) row.append(l.rest.slice(i, at));
      row.append(el('mark', {}, l.rest.slice(at, at + needle.length)));
      i = at + needle.length;
    }
  } else {
    row.append(l.rest);
  }
  return row;
}

// --------------------------------------------------------------- runners --

async function renderRunners() {
  renderNav('#/runners');
  crumb.textContent = '';
  const runners = await api('/api/runners');
  if (!runners.length) {
    app.replaceChildren(card('RUNNERS', el('div', { class: 'empty' },
      '没有 runner 注册过。没有 runner，提交的 run 会一直排队。')));
    return runners;
  }
  const rows = runners.map(rn => el('tr', {},
    el('td', { class: 'mono' }, rn.Name),
    el('td', { class: 'mono dim' }, (rn.Labels || []).join(' ')),
    el('td', { class: 'dim secondary' }, rn.Version),
    el('td', {}, el('span', { class: 'badge ' + runnerTone(rn.Health) }, runnerLabel(rn.Health))),
    el('td', { class: 'dim' }, ago(rn.LastSeenAt))));
  app.replaceChildren(el('div', { class: 'card' },
    el('h2', {}, 'RUNNERS', el('span', { class: 'spacer' }),
      el('span', { class: 'dim' }, runners.length + ' 台')),
    table(['名字', '标签', ['版本', 'secondary'], '状态', '最后心跳'], rows)));
  return runners;
}

function runnerTone(h) {
  return h === 'online' ? 'ok' : h === 'stale' ? 'warn' : 'fail';
}

function runnerLabel(h) {
  // A runner polls continuously, so silence is the signal: "last seen 40
  // minutes ago" means gone, not quiet.
  return h === 'online' ? '在线' : h === 'stale' ? '心跳迟了' : '失联';
}

// ------------------------------------------------------------- schedules --

async function renderSchedules() {
  renderNav('#/schedules');
  crumb.textContent = '';
  const scheds = await api('/api/schedules');
  if (!scheds.length) {
    app.replaceChildren(card('定时任务', el('div', { class: 'empty' },
      '没有注册的定时任务。cron 是在 push 到默认分支时从 workflow 里读出来注册的。')));
    return scheds;
  }
  const rows = scheds.map(sc => el('tr', {},
    el('td', { class: 'mono dim' }, sc.Repo),
    el('td', { class: 'mono' }, sc.WorkflowFile),
    el('td', { class: 'mono' }, sc.Ref),
    el('td', { class: 'mono' }, sc.Cron),
    el('td', { class: 'dim' }, new Date(sc.NextDueAt).toLocaleString())));
  app.replaceChildren(el('div', { class: 'card' },
    el('h2', {}, '定时任务', el('span', { class: 'spacer' }),
      el('span', { class: 'dim' }, scheds.length + ' 条')),
    table(['仓库', 'workflow', '分支', 'cron', '下次'], rows)));
  return scheds;
}

// ----------------------------------------------------------- deployments --

// The deployments page answers the question anyone asks first when something is
// wrong: which version is on production right now. A run listing cannot answer
// it — it only knows that job 47 passed.
async function renderDeployments() {
  renderNav('#/deployments');
  crumb.textContent = '';
  const current = await api('/api/deployments');
  if (!current.length) {
    app.replaceChildren(card('部署', el('div', { class: 'empty' },
      '还没有部署记录。给部署 job 加上 `environment: production`，它跑完就会记一笔。')));
    return current;
  }
  const rows = current.map(d => el('tr', {},
    el('td', {}, el('strong', {}, d.Environment),
      d.URL ? el('a', { href: d.URL, target: '_blank', class: 'dim', style: 'margin-left:8px' }, '↗') : null),
    el('td', { class: 'mono dim secondary' }, d.Repo),
    el('td', { class: 'mono' }, d.Version),
    el('td', { class: 'mono dim secondary' }, (d.SHA || '').slice(0, 7)),
    el('td', {}, el('span', { class: 'badge ' + toneOf('done', d.Result) }, d.Result),
      d.RolledBackFrom ? el('span', { class: 'dim' }, ' 回滚') : null),
    el('td', { class: 'dim' }, el('a', { href: '#/runs/' + d.RunID }, '#' + d.RunID)),
    el('td', { class: 'dim' }, ago(d.CreatedAt)),
    el('td', {}, el('button', {
      onclick: () => act('/api/deployments/rollback', '回滚',
        { repo: d.Repo, environment: d.Environment }),
    }, '回滚'))));

  app.replaceChildren(el('div', { class: 'card' },
    el('h2', {}, '当前部署', el('span', { class: 'spacer' }),
      el('span', { class: 'dim' }, current.length + ' 个环境')),
    table(['环境', ['仓库', 'secondary'], '版本', ['COMMIT', 'secondary'],
           '结果', 'RUN', '时间', ''], rows),
    el('div', { class: 'dim pad' },
      '「回滚」会用上一个成功版本重新派发当初那个部署 workflow——它是一次新的部署，不是撤销。')));
  return current;
}

// ----------------------------------------------------------------- usage --

// Minutes, not money: what a minute costs depends on where the runner runs, and
// a number pretending to be dollars while nobody has told it the machine's
// price is worse than no number.
async function renderUsage() {
  renderNav('#/usage');
  crumb.textContent = '';
  const days = Number(filtersFromHash().days) || 30;
  const data = await api('/api/usage?days=' + days);
  const rows = data.rows || [];

  // Include whatever the URL asked for, even if it is not one of the presets:
  // a picker that silently displays "7 days" while showing one day's data is
  // the same lie as a form field whose value is not what gets submitted.
  const choices = [...new Set([7, 30, 90, days])].sort((a, b) => a - b);
  const picker = el('select', {
    class: 'text narrow',
    onchange: e => { location.hash = '#/usage?days=' + e.target.value; },
  }, ...choices.map(d => el('option', {
    value: d, selected: d === days ? 'selected' : null,
  }, '最近 ' + d + ' 天')));

  if (!rows.length) {
    app.replaceChildren(card('用量',
      el('div', { class: 'row pad' }, picker),
      el('div', { class: 'empty' }, '这个窗口里没有跑完的 job。')));
    return rows;
  }
  const body = rows.map(u => el('tr', {},
    el('td', {}, u.WorkflowName || '(未命名)'),
    el('td', { class: 'mono dim secondary' }, u.Repo),
    el('td', { class: 'num mono' }, machineTime(u.Millis)),
    el('td', {},
      // The bar has already done the comparison a column of numbers asks you
      // to do in your head.
      el('div', { class: 'bar' },
        el('div', { class: 'fill', style: `width:${share(u.Millis, data.total_millis).toFixed(1)}%` }))),
    el('td', { class: 'num dim' }, u.Runs),
    el('td', { class: 'num dim secondary' }, u.Jobs),
    el('td', { class: 'num' }, u.Failed
      ? el('span', { class: 'fail' }, u.Failed)
      : el('span', { class: 'dim' }, '0'))));

  app.replaceChildren(el('div', { class: 'card' },
    el('h2', {}, '用量', el('span', { class: 'spacer' }),
      el('span', { class: 'dim' },
        `${machineTime(data.total_millis)} · ${data.total_runs} 个 run`)),
    el('div', { class: 'row pad' }, picker,
      el('span', { class: 'dim' }, '机器时间，不是钱——一分钟值多少取决于 runner 跑在哪')),
    table(['WORKFLOW', ['仓库', 'secondary'], ['机器时间', 'num'], '占比',
           ['RUN', 'num'], ['JOB', 'num secondary'], ['失败', 'num']], body)));
  return rows;
}

// -------------------------------------------------------------- dispatch --

async function renderDispatch() {
  renderNav('#/dispatch');
  crumb.textContent = '';
  const repos = config.repos && config.repos.length ? config.repos : [];
  const state = { repo: repos[0] || '', ref: 'HEAD', workflows: [], picked: null, inputs: {} };

  const mount = el('div', {});
  app.replaceChildren(card('手动触发', mount));

  const draw = () => {
    const repoField = repos.length
      ? el('select', { class: 'text', onchange: e => { state.repo = e.target.value; load(); } },
          ...repos.map(r => el('option', { value: r, selected: r === state.repo ? 'selected' : null }, r)))
      : el('input', {
          class: 'text', placeholder: 'owner/repo', value: state.repo,
          onchange: e => { state.repo = e.target.value.trim(); load(); },
        });

    const wfField = el('select', {
      class: 'text',
      onchange: e => { state.picked = state.workflows.find(w => w.path === e.target.value) || null; state.inputs = {}; draw(); },
    }, el('option', { value: '' }, state.workflows.length ? '选一个 workflow' : '（没有声明 workflow_dispatch 的）'),
       ...state.workflows.map(w => el('option', {
         value: w.path, selected: state.picked && state.picked.path === w.path ? 'selected' : null,
       }, w.name)));

    const inputFields = [];
    if (state.picked && state.picked.inputs) {
      for (const [name, spec] of Object.entries(state.picked.inputs)) {
        inputFields.push(inputField(name, spec, state));
      }
    }

    // Filtered: replaceChildren stringifies a null into the literal text
    // "null", which is how a stray one ends up on the page.
    mount.replaceChildren(...[
      el('div', { class: 'row pad wrap' },
        el('label', {}, '仓库'), repoField,
        el('label', {}, 'ref'),
        el('input', {
          class: 'text narrow', value: state.ref,
          onchange: e => { state.ref = e.target.value.trim() || 'HEAD'; load(); },
        }),
        el('label', {}, 'workflow'), wfField),
      inputFields.length ? el('div', { class: 'pad-x' }, ...inputFields) : null,
      el('div', { class: 'row pad' },
        el('button', {
          disabled: !state.picked,
          onclick: async () => {
            try {
              const out = await api('/api/dispatch', {
                method: 'POST', headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                  repo: state.repo, workflow_file: state.picked.path,
                  ref: state.ref, actor: 'ui', inputs: state.inputs,
                }),
              });
              location.hash = '#/runs/' + out.run_id;
            } catch (err) {
              if (err instanceof Unauthorized) return renderSignIn();
              mount.prepend(el('div', { class: 'err-banner' }, '触发失败：' + err.message));
            }
          },
        }, '触发'),
        el('span', { class: 'dim' }, '参数在 run 创建之前校验，错了不会有 job 起来')),
    ].filter(Boolean));
  };

  const load = async () => {
    state.workflows = []; state.picked = null;
    draw();
    if (!state.repo) return;
    try {
      state.workflows = await api(`/api/workflows?repo=${encodeURIComponent(state.repo)}&ref=${encodeURIComponent(state.ref)}`);
    } catch (err) {
      if (err instanceof Unauthorized) return renderSignIn();
      mount.prepend(el('div', { class: 'err-banner' }, '读 workflow 失败：' + err.message));
    }
    draw();
  };

  await load();
  return null;
}

function inputField(name, spec, state) {
  if (state.inputs[name] === undefined) state.inputs[name] = spec.default || '';
  const set = v => { state.inputs[name] = v; };
  let control;
  if (spec.type === 'choice' && spec.options) {
    control = el('select', { class: 'text', onchange: e => set(e.target.value) },
      ...spec.options.map(o => el('option', {
        value: o, selected: o === state.inputs[name] ? 'selected' : null,
      }, o)));
  } else if (spec.type === 'boolean') {
    control = el('select', { class: 'text', onchange: e => set(e.target.value) },
      ...['false', 'true'].map(o => el('option', {
        value: o, selected: o === String(state.inputs[name]) ? 'selected' : null,
      }, o)));
  } else {
    control = el('input', {
      class: 'text', type: spec.type === 'number' ? 'number' : 'text',
      value: state.inputs[name], placeholder: spec.default || '',
      onchange: e => set(e.target.value),
    });
  }
  // What is shown is what gets sent. A required choice has no default, so the
  // select displays its first option while the state still holds "" — the form
  // then submits an empty value for a field the user can see is filled in, and
  // the server rejects something nobody typed.
  if (control.value !== undefined && control.value !== '') set(control.value);
  return el('div', { class: 'row field' },
    el('label', {}, name, spec.required ? el('span', { class: 'fail' }, ' *') : null),
    control,
    spec.description ? el('span', { class: 'dim' }, spec.description) : null);
}

// ------------------------------------------------------------------ act --

async function act(url, label, body) {
  try {
    await api(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body || {}),
    });
    route();
  } catch (err) {
    if (err instanceof Unauthorized) return renderSignIn();
    app.prepend(banner(label + '失败：' + err.message));
  }
}

// ---------------------------------------------------------------- router --

async function route() {
  if (timer) { clearTimeout(timer); timer = null; }
  const hash = location.hash.split('?')[0] || '#/';
  try {
    if (!config.loaded) {
      config = { ...(await api('/api/config')), loaded: true };
    }
    let live;
    const run = hash.match(/^#\/runs\/(\d+)/);
    if (run) live = await renderRun(Number(run[1]));
    else if (hash === '#/deployments') live = await renderDeployments();
    else if (hash === '#/usage') live = await renderUsage();
    else if (hash === '#/runners') live = await renderRunners();
    else if (hash === '#/schedules') live = await renderSchedules();
    else if (hash === '#/dispatch') live = await renderDispatch();
    else live = await renderRuns();

    tick.textContent = '刷新于 ' + new Date().toLocaleTimeString();
    // Poll only while something is actually moving. A dashboard that hammers
    // its own control plane while nobody is looking is its own outage.
    const moving = Array.isArray(live)
      ? live.some(r => r.Status && r.Status !== 'done')
      : live && live.Status && live.Status !== 'done';
    if (moving) timer = setTimeout(route, 2000);
  } catch (err) {
    if (err instanceof Unauthorized) { renderSignIn(); return; }
    app.replaceChildren(banner('加载失败：' + err.message));
    timer = setTimeout(route, 5000);
  }
}

window.addEventListener('hashchange', route);
route();
