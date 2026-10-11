package controllerruntime_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/yyewolf/petri"
	"github.com/yyewolf/petri/controllerruntime"
)

func TestWaitUntilWatchesTypedObjects(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	c := clientForTest(t, env)
	ns := env.Namespace(t)

	target := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "later", Namespace: ns}}

	type result struct {
		cm  *corev1.ConfigMap
		err error
	}
	res := make(chan result, 1)
	go func() {
		cm, err := controllerruntime.WaitUntil(t.Context(), c, target,
			func(cm *corev1.ConfigMap) (bool, error) { return cm != nil, nil })
		res <- result{cm, err}
	}()

	time.Sleep(200 * time.Millisecond) // the wait must be watching before the create
	createConfigMap(t, c, ns, "later")

	select {
	case got := <-res:
		if got.err != nil {
			t.Fatalf("WaitUntil: %v", got.err)
		}
		if got.cm == nil || got.cm.Name != "later" {
			t.Errorf("WaitUntil returned %+v, want the created configmap", got.cm)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("WaitUntil did not return after the object was created")
	}
}

func TestWaitUntilDeleted(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	c := clientForTest(t, env)
	ns := env.Namespace(t)

	target := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "doomed", Namespace: ns}}
	createConfigMap(t, c, ns, "doomed")

	type result struct {
		cm  *corev1.ConfigMap
		err error
	}
	res := make(chan result, 1)
	go func() {
		cm, err := controllerruntime.WaitUntil(t.Context(), c, target,
			func(cm *corev1.ConfigMap) (bool, error) { return cm == nil, nil })
		res <- result{cm, err}
	}()

	time.Sleep(200 * time.Millisecond)
	if err := c.Delete(t.Context(), target); err != nil {
		t.Fatalf("delete: %v", err)
	}

	select {
	case got := <-res:
		if got.err != nil {
			t.Fatalf("WaitUntil: %v", got.err)
		}
		if got.cm != nil {
			t.Errorf("deleted wait returned %+v, want nil", got.cm)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("WaitUntil did not return after the object was deleted")
	}
}

func TestWaitForReturnsObject(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	c := clientForTest(t, env)
	ns := env.Namespace(t)

	target := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "here", Namespace: ns}}
	createConfigMap(t, c, ns, "here")

	cm := controllerruntime.WaitFor(t, c, target,
		func(cm *corev1.ConfigMap) (bool, error) { return cm != nil, nil })
	if cm == nil || cm.Name != "here" {
		t.Errorf("WaitFor returned %+v, want the configmap", cm)
	}
}

func TestWaitUntilUnstructuredObjects(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	env.InstallCRDs(t, "../testdata/crds")
	ns := env.Namespace(t)

	// Unstructured objects carry their own apiVersion and kind, so the default
	// scheme works for them.
	c, err := client.New(env.Config(), client.Options{})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	widget := &unstructured.Unstructured{}
	widget.SetAPIVersion("example.com/v1")
	widget.SetKind("Widget")
	widget.SetNamespace(ns)
	widget.SetName("w1")

	type result struct {
		obj *unstructured.Unstructured
		err error
	}
	res := make(chan result, 1)
	go func() {
		obj, err := controllerruntime.WaitUntil(t.Context(), c, widget,
			func(o *unstructured.Unstructured) (bool, error) { return o != nil, nil })
		res <- result{obj, err}
	}()

	time.Sleep(200 * time.Millisecond)
	// The client fills in the object's apiVersion and kind, so create a copy:
	// the wait engine reads the template concurrently.
	if err := c.Create(t.Context(), widget.DeepCopy()); err != nil {
		t.Fatalf("create widget: %v", err)
	}

	select {
	case got := <-res:
		if got.err != nil {
			t.Fatalf("WaitUntil: %v", got.err)
		}
		if got.obj == nil || got.obj.GetName() != "w1" {
			t.Errorf("WaitUntil returned %+v, want the widget", got.obj)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("WaitUntil did not return after the object was created")
	}
}

func TestWaitUntilOptionsAndErrors(t *testing.T) {
	t.Parallel()

	env := petri.Start(t)
	c := clientForTest(t, env)
	ns := env.Namespace(t)

	target := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "absent", Namespace: ns}}

	// Two seconds is enough for a cold controller-runtime client to run
	// discovery and build its REST mapper, and still well under the default.
	_, err := controllerruntime.WaitUntil(t.Context(), c, target,
		func(*corev1.ConfigMap) (bool, error) { return false, nil },
		petri.WithTimeout(2*time.Second),
	)
	if err == nil || !strings.Contains(err.Error(), "never observed") {
		t.Fatalf("timeout error = %v, want a never-observed timeout", err)
	}

	boom := errors.New("boom")
	_, err = controllerruntime.WaitUntil(t.Context(), c, target,
		func(*corev1.ConfigMap) (bool, error) { return false, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("condition error = %v, want boom wrapped", err)
	}
}
