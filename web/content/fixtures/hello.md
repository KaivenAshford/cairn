---
title: 一次放一块石头
type: post
visibility: public
status: done
created: 2026-09-04
tags: [meta]
---

cairn 是荒野里的石堆路标：每个路过的人放一块石头，日积月累就成了地标。

这个站不是博客。博客的单位是文章，而文章的门槛太高了——高到大部分想法在变成文章之前
就已经被忘掉。这里的单位是**条目**：可能是一篇长文，也可能是一条短记、一段笔记、
一个链接、一张清单，或者一段自动采集来的数据。

它们共用同一套属性：

```yaml
type: post | note | log | link | list | photo
visibility: public | unlisted | circle | private
status: seed | growing | done
```

`status: seed` 是有意留的口子——它允许公开还没想清楚的东西。不然每条都得写成结论，
那就又回到了文章的门槛。

`visibility` 那一栏是另一半：有些东西我只想给一部分人看。它不是靠前端藏起来的，
那些内容根本不进这个静态站的构建产物。

中文叫「起居注」。古代逐日记录帝王言行的那种文体——逐日、自动、不加修饰。
还有个规矩是皇帝不许看自己的起居注；这里反过来，是有些部分只给一部分人看。
