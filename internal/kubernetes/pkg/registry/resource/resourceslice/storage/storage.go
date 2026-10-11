/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package storage

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/generic"
	genericregistry "k8s.io/apiserver/pkg/registry/generic/registry"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/apis/resource"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/printers"
	printersinternal "github.com/yyewolf/petri/internal/kubernetes/pkg/printers/internalversion"
	printerstorage "github.com/yyewolf/petri/internal/kubernetes/pkg/printers/storage"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/registry/resource/resourceslice"
)

// REST implements a RESTStorage for ResourceSlice.
type REST struct {
	*genericregistry.Store
}

// NewREST returns a RESTStorage object that will work against ResourceSlice.
func NewREST(optsGetter generic.RESTOptionsGetter) (*REST, error) {
	store := &genericregistry.Store{
		NewFunc:                   func() runtime.Object { return &resource.ResourceSlice{} },
		NewListFunc:               func() runtime.Object { return &resource.ResourceSliceList{} },
		PredicateFunc:             resourceslice.Match,
		DefaultQualifiedResource:  resource.Resource("resourceslices"),
		SingularQualifiedResource: resource.Resource("resourceslice"),

		CreateStrategy:      resourceslice.Strategy,
		UpdateStrategy:      resourceslice.Strategy,
		DeleteStrategy:      resourceslice.Strategy,
		ReturnDeletedObject: true,

		TableConvertor: printerstorage.TableConvertor{TableGenerator: printers.NewTableGenerator().With(printersinternal.AddHandlers)},
	}
	options := &generic.StoreOptions{
		RESTOptions: optsGetter,
		AttrFunc:    resourceslice.GetAttrs,
		TriggerFunc: resourceslice.TriggerFunc,
		Indexers:    resourceslice.Indexers(),
	}
	if err := store.CompleteWithOptions(options); err != nil {
		return nil, err
	}

	return &REST{store}, nil
}
