package parallel_test

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yyewolf/petri"
)

func TestConfigMapNames(t *testing.T) {
	t.Parallel()

	cp := petri.Start(t)

	for _, tt := range []struct {
		name  string
		valid bool
	}{
		{name: "settings", valid: true},
		{name: "Settings", valid: false},
		{name: "settings.v2", valid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Each subtest takes its own deadline. A parallel subtest only starts
			// once its parent returns and a -parallel slot frees up, so a deadline
			// set in the parent may already be spent by then.
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()

			// Each subtest has its own namespace too.
			configMaps := cp.Client().CoreV1().ConfigMaps(cp.Namespace(t))
			_, err := configMaps.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: tt.name},
			}, metav1.CreateOptions{})

			if tt.valid && err != nil {
				t.Fatalf("valid name refused: %v", err)
			}
			if !tt.valid && !apierrors.IsInvalid(err) {
				t.Fatalf("invalid name: got %v, want Invalid", err)
			}
		})
	}
}
