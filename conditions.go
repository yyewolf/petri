package petri

import (
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// Condition reports whether obj satisfies a wait. It must be a pure predicate:
// the wait may evaluate it on the same object more than once. It receives nil
// only for an object that is not there.
type Condition[O runtime.Object] func(O) (bool, error)

// Exists holds once the object is there.
func Exists[O runtime.Object]() Condition[O] {
	return func(obj O) (bool, error) {
		return !isNil(obj), nil
	}
}

// Deleted holds once the object is gone, including when it never existed.
func Deleted[O runtime.Object]() Condition[O] {
	return func(obj O) (bool, error) {
		return isNil(obj), nil
	}
}

// StatusCondition holds once the object's status.conditions contain condType
// with the given status. It reads any condition shape with string Type and
// Status fields, from metav1.Condition to CustomResourceDefinitionCondition,
// and reads *unstructured.Unstructured objects directly, so it works for
// custom resources fetched through DynamicResource.
//
// Objects whose conditions live elsewhere need a condition written by hand.
func StatusCondition[O runtime.Object](condType string, status metav1.ConditionStatus) Condition[O] {
	return func(obj O) (bool, error) {
		if isNil(obj) {
			return false, nil
		}

		if u, ok := any(obj).(*unstructured.Unstructured); ok {
			return unstructuredConditionStatus(u, condType, string(status)), nil
		}

		return reflectedConditionStatus(obj, condType, string(status)), nil
	}
}

// unstructuredConditionStatus reads status.conditions from an unstructured
// object.
func unstructuredConditionStatus(obj *unstructured.Unstructured, condType, status string) bool {
	conditions, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !found {
		return false
	}

	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}

		kind, _ := condition["type"].(string)
		if kind == condType {
			got, _ := condition["status"].(string)

			return got == status
		}
	}

	return false
}

// reflectedConditionStatus reads status.conditions from a typed object.
// Condition types differ per resource, so it looks for the two string fields
// every condition has instead of naming one type.
func reflectedConditionStatus(obj any, condType, status string) bool {
	object := reflect.Indirect(reflect.ValueOf(obj))
	if object.Kind() != reflect.Struct {
		return false
	}

	spec := reflect.Indirect(object.FieldByName("Status"))
	if spec.Kind() != reflect.Struct {
		return false
	}

	conditions := spec.FieldByName("Conditions")
	if conditions.Kind() != reflect.Slice {
		return false
	}

	for i := range conditions.Len() {
		condition := reflect.Indirect(conditions.Index(i))
		if condition.Kind() != reflect.Struct {
			continue
		}

		kind := condition.FieldByName("Type")
		got := condition.FieldByName("Status")
		if kind.Kind() != reflect.String || got.Kind() != reflect.String {
			continue
		}

		if kind.String() == condType {
			return got.String() == status
		}
	}

	return false
}
