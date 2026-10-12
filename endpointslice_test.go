package petri_test

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yyewolf/petri"
)

// TestEndpointSliceControllerCreatesSlicesForSelectorServices checks the
// service-discovery story: a Service with a selector gets an EndpointSlice
// owned by it, managed by the endpointslice controller, and carrying no
// endpoints — no Pod is ever Ready on a nodeless plane.
func TestEndpointSliceControllerCreatesSlicesForSelectorServices(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)

	cp, err := petri.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = cp.Stop() })

	cp.EnableControllers(t, petri.EndpointSliceController)

	cs := clientFor(t, cp.Config())
	ns := newNamespace(ctx, t, cp)

	// EnableControllers returned after the informers synced, so the Service
	// created right away is seen by the controller.
	service, err := cs.CoreV1().Services(ns).Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "web"},
			Ports:    []corev1.ServicePort{{Name: "http", Port: 80}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}

	var slices *discoveryv1.EndpointSliceList

	petri.Retry(t, func(context.Context) (bool, error) {
		list, lerr := cs.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{
			LabelSelector: discoveryv1.LabelServiceName + "=web",
		})
		if lerr != nil {
			return false, lerr
		}

		slices = list

		return len(list.Items) == 1, nil
	})

	slice := slices.Items[0]

	// The slice belongs to the Service by label and by controller owner
	// reference, and names the upstream controller as its manager.
	if got := slice.Labels[discoveryv1.LabelServiceName]; got != "web" {
		t.Errorf("slice %s service-name label = %q, want web", slice.Name, got)
	}
	if got := slice.Labels[discoveryv1.LabelManagedBy]; got != "endpointslice-controller.k8s.io" {
		t.Errorf("slice %s managed-by label = %q, want the endpointslice controller", slice.Name, got)
	}

	ref := metav1.GetControllerOf(&slice)
	if ref == nil || ref.UID != service.UID {
		t.Errorf("slice %s controller ref = %v, want the service", slice.Name, ref)
	}

	if len(slice.Endpoints) != 0 {
		t.Errorf("slice %s has %d endpoints, want 0: no Pod is ever Ready here", slice.Name, len(slice.Endpoints))
	}
}
