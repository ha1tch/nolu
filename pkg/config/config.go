// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package config

// Config is nolu's runtime configuration.
type Config struct {
	// RegistryHost is this nolu instance's hostname, used as the authority
	// component in minted GlobalIDs.
	// Example: "registry.acme.com"
	RegistryHost string `json:"registry_host" env:"NOLU_REGISTRY_HOST"`

	// ListenAddr is the address the HTTP API listens on.
	// Default: ":7070"
	ListenAddr string `json:"listen_addr" env:"NOLU_LISTEN_ADDR"`

	// StorageType is the backend used to persist registry records.
	// "xolu" (default) — uses a dedicated xolu instance.
	// "memory"         — in-process only, no durability (development).
	StorageType string `json:"storage_type" env:"NOLU_STORAGE_TYPE"`

	// XoluURL is the xolu instance URL used when StorageType is "xolu".
	XoluURL string `json:"xolu_url" env:"NOLU_XOLU_URL"`

	// BusType is the event bus backend.
	// "memory" (default) — in-process channels, no durability.
	// "nats"             — NATS JetStream (production).
	BusType string `json:"bus_type" env:"NOLU_BUS_TYPE"`

	// NATSUrl is the NATS server URL when BusType is "nats".
	// Example: "nats://localhost:4222"
	NATSUrl string `json:"nats_url" env:"NOLU_NATS_URL"`

	// NATSStreamName is the JetStream stream name.
	// Default: "NOLU_EVENTS"
	NATSStreamName string `json:"nats_stream_name" env:"NOLU_NATS_STREAM"`

	// NoluURL is used by the embedded proxy in sidecar resolution mode.
	// When set, the proxy resolves tenants by calling this nolu instance's
	// /tenants/{name}/locate endpoint rather than the in-process registry.
	// Leave empty for embedded mode (default).
	// Env: NOLU_URL
	NoluURL string `json:"nolu_url" env:"NOLU_URL"`

	// LogLevel is the zerolog level string.
	// Default: "info"
	LogLevel string `json:"log_level" env:"NOLU_LOG_LEVEL"`

	// HotswapEnabled controls whether the hotswap state machine is active.
	// Default: true
	HotswapEnabled bool `json:"hotswap_enabled" env:"NOLU_HOTSWAP_ENABLED"`

	// HotswapStorageType selects the hotswap manager backend: "memory" or "xolu".
	// When "xolu", the same XoluURL as the registry is used.
	// Default: same as StorageType
	// Env: NOLU_HOTSWAP_STORAGE
	HotswapStorageType string `json:"hotswap_storage_type" env:"NOLU_HOTSWAP_STORAGE"`

	// QuiesceTimeout is the default timeout for waiting for a xolu instance
	// to confirm tenant quiesce. Operator can override per-request.
	// Default: 30s
	QuiesceTimeout string `json:"quiesce_timeout" env:"NOLU_QUIESCE_TIMEOUT"`
}

// Default returns a Config populated with safe development defaults.
func Default() Config {
	return Config{
		RegistryHost:   "localhost",
		ListenAddr:     ":7070",
		StorageType:    "memory",
		BusType:        "memory",
		NATSStreamName:  "NOLU_EVENTS",
		LogLevel:        "info",
		HotswapEnabled:  true,
		QuiesceTimeout:  "30s",
	}
}
