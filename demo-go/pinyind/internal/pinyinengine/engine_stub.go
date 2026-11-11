//go:build !googlepinyin

package pinyinengine

import (
	"fmt"
	"st-go/tui/pinyin"
)

type Engine struct{}

func Open() (*Engine, error) {
	return nil, fmt.Errorf("build the Pinyin helper with make govim-pinyind")
}
func (*Engine) Close() {}
func (*Engine) Lookup(pinyin.Request) (pinyin.Response, error) {
	return pinyin.Response{}, fmt.Errorf("pure-Go Pinyin not built")
}
