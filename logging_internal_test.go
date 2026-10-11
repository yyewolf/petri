package petri

import (
	"strings"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"
	"k8s.io/klog/v2"
)

func TestLineBufferKeepsLastLines(t *testing.T) {
	t.Parallel()

	b := newLineBuffer(2)
	_, _ = b.Write([]byte("one\ntwo\nthr"))
	_, _ = b.Write([]byte("ee\nfour\n"))

	if got, want := b.String(), "three\nfour"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestLineBufferIncludesPartialLine(t *testing.T) {
	t.Parallel()

	b := newLineBuffer(5)
	_, _ = b.Write([]byte("done\nhalf"))

	if got, want := b.String(), "done\nhalf"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestLineBufferConcurrentWrites(t *testing.T) {
	t.Parallel()

	b := newLineBuffer(10)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				_, _ = b.Write([]byte("line\n"))
			}
		})
	}
	wg.Wait()

	if got := strings.Count(b.String(), "line"); got != 10 {
		t.Errorf("kept %d lines, want 10", got)
	}
}

func TestProcessLogCapturesKlogAndLogrus(t *testing.T) {
	t.Parallel()

	buf := processLog()

	klog.Info("petri-klog-marker")
	klog.Flush()
	logrus.Info("petri-logrus-marker")

	got := buf.String()
	for _, marker := range []string{"petri-klog-marker", "petri-logrus-marker"} {
		if !strings.Contains(got, marker) {
			t.Errorf("process log is missing %q; got:\n%s", marker, got)
		}
	}
}
