// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package timeseries

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
)

// RetentionWorker runs periodic retention sweeps against all provisioned
// tenant stores managed by a Manager.
type RetentionWorker struct {
	manager  *DefaultManager
	interval time.Duration
	stop     chan struct{}
	done     chan struct{}
}

// NewRetentionWorker creates a RetentionWorker that calls Purge on every
// open store at the given interval.
func NewRetentionWorker(m *DefaultManager, interval time.Duration) *RetentionWorker {
	return &RetentionWorker{
		manager:  m,
		interval: interval,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start launches the retention goroutine.
func (w *RetentionWorker) Start() {
	go w.run()
}

// Stop signals the worker to stop and waits for it to exit.
func (w *RetentionWorker) Stop() {
	close(w.stop)
	<-w.done
}

func (w *RetentionWorker) run() {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.sweep()
		}
	}
}

func (w *RetentionWorker) sweep() {
	ctx, cancel := context.WithTimeout(context.Background(), w.interval/2)
	defer cancel()

	w.manager.stores.Range(func(key, value any) bool {
		store := value.(Store)
		if err := store.Purge(ctx); err != nil {
			log.Warn().Err(err).Uint16("tenant", key.(uint16)).Msg("ts retention purge failed")
		}
		return true
	})
}
