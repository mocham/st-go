//go:build googlepinyin

package pinyinengine

import (
	"strings"
	"testing"

	"st-go/tui/pinyin"
)

func TestPureGoEngineCandidateCompatibility(t *testing.T) {
	engine, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	response, err := engine.Lookup(pinyin.Request{ID: 1, Prompt: "nh", Page: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"南海", "农行", "浓厚", "那好", "弄好"}
	if strings.Join(response.Candidates, ",") != strings.Join(want, ",") {
		t.Fatalf("nh page 3 = %q, want %q (total %d)", response.Candidates, want, response.Total)
	}
	vectors := []struct {
		prompt string
		want   string
	}{
		{"nh", "你好,女孩,您好,你会,男孩"},
		{"nihao", "你好,你,拟,尼,呢"},
		{"nih", "你好,你会,你还,你和,你很"},
		{"zhongguo", "中国,中,种,重,钟"},
		{"zgr", "中国人,这个人,张国荣,整个人,这个"},
		{"zhongguoren", "中国人,中国,中,种,重"},
		{"xi'an", "西安,西岸,系,西,洗"},
		{"xian", "先,线,县,现,咁"},
		{"women", "我们,我,喔,窝,握"},
		{"zai", "在,再,载,仔,宰"},
		{"shanghai", "上海,伤害,上还,上,商"},
		{"beijing", "北京,背景,被,呗,北"},
		{"ceshi", "测试,测,侧,册,策"},
		{"pinyin", "拼音,品,拼,频,贫"},
		{"gongneng", "功能,共,工,公,供"},
		{"womenzai", "我们在,我们,我,喔,窝"},
	}
	for index, vector := range vectors {
		response, err = engine.Lookup(pinyin.Request{ID: uint64(index + 2), Prompt: vector.prompt})
		if err != nil {
			t.Fatalf("%s: %v", vector.prompt, err)
		}
		if got := strings.Join(response.Candidates, ","); got != vector.want {
			t.Errorf("%s candidates = %q, want %q", vector.prompt, got, vector.want)
		}
	}
}

func TestPureGoEngineHandlesFormerCrashBoundary(t *testing.T) {
	engine, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	for length := 1; length <= pinyin.MaxPrompt; length++ {
		request := pinyin.Request{ID: uint64(length), Prompt: strings.Repeat("a", length)}
		response, err := engine.Lookup(request)
		if err != nil {
			t.Fatalf("%d letters: %v", length, err)
		}
		if err := response.Validate(request); err != nil {
			t.Fatalf("%d letters: %v", length, err)
		}
	}
}
