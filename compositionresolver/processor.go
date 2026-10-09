package compositionresolver

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

// cacheEntry stores a resolved composition-id with an expiration time.
// An empty compositionID means the resource was resolved but had no label.
type cacheEntry struct {
	compositionID string
	expiresAt     time.Time
}

type compositionResolverProcessor struct {
	logger *zap.Logger
	config *Config

	dynClient dynamic.Interface
	mapper    meta.RESTMapper

	cache   map[string]cacheEntry
	cacheMu sync.RWMutex

	// #22 — the discovery cache behind mapper is populated once and never
	// invalidated, so a group/version minted AFTER startup can never be learned.
	// These throttle the explicit invalidation added in resolveFromK8s.
	resetMu         sync.Mutex
	lastMapperReset time.Time

	stopEvict chan struct{}
}

func newProcessor(logger *zap.Logger, cfg *Config) *compositionResolverProcessor {
	return &compositionResolverProcessor{
		logger:    logger,
		config:    cfg,
		cache:     make(map[string]cacheEntry),
		stopEvict: make(chan struct{}),
	}
}

func (p *compositionResolverProcessor) start(_ context.Context, _ component.Host) error {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return err
	}

	p.dynClient, err = dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}

	discoClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return err
	}
	cachedDisco := memory.NewMemCacheClient(discoClient)
	p.mapper = restmapper.NewDeferredDiscoveryRESTMapper(cachedDisco)

	p.logger.Info("compositionresolver processor started",
		zap.Duration("cache_ttl", p.config.CacheTTL),
		zap.Duration("negative_cache_ttl", p.config.NegativeCacheTTL),
		zap.String("label_key", p.config.LabelKey))

	go p.evictExpiredEntries()

	return nil
}

func (p *compositionResolverProcessor) shutdown(_ context.Context) error {
	close(p.stopEvict)
	return nil
}

// evictExpiredEntries periodically removes expired cache entries to prevent
// unbounded memory growth. Runs every 60 seconds until shutdown.
func (p *compositionResolverProcessor) evictExpiredEntries() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			p.cacheMu.Lock()
			evicted := 0
			for uid, entry := range p.cache {
				if now.After(entry.expiresAt) {
					delete(p.cache, uid)
					evicted++
				}
			}
			p.cacheMu.Unlock()
			if evicted > 0 {
				p.logger.Debug("evicted expired cache entries",
					zap.Int("evicted", evicted),
					zap.Int("remaining", len(p.cache)))
			}
		case <-p.stopEvict:
			return
		}
	}
}

// processLogs iterates over all log records and enriches K8s events with
// the krateo.io/composition-id attribute resolved from the involvedObject.
func (p *compositionResolverProcessor) processLogs(ctx context.Context, ld plog.Logs) (plog.Logs, error) {
	for i := 0; i < ld.ResourceLogs().Len(); i++ {
		rl := ld.ResourceLogs().At(i)

		for j := 0; j < rl.ScopeLogs().Len(); j++ {
			sl := rl.ScopeLogs().At(j)
			for k := 0; k < sl.LogRecords().Len(); k++ {
				lr := sl.LogRecords().At(k)
				p.enrichLogRecord(ctx, lr)
			}
		}
	}
	return ld, nil
}

// involvedObjectRef holds the fields we need from the K8s event body.
type involvedObjectRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	UID        string `json:"uid"`
}

type k8sEventBody struct {
	Object struct {
		InvolvedObject involvedObjectRef `json:"involvedObject"`
	} `json:"object"`
}

func (p *compositionResolverProcessor) enrichLogRecord(ctx context.Context, lr plog.LogRecord) {
	ref, ok := p.extractInvolvedObject(lr)
	if !ok || ref.UID == "" {
		return
	}

	compositionID, found := p.lookupCached(ref.UID)
	if !found {
		compositionID = p.resolveFromK8s(ctx, ref)
		p.cacheResult(ref.UID, compositionID)
	}

	if compositionID != "" {
		lr.Attributes().PutStr(p.config.LabelKey, compositionID)
	}
}

// extractInvolvedObject parses the log body (string or map) to get the
// involvedObject reference from a raw K8s watch event.
func (p *compositionResolverProcessor) extractInvolvedObject(lr plog.LogRecord) (involvedObjectRef, bool) {
	var bodyStr string

	switch lr.Body().Type() {
	case pcommon.ValueTypeStr:
		bodyStr = lr.Body().Str()
	case pcommon.ValueTypeMap:
		raw := lr.Body().Map().AsRaw()
		b, err := json.Marshal(raw)
		if err != nil {
			return involvedObjectRef{}, false
		}
		bodyStr = string(b)
	default:
		return involvedObjectRef{}, false
	}

	var evt k8sEventBody
	if err := json.Unmarshal([]byte(bodyStr), &evt); err != nil {
		return involvedObjectRef{}, false
	}

	ref := evt.Object.InvolvedObject
	if ref.Kind == "" || ref.Name == "" {
		return involvedObjectRef{}, false
	}
	return ref, true
}

func (p *compositionResolverProcessor) lookupCached(uid string) (string, bool) {
	p.cacheMu.RLock()
	defer p.cacheMu.RUnlock()

	entry, ok := p.cache[uid]
	if !ok {
		return "", false
	}
	if time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.compositionID, true
}

func (p *compositionResolverProcessor) cacheResult(uid, compositionID string) {
	ttl := p.config.CacheTTL
	if compositionID == "" {
		ttl = p.config.NegativeCacheTTL
	}

	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	p.cache[uid] = cacheEntry{
		compositionID: compositionID,
		expiresAt:     time.Now().Add(ttl),
	}
}

// resolveFromK8s fetches the involvedObject from the K8s API and reads its
// krateo.io/composition-id label.  This mirrors what the Krateo EventRouter
// does in labels.go → findCompositionID().
//
// When the involvedObject IS a composition (GVK group == composition.krateo.io)
// and it carries no inherited composition-id label — i.e. an editor-created ROOT
// composition — its own metadata.uid is returned as the composition-id so its
// reconcile events stay correlatable. Installer-created children keep returning
// the inherited label unchanged; non-composition objects are unaffected.
// invalidateMapper drops the cached discovery information so the next
// RESTMapping re-discovers, and reports whether it actually did.
//
// WHY THIS EXISTS AT ALL — the upstream self-heal cannot fire here (#22).
// DeferredDiscoveryRESTMapper.RESTMappings retries only when the mapping misses
// AND the discovery cache is not "fresh":
//
//	if len(ms) == 0 && !d.cl.Fresh() { d.Reset(); ... }
//
// but memCacheClient.Fresh() reports whether the cache is POPULATED AT ALL, not
// whether it is current — its own comment says so — and cacheValid only goes
// false in Invalidate(). So once discovery succeeds at startup Fresh() is
// permanently true, the retry branch is dead, and nothing re-discovers for the
// life of the process.
//
// That is fatal on Krateo specifically, because the served apiVersion IS the
// chart version: every chart bump mints a new group/version. Measured on
// krateo-057, composition event correlation went to 0% within ~10h of the pod
// starting and stayed there for days (2,077 consecutive unlabelled events before
// a restart, 53/53 labelled after), with no log output because both failure
// paths are Debug and the collector runs at Info (#23).
//
// THROTTLED, because a reset is a full re-discovery. Events legitimately
// reference kinds this cluster does not serve, and resetting per event would
// turn a steady trickle of unknown kinds into a discovery storm against the
// apiserver. The interval reuses NegativeCacheTTL — already "how long before we
// re-check something we could not resolve" — rather than adding a knob that
// would have to be kept in step with it.
func (p *compositionResolverProcessor) invalidateMapper() bool {
	rm, ok := p.mapper.(meta.ResettableRESTMapper)
	if !ok {
		// Defensive: every mapper this processor is wired with today IS
		// resettable (the deferred one in start(), and meta.PriorityRESTMapper
		// which the tests use), but the field is typed as the plain
		// meta.RESTMapper interface, so a future substitution need not be.
		// Report false rather than silently claiming a re-discovery happened.
		return false
	}
	p.resetMu.Lock()
	defer p.resetMu.Unlock()
	if !p.lastMapperReset.IsZero() && time.Since(p.lastMapperReset) < p.config.NegativeCacheTTL {
		return false
	}
	p.lastMapperReset = time.Now()
	rm.Reset()
	return true
}

func (p *compositionResolverProcessor) resolveFromK8s(ctx context.Context, ref involvedObjectRef) string {
	gvk := parseGVK(ref.APIVersion, ref.Kind)

	mapping, err := p.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil && meta.IsNoMatchError(err) && p.invalidateMapper() {
		// The group/version may have been minted after our discovery cache was
		// populated — re-discover once and retry. Only on a NO-MATCH: a
		// transient discovery or network error is not evidence of staleness and
		// must not trigger a re-discovery.
		p.logger.Info("re-discovering API resources after a REST mapping miss",
			zap.String("kind", ref.Kind),
			zap.String("apiVersion", ref.APIVersion))
		mapping, err = p.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	}
	if err != nil {
		p.logger.Debug("REST mapping not found",
			zap.String("kind", ref.Kind),
			zap.String("apiVersion", ref.APIVersion),
			zap.Error(err))
		return ""
	}

	var ri dynamic.ResourceInterface
	if mapping.Scope.Name() == meta.RESTScopeNameRoot {
		ri = p.dynClient.Resource(mapping.Resource)
	} else {
		ri = p.dynClient.Resource(mapping.Resource).Namespace(ref.Namespace)
	}

	obj, err := ri.Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		p.logger.Debug("failed to GET involvedObject",
			zap.String("kind", ref.Kind),
			zap.String("name", ref.Name),
			zap.String("namespace", ref.Namespace),
			zap.Error(err))
		return ""
	}

	labels := obj.GetLabels()
	id := labels[p.config.LabelKey] // labels may be nil; map index on nil map is "" (safe)
	if id == "" && gvk.Group == "composition.krateo.io" {
		// The event is about a composition itself and it carries no inherited
		// composition-id label (an editor-created ROOT composition). Use its OWN
		// uid as the composition-id so its events correlate. We patch the EVENT,
		// never the composition object.
		return string(obj.GetUID())
	}
	return id
}

// parseGVK splits "apiVersion" (e.g. "apps/v1" or "v1") and kind into a GVK.
func parseGVK(apiVersion, kind string) schema.GroupVersionKind {
	gv, _ := schema.ParseGroupVersion(apiVersion)
	return gv.WithKind(kind)
}
