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

		return leaderElect, clippedBackend{backend}, nil
	})
})

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
