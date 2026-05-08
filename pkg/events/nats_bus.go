// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/rs/zerolog/log"
)

// NATSBus is a durable, at-least-once event bus backed by NATS JetStream.
//
// All nolu events are published to a single JetStream stream whose subjects
// match the pattern "nolu.events.>". Consumers receive events from named
// durable push consumers, which retain their position across reconnects and
// process restarts.
//
// Stream configuration (created on first connect if absent):
//
//	Name:        configurable (default "NOLU_EVENTS")
//	Subjects:    ["nolu.events.>"]
//	Storage:     File (durable across restarts)
//	Retention:   LimitsPolicy (time-based, default 7 days)
//	MaxMsgSize:  1 MiB
//	Replicas:    1 (increase for HA deployments)
type NATSBus struct {
	nc         *nats.Conn
	js         jetstream.JetStream
	streamName string
	streamTTL  time.Duration
}

// NATSBusConfig holds configuration for NATSBus.
type NATSBusConfig struct {
	// URL is the NATS server URL, e.g. "nats://localhost:4222"
	URL string
	// StreamName is the JetStream stream name. Default: "NOLU_EVENTS"
	StreamName string
	// StreamTTL is how long messages are retained. Default: 7 days.
	StreamTTL time.Duration
	// ConnectTimeout is the timeout for the initial connection. Default: 10s.
	ConnectTimeout time.Duration
	// MaxReconnects is the number of reconnect attempts. Default: -1 (unlimited).
	MaxReconnects int
}

func (c *NATSBusConfig) applyDefaults() {
	if c.StreamName == "" {
		c.StreamName = "NOLU_EVENTS"
	}
	if c.StreamTTL == 0 {
		c.StreamTTL = 7 * 24 * time.Hour
	}
	if c.ConnectTimeout == 0 {
		c.ConnectTimeout = 10 * time.Second
	}
	if c.MaxReconnects == 0 {
		c.MaxReconnects = -1 // unlimited
	}
}

// NewNATSBus creates a NATSBus, connects to the NATS server, and ensures the
// JetStream stream exists. It blocks until the connection is established or
// the timeout elapses.
func NewNATSBus(cfg NATSBusConfig) (*NATSBus, error) {
	cfg.applyDefaults()

	opts := []nats.Option{
		nats.Name("nolu"),
		nats.MaxReconnects(cfg.MaxReconnects),
		nats.ReconnectWait(2 * time.Second),
		nats.Timeout(cfg.ConnectTimeout),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.Warn().Err(err).Msg("nats: disconnected")
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.Info().Str("url", nc.ConnectedUrl()).Msg("nats: reconnected")
		}),
	}

	nc, err := nats.Connect(cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("nats_bus: connect to %q: %w", cfg.URL, err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("nats_bus: jetstream init: %w", err)
	}

	b := &NATSBus{
		nc:         nc,
		js:         js,
		streamName: cfg.StreamName,
		streamTTL:  cfg.StreamTTL,
	}

	if err := b.ensureStream(context.Background()); err != nil {
		nc.Close()
		return nil, err
	}

	log.Info().
		Str("url", cfg.URL).
		Str("stream", cfg.StreamName).
		Msg("nats_bus: connected and stream ready")

	return b, nil
}

// ensureStream creates the NOLU_EVENTS stream if it does not already exist,
// or updates it if the configuration has changed.
func (b *NATSBus) ensureStream(ctx context.Context) error {
	streamCfg := jetstream.StreamConfig{
		Name:        b.streamName,
		Subjects:    []string{"nolu.events.>"},
		Storage:     jetstream.FileStorage,
		Retention:   jetstream.LimitsPolicy,
		MaxAge:      b.streamTTL,
		MaxMsgSize:  1 << 20, // 1 MiB
		Replicas:    1,
		Description: "nolu federated entity registry events",
	}

	_, err := b.js.CreateOrUpdateStream(ctx, streamCfg)
	if err != nil {
		return fmt.Errorf("nats_bus: ensure stream %q: %w", b.streamName, err)
	}
	return nil
}

// Publish serialises the Envelope to JSON and publishes it to the NATS subject
// derived from env.Subject. Returns when the publish is acknowledged by the
// JetStream server.
func (b *NATSBus) Publish(ctx context.Context, env Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("nats_bus: marshal envelope: %w", err)
	}

	ack, err := b.js.Publish(ctx, env.Subject, data)
	if err != nil {
		return fmt.Errorf("nats_bus: publish to %q: %w", env.Subject, err)
	}

	log.Debug().
		Str("subject", env.Subject).
		Str("global_id", string(env.GlobalID)).
		Uint64("seq", ack.Sequence).
		Msg("nats_bus: published")

	return nil
}

// Subscribe creates a durable JetStream push consumer for the given subject
// pattern. The consumer is named consumerName; if a consumer with this name
// already exists the bus resumes from its last acknowledged position.
//
// The handler is called for each message in a goroutine. Returning a non-nil
// error from the handler causes the message to be negatively acknowledged
// (NakMsg), which re-delivers it after the server's backoff window.
//
// The returned cancel function stops the consumer and frees its resources.
func (b *NATSBus) Subscribe(ctx context.Context, consumerName, subject string, handler func(Envelope) error) (func(), error) {
	// Build a durable consumer scoped to the subject filter.
	consumerCfg := jetstream.ConsumerConfig{
		Name:           sanitiseName(consumerName),
		Durable:        sanitiseName(consumerName),
		FilterSubject:  subject,
		DeliverPolicy:  jetstream.DeliverNewPolicy,
		AckPolicy:      jetstream.AckExplicitPolicy,
		MaxDeliver:     5,
		AckWait:        30 * time.Second,
		Description:    fmt.Sprintf("nolu consumer: %s", consumerName),
	}

	stream, err := b.js.Stream(ctx, b.streamName)
	if err != nil {
		return nil, fmt.Errorf("nats_bus: get stream %q: %w", b.streamName, err)
	}

	consumer, err := stream.CreateOrUpdateConsumer(ctx, consumerCfg)
	if err != nil {
		return nil, fmt.Errorf("nats_bus: create consumer %q: %w", consumerName, err)
	}

	// Start consuming in a background goroutine.
	cctx, err := consumer.Consume(func(msg jetstream.Msg) {
		var env Envelope
		if err := json.Unmarshal(msg.Data(), &env); err != nil {
			log.Error().Err(err).Str("consumer", consumerName).Msg("nats_bus: unmarshal failed, discarding")
			_ = msg.Ack() // don't redeliver malformed messages
			return
		}

		if err := handler(env); err != nil {
			log.Warn().Err(err).Str("consumer", consumerName).Str("id", env.ID).Msg("nats_bus: handler error, will redeliver")
			_ = msg.Nak()
			return
		}

		_ = msg.Ack()
	})
	if err != nil {
		return nil, fmt.Errorf("nats_bus: start consume for %q: %w", consumerName, err)
	}

	log.Info().
		Str("consumer", consumerName).
		Str("subject", subject).
		Msg("nats_bus: subscribed")

	return func() {
		cctx.Stop()
		log.Info().Str("consumer", consumerName).Msg("nats_bus: unsubscribed")
	}, nil
}

// Close drains the NATS connection gracefully, waiting for in-flight publishes
// to complete before closing.
func (b *NATSBus) Close() error {
	if err := b.nc.Drain(); err != nil {
		return fmt.Errorf("nats_bus: drain: %w", err)
	}
	log.Info().Msg("nats_bus: closed")
	return nil
}

// sanitiseName replaces characters that NATS consumer names disallow with
// underscores. NATS consumer names may contain alphanumerics, hyphens, and
// underscores only.
func sanitiseName(name string) string {
	out := make([]byte, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' {
			out[i] = c
		} else {
			out[i] = '_'
		}
	}
	return string(out)
}

// Compile-time interface assertion.
var _ Bus = (*NATSBus)(nil)
