// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Package identity defines the global entity identity model for nolu.
//
// Within a single xolu instance, entities are identified by (entity_type, id)
// pairs, scoped to a tenant. Across xolu instances these identifiers are
// not globally unique — two separate instances may each have a "devices:42"
// entity with no relationship to each other.
//
// nolu assigns every entity a GlobalID: a stable, portable URI that persists
// across ownership changes, instance migrations, and tenant reassignments.
// The GlobalID is the single piece of identity that nolu owns; everything
// else stays inside the xolu instances.
//
// GlobalID format:
//
//	nolu://<registry-host>/<entity-type>/<uuid>
//
// Example:
//
//	nolu://registry.acme.com/devices/01920d4e-9f3b-7a2c-8e1f-4b5c6d7e8f9a
//
// The registry-host component allows federated nolu deployments where
// different registries govern different entity namespaces. A single nolu
// instance is its own registry.
//
// LocalRef is the xolu-side handle: the instance address, tenant ID,
// entity type, and local integer ID. The registry maintains the mapping
// GlobalID ↔ LocalRef and updates it when entities change hands.
package identity

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// GlobalID is a stable, portable identifier for an entity across all xolu
// instances in a nolu federation. Once minted, a GlobalID is never reused
// or reassigned.
type GlobalID string

// MintGlobalID creates a new GlobalID for the given entity type under the
// given registry host. The UUID component is a new random v7 UUID, providing
// monotonic sortability within a registry.
func MintGlobalID(registryHost, entityType string) GlobalID {
	id := uuid.New() // v4; upgrade to v7 when available in the uuid package
	return GlobalID(fmt.Sprintf("nolu://%s/%s/%s", registryHost, entityType, id.String()))
}

// EntityType extracts the entity type segment from a GlobalID.
// Returns an error if the GlobalID is malformed.
func (g GlobalID) EntityType() (string, error) {
	parts, err := g.parse()
	if err != nil {
		return "", err
	}
	return parts[1], nil
}

// RegistryHost extracts the registry host segment from a GlobalID.
func (g GlobalID) RegistryHost() (string, error) {
	parts, err := g.parse()
	if err != nil {
		return "", err
	}
	return parts[0], nil
}

// UUID extracts the UUID segment from a GlobalID.
func (g GlobalID) UUID() (string, error) {
	parts, err := g.parse()
	if err != nil {
		return "", err
	}
	return parts[2], nil
}

func (g GlobalID) parse() ([]string, error) {
	s := string(g)
	if !strings.HasPrefix(s, "nolu://") {
		return nil, fmt.Errorf("identity: malformed GlobalID %q: missing nolu:// scheme", s)
	}
	s = strings.TrimPrefix(s, "nolu://")
	// s is now "registry-host/entity-type/uuid"
	parts := strings.SplitN(s, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, fmt.Errorf("identity: malformed GlobalID %q: expected host/type/uuid", string(g))
	}
	return parts, nil
}

// Validate returns an error if the GlobalID is not well-formed.
func (g GlobalID) Validate() error {
	_, err := g.parse()
	return err
}

// String implements fmt.Stringer.
func (g GlobalID) String() string { return string(g) }

// LocalRef is the xolu-side handle for an entity: the instance base URL,
// tenant ID (0 for unscoped), entity type, and local integer ID.
//
// LocalRef is intentionally not a URI — it carries structured fields so
// that nolu can construct the correct xolu API call without string parsing.
type LocalRef struct {
	// InstanceURL is the base URL of the xolu REST API.
	// Example: "https://xolu.acme.com" or "http://localhost:9090"
	InstanceURL string `json:"instance_url"`

	// TenantID is the xolu tenant. 0 means no tenant scoping.
	TenantID uint16 `json:"tenant_id"`

	// EntityType is the xolu entity collection name.
	// Example: "devices", "shelves", "users"
	EntityType string `json:"entity_type"`

	// LocalID is the xolu integer ID within the entity collection.
	LocalID int `json:"local_id"`

	// TenantName is the human-readable tenant name, if known.
	// This field is optional and informational — it is not used for equality
	// checks or routing decisions. Set it when the name is available at the
	// call site (e.g. from the HTTP request path) so the tenant directory
	// can track name→instance mappings without a separate lookup.
	// Empty means "name unknown"; the directory falls back to TenantID-based
	// tracking in that case.
	TenantName string `json:"tenant_name,omitempty"`
}

// String returns a human-readable representation of the LocalRef.
func (r LocalRef) String() string {
	if r.TenantID == 0 {
		return fmt.Sprintf("%s/%s/%d", r.InstanceURL, r.EntityType, r.LocalID)
	}
	return fmt.Sprintf("%s/tenant/%04X/%s/%d", r.InstanceURL, r.TenantID, r.EntityType, r.LocalID)
}

// OwnershipRecord is the registry's canonical record of which xolu instance
// and tenant currently owns a global entity.
//
// The registry stores one OwnershipRecord per GlobalID. When an entity changes
// hands, the registry creates a new OwnershipRecord with the new LocalRef and
// appends the old one to the History slice. This gives nolu a complete
// audit trail of every transfer without requiring any xolu instance to share
// its internal data.
type OwnershipRecord struct {
	GlobalID  GlobalID  `json:"global_id"`
	Current   LocalRef  `json:"current"`
	Since     time.Time `json:"since"`
	History   []Transfer `json:"history,omitempty"`
}

// Transfer records a single ownership transition.
type Transfer struct {
	From      LocalRef  `json:"from"`
	To        LocalRef  `json:"to"`
	At        time.Time `json:"at"`
	// Protocol is the transfer agreement identifier, if any.
	// This is an opaque string that the application layer defines —
	// e.g. a purchase order number, a contract ID, or a UUID.
	Protocol  string    `json:"protocol,omitempty"`
	// HistoryFrom specifies how much of the entity's history travels
	// with it. "none" means the new owner starts fresh; "full" means
	// the full event stream is replayed; a time.Time value encoded as
	// RFC3339 means replay from that point forward.
	HistoryFrom string   `json:"history_from,omitempty"`
}
