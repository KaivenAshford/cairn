package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// 闸门放行真实形状的产出。这一条防的是「闸门太严，于是有人把它关了」。
func TestGateAllowsRealShape(t *testing.T) {
	s := scanFixture(t)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkExport(data); err != nil {
		t.Fatalf("闸门拦下了正常产出：%v", err)
	}
}

// 闸门拦下每一种「原文混进来了」的形状。
// 每一条都对应一个真会发生的写法——不是想象出来的坏输入。
func TestGateRejects(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		// want 是报错里必须提到的词，用来确认拦下它的是**那条**规则，
		// 而不是碰巧被别的规则挡了（那样规则一改就会静默放行）。
		want string
	}{
		{
			"命令原文当成了值",
			`{"schema":1,"days":{"2026-01-01":{"seen":1,"tools":{"执行":1},"cmd":"grep -r X /home/z/.env"}}}`,
			"不该有的键",
		},
		{
			"绝对路径当成了键",
			`{"schema":1,"days":{"/home/zhang/项目":{"seen":1}}}`,
			"不该有的键",
		},
		{
			"MCP 工具名原样当键（服务名会说出你接了谁）",
			`{"schema":1,"days":{"2026-01-01":{"tools":{"mcp__gitcode__create_pull_request_comment":3}}}}`,
			"不该有的键",
		},
		{
			"项目名原样当键",
			`{"schema":1,"days":{"2026-01-01":{"tools":{"-workspace-typescript-go-OH":3}}}}`,
			"不该有的键",
		},
		{
			"像密钥的字符串当成了值",
			`{"schema":1,"updated":"AKIAIOSFODNN7EXAMPLE"}`,
			"自由文本",
		},
		{
			"一句话当成了值（哪怕它很短）",
			`{"schema":1,"updated":"看一下"}`,
			"自由文本",
		},
		{
			"存了比率而不是计数",
			`{"schema":1,"days":{"2026-01-01":{"seen":0.42}}}`,
			"不是整数",
		},
		{
			"科学计数法（看着像整数，其实是浮点）",
			`{"schema":1,"days":{"2026-01-01":{"seen":1e3}}}`,
			"不是整数",
		},
		{
			"布尔值（「某件私事发生过没有」）",
			`{"schema":1,"days":{"2026-01-01":{"seen":true}}}`,
			"布尔",
		},
		{
			"null",
			`{"schema":1,"days":{"2026-01-01":{"tools":null}}}`,
			"null",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkExport([]byte(c.doc))
			if err == nil {
				t.Fatalf("闸门放行了：%s", c.doc)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("拦是拦住了，但理由不对：%v（想要提到 %q）", err, c.want)
			}
		})
	}
}

// 报错本身不能把拦下来的东西整条印出去：闸门拦住的很可能正是一段密钥，
// 而报错会进日志、进 CI 输出、被贴进 issue。
func TestGateErrorDoesNotEchoTheSecret(t *testing.T) {
	const secret = "AKIAIOSFODNN7EXAMPLE-and-a-very-long-tail"
	err := checkExport([]byte(`{"schema":1,"updated":"` + secret + `"}`))
	if err == nil {
		t.Fatal("没拦住")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("报错把整段原文印出来了：%v", err)
	}
	if !strings.Contains(err.Error(), "…") {
		t.Errorf("报错应该截断并留一个省略号，好让人知道被截过：%v", err)
	}
}

// 白名单必须是手写的。这一条钉住的不是代码，是**改代码的方式**：
// 只要 day 加了字段而没回来改 schemaKeys，闸门就会在写盘前拦住整次采集。
func TestGateKeySetCoversTheStruct(t *testing.T) {
	s := scanFixture(t)
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	// 顺带确认闸门认得出结构体真实用到的每一个键（否则上面 TestGateAllowsRealShape
	// 只是因为 fixture 太小而没碰到某些字段）。
	for _, k := range []string{"schema", "updated", "tzOffsetHours", "days"} {
		if !allowedKey(k) {
			t.Errorf("顶层键 %q 不在白名单里", k)
		}
	}
	if allowedKey("note") {
		t.Error("白名单放行了一个不存在的字段名，说明它不是手写的")
	}
}
