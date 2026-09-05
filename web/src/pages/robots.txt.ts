import type { APIRoute } from 'astro';

/**
 * robots.txt。
 *
 * **故意不写 `Disallow: /u/`。** 这条反直觉，所以必须记下来，不然下一个人会「顺手补上」：
 *
 * robots.txt 管的是**抓取**，不是**收录**。禁止抓取 /u/ 的后果是爬虫根本取不到那一页，
 * 于是也看不到页面上的 `noindex`——而搜索引擎对「不许抓、但从别处发现了链接」的 URL
 * 的处理恰恰是仅凭 URL 收录（无标题无摘要，但路径本身就在搜索结果里）。
 * 而 /u/ 的那串随机路径正是它的全部秘密。链接会从聊天软件的预览、referrer 里泄出来，
 * 这不是假想。
 *
 * 换句话说：**noindex 要生效，前提是允许抓取。** 现有的两层防护——页面上的
 * `<meta name="robots" content="noindex, nofollow">` 与 Caddy 给 /u/* 加的
 * `X-Robots-Tag`——都要求爬虫先取到那个响应。所以这里必须放行。
 *
 * 还有一层：robots.txt 是公开可读的。往里写 `Disallow: /u/` 等于向所有人宣布
 * 「这个站有一批不想被人看见的页面，都在 /u/ 下面」——把注意力精确地指过去。
 */
export const GET: APIRoute = ({ site }) => {
  if (!site) {
    throw new Error('astro.config.mjs 里没有配 site：robots.txt 的 Sitemap 行必须是绝对 URL。');
  }

  const body = `User-agent: *
Allow: /
Sitemap: ${new URL('/sitemap.xml', site).href}
`;

  return new Response(body, { headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
};
