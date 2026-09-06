import raw from '../data/ai.json';

/**
 * /ai 那一页的取数层。
 *
 * 这里做的全部事情是「把每天的计数加起来，再算成比率」。**比率只在这里算，不进 JSON**：
 * 存下来的比率会在下一次采集之后变成一个和分子分母对不上的数，而且没人会发现——
 * 分子分母都在，随时能重算的东西不该被固化。见 pipeline/ai/stat.go 的同一条理由。
 *
 * JSON 里没有任何原文（见 pipeline/ai/gate.go 的出口闸门），所以这一层拿到的全是整数，
 * 不存在「渲染时不小心把某段路径打到页面上」的可能。
 */

// —— JSON 的形状。和 pipeline/ai/stat.go 的结构体一一对应 ——
// 手写而不是从 JSON 推断：推断出来的类型会跟着数据变，
// 采集器少写一个字段时页面会静默少一块，而不是构建失败。
interface Day {
  seen: number;
  prompts: number;
  promptChars: number[];
  turns: number;
  toolCalls: number;
  toolErrors: number;
  tools: Record<string, number>;
  bashCmds: number;
  bashPiped: number;
  bashLen: number[];
  leverage: number[];
  leverageCalls: number[];
  hours: number[];
  tokens: { in: number; cacheRead: number; cacheCreate: number; out: number; thinking: number };
  projects: { count: number; topPermille: number };
}
interface Store {
  schema: number;
  updated: string;
  tzOffsetHours: number;
  days: Record<string, Day>;
}

const store = raw as Store;

const dates = Object.keys(store.days).sort();
const days = dates.map((d) => store.days[d]!);

const sum = (xs: number[]) => xs.reduce((a, b) => a + b, 0);
const total = (pick: (d: Day) => number) => sum(days.map(pick));
/** 把每天的分桶数组按位相加。桶的边界在采集器里写死，所以历史快照能直接叠。 */
const totalArray = (pick: (d: Day) => number[], n: number) =>
  Array.from({ length: n }, (_, i) => sum(days.map((d) => pick(d)[i] ?? 0)));

// —— 分桶的标签。顺序、含义都必须和 pipeline/ai/stat.go 里的 bounds 一致。
// 不一致不会有任何报错，只会让页面上的每个数字都错位一格——所以两处都写了对方的位置。
export const LEVERAGE_LABELS = ['一次没动', '1–2 次', '3–5 次', '6–10 次', '11–30 次', '31 次以上'];
export const BASH_LEN_LABELS = ['80 字符以内', '80–199', '200–499', '500–1499', '1500 以上'];
export const PROMPT_CHAR_LABELS = ['10 字以内', '10–29', '30–79', '80–199', '200 以上'];

const toolTotals: Record<string, number> = {};
for (const d of days) {
  for (const [k, v] of Object.entries(d.tools)) toolTotals[k] = (toolTotals[k] ?? 0) + v;
}

const tokensIn = {
  fresh: total((d) => d.tokens.in),
  cacheRead: total((d) => d.tokens.cacheRead),
  cacheCreate: total((d) => d.tokens.cacheCreate),
};

const leverage = totalArray((d) => d.leverage, LEVERAGE_LABELS.length);
const leverageCalls = totalArray((d) => d.leverageCalls, LEVERAGE_LABELS.length);
const hours = totalArray((d) => d.hours, 24);

export const ai = {
  updated: store.updated,
  tzOffsetHours: store.tzOffsetHours,
  dates,
  /** 有记录的天数。不是「首末相差多少天」——中间空掉的日子不该算进分母。 */
  dayCount: dates.length,
  first: dates[0] ?? '',
  last: dates[dates.length - 1] ?? '',

  prompts: total((d) => d.prompts),
  turns: total((d) => d.turns),
  toolCalls: total((d) => d.toolCalls),
  toolErrors: total((d) => d.toolErrors),
  bashCmds: total((d) => d.bashCmds),
  bashPiped: total((d) => d.bashPiped),

  promptChars: totalArray((d) => d.promptChars, PROMPT_CHAR_LABELS.length),
  bashLen: totalArray((d) => d.bashLen, BASH_LEN_LABELS.length),
  leverage,
  leverageCalls,
  hours,

  tools: Object.entries(toolTotals).sort((a, b) => b[1] - a[1]),

  tokensIn,
  tokensInAll: tokensIn.fresh + tokensIn.cacheRead + tokensIn.cacheCreate,
  tokensOut: total((d) => d.tokens.out),
  thinking: total((d) => d.tokens.thinking),

  /** 每天一条，给日柱状图用。concentrated 是「当天八成以上的动作落在同一个项目上」。 */
  daily: dates.map((date, i) => {
    const d = days[i]!;
    return {
      date,
      calls: d.toolCalls,
      projects: d.projects.count,
      topPermille: d.projects.topPermille,
      concentrated: d.projects.topPermille >= 800,
    };
  }),
};

export const empty = ai.dayCount === 0;

// —— 格式化 ——

/** 千位分隔。数字是这一页的主角，读不出量级的数字等于没写。 */
export function n(v: number): string {
  return v.toLocaleString('en-US');
}

/** 中文量级：一千万以上说「亿」，一万以上说「万」。正文里读得出来才叫结论。 */
export function cn(v: number): string {
  if (v >= 1e8) return `${(v / 1e8).toFixed(1)} 亿`;
  if (v >= 1e4) return `${Math.round(v / 1e4)} 万`;
  return n(v);
}

/**
 * 百分比。分母为 0 时返回 0，不返回 NaN——一份还没采集过的空快照不该让整页变成 NaN%。
 * digits 默认 0：一页上十几个百分数，小数点只会让人去比对根本不重要的一位。
 */
export function pct(part: number, whole: number, digits = 0): string {
  if (whole === 0) return '0%';
  return `${((part / whole) * 100).toFixed(digits)}%`;
}

/** 归一化成 0–1，给 CSS 的 --v 用。max 为 0 时全给 0，避免除零得到 Infinity 把柱子撑爆。 */
export function share(v: number, max: number): number {
  return max === 0 ? 0 : v / max;
}
