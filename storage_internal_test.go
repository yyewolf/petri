package petri

import (
	"context"
	"testing"

	"github.com/k3s-io/kine/pkg/server"
)

// fakeWatchBackend serves one prepared Watch result; every other Backend
// method is unused and panics through the nil embedded interface.
type fakeWatchBackend struct {
	server.Backend

	events chan server.EventBatch
}

func (f fakeWatchBackend) Watch(context.Context, int64) server.WatchResult {
	return server.WatchResult{Eventc: f.events}
}

func TestClippedBackendClipsWatchBatches(t *testing.T) {
	t.Parallel()

	shared := make([]*server.Event, 1, 8) // spare capacity, as kine's memory driver builds it
	shared[0] = &server.Event{Create: true}

	src := make(chan server.EventBatch, 1)
	src <- server.EventBatch{Events: shared, CurrentRev: 7}
	close(src)

	res := clippedBackend{fakeWatchBackend{events: src}}.Watch(t.Context(), 0)

	batch, ok := <-res.Eventc
	if !ok {
		t.Fatal("event channel closed before delivering the batch")
	}
	if len(batch.Events) != 1 || batch.Events[0] != shared[0] || batch.CurrentRev != 7 {
		t.Fatalf("batch = %+v, want the original event at revision 7", batch)
	}
	if cap(batch.Events) != len(batch.Events) {
		t.Errorf("cap = %d, want %d so appends by one watcher cannot write into a batch shared with others",
			cap(batch.Events), len(batch.Events))
	}

	if _, open := <-res.Eventc; open {
		t.Error("event channel still open after the source closed")
	}
}
