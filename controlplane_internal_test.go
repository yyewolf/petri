package petri

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestWithStopErrorSkipsErrorsAlreadyReported(t *testing.T) {
	t.Parallel()

	serveErr := errors.New("listener closed")
	bootErr := fmt.Errorf("wait for readiness: kube-apiserver exited: %w", serveErr)
	other := errors.New("kine stuck")

	tests := []struct {
		name    string
		stopErr error
		want    string
	}{
		{name: "nil", stopErr: nil, want: bootErr.Error()},
		{name: "same as serve error", stopErr: serveErr, want: bootErr.Error()},
		{name: "new error", stopErr: other, want: bootErr.Error() + "\n" + other.Error()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := withStopError(bootErr, tt.stopErr)
			if got.Error() != tt.want {
				t.Errorf("withStopError() = %q, want %q", got, tt.want)
			}

			if n := strings.Count(got.Error(), serveErr.Error()); n != 1 {
				t.Errorf("serve error appears %d times in %q, want 1", n, got)
			}
		})
	}
}
