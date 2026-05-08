// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Package registry — XoluRegistry implementation.
//
// XoluRegistry stores nolu's OwnershipRecords durably in a dedicated xolu
// instance, giving the registry the same ACID guarantees, OQL query
// capability, and operational characteristics as any other xolu deployment.
//
// Storage layout (all within tenant "nolu_registry"):
//
//   Entity type "nolu_records"
//     One document per GlobalID. The document is the full Record serialised
//     as JSON. The xolu integer ID is stable per GlobalID (stored as a field
//     so it can be looked up via OQL) but xolu also assigns its own auto-
//     increment ID which we use for _version-based optimistic concurrency.
//
//   Entity type "nolu_events"
//     Append-only event log. One document per registry event (registered,
//     transferred, retired). Used for auditing and event replay.
//
// Concurrency model:
//
//   xolu's _version field provides optimistic concurrency on writes.
//   Transfer uses xolu's conditional write (POST /save/{id} with _version)
//   to guarantee that the From field matches the current owner atomically.
//   If another Transfer races and wins first, xolu returns 409 and we
//   surface ErrInvalidTransfer to the caller.
//
// Tenant provisioning:
//
//   On first connect, XoluRegistry ensures the "nolu_registry" tenant exists
//   and that both entity collections are accessible. This is idempotent.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ha1tch/nolu/pkg/events"
	"github.com/ha1tch/nolu/pkg/identity"
	"github.com/ha1tch/nolu/pkg/xoluclient"
)

const (
	// xoluTenant is the xolu tenant that owns all nolu registry data.
	xoluTenant = "nolu_registry"

	// xoluRecordsEntity is the entity type for OwnershipRecords.
	xoluRecordsEntity = "nolu_records"

	// xoluEventsEntity is the entity type for the append-only event log.
	xoluEventsEntity = "nolu_events"
)

// XoluRegistry is a durable implementation of Registry backed by a xolu instance.
//
// Thread-safe: the in-process subscription list is protected by a mutex;
// all xolu operations are sent over HTTP and are naturally concurrent.
type XoluRegistry struct {
	client *xoluclient.Client
	bus    events.Bus
	host   string

	mu   sync.Mutex
	subs []memorySub // re-use the same sub type from memory.go
}

// xoluRecord is the JSON shape stored in xolu for each registry record.
// It extends Record with fields that xolu needs for indexing and lookup.
type xoluRecord struct {
	// XoluID is the xolu-assigned integer ID for this document.
	// Populated after the first write; used for subsequent updates.
	XoluID int `json:"xolu_id,omitempty"`

	// GlobalIDStr is the GlobalID stored as a plain string field so that
	// OQL can filter on it (OQL operates on JSON field values).
	GlobalIDStr string `json:"global_id_str"`

	// StatusStr is Status as a plain string for OQL filtering.
	StatusStr string `json:"status_str"`

	// CurrentInstanceURL is the current owner's instance URL, stored as a
	// plain field for OQL-based ListByInstance queries.
	CurrentInstanceURL string `json:"current_instance_url"`

	// CurrentTenantID is the current owner's tenant ID, stored as a plain
	// field for OQL-based ListByInstanceAndTenant queries.
	CurrentTenantID uint16 `json:"current_tenant_id"`

	// EntityTypeStr is the entity type extracted from GlobalID, stored for
	// OQL-based ListByEntityType queries.
	EntityTypeStr string `json:"entity_type_str"`

	// Record is the full registry record, embedded as a nested JSON object.
	Record Record `json:"record"`

	// Version is xolu's _version field, returned on reads and required on
	// conditional writes. Not stored by us — populated from xolu responses.
	Version int `json:"_version,omitempty"`
}

// xoluEvent is the JSON shape stored in xolu for each registry event.
type xoluEvent struct {
	GlobalIDStr string    `json:"global_id_str"`
	Kind        string    `json:"kind"`
	EntityType  string    `json:"entity_type"`
	At          time.Time `json:"at"`
	RecordJSON  string    `json:"record_json"` // JSON-encoded Record snapshot
}

// NewXoluRegistry creates an XoluRegistry backed by the xolu instance at
// baseURL. It connects immediately and provisions the nolu_registry tenant
// and entity collections.
func NewXoluRegistry(ctx context.Context, baseURL, host string, bus events.Bus) (*XoluRegistry, error) {
	client := xoluclient.NewTenant(baseURL, xoluTenant)

	r := &XoluRegistry{
		client: client,
		bus:    bus,
		host:   host,
	}

	if err := r.provision(ctx, baseURL); err != nil {
		return nil, fmt.Errorf("xolu_registry: provision: %w", err)
	}

	return r, nil
}

// provision ensures the nolu_registry tenant and entity collections exist.
// Idempotent — safe to call on every startup.
func (r *XoluRegistry) provision(ctx context.Context, baseURL string) error {
	// Use the root client (no tenant) to check health and provision tenant.
	root := xoluclient.New(baseURL, 0)
	if err := root.Healthy(ctx); err != nil {
		return fmt.Errorf("xolu instance not reachable: %w", err)
	}

	// Ensure the nolu_registry tenant exists.
	if err := root.EnsureTenant(ctx, xoluTenant); err != nil {
		return fmt.Errorf("ensure tenant %q: %w", xoluTenant, err)
	}

	return nil
}

// ── Registry interface ────────────────────────────────────────────────────────

func (r *XoluRegistry) Register(ctx context.Context, registryHost, entityType string, owner identity.LocalRef) (*Record, error) {
	if registryHost == "" {
		registryHost = r.host
	}
	gid := identity.MintGlobalID(registryHost, entityType)

	// Check for pre-existing record (collision guard — should never happen
	// with UUID-based minting, but checked for correctness).
	existing, err := r.getByGlobalID(ctx, gid)
	if err != nil && err != ErrNotFound {
		return nil, fmt.Errorf("xolu_registry register: pre-check: %w", err)
	}
	if existing != nil {
		return nil, ErrAlreadyExists
	}

	now := time.Now().UTC()
	rec := Record{
		OwnershipRecord: identity.OwnershipRecord{
			GlobalID: gid,
			Current:  owner,
			Since:    now,
		},
		Status:    StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}

	xr := r.toXoluRecord(rec, 0)
	data, err := r.marshalXoluRecord(xr)
	if err != nil {
		return nil, err
	}

	result, err := r.client.Create(ctx, xoluRecordsEntity, data)
	if err != nil {
		return nil, fmt.Errorf("xolu_registry register: create: %w", err)
	}

	xoluID, _ := xoluclient.IntID(result)
	rec2 := r.fromXoluResult(result, xoluID)

	r.publish(ctx, Event{
		Kind:     EventRegistered,
		GlobalID: gid,
		Record:   rec2,
		At:       now,
	})
	r.appendEvent(ctx, EventRegistered, gid, rec2, now)

	return rec2, nil
}

func (r *XoluRegistry) Get(ctx context.Context, id identity.GlobalID) (*Record, error) {
	return r.getByGlobalID(ctx, id)
}

func (r *XoluRegistry) Resolve(ctx context.Context, id identity.GlobalID) (identity.LocalRef, error) {
	rec, err := r.getByGlobalID(ctx, id)
	if err != nil {
		return identity.LocalRef{}, err
	}
	if rec.Status == StatusRetired {
		return identity.LocalRef{}, ErrRetired
	}
	return rec.Current, nil
}

func (r *XoluRegistry) Transfer(ctx context.Context, req TransferRequest) (*Record, error) {
	// Fetch the current record including xolu ID and _version.
	xr, xoluID, version, err := r.getXoluRecord(ctx, req.GlobalID)
	if err != nil {
		return nil, err
	}
	if xr.Record.Status == StatusRetired {
		return nil, ErrRetired
	}
	if xr.Record.Current != req.From {
		return nil, fmt.Errorf("%w: expected current owner %s, got %s",
			ErrInvalidTransfer, xr.Record.Current, req.From)
	}

	now := time.Now().UTC()
	xfer := identity.Transfer{
		From:        req.From,
		To:          req.To,
		At:          now,
		Protocol:    req.Protocol,
		HistoryFrom: req.HistoryFrom,
	}
	xr.Record.History = append(xr.Record.History, xfer)
	xr.Record.Current = req.To
	xr.Record.Since = now
	xr.Record.UpdatedAt = now

	updated := r.toXoluRecord(xr.Record, xoluID)
	updated.Version = version // include _version for conditional write
	data, err := r.marshalXoluRecord(updated)
	if err != nil {
		return nil, err
	}

	// Conditional write: xolu will reject with 409 if _version has changed,
	// meaning another Transfer won the race.
	result, err := r.client.Save(ctx, xoluRecordsEntity, xoluID, data)
	if err != nil {
		if isConflict(err) {
			return nil, fmt.Errorf("%w: concurrent transfer detected", ErrInvalidTransfer)
		}
		return nil, fmt.Errorf("xolu_registry transfer: save: %w", err)
	}

	rec := r.fromXoluResult(result, xoluID)
	r.publish(ctx, Event{
		Kind:     EventTransferred,
		GlobalID: req.GlobalID,
		Record:   rec,
		At:       now,
	})
	r.appendEvent(ctx, EventTransferred, req.GlobalID, rec, now)

	return rec, nil
}

func (r *XoluRegistry) Retire(ctx context.Context, id identity.GlobalID, reason string) error {
	xr, xoluID, version, err := r.getXoluRecord(ctx, id)
	if err != nil {
		return err
	}
	if xr.Record.Status == StatusRetired {
		return ErrRetired
	}

	now := time.Now().UTC()
	xr.Record.Status = StatusRetired
	xr.Record.UpdatedAt = now

	updated := r.toXoluRecord(xr.Record, xoluID)
	updated.Version = version
	data, err := r.marshalXoluRecord(updated)
	if err != nil {
		return err
	}

	result, err := r.client.Save(ctx, xoluRecordsEntity, xoluID, data)
	if err != nil {
		if isConflict(err) {
			return fmt.Errorf("%w: concurrent modification detected", ErrInvalidTransfer)
		}
		return fmt.Errorf("xolu_registry retire: save: %w", err)
	}

	rec := r.fromXoluResult(result, xoluID)
	r.publish(ctx, Event{
		Kind:     EventRetired,
		GlobalID: id,
		Record:   rec,
		At:       now,
	})
	r.appendEvent(ctx, EventRetired, id, rec, now)

	return nil
}

func (r *XoluRegistry) Subscribe(ctx context.Context, filter SubscriptionFilter, ch chan<- Event) (func(), error) {
	r.mu.Lock()
	sub := memorySub{filter: filter, ch: ch}
	r.subs = append(r.subs, sub)
	r.mu.Unlock()

	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for i, s := range r.subs {
			if s.ch == ch {
				r.subs = append(r.subs[:i], r.subs[i+1:]...)
				return
			}
		}
	}, nil
}

func (r *XoluRegistry) ListByInstance(ctx context.Context, instanceURL string) ([]identity.GlobalID, error) {
	query := fmt.Sprintf(
		`SELECT global_id_str FROM %s WHERE current_instance_url = '%s' AND status_str = 'active'`,
		xoluRecordsEntity, escapeSingleQuote(instanceURL),
	)
	return r.oqlGlobalIDs(ctx, query)
}

func (r *XoluRegistry) ListByEntityType(ctx context.Context, entityType string) ([]identity.GlobalID, error) {
	query := fmt.Sprintf(
		`SELECT global_id_str FROM %s WHERE entity_type_str = '%s' AND status_str = 'active'`,
		xoluRecordsEntity, escapeSingleQuote(entityType),
	)
	return r.oqlGlobalIDs(ctx, query)
}

func (r *XoluRegistry) ListByInstanceAndTenant(ctx context.Context, instanceURL string, tenantID uint16) ([]identity.GlobalID, error) {
	query := fmt.Sprintf(
		`SELECT global_id_str FROM %s WHERE current_instance_url = '%s' AND current_tenant_id = %d AND status_str = 'active'`,
		xoluRecordsEntity, escapeSingleQuote(instanceURL), tenantID,
	)
	return r.oqlGlobalIDs(ctx, query)
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// getByGlobalID retrieves a Record by GlobalID using OQL.
func (r *XoluRegistry) getByGlobalID(ctx context.Context, id identity.GlobalID) (*Record, error) {
	xr, _, _, err := r.getXoluRecord(ctx, id)
	if err != nil {
		return nil, err
	}
	rec := xr.Record
	return &rec, nil
}

// getXoluRecord retrieves the raw xolu document, its integer ID, and _version.
// Returns (xoluRecord, xoluID, version, error).
func (r *XoluRegistry) getXoluRecord(ctx context.Context, id identity.GlobalID) (*xoluRecord, int, int, error) {
	query := fmt.Sprintf(
		`SELECT * FROM %s WHERE global_id_str = '%s' LIMIT 1`,
		xoluRecordsEntity, escapeSingleQuote(string(id)),
	)
	results, err := r.client.OQL(ctx, query)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("xolu_registry get: oql: %w", err)
	}
	if len(results) == 0 {
		return nil, 0, 0, ErrNotFound
	}

	raw := results[0]
	xr, err := unmarshalXoluRecord(raw)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("xolu_registry get: unmarshal: %w", err)
	}

	xoluID, _ := xoluclient.IntIDFromMap(raw)
	version, _ := intField(raw, "_version")

	return xr, xoluID, version, nil
}

// oqlGlobalIDs runs an OQL query and extracts the global_id_str field from
// each row, converting to GlobalID slice.
func (r *XoluRegistry) oqlGlobalIDs(ctx context.Context, query string) ([]identity.GlobalID, error) {
	results, err := r.client.OQL(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("xolu_registry list: oql: %w", err)
	}
	ids := make([]identity.GlobalID, 0, len(results))
	for _, row := range results {
		if s, ok := row["global_id_str"].(string); ok && s != "" {
			ids = append(ids, identity.GlobalID(s))
		}
	}
	return ids, nil
}

// toXoluRecord converts a Record to the xoluRecord storage shape.
func (r *XoluRegistry) toXoluRecord(rec Record, xoluID int) xoluRecord {
	entityType, _ := rec.GlobalID.EntityType()
	xr := xoluRecord{
		XoluID:             xoluID,
		GlobalIDStr:        string(rec.GlobalID),
		StatusStr:          string(rec.Status),
		CurrentInstanceURL: rec.Current.InstanceURL,
		CurrentTenantID:    rec.Current.TenantID,
		EntityTypeStr:      entityType,
		Record:             rec,
	}
	return xr
}

// marshalXoluRecord serialises an xoluRecord to a map[string]interface{}
// suitable for the xoluclient Create/Save calls.
func (r *XoluRegistry) marshalXoluRecord(xr xoluRecord) (map[string]interface{}, error) {
	b, err := json.Marshal(xr)
	if err != nil {
		return nil, fmt.Errorf("xolu_registry: marshal: %w", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("xolu_registry: unmarshal to map: %w", err)
	}
	return m, nil
}

// unmarshalXoluRecord deserialises a raw xolu response map into an xoluRecord.
func unmarshalXoluRecord(raw map[string]interface{}) (*xoluRecord, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var xr xoluRecord
	if err := json.Unmarshal(b, &xr); err != nil {
		return nil, err
	}
	return &xr, nil
}

// fromXoluResult extracts a Record from a raw xolu response map.
func (r *XoluRegistry) fromXoluResult(raw map[string]interface{}, xoluID int) *Record {
	xr, err := unmarshalXoluRecord(raw)
	if err != nil {
		return nil
	}
	rec := xr.Record
	return &rec
}

// publish delivers an event to in-process subscribers and the event bus.
func (r *XoluRegistry) publish(ctx context.Context, ev Event) {
	r.mu.Lock()
	subs := make([]memorySub, len(r.subs))
	copy(subs, r.subs)
	r.mu.Unlock()

	for _, sub := range subs {
		if matchFilter(sub.filter, ev) {
			select {
			case sub.ch <- ev:
			default:
			}
		}
	}

	if r.bus != nil {
		payload, _ := json.Marshal(ev.Record)
		entityType, _ := ev.GlobalID.EntityType()
		env := events.Envelope{
			ID:         uuid.New().String(),
			Subject:    events.SubjectFor(string(ev.Kind), entityType),
			GlobalID:   ev.GlobalID,
			Kind:       string(ev.Kind),
			EntityType: entityType,
			At:         ev.At,
			Payload:    payload,
		}
		go func() { _ = r.bus.Publish(context.Background(), env) }()
	}
}

// appendEvent writes an event to the nolu_events append-only log in xolu.
// Non-fatal: failures are silently ignored to avoid blocking registry operations.
func (r *XoluRegistry) appendEvent(ctx context.Context, kind EventKind, id identity.GlobalID, rec *Record, at time.Time) {
	entityType, _ := id.EntityType()
	recJSON, _ := json.Marshal(rec)
	ev := xoluEvent{
		GlobalIDStr: string(id),
		Kind:        string(kind),
		EntityType:  entityType,
		At:          at,
		RecordJSON:  string(recJSON),
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	go func() {
		_, _ = r.client.Create(context.Background(), xoluEventsEntity, m)
	}()
}

// matchFilter is the filter predicate shared with MemoryRegistry.
func matchFilter(f SubscriptionFilter, ev Event) bool {
	if len(f.GlobalIDs) > 0 {
		found := false
		for _, id := range f.GlobalIDs {
			if id == ev.GlobalID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(f.EntityTypes) > 0 {
		et, _ := ev.GlobalID.EntityType()
		found := false
		for _, t := range f.EntityTypes {
			if strings.EqualFold(t, et) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// isConflict returns true if the error represents a 409 from xolu.
func isConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "status 409")
}

// escapeSingleQuote escapes single quotes for OQL string literals.
func escapeSingleQuote(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// intField extracts an int from a raw map field (handles float64 from JSON).
func intField(m map[string]interface{}, key string) (int, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

// urlEncode URL-encodes a GlobalID for use in path segments.
func urlEncode(s string) string {
	return url.PathEscape(s)
}

// Compile-time assertion.
var _ Registry = (*XoluRegistry)(nil)
