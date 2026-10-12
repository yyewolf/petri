/*
Copyright 2019 The Kubernetes Authors.

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

package plugins

import (
	"k8s.io/apiserver/pkg/util/feature"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/defaultpreemption"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/deferredpodscheduling"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/dynamicresources"
	plfeature "github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/feature"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/gangscheduling"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/imagelocality"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/interpodaffinity"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/nodeaffinity"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/nodedeclaredfeatures"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/nodename"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/nodeports"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/noderesources"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/nodeunschedulable"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/nodevolumelimits"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/podgrouppodscount"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/podtopologyspread"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/schedulinggates"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/tainttoleration"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/topologyaware"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/volumebinding"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/volumerestrictions"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/plugins/volumezone"
	"github.com/yyewolf/petri/internal/kubernetes/pkg/scheduler/framework/runtime"
)

// NewInTreeRegistry builds the registry with all the in-tree plugins.
// A scheduler that runs out of tree plugins can register additional plugins
// through the WithFrameworkOutOfTreeRegistry option.
func NewInTreeRegistry() runtime.Registry {
	fts := plfeature.NewSchedulerFeaturesFromGates(feature.DefaultFeatureGate)
	registry := runtime.Registry{
		dynamicresources.Name:                runtime.FactoryAdapter(fts, dynamicresources.New),
		imagelocality.Name:                   imagelocality.New,
		tainttoleration.Name:                 runtime.FactoryAdapter(fts, tainttoleration.New),
		nodename.Name:                        runtime.FactoryAdapter(fts, nodename.New),
		nodeports.Name:                       runtime.FactoryAdapter(fts, nodeports.New),
		nodeaffinity.Name:                    runtime.FactoryAdapter(fts, nodeaffinity.New),
		nodedeclaredfeatures.Name:            runtime.FactoryAdapter(fts, nodedeclaredfeatures.New),
		podtopologyspread.Name:               runtime.FactoryAdapter(fts, podtopologyspread.New),
		deferredpodscheduling.Name:           runtime.FactoryAdapter(fts, deferredpodscheduling.New),
		nodeunschedulable.Name:               runtime.FactoryAdapter(fts, nodeunschedulable.New),
		noderesources.Name:                   runtime.FactoryAdapter(fts, noderesources.NewFit),
		noderesources.BalancedAllocationName: runtime.FactoryAdapter(fts, noderesources.NewBalancedAllocation),
		volumebinding.Name:                   runtime.FactoryAdapter(fts, volumebinding.New),
		volumerestrictions.Name:              runtime.FactoryAdapter(fts, volumerestrictions.New),
		volumezone.Name:                      runtime.FactoryAdapter(fts, volumezone.New),
		nodevolumelimits.CSIName:             runtime.FactoryAdapter(fts, nodevolumelimits.NewCSI),
		interpodaffinity.Name:                runtime.FactoryAdapter(fts, interpodaffinity.New),
		queuesort.Name:                       queuesort.New,
		defaultbinder.Name:                   defaultbinder.New,
		defaultpreemption.Name:               runtime.FactoryAdapter(fts, defaultpreemption.New),
		schedulinggates.Name:                 runtime.FactoryAdapter(fts, schedulinggates.New),
		gangscheduling.Name:                  runtime.FactoryAdapter(fts, gangscheduling.New),
		topologyaware.Name:                   runtime.FactoryAdapter(fts, topologyaware.New),
		podgrouppodscount.Name:               runtime.FactoryAdapter(fts, podgrouppodscount.New),
	}

	return registry
}
