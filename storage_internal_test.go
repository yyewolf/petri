package petri

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yyewolf/petri/internal/kine/pkg/server"
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

func TestClippedBackendDrainsSourceAfterCancel(t *testing.T) {
	t.Parallel()

	src := make(chan server.EventBatch) // unbuffered: every send needs a reader
	ctx, cancel := context.WithCancel(t.Context())

	clippedBackend{fakeWatchBackend{events: src}}.Watch(ctx, 0)
	cancel()

	// kine's memory driver keeps sending until it closes the channel; the
	// wrapper must keep reading or that goroutine blocks forever.
	for i := range 3 {
		select {
		case src <- server.EventBatch{CurrentRev: int64(i)}:
		case <-time.After(time.Second):
			t.Fatalf("send %d blocked: the wrapper stopped draining kine's channel after cancel", i)
		}
	}

	close(src)
}

// fakeListStreamBackend serves one prepared ListStream result; every other
// Backend method is unused and panics through the nil embedded interface.
type fakeListStreamBackend struct {
	server.Backend

	result server.ListResult
}

func (f fakeListStreamBackend) ListStream(context.Context, string, string, int64, int64, bool) server.ListResult {
	return f.result
}

func TestLockedBackendListStreamKeepsOrderRevisionAndErrors(t *testing.T) {
	t.Parallel()

	kvs := []*server.KeyValue{{Key: "a"}, {Key: "b"}, {Key: "c"}}

	kvc := make(chan *server.KeyValue, len(kvs))
	for _, kv := range kvs {
		kvc <- kv
	}
	close(kvc)

	errc := make(chan error, 1)
	errc <- server.ErrCompacted
	close(errc)

	res := (&lockedBackend{
		Backend: fakeListStreamBackend{result: server.ListResult{KVc: kvc, Errorc: errc, CurrentRevision: 9}},
	}).ListStream(t.Context(), "", "", 0, 0, false)

	if res.CurrentRevision != 9 {
		t.Errorf("CurrentRevision = %d, want 9", res.CurrentRevision)
	}

	var got []string
	for kv := range res.KVc {
		got = append(got, kv.Key)
	}

	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("keys = %v, want a, b, c in order", got)
	}

	if err := <-res.Errorc; !errors.Is(err, server.ErrCompacted) {
		t.Errorf("error = %v, want ErrCompacted", err)
	}
}
