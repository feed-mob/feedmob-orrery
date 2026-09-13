// node --test internal/server/web/
import test from 'node:test';
import assert from 'node:assert/strict';
import {
  toneOf, verdict, duration, ago, shortRef, classify, splitStamp, groupLines, countMatches,
  machineTime, share,
} from './format.js';

test('颜色跟的是结论，不是状态', () => {
  assert.equal(toneOf('running', ''), 'warn');
  assert.equal(toneOf('pending', ''), 'idle');
  assert.equal(toneOf('done', 'success'), 'ok');
  assert.equal(toneOf('done', 'failure'), 'fail');
  // cancelled 有意不标红：我们是不看了，代码不一定有问题
  assert.equal(toneOf('done', 'cancelled'), 'idle');
  assert.equal(toneOf('done', 'skipped'), 'idle');
  // 落定却没有结论是异常，该显眼
  assert.equal(toneOf('done', ''), 'warn');
});

test('verdict 把等并发组说清楚', () => {
  assert.equal(verdict('pending', ''), '等待并发组');
  assert.equal(verdict('running', ''), 'running');
  assert.equal(verdict('done', 'failure'), 'failure');
});

test('duration 缺任一端就不编造', () => {
  assert.equal(duration(null, '2026-09-13T12:00:00Z'), '');
  assert.equal(duration('2026-09-13T12:00:00Z', null), '');
  assert.equal(duration('2026-09-13T12:00:00Z', '2026-09-13T12:00:00.813Z'), '813ms');
  assert.equal(duration('2026-09-13T12:00:00Z', '2026-09-13T12:00:07Z'), '7.0s');
  assert.equal(duration('2026-09-13T12:00:00Z', '2026-09-13T12:01:30Z'), '1m30s');
  // 结束早于开始是坏数据，显示空白好过显示负数
  assert.equal(duration('2026-09-13T12:00:10Z', '2026-09-13T12:00:00Z'), '');
});

test('ago', () => {
  const now = Date.parse('2026-09-13T12:00:00Z');
  assert.equal(ago('2026-09-13T11:59:30Z', now), '30 秒前');
  assert.equal(ago('2026-09-13T11:30:00Z', now), '30 分钟前');
  assert.equal(ago('2026-09-13T06:00:00Z', now), '6 小时前');
  assert.equal(ago('2026-09-10T12:00:00Z', now), '3 天前');
  assert.equal(ago('', now), '');
  // 时钟偏差会让"未来"出现，不该显示成负数
  assert.equal(ago('2026-09-13T12:00:05Z', now), '0 秒前');
});

test('shortRef', () => {
  assert.equal(shortRef('refs/heads/main'), 'main');
  assert.equal(shortRef('refs/tags/v1.2.3'), 'v1.2.3');
  assert.equal(shortRef('refs/heads/release/2026-09'), 'release/2026-09');
  assert.equal(shortRef(''), '');
  assert.equal(shortRef(undefined), '');
});

test('classify 保留 workflow command 标记', () => {
  assert.equal(classify('::error::boom'), 'err');
  assert.equal(classify('::warning::hmm'), 'err');
  assert.equal(classify('::debug::noisy'), 'dim');
  assert.equal(classify('::orrery:: stop requested'), 'engine');
  assert.equal(classify('::group::Run x'), 'grp');
  assert.equal(classify('ordinary output'), null);
});

test('splitStamp 不把没有时间戳的行弄坏', () => {
  assert.deepEqual(splitStamp('2026-09-13T12:00:00Z hello'),
    { stamp: '2026-09-13T12:00:00Z', rest: 'hello' });
  assert.deepEqual(splitStamp('no stamp here'), { stamp: '', rest: 'no stamp here' });
  // 正文里带空格和冒号都不该影响切分
  assert.deepEqual(splitStamp('2026-09-13T12:00:00.5Z a: b  c'),
    { stamp: '2026-09-13T12:00:00.5Z', rest: 'a: b  c' });
});

test('groupLines 折叠 ::group::', () => {
  const out = groupLines([
    '2026-09-13T12:00:00Z ::group::Starting job container',
    '2026-09-13T12:00:00Z image: ubuntu',
    '2026-09-13T12:00:01Z ::endgroup::',
    '2026-09-13T12:00:02Z plain line',
  ]);
  assert.equal(out.length, 2);
  assert.equal(out[0].kind, 'group');
  assert.equal(out[0].title, 'Starting job container');
  assert.equal(out[0].lines.length, 1);
  assert.equal(out[1].kind, 'line');
  assert.equal(out[1].rest, 'plain line');
});

test('没有 endgroup 的组一直延伸到末尾，而不是吞掉后面的内容', () => {
  // 在组里失败的步骤根本不会打出 endgroup，这是日志里的常态
  const out = groupLines([
    '2026-09-13T12:00:00Z ::group::Run failing thing',
    '2026-09-13T12:00:00Z boom',
    '2026-09-13T12:00:01Z ::error::exit 1',
  ]);
  assert.equal(out.length, 1);
  assert.equal(out[0].kind, 'group');
  assert.equal(out[0].lines.length, 2);
  assert.equal(out[0].lines[1].rest, '::error::exit 1');
});

test('孤立的 endgroup 不会把后面的行吞掉', () => {
  const out = groupLines([
    '2026-09-13T12:00:00Z ::endgroup::',
    '2026-09-13T12:00:01Z still here',
  ]);
  assert.equal(out.length, 1);
  assert.equal(out[0].rest, 'still here');
});

test('countMatches 大小写不敏感', () => {
  const lines = ['Error: nope', 'fine', 'another ERROR'];
  assert.equal(countMatches(lines, 'error'), 2);
  assert.equal(countMatches(lines, ''), 0);
  assert.equal(countMatches(lines, 'missing'), 0);
});

test('十万行也要在合理时间内折叠完', () => {
  const lines = [];
  for (let i = 0; i < 100000; i++) lines.push(`2026-09-13T12:00:00Z line ${i}`);
  const t0 = Date.now();
  const out = groupLines(lines);
  assert.equal(out.length, 100000);
  assert.ok(Date.now() - t0 < 3000, `groupLines 花了 ${Date.now() - t0}ms`);
});

test('machineTime 的单位跟着量级走', () => {
  assert.equal(machineTime(0), '0');
  // 毫秒级的 job 不该被舍成 0——"0 分"会让人不再相信这一页
  assert.equal(machineTime(12), '12 毫秒');
  assert.equal(machineTime(1500), '1.5 秒');
  assert.equal(machineTime(90000), '1.5 分');
  assert.equal(machineTime(3600000), '60 分');
  // 10 分钟以上不再给小数——精度在这里不帮人做决定
  assert.equal(machineTime(630000), '11 分');
});

test('share 不会越界', () => {
  assert.equal(share(50, 100), 50);
  assert.equal(share(0, 0), 0);
  assert.equal(share(200, 100), 100);
  assert.equal(share(-5, 100), 0);
});
