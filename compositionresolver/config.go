package compositionresolver

import (
	"fmt"
	"time"
)

type Config struct {
	// CacheTTL controls how long a resolved composition-id stays cached before
	// re-querying the K8s API.  A longer TTL reduces API pressure but delays
	// detection of label changes on the involvedObject.
	CacheTTL time.Duration `mapstructure:"cache_ttl"`

	// NegativeCacheTTL is the TTL for caching "not found" results (resources
	// that exist but have no krateo.io/composition-id label, or that could
	// not be resolved at all). Shorter than CacheTTL so we re-check sooner.
	NegativeCacheTTL time.Duration `mapstructure:"negative_cache_ttl"`

	// LabelKey is the Kubernetes label on the involvedObject that holds the
	// composition identifier (the composition resource's UID).
	LabelKey string `mapstructure:"label_key"`

	// NameLabelKey is the label on the involvedObject that holds the
	// composition's metadata.name, and the attribute the name is written to.
	// Empty disables the attribute.
	NameLabelKey string `mapstructure:"name_label_key"`

	// NamespaceLabelKey is the label on the involvedObject that holds the
	// composition's metadata.namespace, and the attribute the namespace is
	// written to. Empty disables the attribute.
	NamespaceLabelKey string `mapstructure:"namespace_label_key"`
}

func (c *Config) Validate() error {
	if c.CacheTTL <= 0 {
		return fmt.Errorf("cache_ttl must be positive, got %s", c.CacheTTL)
	}
	if c.NegativeCacheTTL <= 0 {
		return fmt.Errorf("negative_cache_ttl must be positive, got %s", c.NegativeCacheTTL)
	}
	if c.LabelKey == "" {
		return fmt.Errorf("label_key must not be empty")
	}
	// Each key names an attribute on the same record, so two equal keys would
	// silently overwrite one value with another.
	if c.NameLabelKey != "" && (c.NameLabelKey == c.LabelKey || c.NameLabelKey == c.NamespaceLabelKey) {
		return fmt.Errorf("name_label_key %q must differ from label_key and namespace_label_key", c.NameLabelKey)
	}
	if c.NamespaceLabelKey != "" && c.NamespaceLabelKey == c.LabelKey {
		return fmt.Errorf("namespace_label_key %q must differ from label_key", c.NamespaceLabelKey)
	}
	return nil
}
