import type { APIRoute } from 'astro';
import { publicEntries, publicTagIndex, formatDate } from '../lib/entries';

/**
 * sitemap。同样手写，不装 @astrojs/sitemap。
 *
 * 这不是「少一个依赖」的洁癖：审计实测过 @astrojs/sitemap 的默认配置会把
 * /u/<32位随机>/ 一并列进去。sitemap 的用途正是**主动把 URL 递给搜索引擎**，
 * 而 unlisted 的全部防护就是这串路径猜不到——递出去就等于把秘密交给了爬虫，
 * 而且页面上的 noindex 也救不回来：URL 本身已经在别人的库里了。
 * 所以这个文件里只允许出现 publicEntries()，一个 loadEntries() 都不能有。
 *
 * lastmod 用日期而不是完整时间戳：frontmatter 里的 created / updated 本来就是纯日期，
 * 补一个 T00:00:00Z 是在声明一个并不存在的精度。W3C Datetime 允许只到天。
 */

// 与 feed.xml.ts 里那份相同，理由见那边的注释。
const xml = (s: string) =>
  s
    .replace(/[\x00-\x08\x0B\x0C\x0E-\x1F]/g, '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');

/**
 * /now 那一页渲染的就是 id 为 now 的条目（见 pages/now.astro），
 * 于是同一份内容有两个 URL：/now/ 和 /e/now/。
 *
 * sitemap 里只放 /now/，理由：/now/ 是这一页对外的名字——导航里写的是它，
 * 写入通道 `n -i now "…"` 反复覆盖的也是它；/e/now/ 只是「条目按 id 编址」这条
 * 通用规则的副产物，没人会去引用它。两个都递给搜索引擎的话，搜出来的很可能是
 * 那个没人认得的那一个。
 *
 * 遗留问题（不在这个文件里）：两页的 canonical 各指向自己（Base.astro 用的是
 * Astro.url.pathname），所以爬虫从别处爬到 /e/now/ 时仍会把它当独立一页。
 * 真要收口，得让 /e/now/ 的 canonical 指向 /now/ —— 那是 Base.astro 的事。
 * sitemap 只管「我主动推荐哪一个」，这里先把判断留下。
 */
const NOW_ID = 'now';

export const GET: APIRoute = async ({ site }) => {
  if (!site) {
    throw new Error('astro.config.mjs 里没有配 site：sitemap 的 <loc> 必须是绝对 URL。');
  }
  const abs = (path: string) => new URL(path, site).href;

  const entries = await publicEntries();
  const at = (e: (typeof entries)[number]) => e.data.updated ?? e.data.created;

  // 首页的 lastmod 就是最新一条的时间：首页的内容全部来自条目，没有条目变化就没有变化。
  // 不用构建时间，理由同 feed.xml.ts：那会让每次重新构建都产生 diff。
  const newest = entries.length ? new Date(Math.max(...entries.map((e) => at(e).getTime()))) : null;
  const now = entries.find((e) => e.id === NOW_ID);
  const tagGroups = await publicTagIndex();

  const urls: { loc: string; lastmod?: Date }[] = [
    { loc: abs('/'), lastmod: newest ?? undefined },
    // /now/ 这条路由一直在（没有 now 条目时它显示「还没写」），所以无条件列出；
    // lastmod 只有在真有那条条目时才知道。
    { loc: abs('/now/'), lastmod: now ? at(now) : undefined },
    ...entries
      .filter((e) => e.id !== NOW_ID)
      .map((e) => ({ loc: abs(`/e/${e.id}/`), lastmod: at(e) })),
    // 标签页同样是公开、可抓取、鼓励收录的。它的数据源 publicTagIndex() 只看
    // publicEntries()，所以不会把 unlisted 带进来——那条红线在 lib/entries.ts 上守着。
    // lastmod 取这个标签下最新那条：标签页的内容就是那批条目。
    ...tagGroups.map((g) => ({
      loc: abs(`/t/${encodeURIComponent(g.slug)}/`),
      lastmod: new Date(Math.max(...g.entries.map((e) => at(e).getTime()))),
    })),
  ];

  const body = `<?xml version="1.0" encoding="utf-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
${urls
  .map(
    (u) => `  <url>
    <loc>${xml(u.loc)}</loc>${u.lastmod ? `\n    <lastmod>${formatDate(u.lastmod)}</lastmod>` : ''}
  </url>`,
  )
  .join('\n')}
</urlset>
`;

  return new Response(body, {
    headers: { 'Content-Type': 'application/xml; charset=utf-8' },
  });
};
