// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Package routing maintains the subscription routing table for nolu.
//
// When a user or system wants to follow an entity — receive events whenever
// its ownership or state changes — they register a subscription here.
// The router maps GlobalIDs and entity types to subscriber endpoints, and
// delivers events to the right subscribers when the registry emits them.
//
// This is the ActivityPub "follow" semantic translated into nolu's operational
// model. The vocabulary is deliberately different: "watch" rather than
// "follow", "endpoint" rather than "actor", to avoid importing social network
// connotations into an operational tool.
//
// A subscriber is any system that can receive event envelopes. In practice:
//   - Another xolu instance (receives events about assets it is about to own)
//   - A GoToSocial/ActivityPub outbox (bridges to the social layer)
//   - An operations console (displays live status)
//   - A NATS subject (fans out to further consumers)
//
// The routing table is stored durably. Subscriptions survive process restarts.
package routing

import (
	"context"
	"errors"
	"time"

	"github.com/ha1tch/nolu/pkg/identity"
)

var (
	ErrSubscriptionNotFound = errors.New("routing: subscription not found")
	ErrDuplicateSubscription = errors.New("routing: subscription already exists")
)

// EndpointKind classifies what kind of system a subscriber endpoint is.
type EndpointKind string

const (
	// EndpointXolu is a remote xolu instance REST API.
	EndpointXolu EndpointKind = "xolu"
	// EndpointActivityPub is an ActivityPub actor inbox URL.
	EndpointActivityPub EndpointKind = "activitypub"
	// EndpointWebhook is a generic HTTP POST endpoint.
	EndpointWebhook EndpointKind = "webhook"
	// EndpointNATS is a NATS subject on a remote server.
	EndpointNATS EndpointKind = "nats"
)

// Endpoint describes where event envelopes should be delivered.
type Endpoint struct {
	Kind    EndpointKind      `json:"kind"`
	// URL is the delivery address. For NATS this is the server URL;
	// the Subject field carries the NATS subject.
	URL     string            `json:"url"`
	Subject string            `json:"subject,omitempty"` // NATS subject
	// AuthToken is an opaque bearer token for authenticated endpoints.
	// Stored encrypted at rest; never logged.
	AuthToken string          `json:"auth_token,omitempty"`
}

// WatchScope defines what a subscription watches.
// Exactly one of GlobalIDs or EntityTypes should be set; setting both
// is permitted and means "entities of these types AND these specific IDs".
type WatchScope struct {
	// GlobalIDs watches specific entities by identity.
	GlobalIDs []identity.GlobalID `json:"global_ids,omitempty"`
	// EntityTypes watches all entities of these types.
	EntityTypes []string `json:"entity_types,omitempty"`
	// EventKinds restricts which event kinds are delivered.
	// If empty, all kinds are delivered.
	EventKinds []string `json:"event_kinds,omitempty"`
}

// Subscription is a durable record of a subscriber's interest.
type Subscription struct {
	ID          string      `json:"id"`
	// SubscriberID is an opaque identifier for the subscribing system.
	// For xolu instances, this is the instance URL. For ActivityPub actors,
	// this is the actor URI. For webhooks, this is the endpoint URL.
	SubscriberID string     `json:"subscriber_id"`
	Scope       WatchScope  `json:"scope"`
	Endpoint    Endpoint    `json:"endpoint"`
	CreatedAt   time.Time   `json:"created_at"`
	// Active is false when the subscription has been paused or revoked.
	Active      bool        `json:"active"`
}

// Router maintains the subscription routing table and delivers events to
// matching subscribers.
type Router interface {
	// Watch creates a new subscription. Returns ErrDuplicateSubscription if
	// the same subscriber is already watching the same scope.
	Watch(ctx context.Context, sub Subscription) error

	// Unwatch removes a subscription by ID.
	Unwatch(ctx context.Context, subscriptionID string) error

	// Subscribers returns all active subscriptions that match the given
	// GlobalID and event kind. Used by the event delivery pipeline.
	Subscribers(ctx context.Context, id identity.GlobalID, eventKind string) ([]Subscription, error)

	// Deliver sends an event envelope to all matching subscribers.
	// Delivery is best-effort for webhook/ActivityPub endpoints; NATS
	// delivery uses the bus's durability guarantees.
	// Returns the number of subscribers successfully delivered to.
	Deliver(ctx context.Context, env interface{}) (int, error)

	// ListBySubscriber returns all subscriptions held by a given subscriber.
	ListBySubscriber(ctx context.Context, subscriberID string) ([]Subscription, error)

	// PauseSubscriber suspends all deliveries to a subscriber without
	// deleting the subscriptions. Events accumulate (up to the bus's
	// retention limit) and are replayed when the subscriber resumes.
	PauseSubscriber(ctx context.Context, subscriberID string) error

	// ResumeSubscriber re-activates a paused subscriber.
	ResumeSubscriber(ctx context.Context, subscriberID string) error
}
