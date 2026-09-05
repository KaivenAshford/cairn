import type { APIRoute } from 'astro';
import { publicEntries, displayTitle, excerpt } from '../lib/entries';

/**
 * 订阅源。手写 Atom，不装 @astrojs/rss。
 *
 * **为什么手写**：这个仓库的依赖清单是它的一部分，而 feed 只是一段字符串拼接；
 * 更要紧的是插件的默认行为默认「站上有什么就发什么」。审计实测过 @astrojs/sitemap
 * 的默认配置会把 /u/<32位随机>/ 列进产物——unlisted 的全部防护就是猜不到，
 * 把它交给一个我没读过默认值的第三方，等于把秘密的分发权外包出去。
 * 所以出口类产物（feed / sitemap / robots）一律手写，数据源一律 publicEntries()。
 *
 * **为什么是 Atom 而不是 RSS 2.0**，三条，按重要性：
 * 1. 时间格式。Atom 用 RFC 3339，正好是 `Date.toISOString()` 的输出，一个字符不用转；
 *    RSS 2.0 要 RFC 822（`Sat, 04 Sep 2026 00:00:00 GMT`），得手写英文星期与月份表——
 *    在一个中文站里放一张英文月份表，是迟早要被顺手改坏的东西，而它错了不会有人发现：
 *    阅读器只是默默把条目排错序。
 * 2. 转义语义。Atom 的 `<summary type="text">` 明确声明「这段是纯文本」，阅读器不会
 *    把里面的 `<` 当标签；RSS 2.0 的 `<description>` 是纯文本还是 HTML 只是惯例，
 *    没有字段能声明——同一段字在两个阅读器里可能一个显示 `&lt;p&gt;`、一个渲染成段落。
 * 3. Atom 有独立于链接的 `<id>`。以后条目改了 URL（比如按年份归档），
 *    阅读器仍认得这是同一条，不会把整个订阅源当新内容重推一遍。
 *
 * **为什么发摘要而不发全文**：站上的条目大多两三行，全文和摘要差不了多少；而发全文要把
 * 渲染后的整段 HTML 塞进 XML，转义漏一个字符就是整份订阅源解析失败——阅读器的表现是
 * 「这个源坏了」，不是「这一条不对」。取舍的代价是想读全文的人得点一次链接。
 * 等哪天真有几千字的长文，再单独给 post 加 `<content>`，而不是现在为它冒整份的险。
 */

// XML 只有五个预定义实体，实际要转的是 & < > 和属性里的 "。
// 顺序要紧：& 必须第一个换，否则会把刚生成的 &lt; 二次转义成 &amp;lt;。
// 再剥掉 XML 1.0 根本不允许出现的 C0 控制字符：它们的后果不是「显示成乱码」，
// 是整份文档解析失败。正文是手写 / 粘贴来的 markdown，混进一个 \x00 不稀奇。
// （sitemap.xml.ts 里有一份同样的实现。两边都是页面路由，为一个五行函数让一个页面
//  去 import 另一个页面，换来的耦合比省下的五行贵。）
const xml = (s: string) =>
  s
    .replace(/[\x00-\x08\x0B\x0C\x0E-\x1F]/g, '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');

export const GET: APIRoute = async ({ site }) => {
  // site 没配就不发。Atom 的 <id> 与 <link> 必须是绝对 URL，拼不出来时唯一的退路是
  // 发一份相对路径的订阅源——那等于把每个订阅者的阅读器指向他自己那台机器。
  if (!site) {
    throw new Error('astro.config.mjs 里没有配 site：feed 的 <id> 与 <link> 必须是绝对 URL。');
  }
  const abs = (path: string) => new URL(path, site).href;

  // 数据源只能是 publicEntries()。loadEntries() 里混着 unlisted，
  // 把「猜不到的 URL」群发给所有订阅者，等于这一层防护从来没存在过。
  const entries = await publicEntries();

  // 不设条目数上限。设了的话，「feed 条目数 == public 条目数」这条断言就得写成
  // 「等于 min(n, 上限)」，于是「漏掉了谁」这件事再也断言不出来了。
  // 真到几百条再加，那时也该顺手加分页（rel="next"）。

  const at = (e: (typeof entries)[number]) => e.data.updated ?? e.data.created;

  // 站点级 <updated> 取最新一条的时间，不取构建时间：取构建时间的话，一次什么都没改的
  // 重新构建也会让整份 feed 出现 diff——发布时就看不出「这次到底发了什么」，
  // 阅读器那边也白轮询一轮。空站没有「最后更新」，退回 epoch，理由同上：不引入构建时间。
  const updated = entries.length
    ? new Date(Math.max(...entries.map((e) => at(e).getTime())))
    : new Date(0);

  const body = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title type="text">cairn · 起居注</title>
  <subtitle type="text">一个人的可浏览版本。</subtitle>
  <id>${xml(abs('/'))}</id>
  <link rel="alternate" type="text/html" href="${xml(abs('/'))}"/>
  <link rel="self" type="application/atom+xml" href="${xml(abs('/feed.xml'))}"/>
  <updated>${updated.toISOString()}</updated>
  <author><name>cairn</name></author>
${entries
  .map(
    (e) => `  <entry>
    <title type="text">${xml(displayTitle(e))}</title>
    <id>${xml(abs(`/e/${e.id}/`))}</id>
    <link rel="alternate" type="text/html" href="${xml(abs(`/e/${e.id}/`))}"/>
    <published>${e.data.created.toISOString()}</published>
    <updated>${at(e).toISOString()}</updated>
${[e.data.type, ...e.data.tags].map((t) => `    <category term="${xml(t)}"/>`).join('\n')}
    <summary type="text">${xml(excerpt(e, 200))}</summary>
  </entry>`,
  )
  .join('\n')}
</feed>
`;

  return new Response(body, {
    headers: { 'Content-Type': 'application/atom+xml; charset=utf-8' },
  });
};
