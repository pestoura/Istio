//go:build integ
// +build integ

// Copyright Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package telemetry

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/prometheus/common/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"istio.io/istio/pkg/config/mesh"
	"istio.io/istio/pkg/test"
	"istio.io/istio/pkg/test/framework"
	"istio.io/istio/pkg/test/framework/components/cluster"
	"istio.io/istio/pkg/test/framework/components/prometheus"
	"istio.io/istio/pkg/test/util/retry"
)

// PromDiff compares a query with labels to a query of the same metric without labels, and notes the closest matching
// metric.
func PromDiff(t test.Failer, prom prometheus.Instance, cluster cluster.Cluster, query prometheus.Query) {
	t.Helper()
	unlabelled := prometheus.Query{Metric: query.Metric}
	v, _ := prom.Query(cluster, unlabelled)
	if v == nil {
		t.Logf("no metrics found for %v", unlabelled)
		return
	}
	switch v.Type() {
	case model.ValVector:
		value := v.(model.Vector)
		var allMismatches []map[string]string
		full := []model.Metric{}
		for _, s := range value {
			misMatched := map[string]string{}
			for k, want := range query.Labels {
				got := string(s.Metric[model.LabelName(k)])
				if want != got {
					misMatched[k] = got
				}
			}
			if len(misMatched) == 0 {
				continue
			}
			allMismatches = append(allMismatches, misMatched)
			full = append(full, s.Metric)
		}
		if len(allMismatches) == 0 {
			t.Logf("no diff found")
			return
		}
		sort.Slice(allMismatches, func(i, j int) bool {
			return len(allMismatches[i]) < len(allMismatches[j])
		})
		t.Logf("query %q returned %v series, but none matched our query exactly.", query.Metric, len(value))
		t.Logf("Original query: %v", query.String())
		for i, m := range allMismatches {
			t.Logf("Series %d (source: %v/%v)", i, full[i]["namespace"], full[i]["pod"])
			missing := []string{}
			for k, v := range m {
				if v == "" {
					missing = append(missing, k)
				} else {
					t.Logf("  for label %q, wanted %q but got %q", k, query.Labels[k], v)
				}
			}
			if len(missing) > 0 {
				t.Logf("  missing labels: %v", missing)
			}
		}

	default:
		t.Fatalf("PromDiff expects Vector, got %v", v.Type())

	}
}

// PromDump gets all the recorded values for a metric by name and generates a report of the values.
// used for debugging of failures to provide a comprehensive view of traffic experienced.
func PromDump(cluster cluster.Cluster, prometheus prometheus.Instance, query prometheus.Query) string {
	if value, err := prometheus.Query(cluster, query); err == nil {
		return value.String()
	}

	return ""
}

// GetTrustDomain return trust domain of the cluster.
func GetTrustDomain(cluster cluster.Cluster, istioNamespace string) string {
	meshConfigMap, err := cluster.Kube().CoreV1().ConfigMaps(istioNamespace).Get(context.Background(), "istio", metav1.GetOptions{})
	defaultTrustDomain := mesh.DefaultMeshConfig().TrustDomain
	if err != nil {
		return defaultTrustDomain
	}

	configYaml, ok := meshConfigMap.Data["mesh"]
	if !ok {
		return defaultTrustDomain
	}

	cfg, err := mesh.ApplyMeshConfigDefaults(configYaml)
	if err != nil {
		return defaultTrustDomain
	}

	return cfg.TrustDomain
}

// QueryPrometheus queries prometheus and returns the result once the query stabilizes
func QueryPrometheus(t framework.TestContext, cluster cluster.Cluster, query prometheus.Query, promInst prometheus.Instance) (string, error) {
	t.Helper()
	t.Logf("query prometheus with: %v", query)

	val, err := promInst.Query(cluster, query)
	if err != nil {
		return "", err
	}
	got, err := prometheus.Sum(val)
	if err != nil {
		t.Logf("value: %s", val.String())
		return "", fmt.Errorf("could not find metric value: %v", err)
	}
	t.Logf("get value %v", got)
	return val.String(), nil
}

func ValidateMetric(t framework.TestContext, cluster cluster.Cluster, prometheus prometheus.Instance, query prometheus.Query, want float64) {
	t.Helper()
	err := retry.UntilSuccess(func() error {
		got, err := prometheus.QuerySum(cluster, query)
		t.Logf("%s: %f", query.Metric, got)
		if err != nil {
			return err
		}
		if got < want {
			return fmt.Errorf("bad metric value: got %f, want at least %f", got, want)
		}
		return nil
	}, retry.Delay(time.Second), retry.Timeout(time.Second*20))
	if err != nil {
		PromDiff(t, prometheus, cluster, query)
		t.Fatal(err)
	}
}
