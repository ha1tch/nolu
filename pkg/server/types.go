// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"time"

	"github.com/ha1tch/nolu/pkg/identity"
	"github.com/ha1tch/nolu/pkg/registry"
	"github.com/ha1tch/nolu/pkg/transfer"
)

// Type aliases to resolve the bridge types declared in server.go.
type identity_GlobalID = identity.GlobalID
type localRef_type = identity.LocalRef

// ── Record serialisation ──────────────────────────────────────────────────────

func recordToJSON(rec *registry.Record) map[string]interface{} {
	if rec == nil {
		return nil
	}
	history := make([]interface{}, len(rec.History))
	for i, t := range rec.History {
		history[i] = transferToJSON(t)
	}
	return map[string]interface{}{
		"global_id":  string(rec.GlobalID),
		"status":     string(rec.Status),
		"current":    refToJSON(rec.Current),
		"since":      rec.Since.UTC().Format(time.RFC3339),
		"history":    history,
		"created_at": rec.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at": rec.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func refToJSON(ref identity.LocalRef) map[string]interface{} {
	return map[string]interface{}{
		"instance_url": ref.InstanceURL,
		"tenant_id":    ref.TenantID,
		"entity_type":  ref.EntityType,
		"local_id":     ref.LocalID,
	}
}

func transferToJSON(t identity.Transfer) map[string]interface{} {
	m := map[string]interface{}{
		"from":     refToJSON(t.From),
		"to":       refToJSON(t.To),
		"at":       t.At.UTC().Format(time.RFC3339),
	}
	if t.Protocol != "" {
		m["protocol"] = t.Protocol
	}
	if t.HistoryFrom != "" {
		m["history_from"] = t.HistoryFrom
	}
	return m
}

// ── Proposal serialisation ────────────────────────────────────────────────────

func proposalToJSON(p *transfer.Proposal) map[string]interface{} {
	if p == nil {
		return nil
	}
	m := map[string]interface{}{
		"id":        p.ID,
		"global_id": string(p.GlobalID),
		"from":      refToJSON(p.From),
		"to":        refToJSON(p.To),
		"state":     string(p.State),
		"history_offer": map[string]interface{}{
			"mode": p.HistoryOffer.Mode,
			"note": p.HistoryOffer.Note,
		},
		"proposed_at": p.ProposedAt.UTC().Format(time.RFC3339),
		"updated_at":  p.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if p.Protocol != "" {
		m["protocol"] = p.Protocol
	}
	if p.HistorySpec != nil {
		m["history_spec"] = map[string]interface{}{
			"mode": p.HistorySpec.Mode,
			"from": p.HistorySpec.From.UTC().Format(time.RFC3339),
		}
	}
	if p.RejectionReason != "" {
		m["rejection_reason"] = p.RejectionReason
	}
	return m
}

// identity is a helper to convert a plain string to identity.GlobalID.
func toGlobalID(s string) identity.GlobalID {
	return identity.GlobalID(s)
}
