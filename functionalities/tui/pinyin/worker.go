package pinyin

import (
	"context"
	"fmt"
)

type Service interface {
	Lookup(context.Context, Request) (Response, error)
	Exited() <-chan struct{}
	Close() error
}
type Result struct {
	Response Response
	Err      error
}
type Worker struct {
	requests chan Request
	results  chan Result
	cancel   context.CancelFunc
	done     chan struct{}
}

func NewWorker(start func(context.Context) (Service, error)) *Worker {
	ctx, cancel := context.WithCancel(context.Background())
	return newWorker(ctx, cancel, start)
}

func newWorker(ctx context.Context, cancel context.CancelFunc, start func(context.Context) (Service, error)) *Worker {
	w := &Worker{make(chan Request, 1), make(chan Result, 1), cancel, make(chan struct{})}
	go func() {
		defer close(w.done)
		service, err := start(ctx)
		if err != nil {
			w.publish(ctx, Result{Err: err})
			return
		}
		defer service.Close()
		go func() {
			select {
			case <-ctx.Done():
				service.Close()
			case <-w.done:
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-service.Exited():
				w.publish(ctx, Result{Err: fmt.Errorf("pinyin decoder stopped; input mode stopped")})
				return
			case request := <-w.requests:
				response, err := service.Lookup(ctx, request)
				if err == nil {
					err = response.Validate(request)
				}
				w.publish(ctx, Result{response, err})
				if err != nil {
					return
				}
			}
		}
	}()
	return w
}
func (w *Worker) publish(ctx context.Context, r Result) {
	if ctx.Err() != nil {
		return
	}
	select {
	case <-w.results:
	default:
	}
	w.results <- r
}
func (w *Worker) Submit(r Request) error {
	if err := r.Validate(); err != nil {
		return err
	}
	select {
	case <-w.done:
		return fmt.Errorf("pinyin decoder is stopped")
	default:
	}
	select {
	case <-w.requests:
	default:
	}
	w.requests <- r
	return nil
}
func (w *Worker) Results() <-chan Result { return w.results }
func (w *Worker) Done() <-chan struct{}  { return w.done }
func (w *Worker) Close() {
	if w != nil {
		w.cancel()
	}
}
