//go:build googlepinyin

package pinyinengine

import (
	"fmt"

	"st-go/googlepinyin"
	"st-go/tui/pinyin"
)

type Engine struct {
	decoder *googlepinyin.Decoder
}

func Open() (*Engine, error) {
	dictionary, err := dictionaryBytes()
	if err != nil {
		return nil, err
	}
	decoder, err := googlepinyin.Open(dictionary)
	if err != nil {
		return nil, fmt.Errorf("cannot initialize pure-Go Pinyin dictionary: %w", err)
	}
	return &Engine{decoder: decoder}, nil
}

func (*Engine) Close() {}

func (engine *Engine) Lookup(request pinyin.Request) (pinyin.Response, error) {
	response := pinyin.Response{ID: request.ID, Prompt: request.Prompt}
	if err := request.Validate(); err != nil {
		return response, err
	}
	candidates, err := engine.decoder.Lookup(request.Prompt, pinyin.MaxCandidates)
	if err != nil {
		return response, err
	}
	response.Total = len(candidates)
	if response.Total > 0 {
		response.Page = min(request.Page, (response.Total-1)/pinyin.PageSize)
		first := response.Page * pinyin.PageSize
		last := min(first+pinyin.PageSize, response.Total)
		response.Candidates = append(response.Candidates, candidates[first:last]...)
	}
	return response, response.Validate(request)
}
