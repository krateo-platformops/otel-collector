package compositionresolver

import "time"

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
}

func (c *Config) Validate() error {
	return nil
}
