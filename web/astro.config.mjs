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
    // 用 high-contrast 那一对，不是普通的 github-light / github-dark。
    // 理由是 github-dark 的注释色 #6A737D 压在它自带的 #24292e 底上只有 3.05:1，
    // 达不到正文的 AA（4.5:1）。而且这修不掉：shiki 把颜色写成行内 style，
    // 代码块的底色也是主题自带的（行内 background-color 盖住了卡片的淡色底），
    // 前景背景两头都不归 CSS 管，只能换主题。
    // 换过之后实测最低的一档：亮色 5.04:1、暗色 11.12:1（都是注释）。
    // 代价是配色更艳、暗色底更黑（#24292e → #0a0c10）——这是有意付的。
    shikiConfig: {
      themes: { light: 'github-light-high-contrast', dark: 'github-dark-high-contrast' },
    },
  },
});
