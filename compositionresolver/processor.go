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
}

func newProcessor(logger *zap.Logger, cfg *Config) *compositionResolverProcessor {
	return &compositionResolverProcessor{
		logger: logger,
		config: cfg,
		cache:  make(map[string]cacheEntry),
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

	return nil
}

func (p *compositionResolverProcessor) shutdown(_ context.Context) error {
	return nil
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
func (p *compositionResolverProcessor) resolveFromK8s(ctx context.Context, ref involvedObjectRef) string {
	gvk := parseGVK(ref.APIVersion, ref.Kind)

	mapping, err := p.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
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
	if labels == nil {
		return ""
	}
	return labels[p.config.LabelKey]
}

// parseGVK splits "apiVersion" (e.g. "apps/v1" or "v1") and kind into a GVK.
func parseGVK(apiVersion, kind string) schema.GroupVersionKind {
	gv, _ := schema.ParseGroupVersion(apiVersion)
	return gv.WithKind(kind)
}
