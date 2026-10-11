package compositionresolver

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// stalingMapper models the real failure in #22: a mapper whose discovery data
// was captured at startup and does not include a group/version minted later.
// Reset() is what re-discovery would do, so after a Reset it starts resolving.
//
// It deliberately does NOT model DeferredDiscoveryRESTMapper's internal retry,
// because that retry is dead here — it is gated on Fresh(), which reports
// whether the cache is POPULATED rather than whether it is CURRENT, and so is
// permanently true once discovery has succeeded once.
type stalingMapper struct {
	meta.RESTMapper // embedded nil: any un-overridden method would panic, which is intentional
	known           map[schema.GroupVersionKind]bool
	afterReset      map[schema.GroupVersionKind]bool
	resets          int
	// hardErr, when set, is returned INSTEAD of a no-match error — standing in
	// for a transient discovery/network failure.
	hardErr error
}

func (m *stalingMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	if m.hardErr != nil {
		return nil, m.hardErr
	}
	gvk := gk.WithVersion(versions[0])
	if m.known[gvk] {
		gvr, _ := meta.UnsafeGuessKindToResource(gvk)
		return &meta.RESTMapping{Resource: gvr, GroupVersionKind: gvk, Scope: meta.RESTScopeNamespace}, nil
	}
	return nil, &meta.NoKindMatchError{GroupKind: gk, SearchedVersions: versions}
}

func (m *stalingMapper) Reset() {
	m.resets++
	for k, v := range m.afterReset {
		m.known[k] = v
	}
}

func newResetProcessor(t *testing.T, m meta.RESTMapper) *compositionResolverProcessor {
	t.Helper()
	p := newProcessor(zaptest.NewLogger(t), &Config{
		CacheTTL:         5 * time.Minute,
		NegativeCacheTTL: 30 * time.Second,
		LabelKey:         "krateo.io/composition-id",
	})
	p.mapper = m
	return p
}

// TestInvalidateMapper_RediscoversAfterNoMatch is the #22 regression arm. Without
// the explicit invalidation, a group/version minted after startup is unresolvable
// for the life of the process — which on krateo-057 took composition event
// correlation to 0% for days.
func TestInvalidateMapper_RediscoversAfterNoMatch(t *testing.T) {
	bumped := schema.GroupVersionKind{Group: "composition.krateo.io", Version: "v1-8-67", Kind: "BuilderPublish"}
	m := &stalingMapper{
		known:      map[schema.GroupVersionKind]bool{},             // startup discovery missed it
		afterReset: map[schema.GroupVersionKind]bool{bumped: true}, // re-discovery finds it
	}
	p := newResetProcessor(t, m)

	// Precondition: the mapper really does miss before any reset, or this arm
	// would pass against a mapper that never had the problem.
	if _, err := m.RESTMapping(bumped.GroupKind(), bumped.Version); err == nil {
		t.Fatal("precondition: the mapper must MISS this group/version before a reset")
	}

	if !p.invalidateMapper() {
		t.Fatal("invalidateMapper must report that it reset a resettable mapper")
	}
	if m.resets != 1 {
		t.Errorf("resets = %d, want 1", m.resets)
	}
	if _, err := m.RESTMapping(bumped.GroupKind(), bumped.Version); err != nil {
		t.Errorf("after the reset the bumped group/version must resolve, got %v", err)
	}
}

// TestResolveFromK8s_RetriesThroughTheProductionPath is the arm that actually
// guards the fix. The others exercise invalidateMapper() directly, so deleting
// the retry from resolveFromK8s would leave them all green — this one drives
// the real call path end to end and reds if the retry is removed.
func TestResolveFromK8s_RetriesThroughTheProductionPath(t *testing.T) {
	bumped := schema.GroupVersionKind{Group: "composition.krateo.io", Version: "v1-8-67", Kind: "BuilderPublish"}
	// A processor whose dynamic client HAS the object, built via the existing
	// harness so the fake client and the GVR agree...
	p := newTestProcessor(t, []schema.GroupVersionKind{bumped},
		newUnstructured(bumped.GroupVersion().String(), bumped.Kind, "krateo-system", "bp", "root-uid-1", nil))
	// ...but whose MAPPER has not discovered that group/version yet, exactly as
	// after a chart bump.
	m := &stalingMapper{
		known:      map[schema.GroupVersionKind]bool{},
		afterReset: map[schema.GroupVersionKind]bool{bumped: true},
	}
	p.mapper = m

	got := p.resolveFromK8s(context.Background(), involvedObjectRef{
		APIVersion: bumped.GroupVersion().String(), Kind: bumped.Kind,
		Name: "bp", Namespace: "krateo-system", UID: "root-uid-1",
	})

	if m.resets != 1 {
		t.Errorf("resets = %d, want 1 — resolveFromK8s must re-discover on a no-match", m.resets)
	}
	// The object carries no inherited label, so the f13d05b root fallback
	// returns its own uid. Getting it proves the retry reached the GET.
	if got != "root-uid-1" {
		t.Errorf("resolveFromK8s = %q, want \"root-uid-1\" — without the retry the mapping "+
			"stays missed and this returns empty, which is the #22 outage", got)
	}
}

// TestInvalidateMapper_ThrottledByNegativeCacheTTL pins the discovery-storm
// guard. Events legitimately reference kinds the cluster does not serve; without
// the throttle each one would trigger a full re-discovery.
func TestInvalidateMapper_ThrottledByNegativeCacheTTL(t *testing.T) {
	m := &stalingMapper{known: map[schema.GroupVersionKind]bool{}, afterReset: map[schema.GroupVersionKind]bool{}}
	p := newResetProcessor(t, m)

	if !p.invalidateMapper() {
		t.Fatal("the first invalidation must proceed")
	}
	for i := 0; i < 50; i++ {
		if p.invalidateMapper() {
			t.Fatalf("invalidation %d proceeded inside the throttle window — 50 unknown kinds would mean 50 re-discoveries", i+2)
		}
	}
	if m.resets != 1 {
		t.Errorf("resets = %d after 51 attempts, want exactly 1", m.resets)
	}

	// And it must become available again once the window passes, or a genuine
	// later bump would never be picked up.
	p.lastMapperReset = time.Now().Add(-2 * p.config.NegativeCacheTTL)
	if !p.invalidateMapper() {
		t.Error("invalidation must be allowed again after NegativeCacheTTL has elapsed")
	}
	if m.resets != 2 {
		t.Errorf("resets = %d, want 2", m.resets)
	}
}

// TestResolveFromK8s_DoesNotRediscoverOnTransientError — a discovery or network
// error is not evidence of staleness. Re-discovering on it would hammer the
// apiserver during exactly the outage that caused the error.
func TestResolveFromK8s_DoesNotRediscoverOnTransientError(t *testing.T) {
	m := &stalingMapper{
		known:      map[schema.GroupVersionKind]bool{},
		afterReset: map[schema.GroupVersionKind]bool{},
		hardErr:    errors.New("etcdserver: request timed out"),
	}
	p := newResetProcessor(t, m)

	got := p.resolveFromK8s(context.Background(), involvedObjectRef{
		APIVersion: "composition.krateo.io/v1-8-67", Kind: "BuilderPublish",
		Name: "x", Namespace: "krateo-system", UID: "u",
	})
	if got != "" {
		t.Errorf("resolveFromK8s = %q, want empty on a failed mapping", got)
	}
	if m.resets != 0 {
		t.Errorf("resets = %d, want 0 — a transient error must NOT trigger re-discovery", m.resets)
	}
}

// plainMapper implements meta.RESTMapper without Reset(), which
// meta.ResettableRESTMapper requires.
type plainMapper struct{ meta.RESTMapper }

// TestInvalidateMapper_NonResettableMapperIsSafe — the field is typed as the
// plain meta.RESTMapper interface, so the assertion must degrade rather than
// panic if a mapper without Reset() is ever substituted.
//
// NOTE, because the first version of this arm asserted the opposite and failed:
// the STATIC mapper the other tests use (meta.PriorityRESTMapper, via
// restmapper.NewDiscoveryRESTMapper) DOES implement ResettableRESTMapper. So
// this cannot be tested with the existing harness — it needs a mapper that
// genuinely lacks the method.
func TestInvalidateMapper_NonResettableMapperIsSafe(t *testing.T) {
	p := newResetProcessor(t, plainMapper{})
	if p.invalidateMapper() {
		t.Error("a mapper without Reset() must report false, not claim a reset")
	}
}

// TestInvalidateMapper_StaticMapperIsResettable records the fact above, so the
// next reader does not repeat my wrong assumption about the harness.
func TestInvalidateMapper_StaticMapperIsResettable(t *testing.T) {
	p := newTestProcessor(t, []schema.GroupVersionKind{
		{Group: "composition.krateo.io", Version: "v1-8-64", Kind: "BuilderPublish"},
	})
	if _, ok := p.mapper.(meta.ResettableRESTMapper); !ok {
		t.Fatal("expected meta.PriorityRESTMapper to implement ResettableRESTMapper")
	}
	if !p.invalidateMapper() {
		t.Error("it is resettable, so invalidateMapper should report true")
	}
}
