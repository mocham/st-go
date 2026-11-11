package pinyin

import (
	"context"
	"sync"

	"st-go/googlepinyin"
)

// Lookup uses the shared in-process pure-Go decoder. Decoder initialization is
// lazy and concurrency-safe; no helper process, socket, or daemon is involved.
func Lookup(ctx context.Context, request Request) (Response, error) {
	response := Response{ID: request.ID, Prompt: request.Prompt}
	if err := request.Validate(); err != nil {
		return response, err
	}
	if err := ctx.Err(); err != nil {
		return response, err
	}
	decoder, err := googlepinyin.OpenDefault()
	if err != nil {
		return response, err
	}
	candidates, err := decoder.Lookup(request.Prompt, MaxCandidates)
	if err != nil {
		return response, err
	}
	if err := ctx.Err(); err != nil {
		return response, err
	}
	response.Total = len(candidates)
	if response.Total > 0 {
		response.Page = min(request.Page, (response.Total-1)/PageSize)
		first := response.Page * PageSize
		last := min(first+PageSize, response.Total)
		response.Candidates = append(response.Candidates, candidates[first:last]...)
	}
	return response, response.Validate(request)
}

type localService struct {
	exited chan struct{}
	once   sync.Once
}

func newLocalService() *localService { return &localService{exited: make(chan struct{})} }

func (*localService) Lookup(ctx context.Context, request Request) (Response, error) {
	return Lookup(ctx, request)
}
func (service *localService) Exited() <-chan struct{} { return service.exited }
func (service *localService) Close() error {
	service.once.Do(func() { close(service.exited) })
	return nil
}

// NewLocalWorker performs lookups on a coalescing goroutine so an application
// event loop never blocks on first-use corpus validation or a long composition.
func NewLocalWorker() *Worker {
	return NewWorker(func(context.Context) (Service, error) { return newLocalService(), nil })
}
