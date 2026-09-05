import { defineCollection, z } from 'astro:content';
import { glob } from 'astro/loaders';

/**
 * 条目模型。见 ARCHITECTURE.md 第 4 节：
 * 不为每种内容建一个系统，用正交属性描述同一种「条目」。
 * 加一种新内容类型 = 往 TYPES 里多一个值，架构不动。
 */
export const TYPES = ['post', 'note', 'log', 'link', 'list', 'photo'] as const;
export const VISIBILITY = ['public', 'unlisted', 'circle', 'private'] as const;
export const STATUS = ['seed', 'growing', 'done'] as const;

const entries = defineCollection({
  loader: glob({ base: './content/entries', pattern: '**/*.md' }),
  schema: z.object({
    // log / link 这类碎片可以没有标题，展示层用正文首行兜底
    title: z.string().optional(),
    type: z.enum(TYPES).default('log'),

    // 故意默认 private：忘写这个字段时应当「消失」，而不是「泄露」。
    // 见 ARCHITECTURE.md 第 2 节。
    visibility: z.enum(VISIBILITY).default('private'),

    status: z.enum(STATUS).optional(),
    created: z.coerce.date(),
    updated: z.coerce.date().optional(),
    tags: z.array(z.string()).default([]),
  }),
});

export const collections = { entries };
