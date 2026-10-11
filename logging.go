package petri

import (
	"bytes"
	"flag"
	"io"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
	"k8s.io/klog/v2"
)

const logBufferLines = 500

// processLog redirects klog and logrus, which kube-apiserver and kine log
// through, into a buffer of recent lines. Both loggers are process-wide, so
// this happens once per process. PETRI_VERBOSE=1 also copies them to stderr.
var processLog = sync.OnceValue(redirectLogs) //nolint:gochecknoglobals // klog and logrus are process-wide.

// lineBuffer is an [io.Writer] that keeps the last limit complete lines written
// to it, plus any unterminated tail.
type lineBuffer struct {
	mu      sync.Mutex
	limit   int
	lines   []string
	partial []byte
}

func newLineBuffer(limit int) *lineBuffer {
	return &lineBuffer{limit: limit}
}

func (b *lineBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	data := slices.Concat(b.partial, p)

	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}

		b.lines = append(b.lines, string(data[:i]))
		data = data[i+1:]
	}

	b.partial = data

	if over := len(b.lines) - b.limit; over > 0 {
		b.lines = b.lines[over:]
	}

	return len(p), nil
}

func (b *lineBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := strings.Join(b.lines, "\n")
	if len(b.partial) > 0 {
		if out != "" {
			out += "\n"
		}

		out += string(b.partial)
	}

	return out
}

func redirectLogs() *lineBuffer {
	buf := newLineBuffer(logBufferLines)

	var w io.Writer = buf
	if os.Getenv("PETRI_VERBOSE") == "1" {
		w = io.MultiWriter(buf, os.Stderr)
	}

	flags := flag.NewFlagSet("petri-klog", flag.ContinueOnError)
	klog.InitFlags(flags)

	for name, value := range map[string]string{
		"logtostderr":     "false",
		"alsologtostderr": "false",
		"stderrthreshold": "FATAL",
	} {
		_ = flags.Set(name, value) // these flags always exist and accept these values
	}

	klog.SetOutput(w)
	logrus.SetOutput(w)

	return buf
}
