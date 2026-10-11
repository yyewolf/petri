package controllerruntime

import (
	"context"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/yyewolf/petri"
)

// WaitUntil waits until cond holds for obj, watched through c, and returns the
// last observed object (nil if it never existed). It follows the object with a
// list-then-watch, so changes made just before the wait are not missed, and it
// reconnects after watch expiry.
//
// The client must support watches, as client.New's clients do; the manager's
// cached client does not. The condition receives nil only for an object that
// is not there.
func WaitUntil[O client.Object](
	ctx context.Context, c client.Client, obj O, cond func(O) (bool, error), opts ...petri.WaitOption,
) (O, error) {
	name := obj.GetName()
	if name == "" {
		var zero O

		return zero, fmt.Errorf("petri controllerruntime: object %T has no name to wait for", obj)
	}

	wrapped := func(o client.Object) (bool, error) {
		if o == nil {
			var zero O

			return cond(zero)
		}

		typed, ok := o.(O)
		if !ok {
			return false, fmt.Errorf("petri controllerruntime: watch produced %T, want %T", o, obj)
		}

		return cond(typed)
	}

	source := objectClient[O]{c: c, template: obj, scheme: c.Scheme()}

	last, err := petri.WaitUntil[client.Object](ctx, source, name, wrapped, opts...)
	if err != nil {
		var zero O

		return zero, err
	}

	typed, ok := last.(O)
	if !ok {
		var zero O

		return zero, nil
	}

	return typed, nil
}

// WaitFor is WaitUntil for tests: it fails the test when the condition never
// holds or errors, and returns the last observed object.
func WaitFor[O client.Object](
	t testing.TB, c client.Client, obj O, cond func(O) (bool, error), opts ...petri.WaitOption,
) O {
	t.Helper()

	last, err := WaitUntil(t.Context(), c, obj, cond, opts...)
	if err != nil {
		t.Fatal(err)
	}

	return last
}

// objectClient adapts a controller-runtime client to the core wait engine,
// which wants Get-by-name and a raw metav1.ListOptions watch.
type objectClient[O client.Object] struct {
	c        client.Client
	scheme   *runtime.Scheme
	template O
}

func (s objectClient[O]) Get(ctx context.Context, name string, _ metav1.GetOptions) (client.Object, error) {
	obj, ok := s.template.DeepCopyObject().(client.Object)
	if !ok {
		return nil, fmt.Errorf("petri controllerruntime: copy of %T is not a client.Object", s.template)
	}

	key := client.ObjectKey{Name: name}
	if ns := s.template.GetNamespace(); ns != "" {
		key.Namespace = ns
	}

	if err := s.c.Get(ctx, key, obj); err != nil {
		return nil, err
	}

	return obj, nil
}

func (s objectClient[O]) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	w, ok := s.c.(client.WithWatch)
	if !ok {
		return nil, fmt.Errorf("petri controllerruntime: client %T does not support watches", s.c)
	}

	list, err := s.newList()
	if err != nil {
		return nil, err
	}

	listOptions := client.ListOptions{Raw: &opts}
	if ns := s.template.GetNamespace(); ns != "" {
		listOptions.Namespace = ns
	}

	return w.Watch(ctx, list, &listOptions)
}

// newList builds the list object of the template's kind, which is what a
// controller-runtime watch reads events into.
func (s objectClient[O]) newList() (client.ObjectList, error) {
	if u, ok := any(s.template).(*unstructured.Unstructured); ok {
		gvk := u.GroupVersionKind()
		if gvk.Empty() {
			return nil, fmt.Errorf("petri controllerruntime: object %T has no apiVersion or kind", s.template)
		}

		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))

		return list, nil
	}

	kinds, _, err := s.scheme.ObjectKinds(s.template)
	if err != nil {
		return nil, fmt.Errorf("petri controllerruntime: look up list kind of %T: %w", s.template, err)
	}

	list, err := s.scheme.New(kinds[0].GroupVersion().WithKind(kinds[0].Kind + "List"))
	if err != nil {
		return nil, fmt.Errorf("petri controllerruntime: build list of %T: %w", s.template, err)
	}

	typed, ok := list.(client.ObjectList)
	if !ok {
		return nil, fmt.Errorf("petri controllerruntime: list of %T is not a client.ObjectList", s.template)
	}

	return typed, nil
}
