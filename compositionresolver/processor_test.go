package compositionresolver

import (
	"context"
	"reflect"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/plog"
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
	mapper := restmapper.NewDiscoveryRESTMapper(apiGroupResources)

	p := newProcessor(zaptest.NewLogger(t), &Config{
		CacheTTL:          5 * time.Minute,
		NegativeCacheTTL:  30 * time.Second,
		LabelKey:          "krateo.io/composition-id",
		NameLabelKey:      "krateo.io/composition-name",
		NamespaceLabelKey: "krateo.io/composition-namespace",
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
		want resolution
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
			want: resolution{id: "installer-root-id"},
		},
		{
			name: "composition WITHOUT label falls back to its own uid (editor root)",
			ref:  involvedObjectRef{APIVersion: compAPIVersion, Kind: compKind, Name: "root", Namespace: "krateo-system", UID: "root-own-uid"},
			objs: []runtime.Object{
				newUnstructured(compAPIVersion, compKind, "krateo-system", "root", "root-own-uid", nil),
			},
			gvks: []schema.GroupVersionKind{compGVK},
			want: resolution{id: "root-own-uid", name: "root", namespace: "krateo-system"},
		},
		{
			name: "non-composition object without label returns empty",
			ref:  involvedObjectRef{APIVersion: "apps/v1", Kind: "Deployment", Name: "nginx", Namespace: "default", UID: "deploy-uid"},
			objs: []runtime.Object{
				newUnstructured("apps/v1", "Deployment", "default", "nginx", "deploy-uid", nil),
			},
			gvks: []schema.GroupVersionKind{deployGVK},
			want: resolution{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestProcessor(t, tc.gvks, tc.objs...)
			got := p.resolveFromK8s(context.Background(), tc.ref)
			if got != tc.want {
				t.Fatalf("resolveFromK8s() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// eventRecord builds a log record shaped like a k8sobjects watch event (map
// body), the input enrichLogRecord parses in production.
func eventRecord(ref involvedObjectRef) plog.LogRecord {
	lr := plog.NewLogRecord()
	_ = lr.Body().SetEmptyMap().FromRaw(map[string]any{
		"type": "ADDED",
		"object": map[string]any{
			"apiVersion": "v1",
			"kind":       "Event",
			"involvedObject": map[string]any{
				"apiVersion": ref.APIVersion,
				"kind":       ref.Kind,
				"name":       ref.Name,
				"namespace":  ref.Namespace,
				"uid":        ref.UID,
			},
		},
	})
	return lr
}

// TestEnrichLogRecord_NameAndNamespace drives the full per-record path (body
// parse, resolve, cache, write) and checks the EXACT attribute set, so an
// attribute written as "" or written when it should not be fails the same way a
// missing one does.
func TestEnrichLogRecord_NameAndNamespace(t *testing.T) {
	const (
		idKey   = "krateo.io/composition-id"
		nameKey = "krateo.io/composition-name"
		nsKey   = "krateo.io/composition-namespace"
		compAV  = "composition.krateo.io/v1-2-3"
	)
	compGVK := schema.GroupVersionKind{Group: "composition.krateo.io", Version: "v1-2-3", Kind: "Portal"}
	restGVK := schema.GroupVersionKind{Group: "templates.krateo.io", Version: "v1", Kind: "RESTAction"}
	deployGVK := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	gvks := []schema.GroupVersionKind{compGVK, restGVK, deployGVK}

	tests := []struct {
		name string
		ref  involvedObjectRef
		obj  runtime.Object
		want map[string]any
	}{
		{
			// A composition-managed object, labelled the way composition-dynamic-
			// controller labels what it renders (e.g. RESTActions in krateo-dashboard).
			name: "managed object: id, name and namespace all from its labels",
			ref:  involvedObjectRef{APIVersion: "templates.krateo.io/v1", Kind: "RESTAction", Name: "portal-events", Namespace: "krateo-system", UID: "ra-uid"},
			obj: newUnstructured("templates.krateo.io/v1", "RESTAction", "krateo-system", "portal-events", "ra-uid", map[string]interface{}{
				idKey:   "portal-comp-uid",
				nameKey: "portal",
				nsKey:   "portal-ns",
			}),
			want: map[string]any{idKey: "portal-comp-uid", nameKey: "portal", nsKey: "portal-ns"},
		},
		{
			// The object IS the composition and has no inherited id: the id is its
			// own uid, so name and namespace are its own metadata.name/namespace.
			name: "root composition itself: own uid, own name, own namespace",
			ref:  involvedObjectRef{APIVersion: compAV, Kind: "Portal", Name: "my-portal", Namespace: "team-a", UID: "portal-own-uid"},
			obj:  newUnstructured(compAV, "Portal", "team-a", "my-portal", "portal-own-uid", nil),
			want: map[string]any{idKey: "portal-own-uid", nameKey: "my-portal", nsKey: "team-a"},
		},
		{
			// An installer child composition: the inherited label id names the
			// installer, so name/namespace come from the labels too, NOT from the
			// child's own metadata, which would pair the installer's id with the
			// child's name.
			name: "child composition with inherited labels: all three from the labels",
			ref:  involvedObjectRef{APIVersion: compAV, Kind: "Portal", Name: "portal", Namespace: "krateo-system", UID: "child-uid"},
			obj: newUnstructured(compAV, "Portal", "krateo-system", "portal", "child-uid", map[string]interface{}{
				idKey:   "installer-uid",
				nameKey: "installer",
				nsKey:   "krateo-system",
			}),
			want: map[string]any{idKey: "installer-uid", nameKey: "installer", nsKey: "krateo-system"},
		},
		{
			name: "id label without name/namespace labels: id only, no empty strings",
			ref:  involvedObjectRef{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Namespace: "default", UID: "web-uid"},
			obj: newUnstructured("apps/v1", "Deployment", "default", "web", "web-uid", map[string]interface{}{
				idKey: "some-comp-uid",
			}),
			want: map[string]any{idKey: "some-comp-uid"},
		},
		{
			name: "id and name labels without namespace: namespace absent",
			ref:  involvedObjectRef{APIVersion: "apps/v1", Kind: "Deployment", Name: "api", Namespace: "default", UID: "api-uid"},
			obj: newUnstructured("apps/v1", "Deployment", "default", "api", "api-uid", map[string]interface{}{
				idKey:   "some-comp-uid",
				nameKey: "shop",
			}),
			want: map[string]any{idKey: "some-comp-uid", nameKey: "shop"},
		},
		{
			// Name/namespace labels mean nothing without an id: nothing is written.
			name: "non-composition object: nothing written, even with stray name/namespace labels",
			ref:  involvedObjectRef{APIVersion: "apps/v1", Kind: "Deployment", Name: "nginx", Namespace: "default", UID: "nginx-uid"},
			obj: newUnstructured("apps/v1", "Deployment", "default", "nginx", "nginx-uid", map[string]interface{}{
				nameKey: "stray",
				nsKey:   "stray-ns",
			}),
			want: map[string]any{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestProcessor(t, gvks, tc.obj)
			// Twice: the second pass is served from the cache, which must hold the
			// whole resolution, not just the id.
			for pass := 1; pass <= 2; pass++ {
				lr := eventRecord(tc.ref)
				p.enrichLogRecord(context.Background(), lr)
				if got := lr.Attributes().AsRaw(); !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("pass %d: attributes = %v, want %v", pass, got, tc.want)
				}
			}
		})
	}
}

// TestEnrichLogRecord_DisabledKeys: an empty name_label_key/namespace_label_key
// turns that attribute off, for the label case and the composition-itself case.
func TestEnrichLogRecord_DisabledKeys(t *testing.T) {
	compGVK := schema.GroupVersionKind{Group: "composition.krateo.io", Version: "v1-2-3", Kind: "Portal"}
	p := newTestProcessor(t, []schema.GroupVersionKind{compGVK},
		newUnstructured("composition.krateo.io/v1-2-3", "Portal", "team-a", "my-portal", "portal-own-uid", nil))
	p.config.NameLabelKey = ""
	p.config.NamespaceLabelKey = ""

	lr := eventRecord(involvedObjectRef{APIVersion: "composition.krateo.io/v1-2-3", Kind: "Portal", Name: "my-portal", Namespace: "team-a", UID: "portal-own-uid"})
	p.enrichLogRecord(context.Background(), lr)
	want := map[string]any{"krateo.io/composition-id": "portal-own-uid"}
	if got := lr.Attributes().AsRaw(); !reflect.DeepEqual(got, want) {
		t.Fatalf("attributes = %v, want %v", got, want)
	}
}

func TestConfigValidate_KeyCollisions(t *testing.T) {
	base := func() *Config { return createDefaultConfig().(*Config) }
	if err := base().Validate(); err != nil {
		t.Fatalf("default config must validate: %v", err)
	}

	disabled := base()
	disabled.NameLabelKey, disabled.NamespaceLabelKey = "", ""
	if err := disabled.Validate(); err != nil {
		t.Errorf("empty name/namespace keys (disabled) must validate: %v", err)
	}

	for name, mutate := range map[string]func(*Config){
		"name == id":        func(c *Config) { c.NameLabelKey = c.LabelKey },
		"namespace == id":   func(c *Config) { c.NamespaceLabelKey = c.LabelKey },
		"name == namespace": func(c *Config) { c.NameLabelKey = c.NamespaceLabelKey },
	} {
		c := base()
		mutate(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want an error", name)
		}
	}
}
