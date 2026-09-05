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

  return all.sort((a, b) => b.data.created.getTime() - a.data.created.getTime());
}

/** 进静态产物的两类。circle / private 在这里被挡住，不会出现在 dist 的任何文件里。 */
export async function publicEntries(): Promise<Entry[]> {
  return (await loadEntries()).filter((e) => e.data.visibility === 'public');
}

export async function unlistedEntries(): Promise<Entry[]> {
  return (await loadEntries()).filter((e) => e.data.visibility === 'unlisted');
}

/** 去掉一行 markdown 的标记，只留文字。分隔线这类纯标记行会变成空串。 */
function stripMarkdown(line: string): string {
  const t = line.trim();
  if (/^([-*_])\1{2,}$/.test(t)) return ''; // --- *** ___ 是分隔线，不是内容
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
  // 跳过代码围栏和分隔线：它们是标记不是内容，拿来当标题只会得到「```」或者「（无题）」。
  const line =
    (entry.body ?? '').split('\n').find((l) => {
      const t = l.trim();
      return t.length > 0 && !t.startsWith('```') && !/^([-*_])\1{2,}$/.test(t);
    }) ?? '';
  const stripped = stripMarkdown(line);
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
