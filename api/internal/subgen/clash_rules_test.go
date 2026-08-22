package subgen

import (
	"encoding/json"
	"strings"
	"testing"
)

// 这条护的是一个曾经真实存在的缺陷：配置写着 `mode: rule`，却一条 `rules` 都没有。
// mihomo 在规则全不匹配时回落到 DIRECT，所以「没有规则」= **全部直连**，
// 用户看到的现象是节点全在、延迟正常、被墙的站点一个都打不开。
func TestRenderClashHasRulesAndMatchFallback(t *testing.T) {
	out, err := Render(FormatClash, Document{Proxies: sampleProxies()})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := string(out)

	if !strings.Contains(got, "\nrules:\n") {
		t.Fatalf("配置里没有 rules 段 —— mode: rule 下等于全部直连:\n%s", got)
	}

	// MATCH 必须存在，且目标必须是默认组的**逐字**名字。
	// 指向一个不存在的组会让 mihomo 拒绝加载整份配置。
	wantMatch := "- MATCH," + GroupDefault
	if !strings.Contains(got, wantMatch) {
		t.Errorf("缺少兜底规则 %q:\n%s", wantMatch, got)
	}
	if !strings.Contains(got, "- name: "+yamlString(GroupDefault)) {
		t.Errorf("MATCH 指向的组 %q 在 proxy-groups 里不存在", GroupDefault)
	}

	// MATCH 必须是最后一条：它匹配一切，排在它后面的规则永远不会被求值。
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	var ruleLines []string
	inRules := false
	for _, l := range lines {
		if l == "rules:" {
			inRules = true
			continue
		}
		if inRules && strings.HasPrefix(l, "  - ") {
			ruleLines = append(ruleLines, strings.TrimPrefix(l, "  - "))
		}
	}
	if len(ruleLines) == 0 {
		t.Fatal("rules 段是空的")
	}
	if last := ruleLines[len(ruleLines)-1]; last != "MATCH,"+GroupDefault {
		t.Errorf("MATCH 不是最后一条，实际最后一条是 %q —— 它后面的规则永远不会生效", last)
	}
	for _, r := range ruleLines[:len(ruleLines)-1] {
		if strings.HasPrefix(r, "MATCH,") {
			t.Errorf("MATCH 出现在中间：%q", r)
		}
	}

	// 私有网段直连，且带 no-resolve（否则为了判断一条 IP 规则会先去做 DNS 解析）。
	for _, want := range []string{
		"IP-CIDR,192.168.0.0/16,DIRECT,no-resolve",
		"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
		"GEOIP,CN,DIRECT",
	} {
		if !strings.Contains(got, "- "+want) {
			t.Errorf("缺少规则 %q", want)
		}
	}
}

// 伪节点（停用/到期通知）走的是同一个渲染路径，规则段同样要在 ——
// 否则「账号停用」这条通知反而会因为全部直连而让用户以为一切正常。
func TestRenderClashRulesPresentForNoticeOnlyDocument(t *testing.T) {
	out, err := Render(FormatClash, Document{Proxies: []Proxy{NoticeProxy("⚠️ 账号已停用")}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(out), "- MATCH,"+GroupDefault) {
		t.Errorf("伪节点文档缺 rules 段:\n%s", out)
	}
}

// sing-box 的默认出站以前靠「route.final 留空时取第一个 outbound」这条隐式默认。
// 显式写出来之后，这条测试让它变成断言：有人往 outbounds 前面插东西时会红。
func TestRenderSingboxHasExplicitRouteFinal(t *testing.T) {
	out, err := Render(FormatSingbox, Document{Proxies: sampleProxies()})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var cfg struct {
		Route struct {
			Final string `json:"final"`
		} `json:"route"`
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("产出不是合法 JSON: %v", err)
	}
	if cfg.Route.Final != GroupDefault {
		t.Errorf("route.final = %q，期望 %q", cfg.Route.Final, GroupDefault)
	}
	// final 指向的 tag 必须真的存在，否则 sing-box 拒绝加载。
	found := false
	for _, ob := range cfg.Outbounds {
		if ob.Tag == cfg.Route.Final {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("route.final 指向的 outbound tag %q 不存在", cfg.Route.Final)
	}
}
