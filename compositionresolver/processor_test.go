package compositionresolver

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/restmapper"
)

// newTestProcessor builds a processor wired to a fake dynamic client seeded
// with the given objects and a RESTMapper covering the given GVKs, so
// resolveFromK8s can be exercised without a real cluster.
func newTestProcessor(t *testing.T, gvks []schema.GroupVersionKind, objs ...runtime.Object) *compositionResolverProcessor {
	t.Helper()

	scheme := runtime.NewScheme()
	// The fake dynamic client needs to know how to List each GVK it serves.
	gvrToListKind := map[schema.GroupVersionResource]string{}
	var apiGroupResources []*restmapper.APIGroupResources
	byGroup := map[string]*restmapper.APIGroupResources{}

	for _, gvk := range gvks {
		// Use the same kind→resource guess the fake dynamic client uses
		// internally to key objects, so RESTMapping and storage agree.
		gvr, _ := meta.UnsafeGuessKindToResource(gvk)
		plural := gvr.Resource
		gvrToListKind[gvr] = gvk.Kind + "List"

		agr, ok := byGroup[gvk.Group]
		if !ok {
			agr = &restmapper.APIGroupResources{
				Group: metav1.APIGroup{
					Name: gvk.Group,
					Versions: []metav1.GroupVersionForDiscovery{
						{GroupVersion: gvk.GroupVersion().String(), Version: gvk.Version},
					},
					PreferredVersion: metav1.GroupVersionForDiscovery{
						GroupVersion: gvk.GroupVersion().String(), Version: gvk.Version,
					},
				},
				VersionedResources: map[string][]metav1.APIResource{},
			}
			byGroup[gvk.Group] = agr
			apiGroupResources = append(apiGroupResources, agr)
		}
		agr.VersionedResources[gvk.Version] = append(agr.VersionedResources[gvk.Version], metav1.APIResource{
			Name:       plural,
			Namespaced: true,
			Kind:       gvk.Kind,
		})
	}

	dynClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objs...)
	// Wrapped so the processor's ResettableRESTMapper contract is satisfied AND so a test
	// can observe resets. The production mapper is a DeferredDiscoveryRESTMapper, which is
	// resettable; NewDiscoveryRESTMapper is not, and a bare meta.RESTMapper here would
	// silently stop compiling against the real field type.
	mapper := &countingResettableMapper{RESTMapper: restmapper.NewDiscoveryRESTMapper(apiGroupResources)}

	p := newProcessor(zaptest.NewLogger(t), &Config{
		CacheTTL:         5 * time.Minute,
		NegativeCacheTTL: 30 * time.Second,
		LabelKey:         "krateo.io/composition-id",
	})
	p.dynClient = dynClient
	p.mapper = mapper
	return p
}

func newUnstructured(apiVersion, kind, namespace, name, uid string, labels map[string]interface{}) *unstructured.Unstructured {
	metadata := map[string]interface{}{
		"name":      name,
		"namespace": namespace,
		"uid":       uid,
	}
	if labels != nil {
		metadata["labels"] = labels
	}
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": apiVersion,
			"kind":       kind,
			"metadata":   metadata,
		},
	}
}

func TestResolveFromK8s(t *testing.T) {
	const (
		compAPIVersion = "composition.krateo.io/v1alpha1"
		compKind       = "FireworksApp"
		labelKey       = "krateo.io/composition-id"
	)
	compGVK := schema.GroupVersionKind{Group: "composition.krateo.io", Version: "v1alpha1", Kind: compKind}
	deployGVK := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}

	tests := []struct {
		name string
		ref  involvedObjectRef
		objs []runtime.Object
		gvks []schema.GroupVersionKind
		want string
	}{
		{
			name: "composition WITH inherited label returns the label (installer child)",
			ref:  involvedObjectRef{APIVersion: compAPIVersion, Kind: compKind, Name: "child", Namespace: "krateo-system", UID: "child-own-uid"},
			objs: []runtime.Object{
				newUnstructured(compAPIVersion, compKind, "krateo-system", "child", "child-own-uid", map[string]interface{}{
					labelKey: "installer-root-id",
				}),
			},
			gvks: []schema.GroupVersionKind{compGVK},
			want: "installer-root-id",
		},
		{
			name: "composition WITHOUT label falls back to its own uid (editor root)",
			ref:  involvedObjectRef{APIVersion: compAPIVersion, Kind: compKind, Name: "root", Namespace: "krateo-system", UID: "root-own-uid"},
			objs: []runtime.Object{
				newUnstructured(compAPIVersion, compKind, "krateo-system", "root", "root-own-uid", nil),
			},
			gvks: []schema.GroupVersionKind{compGVK},
			want: "root-own-uid",
		},
		{
			name: "non-composition object without label returns empty",
			ref:  involvedObjectRef{APIVersion: "apps/v1", Kind: "Deployment", Name: "nginx", Namespace: "default", UID: "deploy-uid"},
			objs: []runtime.Object{
				newUnstructured("apps/v1", "Deployment", "default", "nginx", "deploy-uid", nil),
			},
			gvks: []schema.GroupVersionKind{deployGVK},
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestProcessor(t, tc.gvks, tc.objs...)
			got := p.resolveFromK8s(context.Background(), tc.ref)
			if got != tc.want {
				t.Fatalf("resolveFromK8s() = %q, want %q", got, tc.want)
			}
		})
	}
}

// countingResettableMapper makes a non-resettable RESTMapper satisfy
// meta.ResettableRESTMapper and records how many times Reset was called.
type countingResettableMapper struct {
	meta.RESTMapper
	resets int
}

func (m *countingResettableMapper) Reset() { m.resets++ }

// TestRESTMappingMissResetsDiscoveryCacheOnce covers the fix for the stale-discovery defect:
// the deferred mapper is built once at start() and caches the API surface, so a kind whose
// apiVersion was released afterwards is permanently unmappable. A miss must drop the cache
// and retry; a burst of misses must NOT, or every event naming a nonexistent kind would
// re-list the apiserver.
func TestRESTMappingMissResetsDiscoveryCacheOnce(t *testing.T) {
	p := newTestProcessor(t, []schema.GroupVersionKind{
		{Group: "composition.krateo.io", Version: "v1-12-38", Kind: "Snowplow"},
	})
	m := p.mapper.(*countingResettableMapper)

	// A kind this mapper has never seen -- the shape of a version minted after start().
	ref := involvedObjectRef{
		APIVersion: "composition.krateo.io/v1-12-41",
		Kind:       "Snowplow", Namespace: "krateo-system", Name: "snowplow",
	}

	if got := p.resolveFromK8s(context.Background(), ref); got != "" {
		t.Fatalf("unmappable kind should resolve to empty, got %q", got)
	}
	if m.resets != 1 {
		t.Fatalf("a mapping miss must reset the discovery cache exactly once, got %d", m.resets)
	}

	// Second miss inside the throttle window: must not reset again.
	_ = p.resolveFromK8s(context.Background(), ref)
	if m.resets != 1 {
		t.Fatalf("throttle must suppress a second reset within %s, got %d resets", mapperResetInterval, m.resets)
	}

	// Outside the window it may reset again.
	p.lastMapperReset = time.Now().Add(-2 * mapperResetInterval)
	_ = p.resolveFromK8s(context.Background(), ref)
	if m.resets != 2 {
		t.Fatalf("a miss after the throttle window must reset again, got %d resets", m.resets)
	}
}
