package petri

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/k3s-io/kine/pkg/drivers"
	"github.com/k3s-io/kine/pkg/drivers/memory"
	"github.com/k3s-io/kine/pkg/endpoint"
	"github.com/k3s-io/kine/pkg/server"
)

const (
	// kineScheme names petri's wrapper around kine's memory driver.
	kineScheme   = "petri-memory"
	kineEndpoint = kineScheme + "://"
	kineListener = "tcp://127.0.0.1:0"
	// kine's CLI defaults. The library leaves them zero, which would disable
	// the watch progress notifications kube-apiserver relies on.
	kineEmulatedETCDVersion = "3.6.11"
	kineNotifyInterval      = 5 * time.Second
	watchBuffer             = 100
)

// registerKineDriver registers kineScheme once. kine's registry is a plain map,
// so it must not be written while another control plane is starting.
var registerKineDriver = sync.OnceFunc(func() { //nolint:gochecknoglobals // kine's driver registry is process-wide.
	drivers.Register(kineScheme, func(
		ctx context.Context, wg *sync.WaitGroup, cfg *drivers.Config,
	) (bool, server.Backend, error) {
		leaderElect, backend, err := memory.New(ctx, wg, cfg)
		if err != nil {
			return false, nil, fmt.Errorf("kine memory driver: %w", err)
		}

		return leaderElect, clippedBackend{&lockedBackend{Backend: backend}}, nil
	})
})

// lockedBackend serializes the memory driver's reads against its writes.
// kine's memory driver keeps every read but ListStream under its own lock;
// the streaming one iterates its store on a goroutine with no lock held, so
// an informer list racing a create trips the race detector inside the driver.
// Every non-watch call runs under one mutex here, and ListStream drains its
// stream before returning, so the store never changes while it is read.
// Watch stays unlocked: its snapshot path locks itself, and it runs for the
// plane's whole life.
type lockedBackend struct {
	server.Backend // Start and Watch pass straight through.

	mu sync.Mutex
}

func (b *lockedBackend) Get(
	ctx context.Context, key string, revision int64, keysOnly bool,
) (int64, *server.KeyValue, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.Backend.Get(ctx, key, revision, keysOnly)
}

func (b *lockedBackend) Create(ctx context.Context, key string, value []byte, lease int64) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.Backend.Create(ctx, key, value, lease)
}

func (b *lockedBackend) Delete(
	ctx context.Context, key string, revision int64,
) (int64, *server.KeyValue, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.Backend.Delete(ctx, key, revision)
}

func (b *lockedBackend) List(
	ctx context.Context, key, end string, limit, revision int64, keysOnly bool,
) (int64, []*server.KeyValue, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.Backend.List(ctx, key, end, limit, revision, keysOnly)
}

// ListStream drains the stream under the lock: kine's iterator reads the live
// store, so it must not outlive the window in which no write can happen.
func (b *lockedBackend) ListStream(
	ctx context.Context, key, end string, limit, revision int64, keysOnly bool,
) server.ListResult {
	b.mu.Lock()
	defer b.mu.Unlock()

	res := b.Backend.ListStream(ctx, key, end, limit, revision, keysOnly)

	var (
		kvs []*server.KeyValue
		err error
	)

	for batch := range res.KVc {
		kvs = append(kvs, batch)
	}

	for e := range res.Errorc {
		err = e
	}

	out := make(chan *server.KeyValue, len(kvs)+1)
	errc := make(chan error, 1)

	for _, kv := range kvs {
		out <- kv
	}
	close(out)

	if err != nil {
		errc <- err
	}
	close(errc)

	return server.ListResult{KVc: out, Errorc: errc, CurrentRevision: res.CurrentRevision}
}

func (b *lockedBackend) Count(ctx context.Context, key, end string, revision int64) (int64, int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.Backend.Count(ctx, key, end, revision)
}

func (b *lockedBackend) Update(
	ctx context.Context, key string, value []byte, revision, lease int64,
) (int64, *server.KeyValue, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.Backend.Update(ctx, key, value, revision, lease)
}

//nolint:staticcheck // kine's Backend interface names it DbSize.
func (b *lockedBackend) DbSize(ctx context.Context) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.Backend.DbSize(ctx)
}

func (b *lockedBackend) CurrentRevision(ctx context.Context) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.Backend.CurrentRevision(ctx)
}

func (b *lockedBackend) Compact(ctx context.Context, revision int64) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.Backend.Compact(ctx, revision)
}

// clippedBackend works around a kine data race: the memory driver broadcasts
// one EventBatch to every watcher, and kine's watch loop appends queued events
// onto batch.Events. With spare capacity in that slice, concurrent watchers
// write into the same array and can overwrite each other's events. Clipping
// every batch to its length makes those appends copy instead.
type clippedBackend struct {
	server.Backend
}

func (b clippedBackend) Watch(ctx context.Context, revision int64) server.WatchResult {
	res := b.Backend.Watch(ctx, revision)
	src := res.Eventc
	out := make(chan server.EventBatch, watchBuffer)

	go func() {
		defer close(out)

		for batch := range src {
			batch.Events = slices.Clip(batch.Events)

			select {
			case out <- batch:
			case <-ctx.Done():
				// Keep reading until kine closes the channel, or its sender blocks forever.
				for range src {
				}

				return
			}
		}
	}()

	res.Eventc = out

	return res
}

// startKine serves an in-memory etcd-compatible store until ctx is cancelled
// and returns its client endpoints. wg tracks kine's goroutines.
func startKine(ctx context.Context, wg *sync.WaitGroup) ([]string, error) {
	registerKineDriver()

	etcd, err := endpoint.Listen(ctx, endpoint.Config{
		WaitGroup:           wg,
		Endpoint:            kineEndpoint,
		Listener:            kineListener,
		EmulatedETCDVersion: kineEmulatedETCDVersion,
		NotifyInterval:      kineNotifyInterval,
	})
	if err != nil {
		return nil, fmt.Errorf("start kine: %w", err)
	}

	return etcd.Endpoints, nil
}
