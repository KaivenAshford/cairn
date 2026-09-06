// @ts-check
import { defineConfig } from 'astro/config';

// 换成你自己的域名。canonical 依赖它（Base.astro）。
// scripts/deploy 会替你换这一行——它是整条部署链上**唯一**还要做文本替换的地方，
// 所以下面那个 `const site = '…'` 的形状别改（脚本按行首那段 sed，改完还会读回来核对）。
const site = 'https://example.com';

// 占位域名发上线的后果不是「链接不好看」：每一页的 canonical 都会指向别人的域名，
// 等于让搜索引擎把整站归给他们，而 /u/ 未列出页还会把自己的随机路径写进那条 canonical。
// 所以每次构建都喊一声——三处域名最容易只改两处。
// 那三处是：这里、.env 的 CAIRN_DOMAIN（Caddy 签证书用）、.env 的 CAIRN_URL。
// deploy/Caddyfile 里**没有**域名，它读 {$CAIRN_DOMAIN}，就是为了少一处要 sed 的文本。
if (site.includes('example.com')) {
  console.warn('\n  ⚠ astro.config.mjs 里的 site 还是占位域名 example.com。');
  console.warn('    另外两处都在 .env：CAIRN_DOMAIN 和 CAIRN_URL。');
  console.warn('    三处一起换：scripts/deploy（第一次）或 scripts/deploy --update。\n');
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
