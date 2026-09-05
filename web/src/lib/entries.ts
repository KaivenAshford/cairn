import { getCollection, type CollectionEntry } from 'astro:content';

export type Entry = CollectionEntry<'entries'>;

/**
 * unlisted 的文件名就是 URL，它的全部防护就是猜不到。见 ARCHITECTURE.md 第 2 节。
 *
 * 判的是「id 里**存在**一段猜不到的路径段」，而不是「整个 id 是随机串」：
 * 按目录归档时 glob loader 给出的 id 形如 `2026/<32位随机>`，URL 同样猜不到，
 * 却会被整串锚定的正则一律拒掉——报错还会让人去做他其实已经做过的事。
 */
const RANDOM_SEGMENT = /^[0-9a-f]{32,}$/;
const isUnguessable = (id: string) => id.split('/').some((seg) => RANDOM_SEGMENT.test(seg));

/**
 * 加载内容目录里的全部条目。
 *
 * 内容目录是一个单独的 private 仓库（见 ARCHITECTURE.md 第 2 节），四层可见度的条目
 * 混在一起，由 frontmatter 决定谁进静态产物。所以这里读全部，再由下面的
 * publicEntries / unlistedEntries 显式挑出该进产物的那些。
 *
 * 「哪些内容会被构建出去」因此是一条**软**的防线（字段过滤），不再是硬的（目录分离）。
 * 这个取舍是有意的，因为前提变了：代码仓里没有内容，「私密条目被提交进公开仓库」
 * 那条不可逆的路径已经不存在；过滤出错的后果是私密内容进了 dist，重新构建就能修。
 * 代价是这条软防线必须有测试钉住——见 scripts/test-visibility。
 */
export async function loadEntries(): Promise<Entry[]> {
  const all = await getCollection('entries');

  // unlisted 用了猜得到的文件名，等于没有防护。四层可见度里只有这一层的强度取决于
  // 文件名，所以它必须大声失败：服务端写入通道会强制生成随机名，但手写的、改过名的、
  // 从别处同步来的 markdown 绕不过构建这一关。
  const guessable = all.filter((e) => e.data.visibility === 'unlisted' && !isUnguessable(e.id));
  if (guessable.length > 0) {
    const list = guessable.map((e) => `  - ${e.id}`).join('\n');
    throw new Error(
      `以下 unlisted 条目的文件名不是长随机串：\n${list}\n\n` +
        `unlisted 不需要登录，它的全部防护就是「猜不到」——文件名即 URL。\n` +
        `用 \`openssl rand -hex 16\` 重命名（随机串可以是文件名，也可以是它的某一级目录名），\n` +
        `或者改用 visibility: private。`,
    );
  }

  // 标签会变成路由参数 `/t/<slug>/`，而路由参数在静态构建里就是 dist 下的目录名。
  // 所以一个含 `/` 的标签是一条相对路径，一个 `..` 直接就往 dist 外面写——
  // `/t/../index.html` 落在 dist 根上，正好盖掉首页。
  // 「标签是我自己写的，不会有恶意」不构成理由：从手机上发一条
  // `n -g "读书 笔记/2026"` 是手滑，不是攻击，而后果一样。
  //
  // 和上面那条 unlisted 文件名守卫一样**大声失败**，不静默规范化：
  // 悄悄把 `a/b` 改写成 `a-b` 的话，两个本来不同的标签会合并成一页，而没人会发现。
  // 只查会真正变成 URL 的那两层（public 进 /t/ 与条目页，unlisted 的条目页也印标签）；
  // circle / private 的标签不进任何产物，不该让公开站构建不过。
  const badTags: string[] = [];
  const collided: string[] = [];
  const seenSlug = new Map<string, string>(); // slug → 第一个用它的原文标签
  for (const e of all) {
    if (e.data.visibility !== 'public' && e.data.visibility !== 'unlisted') continue;
    for (const tag of e.data.tags) {
      const slug = tagSlug(tag);
      if (slug === '') {
        badTags.push(`  - ${e.id}：${JSON.stringify(tag)}`);
        continue;
      }
      // 折叠不安全字符之后，两个不同的标签可能落到同一个 slug 上（`C#` 和 `C++` 都是 `c`）。
      // 那会让两个标签共用一页、其中一个的名字凭空消失。宁可构建失败，让人改标签。
      const prev = seenSlug.get(slug);
      if (prev !== undefined && prev !== tag) {
        collided.push(`  - ${JSON.stringify(prev)} 与 ${JSON.stringify(tag)} 都会变成 /t/${slug}/`);
      } else {
        seenSlug.set(slug, tag);
      }
    }
  }
  if (collided.length > 0) {
    throw new Error(
      `以下标签会挤进同一个页面：\n${[...new Set(collided)].join('\n')}\n\n` +
        `标签里除字母、数字、中文以外的字符都会被折成 \`-\`，于是不同的标签可能撞成同一个 URL。\n` +
        `改掉其中一个（比如 \`C#\` → \`csharp\`）。`,
    );
  }
  if (badTags.length > 0) {
    throw new Error(
      `以下标签不能当作 URL 里的一段：\n${badTags.join('\n')}\n\n` +
        `标签会变成 /t/<标签>/ 这个路由的参数，也就是 dist 下的目录名。\n` +
        `含 \`/\`、为空、或者是 \`.\` / \`..\` 的标签会让产物落在意料之外的地方。\n` +
        `把标签改成一个词（中文原样即可，空格会折成 -）。`,
    );
  }

  return all.sort((a, b) => b.data.created.getTime() - a.data.created.getTime());
}

/** 进静态产物的两类。circle / private 在这里被挡住，不会出现在 dist 的任何文件里。 */
export async function publicEntries(): Promise<Entry[]> {
  return (await loadEntries()).filter((e) => e.data.visibility === 'public');
}

export async function unlistedEntries(): Promise<Entry[]> {
  return (await loadEntries()).filter((e) => e.data.visibility === 'unlisted');
}

/* ── 标签 ─────────────────────────────────────────────────────────────────── */

/**
 * 标签 → URL 里的那一段（未编码）。
 *
 * 只做两件事：把首尾空白去掉、中间的空白折成 `-`，再把 ASCII 大写降成小写。
 *
 * **故意不做音译、不剥非 ASCII。** 剥掉的话，一个纯中文标签会变成空串，
 * 于是「起居注」「排版」「编译器」全挤进同一页——而这个站的标签基本都是中文。
 * 中文原样留在 slug 里，靠 URL 的百分号编码上路（见 tagHref）。
 *
 * 大写降小写是为了让 `Go` 和 `go` 落在同一页：标签是手打的，
 * 同一个词因为大小写分成两页，等于标签没起作用。
 * 代价是显示名要挑一个（见 publicTagIndex：取最先出现的那种写法）。
 */
export function tagSlug(tag: string): string {
  return tag
    .trim()
    .toLowerCase()
    .replace(/[^\p{L}\p{N}]+/gu, '-')
    .replace(/^-+|-+$/g, '');
}

/**
 * 标签页的链接。**拼 URL 只走这一个口子。**
 *
 * 上面那条「只保留字母数字和中文」的规则是这里能成立的前提：
 * Astro 写盘时会对路由参数做百分号编码，而**编码后的字符串就是盘上的目录名**——
 * 标签 `C#` 会落成字面的 `c%23` 四个字符的目录，浏览器请求 /t/c%23/ 时
 * 服务器把它解回 `/t/c#/`，于是 404。中文之所以没事，是因为 Astro 不编码非 ASCII，
 * 盘上就是原文，编码后的 href 请求过来正好解回它。
 */
export function tagHref(tag: string): string {
  return `/t/${encodeURIComponent(tagSlug(tag))}/`;
}

export interface TagGroup {
  /** URL 里的那一段，未编码。也是 dist 下的目录名。 */
  slug: string;
  /** 显示用的原文。 */
  name: string;
  /** 这个标签下的条目，沿用时间流的倒序。 */
  entries: Entry[];
}

/**
 * 标签索引。**数据源只能是 publicEntries()。**
 *
 * 这不是风格问题：unlisted 条目的标签一旦聚进来，就会生成一个「这个标签下有 1 条」的
 * 页面，上面印着那条 unlisted 的标题和 URL——而它的全部防护就是猜不到，
 * 标签页却是猜得到的（`/t/读书/`）。一个公开页面把随机路径念出来，这层防护就没了。
 * 见 ARCHITECTURE.md 第 2 节，以及 scripts/test-visibility 里对应的断言。
 */
export async function publicTagIndex(): Promise<TagGroup[]> {
  const groups = new Map<string, TagGroup>();
  // publicEntries() 已经按 created 倒序，插入顺序即展示顺序，不用再排一次。
  for (const e of await publicEntries()) {
    for (const tag of e.data.tags) {
      const slug = tagSlug(tag);
      let g = groups.get(slug);
      if (!g) {
        // 显示名取最先出现的那种写法：同一个 slug 下可能有 `Go` 和 `go`，
        // 总得挑一个，挑「最新那条条目怎么写的」比挑字典序更贴近当下的写法。
        g = { slug, name: tag.trim(), entries: [] };
        groups.set(slug, g);
      }
      // 同一条条目把同一个标签写了两遍（`[Go, go]`）时只算一次，
      // 否则它会在自己的标签页上出现两次。
      if (!g.entries.includes(e)) g.entries.push(e);
    }
  }
  // 条数多的在前，同条数按 slug 定序——顺序必须是确定的，否则每次构建的产物都不一样。
  return [...groups.values()].sort(
    (a, b) => b.entries.length - a.entries.length || a.slug.localeCompare(b.slug),
  );
}

/** 去掉一行 markdown 的标记，只留文字。分隔线这类纯标记行会变成空串。 */
function stripMarkdown(line: string): string {
  const t = line.trim();
  if (/^([-*_])\1{2,}$/.test(t)) return ''; // --- *** ___ 是分隔线，不是内容
  // 表格行整行丢掉。它的内容离开了行列关系就是一串没有主谓的词，
  // 出现在卡片摘要或 meta description 里只会是「| 页面 | 对每个访客一样吗 |」。
  if (t.startsWith('|')) return '';
  return t
    .replace(/^(?:[#>]+|[-*+]|\d+[.)])\s+/, '') // 行首的标题 / 引用 / 列表标记
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, '$1') // 链接与图片：留下文字
    // 反引号里的是代码，不是标记：`CAIRN_CONTENT_DIR` 的下划线不是斜体。
    // 光调换顺序不够——先剥掉反引号的话，里面的内容照样裸露给下面的强调规则，
    // 于是标识符在首页标题和 meta description 里变成 CAIRNCONTENTDIR。必须先切开。
    .split(/(`[^`]+`)/g)
    .map((seg, i) =>
      i % 2
        ? seg.slice(1, -1) // 代码段：只去反引号，内容原样
        : seg
            .replace(/(\*\*|__)(.*?)\1/g, '$2') // 段外：粗体
            .replace(/(\*|_)(.*?)\1/g, '$2'), // 段外：斜体
    )
    .join('')
    .trim();
}

/** log / link 这类碎片可以没标题，用正文首行兜底。 */
export function displayTitle(entry: Entry): string {
  if (entry.data.title) return entry.data.title;
  // 找第一行**剥完还剩字**的，而不是第一行原文非空的。
  // 代码围栏、分隔线、表格行剥完都是空串——只看原文的话，正文以表格开头的条目
  // 标题会退回「（无题）」，而同一页的 meta description 里明明有下一段正文。
  let stripped = '';
  for (const l of (entry.body ?? '').split('\n')) {
    const t = l.trim();
    if (!t || t.startsWith('```')) continue;
    const s = stripMarkdown(l);
    if (s) {
      stripped = s;
      break;
    }
  }
  return stripped.length > 60 ? stripped.slice(0, 60) + '…' : stripped || '（无题）';
}

/**
 * 摘要：正文开头的一段纯文本，用在时间流和 meta description 上。
 *
 * 不需要剥 frontmatter —— Astro 5 的 content layer 交给我们的 entry.body 本来就不含它。
 * （原先这里有一行 `.replace(/^---[\s\S]*?---/, '')`：对 body 是死代码，却会在正文以
 * `---` 开头时从开头一路删到下一个 `---`，把第一段整个吃掉。）
 */
export function excerpt(entry: Entry, max = 96): string {
  // 代码块按行开关，不用正则成对匹配：写到一半、或粘贴时少了结尾围栏的正文很常见，
  // 而成对匹配对未闭合围栏一个字都删不掉——连 ``` 标记本身都会进 meta description。
  // 按 CommonMark，没有结尾围栏就一路开到文末。
  let inCode = false;
  const text = (entry.body ?? '')
    .split('\n')
    .filter((l) => {
      const t = l.trim();
      if (!inCode && t.startsWith('```')) {
        inCode = true;
        return false;
      }
      if (inCode) {
        if (/^```+\s*$/.test(t)) inCode = false;
        return false;
      }
      return true;
    })
    .map(stripMarkdown)
    .join(' ')
    .replace(/\s+/g, ' ')
    .trim();
  return text.length > max ? text.slice(0, max) + '…' : text;
}

/**
 * list 条目的前几项，直接铺在时间流的卡片上。
 *
 * 清单的信息全在项里，标题只是个文件名——只给标题的话，一张「这个季度在读」的卡
 * 和一条短记在屏幕上一样重，读者没有任何理由点进去。
 * 只认顶层列表项：缩进的子项依附上一条，单独抽出来会丢掉上下文。
 */
export function listItems(entry: Entry): string[] {
  const out: string[] = [];
  let inCode = false;
  for (const raw of (entry.body ?? '').split('\n')) {
    if (raw.trim().startsWith('```')) {
      inCode = !inCode;
      continue;
    }
    if (inCode) continue;
    // 顶层就是顶层，不留缩进余量。CommonMark 允许顶层项带 0~3 个空格，
    // 但这个站的正文（手写的和 scripts/n 写的）都从第 0 列起；而 2 空格恰好是
    // 多数编辑器缩进子项的量，放行它等于把子项当顶层铺出来——正是上面注释要避免的。
    if (!/^(?:[-*+]|\d+[.)])\s+/.test(raw)) continue;
    const text = stripMarkdown(raw);
    if (text) out.push(text);
  }
  return out;
}

/**
 * link 条目指向的域名，显示在卡片上。
 *
 * 「这条链接通向哪」是外链最重要的一条信息，而它现在藏在正文的 markdown 语法里。
 * 解析失败就返回 null，卡片少一行而已——不为一个装饰性的字段让构建挂掉。
 */
export function linkHost(entry: Entry): string | null {
  const body = entry.body ?? '';
  // 图片语法 ![alt](url) 也以 `](` 结尾，所以要连 `[` 前面那个 ! 一起看——
  // 否则正文里配了图的外链，卡片上显示的会是图床域名。
  // 「这条链接通向哪」正是这个字段存在的唯一理由，指错地方比不显示更糟。
  let url = body.match(/(?<!!)\[[^\]]*\]\((https?:\/\/[^)\s]+)\)/)?.[1] ?? null;
  if (!url) {
    for (const g of body.matchAll(/(https?:\/\/[^\s)<>"']+)/g)) {
      if (/!\[[^\]]*\]\($/.test(body.slice(0, g.index))) continue;
      url = g[1];
      break;
    }
  }
  if (!url) return null;
  try {
    return new URL(url).host.replace(/^www\./, '');
  } catch {
    return null;
  }
}

export interface EntryImage {
  src: string;
  alt: string;
}

/**
 * photo 条目正文里的第一张图，铺在时间流的卡片上。
 *
 * photo 是六型里唯一「拿到了颜色却没有结构分支」的一支：卡片上从不出现图像，
 * 正文位置显示的是 excerpt 剥出来的 alt 文本——一张影像卡上印着「一张图的说明」，
 * 而图不在。这个函数就是补上那个分支。
 *
 * **只认绝对路径（`/…`）和远程图（`https://…`），相对路径一律返回 null。**
 * 这一条不是保守，是因为两条渲染路径不一样：
 *   · `<Content />`（条目页）走 Astro 的图片管线，markdown 里的相对路径会被解析成
 *     真实文件、压缩、加 hash，最后 src 指向 `/_astro/xxx.hash.webp`；
 *   · 这里是拿正则从**正文原文**里抠出来的裸 src，一个字都没经过那条管线。
 * 于是同一条 `![](./a.jpg)`，条目页上是好的，首页卡片上会指向 `/e/<id>/a.jpg`
 * ——一个从来没被拷进 dist 的路径。**坏图比没有图更糟**：没有图只是少一块内容，
 * 坏图是一个当场就露馅的破洞，而且它只在首页出现，改完条目页看一眼是发现不了的。
 *
 * 想让相对路径也能上卡片，正确的做法是走 `getImage()` 把它送进同一条管线，
 * 而不是把裸 src 印出去。真需要时再做；在那之前，配图写成 `/img/…` 或图床 URL。
 */
export function firstImage(entry: Entry): EntryImage | null {
  let inCode = false;
  for (const raw of (entry.body ?? '').split('\n')) {
    // 代码块里的 `![](…)` 是被展示的语法，不是这条条目的配图。
    if (raw.trim().startsWith('```')) {
      inCode = !inCode;
      continue;
    }
    if (inCode) continue;
    // ![alt](src "title")：尖括号包裹和结尾的可选 title 都是 CommonMark 的合法写法，
    // 不认的话 src 里会混进一个 `"` 和半句标题，直接写进 <img src>。
    const m = raw.match(/!\[([^\]]*)\]\(\s*<?([^)\s>]+)>?(?:\s+["'][^"']*["'])?\s*\)/);
    if (!m) continue;
    const src = m[2];
    if (!/^(?:https?:)?\/\//.test(src) && !src.startsWith('/')) continue;
    return { src, alt: m[1].trim() };
  }
  return null;
}

export const TYPE_LABEL: Record<string, string> = {
  post: '文章',
  note: '笔记',
  log: '短记',
  link: '链接',
  list: '清单',
  photo: '影像',
};

/**
 * frontmatter 里的 created / updated 是纯日期（2026-09-04），z.coerce.date() 按 UTC
 * 午夜解析。所以这里必须按 UTC 取字段：用 getFullYear/getMonth/getDate 会在 UTC 以西的
 * 机器上把每一条都渲染成前一天（实测 America/Los_Angeles 下 created: 2026-09-04
 * 显示成 2026-09-03），而同一页上的 <time datetime> 又是对的——机器读到的和人看到的不一致。
 */
export function formatDate(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0');
  return `${d.getUTCFullYear()}-${p(d.getUTCMonth() + 1)}-${p(d.getUTCDate())}`;
}
