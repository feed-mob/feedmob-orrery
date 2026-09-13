// Pure helpers, in their own module so they can be tested without a browser.
// The crash that made the log panel render nothing on a large job got through
// because none of this was covered; these are the parts worth pinning down.
'use strict';

// toneOf follows the verdict, not the status: a run still going is neutral, and
// "cancelled" is deliberately not red — we stopped looking, the code is not
// necessarily bad.
export function toneOf(status, result) {
  if (status === 'pending') return 'idle';
  if (status !== 'done') return 'warn';
  switch (result) {
    case 'success': return 'ok';
    case 'failure': return 'fail';
    case 'cancelled': case 'skipped': return 'idle';
    default: return 'warn';
  }
}

export function verdict(status, result) {
  if (status === 'pending') return '等待并发组';
  if (status !== 'done') return status;
  return result || 'done';
}

export function duration(a, b) {
  if (!a || !b) return '';
  const d = new Date(b) - new Date(a);
  if (!isFinite(d) || d < 0) return '';
  if (d < 1000) return d + 'ms';
  if (d < 60000) return (d / 1000).toFixed(1) + 's';
  const m = Math.floor(d / 60000);
  return m + 'm' + Math.round((d % 60000) / 1000) + 's';
}

export function ago(iso, now = Date.now()) {
  if (!iso) return '';
  const d = (now - new Date(iso)) / 1000;
  if (!isFinite(d)) return '';
  if (d < 60) return Math.max(0, Math.round(d)) + ' 秒前';
  if (d < 3600) return Math.round(d / 60) + ' 分钟前';
  if (d < 86400) return Math.round(d / 3600) + ' 小时前';
  return Math.round(d / 86400) + ' 天前';
}

export function shortRef(ref) {
  return (ref || '').replace(/^refs\/heads\//, '').replace(/^refs\/tags\//, '');
}

// classify says how one log line should read. Workflow commands are kept rather
// than stripped: `::error::` is how a step says what went wrong.
export function classify(rest) {
  if (/^::(error|warning)::/.test(rest)) return 'err';
  if (/^::debug::/.test(rest)) return 'dim';
  if (/^::orrery::/.test(rest)) return 'engine';
  if (/^::(group|endgroup)::/.test(rest)) return 'grp';
  return null;
}

// splitStamp separates the RFC3339 timestamp the log endpoint prefixes from the
// line itself. A line with no stamp is returned whole rather than mangled.
export function splitStamp(line) {
  const m = line.match(/^(\d{4}-\d{2}-\d{2}T\S+Z)\s([\s\S]*)$/);
  return m ? { stamp: m[1], rest: m[2] } : { stamp: '', rest: line };
}

// groupLines folds `::group::` … `::endgroup::` the way GitHub does, so a log
// with twenty setup groups opens as twenty headings instead of a wall.
//
// Unbalanced markers are the norm in real logs — a step that fails inside a
// group never prints its endgroup — so an unclosed group simply runs to the end
// rather than swallowing everything after it into nothing.
export function groupLines(lines) {
  const out = [];
  let open = null;
  for (const line of lines) {
    const { stamp, rest } = splitStamp(line);
    const start = rest.match(/^::group::(.*)$/);
    if (start) {
      open = { kind: 'group', title: start[1].trim(), stamp, lines: [] };
      out.push(open);
      continue;
    }
    if (/^::endgroup::/.test(rest)) {
      open = null;
      continue;
    }
    if (open) open.lines.push({ stamp, rest });
    else out.push({ kind: 'line', stamp, rest });
  }
  return out;
}

// matches counts how many lines contain the needle, case-insensitively. Used by
// the in-log find box to say "3 处" before highlighting them.
export function countMatches(lines, needle) {
  if (!needle) return 0;
  const n = needle.toLowerCase();
  let count = 0;
  for (const l of lines) if (l.toLowerCase().includes(n)) count++;
  return count;
}

// machineTime renders a duration for the usage table. Milliseconds are what the
// database has; a unit someone can compare is what the page needs, and that
// changes with the magnitude — "0.0 分" for a job that took 12ms is the same
// failure as reporting nothing.
export function machineTime(ms) {
  if (!ms) return '0';
  if (ms < 1000) return ms + ' 毫秒';
  if (ms < 60000) return (ms / 1000).toFixed(1) + ' 秒';
  const m = ms / 60000;
  return (m < 10 ? m.toFixed(1) : String(Math.round(m))) + ' 分';
}

// share is a workflow's slice of the total, for the bar next to each row. A
// table of numbers makes you do the comparison; a bar has already done it.
export function share(seconds, total) {
  if (!total) return 0;
  return Math.max(0, Math.min(100, (seconds / total) * 100));
}
