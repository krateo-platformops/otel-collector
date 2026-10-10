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

// mapperResetInterval bounds how often a REST mapping miss may drop the discovery cache.
// Low enough that a newly-released apiVersion becomes resolvable within a minute, high
// enough that a stream of events naming a kind that does not exist cannot turn every one
// of them into a full re-list of the apiserver's groups and versions.
const mapperResetInterval = 30 * time.Second

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
	// ResettableRESTMapper, not RESTMapper: the deferred mapper caches discovery and
	// MUST be reset when the API surface changes. See resolveFromK8s.
	mapper meta.ResettableRESTMapper

	// lastMapperReset throttles the reset below. A kind that genuinely does not exist
	// fails every time, and resetting per failure would re-list the whole API surface
	// on every such event.
	lastMapperReset time.Time
	mapperMu        sync.Mutex

	cache   map[string]cacheEntry
	cacheMu sync.RWMutex

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
func (p *compositionResolverProcessor) resolveFromK8s(ctx context.Context, ref involvedObjectRef) string {
	gvk := parseGVK(ref.APIVersion, ref.Kind)

	mapping, err := p.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		// The mapper is a DeferredDiscoveryRESTMapper built ONCE in start(). It caches the
		// API surface as discovered at that moment and never refreshes itself. Krateo mints a
		// NEW apiVersion on every component release -- a composition bump takes
		// composition.krateo.io/v1-12-38 to v1-12-41, and four such versions appeared on a
		// single day -- so any kind released after this collector started is permanently
		// unmappable, and the processor silently returns "" for it forever. That is not a
		// transient miss: without a reset it can never recover short of a pod restart.
		//
		// Measured before this fix: 42,580 of 42,592 resolution attempts on root compositions
		// failed, with the fallback firing on twelve. Both failure paths log at Debug while
		// the collector runs at Info, so the feature was silently dead rather than noisy.
		//
		// Reset() drops the cached discovery so the next call re-lists. It is throttled
		// because a genuinely nonexistent kind fails every time and would otherwise re-list
		// the entire API surface per event.
		if p.resetMapper() {
			mapping, err = p.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		}
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

// resetMapper drops the deferred mapper's cached discovery so the next RESTMapping re-lists
// the API surface, and reports whether it actually did so.
//
// Throttled to at most once per mapperResetInterval. A kind that does not exist fails on
// every event, and an unthrottled reset would turn each one into a full re-list of every
// group and version on the apiserver.
func (p *compositionResolverProcessor) resetMapper() bool {
	p.mapperMu.Lock()
	defer p.mapperMu.Unlock()

	if time.Since(p.lastMapperReset) < mapperResetInterval {
		return false
	}
	p.lastMapperReset = time.Now()
	p.mapper.Reset()
	p.logger.Info("reset the discovery cache after a REST mapping miss",
		zap.Duration("throttle", mapperResetInterval))
	return true
}
