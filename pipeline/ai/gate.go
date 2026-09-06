package main

// 出口闸门。
//
// 这份 JSON 要进代码仓，代码仓是公开的。而它的原料是 transcript：真实对话、绝对路径、
// 仓库名、偶尔还有贴进终端的密钥。所以「产出里没有原文」不能是一条口头约定，
// 得是一道**写盘之前会拦下来**的检查。
//
// 做法不是「扫一遍看看有没有像密钥的东西」——那是黑名单，永远漏。这里用的是白名单，
// 而且白到极致：
//
//	这份文档里**根本没有能装下自由文本的位置**。
//
//	· 字符串值：只准是 YYYY-MM-DD。别的一律拒。
//	· 对象的键：只准是 schema 里写死的那些字段名、toolClasses 里那几个类别名，或者一个日期。
//	· 数值：只准是整数。
//	· 布尔、null：不准出现。
//
// 于是不存在「一段路径混进来了但没被规则命中」这种可能：路径不是日期，就没有它能待的槽。
// 密钥、消息正文、分支名同理。要绕过这道闸门，只能先给 schema 加一个新的字符串字段，
// 而那件事在这个文件里改不动——加字段的人必须回来改这里的白名单，那正是要他停下来想的时刻。
//
// 闸门失败时**不写文件**。宁可这一次采集白跑，也不要写出一份「大概没问题」的 JSON。

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// isoDate 是这份文档里唯一允许的字符串形状。
var isoDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// schemaKeys 是这份文档允许出现的字段名，一个一个手写。
//
// 它**故意不从结构体的 tag 反射生成**：反射生成的话，谁给 day 加一个
// `Note string \`json:"note"\“ 字段，白名单会自动跟着放行，闸门就成了摆设。
// 手写的代价是加字段要改两处；换来的是「加字段」这个动作必然经过这一行。
var schemaKeys = map[string]bool{
	"schema": true, "updated": true, "tzOffsetHours": true, "days": true,

	"seen": true, "prompts": true, "promptChars": true, "turns": true,
	"toolCalls": true, "toolErrors": true, "tools": true,
	"bashCmds": true, "bashPiped": true, "bashLen": true,
	"leverage": true, "leverageCalls": true, "hours": true, "tokens": true, "projects": true,

	"in": true, "cacheRead": true, "cacheCreate": true, "out": true, "thinking": true,

	"count": true, "topPermille": true,
}

// keyShape 是一道**独立于白名单**的第二重检查：哪怕有人往 schemaKeys 或 toolClasses 里
// 塞了东西，键仍然必须短、必须没有分隔符和空白。
// 24 个字符是够用的上限（最长的合法键 tzOffsetHours 是 13），而任何路径、
// MCP 工具名（mcp__gitcode__create_pull_request_comment 有 40 个字符）、
// 密钥都过不了。CJK 类别名走 unicode 判断，不走这条正则。
var keyShape = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,24}$`)

const maxKeyRunes = 24

func allowedKey(k string) bool {
	if utf8.RuneCountInString(k) > maxKeyRunes {
		return false
	}
	if isoDate.MatchString(k) {
		return true
	}
	if schemaKeys[k] && keyShape.MatchString(k) {
		return true
	}
	for _, c := range toolClasses {
		if k == c {
			return true
		}
	}
	return false
}

// checkExport 走一遍**已经序列化过再解回来**的值，而不是走 Go 的结构体。
// 走结构体只能看见你以为存在的字段；走 JSON 看见的是真正会写进文件的每一个字节。
// map[string]int 这种键由数据决定的地方，只有后一种走法拦得住。
func checkExport(data []byte) error {
	var v any
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber() // 不走 float64：整数检查要看原始字面量，1e3 和 1000 不是一回事
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("产出不是合法 JSON：%w", err)
	}
	return walk(v, "$")
}

func walk(v any, path string) error {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if !allowedKey(k) {
				return fmt.Errorf("%s 里出现了不该有的键 %q——"+
					"这份 JSON 的键只能是 schema 字段名、工具类别名或日期", path, clip(k))
			}
			if err := walk(child, path+"."+k); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range t {
			if err := walk(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case string:
		if !isoDate.MatchString(t) {
			return fmt.Errorf("%s 是一段自由文本 %q——"+
				"这份 JSON 里唯一允许的字符串是 YYYY-MM-DD", path, clip(t))
		}
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return fmt.Errorf("%s 不是一个数：%v", path, err)
		}
		if strings.ContainsAny(t.String(), ".eE") || math.Trunc(f) != f {
			return fmt.Errorf("%s = %s 不是整数——聚合结果全部记成整数计数，"+
				"出现小数说明有人把比率存了下来（比率应该在页面上现算）", path, t.String())
		}
		if math.Abs(f) > 1<<53 {
			return fmt.Errorf("%s = %s 超出安全整数范围", path, t.String())
		}
	case bool:
		return fmt.Errorf("%s 是布尔值——这份 JSON 只有计数，布尔通常意味着"+
			"「某件私事发生过没有」，不该出现", path)
	case nil:
		return fmt.Errorf("%s 是 null——空缺应当写成 0，null 会在页面上算成 NaN", path)
	default:
		return fmt.Errorf("%s 是没见过的类型 %T", path, v)
	}
	return nil
}

// clip 让报错本身也不泄露：闸门拦下来的很可能正是一段密钥或路径，
// 把它整条打进日志（日志可能被贴进 issue）就等于闸门白拦了。
func clip(s string) string {
	const n = 12
	r := []rune(s)
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
