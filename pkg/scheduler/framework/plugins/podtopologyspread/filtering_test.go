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

package podtopologyspread

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/feature"
	plugintesting "k8s.io/kubernetes/pkg/scheduler/framework/plugins/testing"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	"k8s.io/kubernetes/pkg/scheduler/internal/cache"
	st "k8s.io/kubernetes/pkg/scheduler/testing"
	"k8s.io/utils/pointer"
)

var cmpOpts = []cmp.Option{
	cmp.Comparer(func(s1 labels.Selector, s2 labels.Selector) bool {
		return reflect.DeepEqual(s1, s2)
	}),
	cmp.Comparer(func(p1, p2 criticalPaths) bool {
		p1.sort()
		p2.sort()
		return p1[0] == p2[0] && p1[1] == p2[1]
	}),
}

var (
	topologySpreadFunc = frameworkruntime.FactoryAdapter(feature.Features{}, New)
	ignorePolicy       = v1.NodeInclusionPolicyIgnore
	honorPolicy        = v1.NodeInclusionPolicyHonor
	fooSelector        = st.MakeLabelSelector().Exists("foo").Obj()
	barSelector        = st.MakeLabelSelector().Exists("bar").Obj()

	taints = []v1.Taint{{Key: v1.TaintNodeUnschedulable, Value: "", Effect: v1.TaintEffectPreferNoSchedule}}
)

func (p *criticalPaths) sort() {
	if p[0].MatchNum == p[1].MatchNum && p[0].TopologyValue > p[1].TopologyValue {
		// Swap TopologyValue to make them sorted alphabetically.
		p[0].TopologyValue, p[1].TopologyValue = p[1].TopologyValue, p[0].TopologyValue
	}
}

func TestPreFilterState(t *testing.T) {

	tests := []struct {
		name                       string
		pod                        *v1.Pod
		nodes                      []*v1.Node
		existingPods               []*v1.Pod
		objs                       []runtime.Object
		defaultConstraints         []v1.TopologySpreadConstraint
		want                       *preFilterState
		enableMinDomains           bool
		enableNodeInclustionPolicy bool
		enableMatchLabelKeys       bool
	}{
		{
			name: "clean cluster with one spreadConstraint",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(5, "zone", v1.DoNotSchedule, st.MakeLabelSelector().Label("foo", "bar").Obj(), nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            5,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, st.MakeLabelSelector().Label("foo", "bar").Obj()),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone1", 0}, {"zone2", 0}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}: 0,
					{key: "zone", value: "zone2"}: 0,
				},
			},
		},
		{
			name: "normal case with one spreadConstraint",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone2", 2}, {"zone1", 3}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}: 3,
					{key: "zone", value: "zone2"}: 2,
				},
			},
		},
		{
			name: "normal case with one spreadConstraint, on a 3-zone cluster",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
				st.MakeNode().Name("node-o").Label("zone", "zone3").Label("node", "node-o").Obj(),
				st.MakeNode().Name("node-p").Label("zone", "zone3").Label("node", "node-p").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone3", 0}, {"zone2", 2}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}: 3,
					{key: "zone", value: "zone2"}: 2,
					{key: "zone", value: "zone3"}: 0,
				},
			},
		},
		{
			name: "namespace mismatch doesn't count",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Namespace("ns1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Namespace("ns2").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone2", 1}, {"zone1", 2}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}: 2,
					{key: "zone", value: "zone2"}: 1,
				},
			},
		},
		{
			name: "normal case with two spreadConstraints",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y3").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y4").Node("node-y").Label("foo", "").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone1", 3}, {"zone2", 4}},
					"node": {{"node-x", 0}, {"node-b", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}:  3,
					{key: "zone", value: "zone2"}:  4,
					{key: "node", value: "node-a"}: 2,
					{key: "node", value: "node-b"}: 1,
					{key: "node", value: "node-x"}: 0,
					{key: "node", value: "node-y"}: 4,
				},
			},
		},
		{
			name: "soft spreadConstraints should be bypassed",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.ScheduleAnyway, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.ScheduleAnyway, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y3").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y4").Node("node-y").Label("foo", "").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone1", 3}, {"zone2", 4}},
					"node": {{"node-b", 1}, {"node-a", 2}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}:  3,
					{key: "zone", value: "zone2"}:  4,
					{key: "node", value: "node-a"}: 2,
					{key: "node", value: "node-b"}: 1,
					{key: "node", value: "node-y"}: 4,
				},
			},
		},
		{
			name: "different labelSelectors - simple version",
			pod: st.MakePod().Name("p").Label("foo", "").Label("bar", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, barSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b").Node("node-b").Label("bar", "").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, barSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone2", 0}, {"zone1", 1}},
					"node": {{"node-a", 0}, {"node-y", 0}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}:  1,
					{key: "zone", value: "zone2"}:  0,
					{key: "node", value: "node-a"}: 0,
					{key: "node", value: "node-b"}: 1,
					{key: "node", value: "node-y"}: 0,
				},
			},
		},
		{
			name: "different labelSelectors - complex pods",
			pod: st.MakePod().Name("p").Label("foo", "").Label("bar", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, barSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Label("bar", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "").Label("bar", "").Obj(),
				st.MakePod().Name("p-y3").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y4").Node("node-y").Label("foo", "").Label("bar", "").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, barSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone1", 3}, {"zone2", 4}},
					"node": {{"node-b", 0}, {"node-a", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}:  3,
					{key: "zone", value: "zone2"}:  4,
					{key: "node", value: "node-a"}: 1,
					{key: "node", value: "node-b"}: 0,
					{key: "node", value: "node-y"}: 2,
				},
			},
		},
		{
			name: "two spreadConstraints, and with podAffinity",
			pod: st.MakePod().Name("p").Label("foo", "").
				NodeAffinityNotIn("node", []string{"node-x"}). // exclude node-x
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y3").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y4").Node("node-y").Label("foo", "").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone1", 3}, {"zone2", 4}},
					"node": {{"node-b", 1}, {"node-a", 2}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}:  3,
					{key: "zone", value: "zone2"}:  4,
					{key: "node", value: "node-a"}: 2,
					{key: "node", value: "node-b"}: 1,
					{key: "node", value: "node-y"}: 4,
				},
			},
		},
		{
			name: "default constraints and a service",
			pod:  st.MakePod().Name("p").Label("foo", "bar").Label("baz", "kar").Obj(),
			defaultConstraints: []v1.TopologySpreadConstraint{
				{MaxSkew: 3, TopologyKey: "node", WhenUnsatisfiable: v1.DoNotSchedule},
				{MaxSkew: 2, TopologyKey: "node", WhenUnsatisfiable: v1.ScheduleAnyway},
				{MaxSkew: 5, TopologyKey: "rack", WhenUnsatisfiable: v1.DoNotSchedule},
			},
			objs: []runtime.Object{
				&v1.Service{Spec: v1.ServiceSpec{Selector: map[string]string{"foo": "bar"}}},
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            3,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, st.MakeLabelSelector().Label("foo", "bar").Obj()),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
					{
						MaxSkew:            5,
						TopologyKey:        "rack",
						Selector:           mustConvertLabelSelectorAsSelector(t, st.MakeLabelSelector().Label("foo", "bar").Obj()),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"node": newCriticalPaths(),
					"rack": newCriticalPaths(),
				},
				TpPairToMatchNum: make(map[topologyPair]int),
			},
		},
		{
			name: "default constraints and a service that doesn't match",
			pod:  st.MakePod().Name("p").Label("foo", "bar").Obj(),
			defaultConstraints: []v1.TopologySpreadConstraint{
				{MaxSkew: 3, TopologyKey: "node", WhenUnsatisfiable: v1.DoNotSchedule},
			},
			objs: []runtime.Object{
				&v1.Service{Spec: v1.ServiceSpec{Selector: map[string]string{"baz": "kep"}}},
			},
			want: &preFilterState{},
		},
		{
			name: "default constraints and a service, but pod has constraints",
			pod: st.MakePod().Name("p").Label("foo", "bar").Label("baz", "tar").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, st.MakeLabelSelector().Label("baz", "tar").Obj(), nil, nil, nil, nil).
				SpreadConstraint(2, "planet", v1.ScheduleAnyway, st.MakeLabelSelector().Label("fot", "rok").Obj(), nil, nil, nil, nil).
				Obj(),
			defaultConstraints: []v1.TopologySpreadConstraint{
				{MaxSkew: 2, TopologyKey: "node", WhenUnsatisfiable: v1.DoNotSchedule},
			},
			objs: []runtime.Object{
				&v1.Service{Spec: v1.ServiceSpec{Selector: map[string]string{"foo": "bar"}}},
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, st.MakeLabelSelector().Label("baz", "tar").Obj()),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": newCriticalPaths(),
				},
				TpPairToMatchNum: make(map[topologyPair]int),
			},
		},
		{
			name: "default soft constraints and a service",
			pod:  st.MakePod().Name("p").Label("foo", "bar").Obj(),
			defaultConstraints: []v1.TopologySpreadConstraint{
				{MaxSkew: 2, TopologyKey: "node", WhenUnsatisfiable: v1.ScheduleAnyway},
			},
			objs: []runtime.Object{
				&v1.Service{Spec: v1.ServiceSpec{Selector: map[string]string{"foo": "bar"}}},
			},
			want: &preFilterState{},
		},
		{
			name: "TpKeyToDomainsNum is calculated when MinDomains is enabled",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y3").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y4").Node("node-y").Label("foo", "").Obj(),
			},
			enableMinDomains: true,
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone1", 3}, {"zone2", 4}},
					"node": {{"node-x", 0}, {"node-b", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}:  3,
					{key: "zone", value: "zone2"}:  4,
					{key: "node", value: "node-a"}: 2,
					{key: "node", value: "node-b"}: 1,
					{key: "node", value: "node-x"}: 0,
					{key: "node", value: "node-y"}: 4,
				},
				TpKeyToDomainsNum: map[string]int{
					"zone": 2,
					"node": 4,
				},
			},
		},
		{
			name: "feature gate disabled with NodeAffinityPolicy",
			pod: st.MakePod().Name("p").Label("foo", "").
				NodeSelector(map[string]string{"foo": ""}).
				SpreadConstraint(1, "node", v1.DoNotSchedule, barSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("node", "node-a").Label("foo", "").Obj(),
				st.MakeNode().Name("node-b").Label("node", "node-b").Label("foo", "").Obj(),
				st.MakeNode().Name("node-c").Label("node", "node-c").Label("bar", "").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a").Node("node-a").Label("bar", "").Obj(),
				st.MakePod().Name("p-b").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-c").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-d").Node("node-c").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, barSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"node": {{"node-a", 1}, {"node-b", 2}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "node", value: "node-a"}: 1,
					{key: "node", value: "node-b"}: 2,
				},
			},
			enableNodeInclustionPolicy: false,
		},
		{
			name: "NodeAffinityPolicy honored with labelSelectors",
			pod: st.MakePod().Name("p").Label("foo", "").
				NodeSelector(map[string]string{"foo": ""}).
				SpreadConstraint(1, "node", v1.DoNotSchedule, barSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("node", "node-a").Label("foo", "").Obj(),
				st.MakeNode().Name("node-b").Label("node", "node-b").Label("foo", "").Obj(),
				st.MakeNode().Name("node-c").Label("node", "node-c").Label("bar", "").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a").Node("node-a").Label("bar", "").Obj(),
				st.MakePod().Name("p-b").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-c").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-d").Node("node-c").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, barSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"node": {{"node-a", 1}, {"node-b", 2}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "node", value: "node-a"}: 1,
					{key: "node", value: "node-b"}: 2,
				},
			},
			enableNodeInclustionPolicy: true,
		},
		{
			name: "NodeAffinityPolicy ignored with labelSelectors",
			pod: st.MakePod().Name("p").Label("foo", "").
				NodeSelector(map[string]string{"foo": ""}).
				SpreadConstraint(1, "node", v1.DoNotSchedule, barSelector, nil, &ignorePolicy, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("node", "node-a").Label("foo", "").Obj(),
				st.MakeNode().Name("node-b").Label("node", "node-b").Label("foo", "").Obj(),
				st.MakeNode().Name("node-c").Label("node", "node-c").Label("bar", "").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a").Node("node-a").Label("bar", "").Obj(),
				st.MakePod().Name("p-b").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-c").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-d").Node("node-c").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, barSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyIgnore,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"node": {{"node-c", 0}, {"node-a", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "node", value: "node-a"}: 1,
					{key: "node", value: "node-b"}: 2,
					{key: "node", value: "node-c"}: 0,
				},
			},
			enableNodeInclustionPolicy: true,
		},
		{
			name: "NodeAffinityPolicy honored with nodeAffinity",
			pod: st.MakePod().Name("p").Label("foo", "").
				NodeAffinityIn("foo", []string{""}).
				SpreadConstraint(1, "node", v1.DoNotSchedule, barSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("node", "node-a").Label("foo", "").Obj(),
				st.MakeNode().Name("node-b").Label("node", "node-b").Label("foo", "").Obj(),
				st.MakeNode().Name("node-c").Label("node", "node-c").Label("bar", "").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a").Node("node-a").Label("bar", "").Obj(),
				st.MakePod().Name("p-c").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-d").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-e").Node("node-c").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, barSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"node": {{"node-a", 1}, {"node-b", 2}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "node", value: "node-a"}: 1,
					{key: "node", value: "node-b"}: 2,
				},
			},
			enableNodeInclustionPolicy: true,
		},
		{
			name: "NodeAffinityPolicy ignored with nodeAffinity",
			pod: st.MakePod().Name("p").Label("foo", "").
				NodeAffinityIn("foo", []string{""}).
				SpreadConstraint(1, "node", v1.DoNotSchedule, barSelector, nil, &ignorePolicy, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("node", "node-a").Label("foo", "").Obj(),
				st.MakeNode().Name("node-b").Label("node", "node-b").Label("foo", "").Obj(),
				st.MakeNode().Name("node-c").Label("node", "node-c").Label("bar", "").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a").Node("node-a").Label("bar", "").Obj(),
				st.MakePod().Name("p-b").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-c").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-d").Node("node-c").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, barSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyIgnore,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"node": {{"node-c", 0}, {"node-a", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "node", value: "node-a"}: 1,
					{key: "node", value: "node-b"}: 2,
					{key: "node", value: "node-c"}: 0,
				},
			},
			enableNodeInclustionPolicy: true,
		},
		{
			name: "feature gate disabled with NodeTaintsPolicy",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "node", v1.DoNotSchedule, barSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-c").Label("node", "node-c").Taints(taints).Label("bar", "").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a").Node("node-a").Label("bar", "").Obj(),
				st.MakePod().Name("p-b").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-c").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-d").Node("node-c").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, barSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"node": {{"node-c", 0}, {"node-a", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "node", value: "node-a"}: 1,
					{key: "node", value: "node-b"}: 2,
					{key: "node", value: "node-c"}: 0,
				},
			},
			enableNodeInclustionPolicy: false,
		},
		{
			name: "NodeTaintsPolicy ignored",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "node", v1.DoNotSchedule, barSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-c").Label("node", "node-c").Taints(taints).Label("bar", "").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a").Node("node-a").Label("bar", "").Obj(),
				st.MakePod().Name("p-b").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-c").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-d").Node("node-c").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, barSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"node": {{"node-c", 0}, {"node-a", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "node", value: "node-a"}: 1,
					{key: "node", value: "node-b"}: 2,
					{key: "node", value: "node-c"}: 0,
				},
			},
			enableNodeInclustionPolicy: true,
		},
		{
			name: "NodeTaintsPolicy honored",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "node", v1.DoNotSchedule, barSelector, nil, nil, &honorPolicy, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-c").Label("node", "node-c").Taints(taints).Label("bar", "").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a").Node("node-a").Label("bar", "").Obj(),
				st.MakePod().Name("p-b").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-c").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-d").Node("node-c").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, barSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyHonor,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"node": {{"node-a", 1}, {"node-b", 2}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "node", value: "node-a"}: 1,
					{key: "node", value: "node-b"}: 2,
				},
			},
			enableNodeInclustionPolicy: true,
		},
		{
			name: "NodeTaintsPolicy honored with tolerated taints",
			pod: st.MakePod().Name("p").Label("foo", "").
				Toleration(v1.TaintNodeUnschedulable).
				SpreadConstraint(1, "node", v1.DoNotSchedule, barSelector, nil, nil, &honorPolicy, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-c").Label("node", "node-c").Taints(taints).Label("bar", "").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a").Node("node-a").Label("bar", "").Obj(),
				st.MakePod().Name("p-b").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-c").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-d").Node("node-c").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, barSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyHonor,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"node": {{"node-c", 0}, {"node-a", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "node", value: "node-a"}: 1,
					{key: "node", value: "node-b"}: 2,
					{key: "node", value: "node-c"}: 0,
				},
			},
			enableNodeInclustionPolicy: true,
		},
		{
			name: "two node inclusion Constraints, zone: honor/ignore, node: ignore/ignore",
			pod: st.MakePod().Name("p").Label("foo", "").
				NodeSelector(map[string]string{"foo": ""}).
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, &ignorePolicy, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Label("foo", "").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Label("foo", "").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-b2").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-x1").Node("node-x").Label("foo", "").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyIgnore,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone1", 0}, {"zone2", 1}},
					"node": {{"node-a", 0}, {"node-x", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}:  0,
					{key: "zone", value: "zone2"}:  1,
					{key: "node", value: "node-a"}: 0,
					{key: "node", value: "node-b"}: 2,
					{key: "node", value: "node-x"}: 1,
				},
			},
			enableNodeInclustionPolicy: true,
		},
		{
			name: "two node inclusion Constraints, zone: honor/honor, node: honor/ignore",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, &honorPolicy, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Label("foo", "").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Taints(taints).Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Label("foo", "").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-b2").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-x1").Node("node-x").Label("foo", "").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyHonor,
					},
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone1", 0}, {"zone2", 1}},
					"node": {{"node-a", 0}, {"node-x", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}:  0,
					{key: "zone", value: "zone2"}:  1,
					{key: "node", value: "node-a"}: 0,
					{key: "node", value: "node-b"}: 2,
					{key: "node", value: "node-x"}: 1,
				},
			},
			enableNodeInclustionPolicy: true,
		},
		{
			name: "two node inclusion Constraints, zone: honor/ignore, node: honor/ignore",
			pod: st.MakePod().Name("p").Label("foo", "").
				NodeSelector(map[string]string{"foo": ""}).
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Label("foo", "").Taints(taints).Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Label("foo", "").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-x1").Node("node-x").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone1", 0}, {"zone2", 1}},
					"node": {{"node-b", 0}, {"node-x", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}:  0,
					{key: "zone", value: "zone2"}:  1,
					{key: "node", value: "node-b"}: 0,
					{key: "node", value: "node-x"}: 1,
				},
			},
			enableNodeInclustionPolicy: true,
		},
		{
			name: "two node inclusion Constraints, zone: ignore/ignore, node: honor/honor",
			pod: st.MakePod().Name("p").Label("foo", "").
				NodeSelector(map[string]string{"foo": ""}).
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, &ignorePolicy, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, &honorPolicy, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Label("foo", "").Taints(taints).Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Label("foo", "").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Label("foo", "").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-x1").Node("node-x").Label("foo", "").Obj(),
				st.MakePod().Name("p-x2").Node("node-x").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyIgnore,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyHonor,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone1", 2}, {"zone2", 3}},
					"node": {{"node-y", 1}, {"node-x", 2}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}:  2,
					{key: "zone", value: "zone2"}:  3,
					{key: "node", value: "node-x"}: 2,
					{key: "node", value: "node-y"}: 1,
				},
			},
			enableNodeInclustionPolicy: true,
		},
		{
			name: "matchLabelKeys ignored when feature gate disabled",
			pod: st.MakePod().Name("p").Label("foo", "").Label("bar", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, []string{"bar"}).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone2", 2}, {"zone1", 3}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}: 3,
					{key: "zone", value: "zone2"}: 2,
				},
			},
			enableMatchLabelKeys: false,
		},
		{
			name: "matchLabelKeys ANDed with LabelSelector when LabelSelector isn't empty",
			pod: st.MakePod().Name("p").Label("foo", "").Label("bar", "a").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, []string{"bar"}).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Label("bar", "a").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Label("bar", "a").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("bar", "").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, st.MakeLabelSelector().Exists("foo").Label("bar", "a").Obj()),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone2", 1}, {"zone1", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}: 1,
					{key: "zone", value: "zone2"}: 1,
				},
			},
			enableMatchLabelKeys: true,
		},
		{
			name: "matchLabelKeys ANDed with LabelSelector when LabelSelector is empty",
			pod: st.MakePod().Name("p").Label("foo", "a").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, st.MakeLabelSelector().Obj(), nil, nil, nil, []string{"foo"}).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "a").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "a").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "a").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "a").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "a").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, st.MakeLabelSelector().Label("foo", "a").Obj()),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone2", 2}, {"zone1", 3}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}: 3,
					{key: "zone", value: "zone2"}: 2,
				},
			},
			enableMatchLabelKeys: true,
		},
		{
			name: "key in matchLabelKeys is ignored when it isn't exist in pod.labels",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, []string{"bar"}).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "a").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "a").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "a").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "a").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "a").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					{
						MaxSkew:            1,
						TopologyKey:        "zone",
						Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone2", 2}, {"zone1", 3}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}: 3,
					{key: "zone", value: "zone2"}: 2,
				},
			},
			enableMatchLabelKeys: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			args := &config.PodTopologySpreadArgs{
				DefaultConstraints: tt.defaultConstraints,
				DefaultingType:     config.ListDefaulting,
			}

			p := plugintesting.SetupPluginWithInformers(ctx, t, topologySpreadFunc, args, cache.NewSnapshot(tt.existingPods, tt.nodes), tt.objs)
			p.(*PodTopologySpread).enableMinDomainsInPodTopologySpread = tt.enableMinDomains
			p.(*PodTopologySpread).enableNodeInclusionPolicyInPodTopologySpread = tt.enableNodeInclustionPolicy
			p.(*PodTopologySpread).enableMatchLabelKeysInPodTopologySpread = tt.enableMatchLabelKeys

			cs := framework.NewCycleState()
			if _, s := p.(*PodTopologySpread).PreFilter(ctx, cs, tt.pod); !s.IsSuccess() {
				t.Fatal(s.AsError())
			}
			got, err := getPreFilterState(cs)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, got, cmpOpts...); diff != "" {
				t.Errorf("PodTopologySpread#PreFilter() returned diff (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPreFilterStateAddPod(t *testing.T) {
	nodeConstraint := topologySpreadConstraint{
		MaxSkew:            1,
		TopologyKey:        "node",
		Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
		MinDomains:         1,
		NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
		NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
	}
	zoneConstraint := nodeConstraint
	zoneConstraint.TopologyKey = "zone"
	tests := []struct {
		name                       string
		preemptor                  *v1.Pod
		addedPod                   *v1.Pod
		existingPods               []*v1.Pod
		nodeIdx                    int // denotes which node 'addedPod' belongs to
		nodes                      []*v1.Node
		want                       *preFilterState
		enableNodeInclustionPolicy bool
	}{
		{
			name: "node a and b both impact current min match",
			preemptor: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			addedPod:     st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
			existingPods: nil, // it's an empty cluster
			nodeIdx:      0,
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{nodeConstraint},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"node": {{"node-b", 0}, {"node-a", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "node", value: "node-a"}: 1,
					{key: "node", value: "node-b"}: 0,
				},
			},
		},
		{
			name: "only node a impacts current min match",
			preemptor: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			addedPod: st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
			},
			nodeIdx: 0,
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{nodeConstraint},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"node": {{"node-a", 1}, {"node-b", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "node", value: "node-a"}: 1,
					{key: "node", value: "node-b"}: 1,
				},
			},
		},
		{
			name: "add a pod in a different namespace doesn't change topologyKeyToMinPodsMap",
			preemptor: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			addedPod: st.MakePod().Name("p-a1").Namespace("ns1").Node("node-a").Label("foo", "").Obj(),
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
			},
			nodeIdx: 0,
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{nodeConstraint},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"node": {{"node-a", 0}, {"node-b", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "node", value: "node-a"}: 0,
					{key: "node", value: "node-b"}: 1,
				},
			},
		},
		{
			name: "add pod on non-critical node won't trigger re-calculation",
			preemptor: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			addedPod: st.MakePod().Name("p-b2").Node("node-b").Label("foo", "").Obj(),
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
			},
			nodeIdx: 1,
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{nodeConstraint},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"node": {{"node-a", 0}, {"node-b", 2}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "node", value: "node-a"}: 0,
					{key: "node", value: "node-b"}: 2,
				},
			},
		},
		{
			name: "node a and x both impact topologyKeyToMinPodsMap on zone and node",
			preemptor: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			addedPod:     st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
			existingPods: nil, // it's an empty cluster
			nodeIdx:      0,
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{zoneConstraint, nodeConstraint},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone2", 0}, {"zone1", 1}},
					"node": {{"node-x", 0}, {"node-a", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}:  1,
					{key: "zone", value: "zone2"}:  0,
					{key: "node", value: "node-a"}: 1,
					{key: "node", value: "node-x"}: 0,
				},
			},
		},
		{
			name: "only node a impacts topologyKeyToMinPodsMap on zone and node",
			preemptor: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			addedPod: st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-x1").Node("node-x").Label("foo", "").Obj(),
			},
			nodeIdx: 0,
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{zoneConstraint, nodeConstraint},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone1", 1}, {"zone2", 1}},
					"node": {{"node-a", 1}, {"node-x", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}:  1,
					{key: "zone", value: "zone2"}:  1,
					{key: "node", value: "node-a"}: 1,
					{key: "node", value: "node-x"}: 1,
				},
			},
		},
		{
			name: "node a impacts topologyKeyToMinPodsMap on node, node x impacts topologyKeyToMinPodsMap on zone",
			preemptor: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			addedPod: st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-b2").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-x1").Node("node-x").Label("foo", "").Obj(),
			},
			nodeIdx: 0,
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{zoneConstraint, nodeConstraint},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone2", 1}, {"zone1", 3}},
					"node": {{"node-a", 1}, {"node-x", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}:  3,
					{key: "zone", value: "zone2"}:  1,
					{key: "node", value: "node-a"}: 1,
					{key: "node", value: "node-b"}: 2,
					{key: "node", value: "node-x"}: 1,
				},
			},
		},
		{
			name: "Constraints hold different labelSelectors, node a impacts topologyKeyToMinPodsMap on zone",
			preemptor: st.MakePod().Name("p").Label("foo", "").Label("bar", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, barSelector, nil, nil, nil, nil).
				Obj(),
			addedPod: st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Label("bar", "").Obj(),
				st.MakePod().Name("p-x1").Node("node-x").Label("foo", "").Label("bar", "").Obj(),
				st.MakePod().Name("p-x2").Node("node-x").Label("bar", "").Obj(),
			},
			nodeIdx: 0,
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					zoneConstraint,
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, barSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone2", 1}, {"zone1", 2}},
					"node": {{"node-a", 0}, {"node-b", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}:  2,
					{key: "zone", value: "zone2"}:  1,
					{key: "node", value: "node-a"}: 0,
					{key: "node", value: "node-b"}: 1,
					{key: "node", value: "node-x"}: 2,
				},
			},
		},
		{
			name: "Constraints hold different labelSelectors, node a impacts topologyKeyToMinPodsMap on both zone and node",
			preemptor: st.MakePod().Name("p").Label("foo", "").Label("bar", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, barSelector, nil, nil, nil, nil).
				Obj(),
			addedPod: st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Label("bar", "").Obj(),
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-b").Label("bar", "").Obj(),
				st.MakePod().Name("p-x1").Node("node-x").Label("foo", "").Label("bar", "").Obj(),
				st.MakePod().Name("p-x2").Node("node-x").Label("bar", "").Obj(),
			},
			nodeIdx: 0,
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{
					zoneConstraint,
					{
						MaxSkew:            1,
						TopologyKey:        "node",
						Selector:           mustConvertLabelSelectorAsSelector(t, barSelector),
						MinDomains:         1,
						NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
						NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
					},
				},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone1", 1}, {"zone2", 1}},
					"node": {{"node-a", 1}, {"node-b", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}:  1,
					{key: "zone", value: "zone2"}:  1,
					{key: "node", value: "node-a"}: 1,
					{key: "node", value: "node-b"}: 1,
					{key: "node", value: "node-x"}: 2,
				},
			},
		},
		{
			name: "add a pod that doesn't match node affinity when NodeInclustionPolicy disabled",
			preemptor: st.MakePod().Name("p").Label("foo", "").NodeAffinityNotIn("foo", []string{"bar"}).
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodeIdx:  0,
			addedPod: st.MakePod().Name("p-a1").Node("node-b").Label("foo", "").Label("zone", "zone2").Obj(),
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-a").Label("foo", "").Label("zone", "zone1").Obj(),
				st.MakePod().Name("p-b2").Node("node-b").Label("foo", "").Label("zone", "zone2").Obj(),
			},
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Label("foo", "bar").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone2").Label("node", "node-b").Label("foo", "").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{zoneConstraint},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone2", 1}, {MatchNum: math.MaxInt32}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone2"}: 1,
				},
			},
			enableNodeInclustionPolicy: false,
		},
		{
			name: "add a pod that doesn't match node affinity when NodeInclustionPolicy enabled",
			preemptor: st.MakePod().Name("p").Label("foo", "").NodeAffinityNotIn("foo", []string{"bar"}).
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodeIdx:  0,
			addedPod: st.MakePod().Name("p-a1").Node("node-b").Label("foo", "").Label("zone", "zone2").Obj(),
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-a").Label("foo", "").Label("zone", "zone1").Obj(),
				st.MakePod().Name("p-b2").Node("node-b").Label("foo", "").Label("zone", "zone2").Obj(),
			},
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Label("foo", "bar").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone2").Label("node", "node-b").Label("foo", "").Obj(),
			},
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{zoneConstraint},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone2", 1}, {MatchNum: math.MaxInt32}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone2"}: 1,
				},
			},
			enableNodeInclustionPolicy: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			snapshot := cache.NewSnapshot(tt.existingPods, tt.nodes)
			pl := plugintesting.SetupPlugin(t, topologySpreadFunc, &config.PodTopologySpreadArgs{DefaultingType: config.ListDefaulting}, snapshot)
			p := pl.(*PodTopologySpread)
			p.enableNodeInclusionPolicyInPodTopologySpread = tt.enableNodeInclustionPolicy

			cs := framework.NewCycleState()
			if _, s := p.PreFilter(ctx, cs, tt.preemptor); !s.IsSuccess() {
				t.Fatal(s.AsError())
			}
			nodeInfo, err := snapshot.Get(tt.nodes[tt.nodeIdx].Name)
			if err != nil {
				t.Fatal(err)
			}
			if s := p.AddPod(ctx, cs, tt.preemptor, framework.NewPodInfo(tt.addedPod), nodeInfo); !s.IsSuccess() {
				t.Fatal(s.AsError())
			}
			state, err := getPreFilterState(cs)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(state, tt.want, cmpOpts...); diff != "" {
				t.Errorf("PodTopologySpread.AddPod() returned diff (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPreFilterStateRemovePod(t *testing.T) {
	nodeConstraint := topologySpreadConstraint{
		MaxSkew:            1,
		TopologyKey:        "node",
		Selector:           mustConvertLabelSelectorAsSelector(t, fooSelector),
		MinDomains:         1,
		NodeAffinityPolicy: v1.NodeInclusionPolicyHonor,
		NodeTaintsPolicy:   v1.NodeInclusionPolicyIgnore,
	}
	zoneConstraint := nodeConstraint
	zoneConstraint.TopologyKey = "zone"
	tests := []struct {
		name          string
		preemptor     *v1.Pod // preemptor pod
		nodes         []*v1.Node
		existingPods  []*v1.Pod
		deletedPodIdx int     // need to reuse *Pod of existingPods[i]
		deletedPod    *v1.Pod // this field is used only when deletedPodIdx is -1
		nodeIdx       int     // denotes which node "deletedPod" belongs to
		want          *preFilterState
	}{
		{
			// A high priority pod may not be scheduled due to node taints or resource shortage.
			// So preemption is triggered.
			name: "one spreadConstraint on zone, topologyKeyToMinPodsMap unchanged",
			preemptor: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-x1").Node("node-x").Label("foo", "").Obj(),
			},
			deletedPodIdx: 0, // remove pod "p-a1"
			nodeIdx:       0, // node-a
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{zoneConstraint},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone1", 1}, {"zone2", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}: 1,
					{key: "zone", value: "zone2"}: 1,
				},
			},
		},
		{
			name: "one spreadConstraint on node, topologyKeyToMinPodsMap changed",
			preemptor: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-x1").Node("node-x").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			deletedPodIdx: 0, // remove pod "p-a1"
			nodeIdx:       0, // node-a
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{zoneConstraint},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone1", 1}, {"zone2", 2}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}: 1,
					{key: "zone", value: "zone2"}: 2,
				},
			},
		},
		{
			name: "delete an irrelevant pod won't help",
			preemptor: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a0").Node("node-a").Label("bar", "").Obj(),
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-x1").Node("node-x").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			deletedPodIdx: 0, // remove pod "p-a0"
			nodeIdx:       0, // node-a
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{zoneConstraint},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone1", 2}, {"zone2", 2}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}: 2,
					{key: "zone", value: "zone2"}: 2,
				},
			},
		},
		{
			name: "delete a non-existing pod won't help",
			preemptor: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-x1").Node("node-x").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			deletedPodIdx: -1,
			deletedPod:    st.MakePod().Name("p-a0").Node("node-a").Label("bar", "").Obj(),
			nodeIdx:       0, // node-a
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{zoneConstraint},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone1", 2}, {"zone2", 2}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}: 2,
					{key: "zone", value: "zone2"}: 2,
				},
			},
		},
		{
			name: "two spreadConstraints",
			preemptor: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-x1").Node("node-x").Label("foo", "").Obj(),
				st.MakePod().Name("p-x2").Node("node-x").Label("foo", "").Obj(),
			},
			deletedPodIdx: 3, // remove pod "p-x1"
			nodeIdx:       2, // node-x
			want: &preFilterState{
				Constraints: []topologySpreadConstraint{zoneConstraint, nodeConstraint},
				TpKeyToCriticalPaths: map[string]*criticalPaths{
					"zone": {{"zone2", 1}, {"zone1", 3}},
					"node": {{"node-b", 1}, {"node-x", 1}},
				},
				TpPairToMatchNum: map[topologyPair]int{
					{key: "zone", value: "zone1"}:  3,
					{key: "zone", value: "zone2"}:  1,
					{key: "node", value: "node-a"}: 2,
					{key: "node", value: "node-b"}: 1,
					{key: "node", value: "node-x"}: 1,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			snapshot := cache.NewSnapshot(tt.existingPods, tt.nodes)
			pl := plugintesting.SetupPlugin(t, topologySpreadFunc, &config.PodTopologySpreadArgs{DefaultingType: config.ListDefaulting}, snapshot)
			p := pl.(*PodTopologySpread)
			cs := framework.NewCycleState()
			if _, s := p.PreFilter(ctx, cs, tt.preemptor); !s.IsSuccess() {
				t.Fatal(s.AsError())
			}

			deletedPod := tt.deletedPod
			if tt.deletedPodIdx < len(tt.existingPods) && tt.deletedPodIdx >= 0 {
				deletedPod = tt.existingPods[tt.deletedPodIdx]
			}

			nodeInfo, err := snapshot.Get(tt.nodes[tt.nodeIdx].Name)
			if err != nil {
				t.Fatal(err)
			}
			if s := p.RemovePod(ctx, cs, tt.preemptor, framework.NewPodInfo(deletedPod), nodeInfo); !s.IsSuccess() {
				t.Fatal(s.AsError())
			}

			state, err := getPreFilterState(cs)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(state, tt.want, cmpOpts...); diff != "" {
				t.Errorf("PodTopologySpread.RemovePod() returned diff (-want,+got):\n%s", diff)
			}
		})
	}
}

func BenchmarkFilter(b *testing.B) {
	tests := []struct {
		name             string
		pod              *v1.Pod
		existingPodsNum  int
		allNodesNum      int
		filteredNodesNum int
	}{
		{
			name: "1000nodes/single-constraint-zone",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, v1.LabelTopologyZone, v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			existingPodsNum:  10000,
			allNodesNum:      1000,
			filteredNodesNum: 500,
		},
		{
			name: "1000nodes/single-constraint-node",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, v1.LabelHostname, v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			existingPodsNum:  10000,
			allNodesNum:      1000,
			filteredNodesNum: 500,
		},
		{
			name: "1000nodes/two-Constraints-zone-node",
			pod: st.MakePod().Name("p").Label("foo", "").Label("bar", "").
				SpreadConstraint(1, v1.LabelTopologyZone, v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, v1.LabelHostname, v1.DoNotSchedule, barSelector, nil, nil, nil, nil).
				Obj(),
			existingPodsNum:  10000,
			allNodesNum:      1000,
			filteredNodesNum: 500,
		},
	}
	for _, tt := range tests {
		var state *framework.CycleState
		b.Run(tt.name, func(b *testing.B) {
			existingPods, allNodes, _ := st.MakeNodesAndPodsForEvenPodsSpread(tt.pod.Labels, tt.existingPodsNum, tt.allNodesNum, tt.filteredNodesNum)
			ctx := context.Background()
			pl := plugintesting.SetupPlugin(b, topologySpreadFunc, &config.PodTopologySpreadArgs{DefaultingType: config.ListDefaulting}, cache.NewSnapshot(existingPods, allNodes))
			p := pl.(*PodTopologySpread)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				state = framework.NewCycleState()
				if _, s := p.PreFilter(ctx, state, tt.pod); !s.IsSuccess() {
					b.Fatal(s.AsError())
				}
				filterNode := func(i int) {
					n, _ := p.sharedLister.NodeInfos().Get(allNodes[i].Name)
					p.Filter(ctx, state, tt.pod, n)
				}
				p.parallelizer.Until(ctx, len(allNodes), filterNode, "")
			}
		})
		b.Run(tt.name+"/Clone", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				state.Clone()
			}
		})
	}
}

func mustConvertLabelSelectorAsSelector(t *testing.T, ls *metav1.LabelSelector) labels.Selector {
	t.Helper()
	s, err := metav1.LabelSelectorAsSelector(ls)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSingleConstraint(t *testing.T) {
	tests := []struct {
		name                       string
		pod                        *v1.Pod
		nodes                      []*v1.Node
		existingPods               []*v1.Pod
		wantStatusCode             map[string]framework.Code
		enableMinDomains           bool
		enableNodeInclustionPolicy bool
	}{
		{
			name: "no existing pods",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Success,
				"node-b": framework.Success,
				"node-x": framework.Success,
				"node-y": framework.Success,
			},
		},
		{
			name: "no existing pods, incoming pod doesn't match itself",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, barSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Success,
				"node-b": framework.Success,
				"node-x": framework.Success,
				"node-y": framework.Success,
			},
		},
		{
			name: "existing pods in a different namespace do not count",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Namespace("ns1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Namespace("ns2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-x1").Node("node-x").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Success,
				"node-b": framework.Success,
				"node-x": framework.Unschedulable,
				"node-y": framework.Unschedulable,
			},
		},
		{
			name: "pods spread across zones as 3/3, all nodes fit",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y3").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Success,
				"node-b": framework.Success,
				"node-x": framework.Success,
				"node-y": framework.Success,
			},
		},
		{
			// TODO(Huang-Wei): maybe document this to remind users that typos on node labels
			// can cause unexpected behavior
			name: "pods spread across zones as 1/2 due to absence of label 'zone' on node-b",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zon", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-x1").Node("node-x").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Success,
				"node-b": framework.UnschedulableAndUnresolvable,
				"node-x": framework.Unschedulable,
				"node-y": framework.Unschedulable,
			},
		},
		{
			name: "pod cannot be scheduled as all nodes don't have label 'rack'",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "rack", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.UnschedulableAndUnresolvable,
				"node-x": framework.UnschedulableAndUnresolvable,
			},
		},
		{
			name: "pods spread across nodes as 2/1/0/3, only node-x fits",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y3").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Unschedulable,
				"node-x": framework.Success,
				"node-y": framework.Unschedulable,
			},
		},
		{
			name: "pods spread across nodes as 2/1/0/3, maxSkew is 2, node-b and node-x fit",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(2, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y3").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Success,
				"node-x": framework.Success,
				"node-y": framework.Unschedulable,
			},
		},
		{
			// not a desired case, but it can happen
			// TODO(Huang-Wei): document this "pod-not-match-itself" case
			// in this case, placement of the new pod doesn't change pod distribution of the cluster
			// as the incoming pod doesn't have label "foo"
			name: "pods spread across nodes as 2/1/0/3, but pod doesn't match itself",
			pod: st.MakePod().Name("p").Label("bar", "").
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y3").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Success,
				"node-x": framework.Success,
				"node-y": framework.Unschedulable,
			},
		},
		{
			// only node-a and node-y are considered, so pods spread as 2/~1~/~0~/3
			// ps: '~num~' is a markdown symbol to denote a crossline through 'num'
			// but in this unit test, we don't run NodeAffinity/TaintToleration Predicate, so node-b and node-x are
			// still expected to be fits;
			// the fact that node-a fits can prove the underlying logic works
			name: "incoming pod has nodeAffinity, pods spread as 2/~1~/~0~/3, hence node-a fits",
			pod: st.MakePod().Name("p").Label("foo", "").
				NodeAffinityIn("node", []string{"node-a", "node-y"}).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y3").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Success,
				"node-b": framework.Success, // in real case, it's false
				"node-x": framework.Success, // in real case, it's false
				"node-y": framework.Unschedulable,
			},
		},
		{
			name: "terminating Pods should be excluded",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("node", "node-b").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a").Node("node-a").Label("foo", "").Terminating().Obj(),
				st.MakePod().Name("p-b").Node("node-b").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Success,
				"node-b": framework.Unschedulable,
			},
		},
		{
			// In this unit test, NodeAffinity plugin is not involved, so node-b still fits
			name: "incoming pod has nodeAffinity, pods spread as 0/~2~/0/1, hence node-a fits",
			pod: st.MakePod().Name("p").Label("foo", "").
				NodeAffinityNotIn("node", []string{"node-b"}).
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-b2").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Success,
				"node-b": framework.Success, // in real case, it's Unschedulable
				"node-x": framework.Unschedulable,
				"node-y": framework.Unschedulable,
			},
		},
		{
			name: "pods spread across nodes as 2/2/1, maxSkew is 2, and the number of domains < minDomains, then the third node fits",
			pod: st.MakePod().Name("p").Label("foo", "").SpreadConstraint(
				2,
				"node",
				v1.DoNotSchedule,
				fooSelector,
				pointer.Int32(4), // larger than the number of domains(3)
				nil,
				nil,
				nil,
			).Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-c").Label("node", "node-c").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-b2").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-c1").Node("node-c").Label("foo", "").Obj(),
			},
			enableMinDomains: true,
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Unschedulable,
				"node-c": framework.Success,
			},
		},
		{
			name: "pods spread across nodes as 2/2/1, maxSkew is 2, and the number of domains > minDomains, then the all nodes fit",
			pod: st.MakePod().Name("p").Label("foo", "").SpreadConstraint(
				2,
				"node",
				v1.DoNotSchedule,
				fooSelector,
				pointer.Int32(2), // smaller than the number of domains(3)
				nil,
				nil,
				nil,
			).Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-c").Label("node", "node-c").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-b2").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-c1").Node("node-c").Label("foo", "").Obj(),
			},
			enableMinDomains: true,
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Success,
				"node-b": framework.Success,
				"node-c": framework.Success,
			},
		},
		{
			name: "pods spread across zone as 2/1, maxSkew is 2 and the number of domains < minDomains, then the third and fourth nodes fit",
			pod: st.MakePod().Name("p").Label("foo", "").SpreadConstraint(
				2,
				"zone",
				v1.DoNotSchedule,
				fooSelector,
				pointer.Int32(3), // larger than the number of domains(2)
				nil,
				nil,
				nil,
			).Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b2").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			enableMinDomains: true,
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Unschedulable,
				"node-x": framework.Success,
				"node-y": framework.Success,
			},
		},
		{
			name: "pods spread across zone as 2/1, maxSkew is 2 and the number of domains > minDomains, then the all nodes fit",
			pod: st.MakePod().Name("p").Label("foo", "").SpreadConstraint(
				2,
				"zone",
				v1.DoNotSchedule,
				fooSelector,
				pointer.Int32(1), // smaller than the number of domains(2)
				nil,
				nil,
				nil,
			).Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b2").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			enableMinDomains: true,
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Success,
				"node-b": framework.Success,
				"node-x": framework.Success,
				"node-y": framework.Success,
			},
		},
		{
			// pods spread across node as 1/1/0/~0~
			name: "NodeAffinityPolicy honored with labelSelectors",
			pod: st.MakePod().Name("p").Label("foo", "").
				NodeSelector(map[string]string{"foo": ""}).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("node", "node-a").Label("foo", "").Obj(),
				st.MakeNode().Name("node-b").Label("node", "node-b").Label("foo", "").Obj(),
				st.MakeNode().Name("node-x").Label("node", "node-x").Label("foo", "").Obj(),
				st.MakeNode().Name("node-y").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b2").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Unschedulable,
				"node-x": framework.Success,
				"node-y": framework.Success, // in real case, when we disable NodeAffinity Plugin, node-y will be success.
			},
			enableNodeInclustionPolicy: true,
		},
		{
			// pods spread across node as 1/1/0/~1~
			name: "NodeAffinityPolicy ignored with labelSelectors",
			pod: st.MakePod().Name("p").Label("foo", "").
				NodeSelector(map[string]string{"foo": ""}).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, &ignorePolicy, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("node", "node-a").Label("foo", "").Obj(),
				st.MakeNode().Name("node-b").Label("node", "node-b").Label("foo", "").Obj(),
				st.MakeNode().Name("node-x").Label("node", "node-x").Label("foo", "").Obj(),
				st.MakeNode().Name("node-y").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b2").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Unschedulable,
				"node-x": framework.Success,
				"node-y": framework.Unschedulable,
			},
			enableNodeInclustionPolicy: true,
		},
		{
			// pods spread across node as 1/1/0/~0~
			name: "NodeAffinityPolicy honored with nodeAffinity",
			pod: st.MakePod().Name("p").Label("foo", "").
				NodeAffinityIn("foo", []string{""}).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("node", "node-a").Label("foo", "").Obj(),
				st.MakeNode().Name("node-b").Label("node", "node-b").Label("foo", "").Obj(),
				st.MakeNode().Name("node-x").Label("node", "node-x").Label("foo", "").Obj(),
				st.MakeNode().Name("node-y").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b2").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Unschedulable,
				"node-x": framework.Success,
				"node-y": framework.Success, // in real case, when we disable NodeAffinity Plugin, node-y will be success.
			},
			enableNodeInclustionPolicy: true,
		},
		{
			// pods spread across node as 1/1/0/~1~
			name: "NodeAffinityPolicy ignored with labelSelectors",
			pod: st.MakePod().Name("p").Label("foo", "").
				NodeAffinityIn("foo", []string{""}).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, &ignorePolicy, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("node", "node-a").Label("foo", "").Obj(),
				st.MakeNode().Name("node-b").Label("node", "node-b").Label("foo", "").Obj(),
				st.MakeNode().Name("node-x").Label("node", "node-x").Label("foo", "").Obj(),
				st.MakeNode().Name("node-y").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b2").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Unschedulable,
				"node-x": framework.Success,
				"node-y": framework.Unschedulable,
			},
			enableNodeInclustionPolicy: true,
		},
		{
			// pods spread across node as 1/1/0/~0~
			name: "NodeTaintsPolicy honored",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, &honorPolicy, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("node", "node-a").Label("foo", "").Obj(),
				st.MakeNode().Name("node-b").Label("node", "node-b").Label("foo", "").Obj(),
				st.MakeNode().Name("node-x").Label("node", "node-x").Label("foo", "").Obj(),
				st.MakeNode().Name("node-y").Label("node", "node-y").Taints(taints).Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b2").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Unschedulable,
				"node-x": framework.Success,
				"node-y": framework.Success, // in real case, when we disable TaintToleration Plugin, node-y will be success.
			},
			enableNodeInclustionPolicy: true,
		},
		{
			// pods spread across node as 1/1/0/~1~
			name: "NodeTaintsPolicy ignored",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("node", "node-a").Label("foo", "").Obj(),
				st.MakeNode().Name("node-b").Label("node", "node-b").Label("foo", "").Obj(),
				st.MakeNode().Name("node-x").Label("node", "node-x").Label("foo", "").Obj(),
				st.MakeNode().Name("node-y").Label("node", "node-y").Taints(taints).Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-b1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b2").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Unschedulable,
				"node-x": framework.Success,
				"node-y": framework.Unschedulable,
			},
			enableNodeInclustionPolicy: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := cache.NewSnapshot(tt.existingPods, tt.nodes)
			pl := plugintesting.SetupPlugin(t, topologySpreadFunc, &config.PodTopologySpreadArgs{DefaultingType: config.ListDefaulting}, snapshot)
			p := pl.(*PodTopologySpread)
			p.enableMinDomainsInPodTopologySpread = tt.enableMinDomains
			p.enableNodeInclusionPolicyInPodTopologySpread = tt.enableNodeInclustionPolicy
			state := framework.NewCycleState()
			if _, s := p.PreFilter(context.Background(), state, tt.pod); !s.IsSuccess() {
				t.Errorf("preFilter failed with status: %v", s)
			}

			for _, node := range tt.nodes {
				nodeInfo, _ := snapshot.NodeInfos().Get(node.Name)
				status := p.Filter(context.Background(), state, tt.pod, nodeInfo)
				if len(tt.wantStatusCode) != 0 && status.Code() != tt.wantStatusCode[node.Name] {
					t.Errorf("[%s]: expected status code %v got %v", node.Name, tt.wantStatusCode[node.Name], status.Code())
				}
			}
		})
	}
}

func TestMultipleConstraints(t *testing.T) {
	tests := []struct {
		name                       string
		pod                        *v1.Pod
		nodes                      []*v1.Node
		existingPods               []*v1.Pod
		wantStatusCode             map[string]framework.Code
		enableNodeInclustionPolicy bool
	}{
		{
			// 1. to fulfil "zone" constraint, incoming pod can be placed on any zone (hence any node)
			// 2. to fulfil "node" constraint, incoming pod can be placed on node-x
			// intersection of (1) and (2) returns node-x
			name: "two Constraints on zone and node, spreads = [3/3, 2/1/0/3]",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y3").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Unschedulable,
				"node-x": framework.Success,
				"node-y": framework.Unschedulable,
			},
		},
		{
			// 1. to fulfil "zone" constraint, incoming pod can be placed on zone1 (node-a or node-b)
			// 2. to fulfil "node" constraint, incoming pod can be placed on node-x
			// intersection of (1) and (2) returns no node
			name: "two Constraints on zone and node, spreads = [3/4, 2/1/0/4]",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y3").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y4").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Unschedulable,
				"node-x": framework.Unschedulable,
				"node-y": framework.Unschedulable,
			},
		},
		{
			// 1. to fulfil "zone" constraint, incoming pod can be placed on zone2 (node-x or node-y)
			// 2. to fulfil "node" constraint, incoming pod can be placed on node-a, node-b or node-x
			// intersection of (1) and (2) returns node-x
			name: "Constraints hold different labelSelectors, spreads = [1/0, 1/0/0/1]",
			pod: st.MakePod().Name("p").Label("foo", "").Label("bar", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, barSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("bar", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Unschedulable,
				"node-x": framework.Success,
				"node-y": framework.Unschedulable,
			},
		},
		{
			// 1. to fulfil "zone" constraint, incoming pod can be placed on zone2 (node-x or node-y)
			// 2. to fulfil "node" constraint, incoming pod can be placed on node-a or node-b
			// intersection of (1) and (2) returns no node
			name: "Constraints hold different labelSelectors, spreads = [1/0, 0/0/1/1]",
			pod: st.MakePod().Name("p").Label("foo", "").Label("bar", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, barSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-x1").Node("node-x").Label("bar", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("bar", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Unschedulable,
				"node-x": framework.Unschedulable,
				"node-y": framework.Unschedulable,
			},
		},
		{
			// 1. to fulfil "zone" constraint, incoming pod can be placed on zone1 (node-a or node-b)
			// 2. to fulfil "node" constraint, incoming pod can be placed on node-b or node-x
			// intersection of (1) and (2) returns node-b
			name: "Constraints hold different labelSelectors, spreads = [2/3, 1/0/0/1]",
			pod: st.MakePod().Name("p").Label("foo", "").Label("bar", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, barSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-a2").Node("node-a").Label("foo", "").Label("bar", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
				st.MakePod().Name("p-y2").Node("node-y").Label("foo", "").Label("bar", "").Obj(),
				st.MakePod().Name("p-y3").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Success,
				"node-x": framework.Unschedulable,
				"node-y": framework.Unschedulable,
			},
		},
		{
			// 1. pod doesn't match itself on "zone" constraint, so it can be put onto any zone
			// 2. to fulfil "node" constraint, incoming pod can be placed on node-a or node-b
			// intersection of (1) and (2) returns node-a and node-b
			name: "Constraints hold different labelSelectors but pod doesn't match itself on 'zone' constraint",
			pod: st.MakePod().Name("p").Label("bar", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, barSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-x1").Node("node-x").Label("bar", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("bar", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Success,
				"node-b": framework.Success,
				"node-x": framework.Unschedulable,
				"node-y": framework.Unschedulable,
			},
		},
		{
			// 1. to fulfil "zone" constraint, incoming pod can be placed on any zone (hence any node)
			// 2. to fulfil "node" constraint, incoming pod can be placed on node-b (node-x doesn't have the required label)
			// intersection of (1) and (2) returns node-b
			name: "two Constraints on zone and node, absence of label 'node' on node-x, spreads = [1/1, 1/0/0/1]",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-y3").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Success,
				"node-x": framework.UnschedulableAndUnresolvable,
				"node-y": framework.Unschedulable,
			},
		},
		{
			// 1. to fulfil "zone" constraint, pods spread across zones as 2/~0~
			// 2. to fulfil "node" constraint, pods spread across zones as 1/1/0/~1~
			// intersection of (1) and (2) returns node-x
			name: "two node inclusion Constraints, zone: honor/ignore, node: ignore/ignore",
			pod: st.MakePod().Name("p").Label("foo", "").
				NodeSelector(map[string]string{"foo": ""}).
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, &ignorePolicy, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Label("foo", "").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Label("foo", "").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Label("foo", "").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Unschedulable,
				"node-x": framework.Success,
				"node-y": framework.Unschedulable,
			},
			enableNodeInclustionPolicy: true,
		},
		{
			// 1. to fulfil "zone" constraint, pods spread across zones as 2/0
			// 2. to fulfil "node" constraint, pods spread across zones as 1/1/0/~1~
			// intersection of (1) and (2) returns node-x
			name: "two node inclusion Constraints, zone: honor/honor, node: honor/ignore",
			pod: st.MakePod().Name("p").Label("foo", "").
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, &honorPolicy, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Label("foo", "").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Label("foo", "").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Label("foo", "").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Taints(taints).Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Unschedulable,
				"node-x": framework.Success,
				"node-y": framework.Unschedulable,
			},
			enableNodeInclustionPolicy: true,
		},
		{
			// 1. to fulfil "zone" constraint, pods spread across zones as 1/~1~
			// 2. to fulfil "node" constraint, pods spread across zones as 1/0/0/~1~
			// intersection of (1) and (2) returns node-x
			name: "two node inclusion Constraints, zone: honor/ignore, node: honor/ignore",
			pod: st.MakePod().Name("p").Label("foo", "").
				NodeSelector(map[string]string{"foo": ""}).
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, nil, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Label("foo", "").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Label("foo", "").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Label("foo", "").Taints(taints).Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Success,
				"node-x": framework.Success,
				"node-y": framework.Unschedulable,
			},
			enableNodeInclustionPolicy: true,
		},
		{
			// 1. to fulfil "zone" constraint, pods spread across zones as 1/0
			// 2. to fulfil "node" constraint, pods spread across zones as 1/~1~/0/~1~
			// intersection of (1) and (2) returns node-x
			name: "two node inclusion Constraints, zone: honor/honor, node: ignore/ignore",
			pod: st.MakePod().Name("p").Label("foo", "").
				NodeSelector(map[string]string{"foo": ""}).
				SpreadConstraint(1, "zone", v1.DoNotSchedule, fooSelector, nil, nil, &honorPolicy, nil).
				SpreadConstraint(1, "node", v1.DoNotSchedule, fooSelector, nil, &ignorePolicy, nil, nil).
				Obj(),
			nodes: []*v1.Node{
				st.MakeNode().Name("node-a").Label("zone", "zone1").Label("node", "node-a").Label("foo", "").Obj(),
				st.MakeNode().Name("node-b").Label("zone", "zone1").Label("node", "node-b").Obj(),
				st.MakeNode().Name("node-x").Label("zone", "zone2").Label("node", "node-x").Label("foo", "").Obj(),
				st.MakeNode().Name("node-y").Label("zone", "zone2").Label("node", "node-y").Label("foo", "").Taints(taints).Obj(),
			},
			existingPods: []*v1.Pod{
				st.MakePod().Name("p-a1").Node("node-a").Label("foo", "").Obj(),
				st.MakePod().Name("p-b1").Node("node-b").Label("foo", "").Obj(),
				st.MakePod().Name("p-y1").Node("node-y").Label("foo", "").Obj(),
			},
			wantStatusCode: map[string]framework.Code{
				"node-a": framework.Unschedulable,
				"node-b": framework.Unschedulable,
				"node-x": framework.Success,
				"node-y": framework.Unschedulable,
			},
			enableNodeInclustionPolicy: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := cache.NewSnapshot(tt.existingPods, tt.nodes)
			pl := plugintesting.SetupPlugin(t, topologySpreadFunc, &config.PodTopologySpreadArgs{DefaultingType: config.ListDefaulting}, snapshot)
			p := pl.(*PodTopologySpread)
			p.enableNodeInclusionPolicyInPodTopologySpread = tt.enableNodeInclustionPolicy
			state := framework.NewCycleState()
			if _, s := p.PreFilter(context.Background(), state, tt.pod); !s.IsSuccess() {
				t.Errorf("preFilter failed with status: %v", s)
			}

			for _, node := range tt.nodes {
				nodeInfo, _ := snapshot.NodeInfos().Get(node.Name)
				status := p.Filter(context.Background(), state, tt.pod, nodeInfo)
				if len(tt.wantStatusCode) != 0 && status.Code() != tt.wantStatusCode[node.Name] {
					t.Errorf("[%s]: expected error code %v got %v", node.Name, tt.wantStatusCode[node.Name], status.Code())
				}
			}
		})
	}
}

func TestPreFilterDisabled(t *testing.T) {
	pod := &v1.Pod{}
	nodeInfo := framework.NewNodeInfo()
	node := v1.Node{}
	nodeInfo.SetNode(&node)
	p := plugintesting.SetupPlugin(t, topologySpreadFunc, &config.PodTopologySpreadArgs{DefaultingType: config.ListDefaulting}, cache.NewEmptySnapshot())
	cycleState := framework.NewCycleState()
	gotStatus := p.(*PodTopologySpread).Filter(context.Background(), cycleState, pod, nodeInfo)
	wantStatus := framework.AsStatus(fmt.Errorf(`reading "PreFilterPodTopologySpread" from cycleState: %w`, framework.ErrNotFound))
	if !reflect.DeepEqual(gotStatus, wantStatus) {
		t.Errorf("status does not match: %v, want: %v", gotStatus, wantStatus)
	}
}
