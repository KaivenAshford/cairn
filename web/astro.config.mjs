// @ts-check
import { defineConfig } from 'astro/config';

// 换成你自己的域名。canonical 依赖它（Base.astro）。
const site = 'https://example.com';

// 占位域名发上线的后果不是「链接不好看」：每一页的 canonical 都会指向别人的域名，
// 等于让搜索引擎把整站归给他们，而 /u/ 未列出页还会把自己的随机路径写进那条 canonical。
// 所以每次构建都喊一声——三处占位符（这里、deploy/Caddyfile、.env 的 CAIRN_URL）
// 最容易只改两处。改完域名这段自然就不再出现。
if (site.includes('example.com')) {
  console.warn('\n  ⚠ astro.config.mjs 里的 site 还是占位域名 example.com。');
  console.warn('    另外两处：deploy/Caddyfile、.env 的 CAIRN_URL。\n');
}

export default defineConfig({
  site,
  build: { format: 'directory' },
  markdown: {
    shikiConfig: { themes: { light: 'github-light', dark: 'github-dark' } },
  },
});
