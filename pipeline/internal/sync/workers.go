package sync

import (
	"context"
	"sync"
)

const (
	defaultCongressWorkers = 5
	defaultGovInfoWorkers  = 3
	// defaultTextWorkers is how many bills sync-texts fetches and stores text for at once. The
	// downloads share www.congress.gov's budget; most of a version's time is the writes (#361).
	defaultTextWorkers = 4
)

// workerPool runs fn for each item using n goroutines. It doesn't pace them: every upstream
// request waits on its API's shared budget in the upstream client.
func workerPool[T any](ctx context.Context, items []T, workers int, fn func(context.Context, T)) {
	if len(items) == 0 {
		return
	}

	workers = clampWorkers(workers, len(items))
	ch := make(chan T, len(items))
	for _, item := range items {
		ch <- item
	}
	close(ch)
	workerPoolChan(ctx, ch, workers, fn)
}

// workerPoolChan runs fn for each item received from ch using n goroutines, until ch is closed or
// ctx is done. A sender must stop sending once ctx is done: the workers stop receiving.
func workerPoolChan[T any](ctx context.Context, ch <-chan T, workers int, fn func(context.Context, T)) {
	var wg sync.WaitGroup
	for range max(workers, 1) {
		wg.Go(func() { processItems(ctx, ch, fn) })
	}
	wg.Wait()
}

func clampWorkers(workers, itemCount int) int {
	if workers <= 0 {
		return 1
	}
	if workers > itemCount {
		return itemCount
	}
	return workers
}

func processItems[T any](ctx context.Context, ch <-chan T, fn func(context.Context, T)) {
	for item := range ch {
		if ctx.Err() != nil {
			return
		}
		fn(ctx, item)
	}
}
