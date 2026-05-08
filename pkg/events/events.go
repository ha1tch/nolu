// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Package events defines nolu's event bus abstraction.
//
// The event bus is the substrate that carries registry events across process
// boundaries — to xolu instances, to the ActivityPub social layer, to
// monitoring systems, and to any future subscriber.
//
// The interface is intentionally substrate-agnostic. The initial implementation
// will use an in-process channel bus sufficient for single-node deployments.
// The production implementation is expected to use NATS JetStream. A future
// implementation may use aulsql when that project matures to carry this load.
//
// Substrate requirements:
//
//   - At-least-once delivery (duplicate events must be tolerable by consumers)
//   - Durable streams: a consumer that reconnects must be able to replay from
//     its last acknowledged position
//   - Subject-based routing: consumers subscribe by subject pattern, not by
//     polling a shared queue
//
// Subject naming convention:
//
//	nolu.events.<kind>.<entity-type>
//
// Examples:
//
//	nolu.events.registered.devices
//	nolu.events.transferred.shelves
//	nolu.events.retired.users
//
// Wildcard subscriptions are supported by the bus; the exact syntax depends
// on the backend (NATS uses '>' for recursive wildcards, '*' for single
// token wildcards).
package events

import (
	"context"
	"fmt"
	"time"

	"github.com/ha1tch/nolu/pkg/identity"
)

// Envelope is the wire format for all nolu events published to the bus.
// It wraps a registry.Event with routing metadata so that consumers can
// filter and route without deserialising the full payload.
type Envelope struct {
	// ID is a unique identifier for this event instance.
	// Consumers use this for deduplication.
	ID string `json:"id"`

	// Subject is the routing subject for this event.
	// Format: "nolu.events.<kind>.<entity-type>"
	Subject string `json:"subject"`

	// GlobalID is the affected entity's global identity.
	GlobalID identity.GlobalID `json:"global_id"`

	// Kind is the event type (registered, transferred, retired).
	Kind string `json:"kind"`

	// EntityType is the entity type extracted from GlobalID for routing.
	EntityType string `json:"entity_type"`

	// At is the time the event was emitted by the registry.
	At time.Time `json:"at"`

	// Payload is the full event body, JSON-encoded.
	// Consumers that need the full Record can unmarshal this.
	Payload []byte `json:"payload"`
}

// SubjectFor returns the canonical NATS subject for an event.
func SubjectFor(kind, entityType string) string {
	return fmt.Sprintf("nolu.events.%s.%s", kind, entityType)
}

// SubjectAll is the wildcard subject that matches all nolu events.
// In NATS syntax: "nolu.events.>"
const SubjectAll = "nolu.events.>"

// Bus is the event bus interface. Implementations must be safe for
// concurrent use.
type Bus interface {
	// Publish sends an event envelope to the bus.
	// Returns when the event has been accepted by the bus infrastructure
	// (not necessarily delivered to all consumers).
	Publish(ctx context.Context, env Envelope) error

	// Subscribe registers a consumer on the given subject pattern.
	// The handler is called for each matching event. The consumer is
	// identified by name for durable stream position tracking.
	//
	// The bus guarantees at-least-once delivery. Handlers must be
	// idempotent. A non-nil error from the handler causes the bus to
	// redeliver the event (up to the backend's retry limit).
	//
	// Returns a cancel function that unsubscribes the consumer.
	Subscribe(ctx context.Context, consumerName, subject string, handler func(Envelope) error) (cancel func(), err error)

	// Close shuts down the bus connection gracefully.
	Close() error
}

// NoopBus is a Bus implementation that discards all events. Useful for
// testing and for minimal single-node deployments that do not need
// cross-process event delivery.
type NoopBus struct{}

func (NoopBus) Publish(_ context.Context, _ Envelope) error { return nil }
func (NoopBus) Subscribe(_ context.Context, _, _ string, _ func(Envelope) error) (func(), error) {
	return func() {}, nil
}
func (NoopBus) Close() error { return nil }

// MemoryBus is an in-process bus backed by Go channels. It does not
// provide durability or persistence — events are lost if the process
// restarts. Intended for development and test environments.
//
// For production use, replace with a NATS JetStream implementation.
type MemoryBus struct {
	subs map[string][]memSub
}

type memSub struct {
	name    string
	subject string
	handler func(Envelope) error
}

// NewMemoryBus creates a new in-process MemoryBus.
func NewMemoryBus() *MemoryBus {
	return &MemoryBus{subs: make(map[string][]memSub)}
}

func (b *MemoryBus) Publish(_ context.Context, env Envelope) error {
	for subj, subs := range b.subs {
		if matchSubject(subj, env.Subject) {
			for _, s := range subs {
				// Best-effort: ignore handler errors in memory bus.
				_ = s.handler(env)
			}
		}
	}
	return nil
}

func (b *MemoryBus) Subscribe(_ context.Context, name, subject string, handler func(Envelope) error) (func(), error) {
	b.subs[subject] = append(b.subs[subject], memSub{name: name, subject: subject, handler: handler})
	return func() {
		subs := b.subs[subject]
		for i, s := range subs {
			if s.name == name {
				b.subs[subject] = append(subs[:i], subs[i+1:]...)
				return
			}
		}
	}, nil
}

func (b *MemoryBus) Close() error { return nil }

// matchSubject checks whether a subscription subject pattern matches an
// event subject. Supports '>' as a recursive wildcard suffix.
func matchSubject(pattern, subject string) bool {
	if pattern == subject {
		return true
	}
	if len(pattern) > 0 && pattern[len(pattern)-1] == '>' {
		prefix := pattern[:len(pattern)-1]
		return len(subject) >= len(prefix) && subject[:len(prefix)] == prefix
	}
	return false
}

// Compile-time interface assertions.
var _ Bus = NoopBus{}
var _ Bus = (*MemoryBus)(nil)

// ActivityPubBridge is a future Bus decorator that publishes selected events
// as ActivityPub activities to a configured outbox endpoint. It wraps an
// inner Bus and intercepts Publish calls, forwarding them both to the inner
// bus and to the ActivityPub layer.
//
// This type is declared here as a placeholder; the implementation is deferred
// until the social layer design is finalised. The interface is intentionally
// simple: the bridge is a Bus that wraps another Bus.
type ActivityPubBridge struct {
	Inner      Bus
	OutboxURL  string
	// FilterKinds restricts which event kinds are forwarded to ActivityPub.
	// If empty, all kinds are forwarded.
	FilterKinds []string
}

func (a *ActivityPubBridge) Publish(ctx context.Context, env Envelope) error {
	// Forward to inner bus unconditionally.
	if err := a.Inner.Publish(ctx, env); err != nil {
		return err
	}
	// ActivityPub forwarding: not yet implemented.
	// When implemented, this will POST an ActivityPub Create/Update/Delete
	// activity to a.OutboxURL, with the envelope's GlobalID as the object.
	return nil
}

func (a *ActivityPubBridge) Subscribe(ctx context.Context, name, subject string, handler func(Envelope) error) (func(), error) {
	return a.Inner.Subscribe(ctx, name, subject, handler)
}

func (a *ActivityPubBridge) Close() error { return a.Inner.Close() }

var _ Bus = (*ActivityPubBridge)(nil)

// Sentinel timestamp used by tests to verify event ordering.
var EpochZero = time.Time{}
