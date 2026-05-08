// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	oluerr "github.com/ha1tch/xolu/pkg/errors"
	"github.com/ha1tch/xolu/pkg/timeseries"
)

// --- Request / response types ---

type tsProvisionResponse struct {
	TenantID   string `json:"tenant_id"`
	Timeseries string `json:"timeseries"`
}

// Timeline management

type tsDefineTimelineRequest struct {
	ID            int    `json:"id"`
	Name          string `json:"name,omitempty"`
	Dims          int    `json:"dims"`
	RetentionDays int    `json:"retention_days,omitempty"`
}

type tsTimelineResponse struct {
	ID            int       `json:"id"`
	Name          string    `json:"name,omitempty"`
	Dims          int       `json:"dims"`
	RetentionDays int       `json:"retention_days"`
	CreatedAt     time.Time `json:"created_at"`
	FirstWriteAt  *time.Time `json:"first_write_at,omitempty"`
}

// Write

type tsAppendRequest struct {
	Timeline int       `json:"timeline"`
	Dims     []uint64  `json:"dims"`
	Time     string    `json:"time"`
	Nums     []float64 `json:"nums,omitempty"`
	Payload  any       `json:"payload,omitempty"`
}

type tsBatchAppendRequest struct {
	Events []tsAppendRequest `json:"events"`
}

type tsBatchAppendResponse struct {
	Total    int `json:"total"`
	Accepted int `json:"accepted"`
	Failed   int `json:"failed"`
}

// Query

type tsEventResponse struct {
	Timeline int       `json:"timeline"`
	Dims     []uint64  `json:"dims"`
	Time     time.Time `json:"time"`
	Nums     []float64 `json:"nums,omitempty"`
	Payload  any       `json:"payload,omitempty"`
}

type tsRangeResponse struct {
	Count  uint64            `json:"count"`
	Events []tsEventResponse `json:"events"`
}

// Aggregate

type tsAggregateRequest struct {
	Timeline  int    `json:"timeline"`
	Dims      []uint64 `json:"dims"`
	From      string `json:"from"`
	To        string `json:"to"`
	NumField  int    `json:"num_field"`
	Function  string `json:"function"`
	Interval  string `json:"interval,omitempty"`
}

type tsBucketResponse struct {
	Time  time.Time `json:"time"`
	Value float64   `json:"value"`
	Count uint64    `json:"count"`
}

type tsAggregateResponse struct {
	Timeline int                `json:"timeline"`
	NumField int                `json:"num_field"`
	Function string             `json:"function"`
	Interval string             `json:"interval,omitempty"`
	// Bucketed result
	Buckets []tsBucketResponse `json:"buckets,omitempty"`
	// Scalar result
	Value *float64   `json:"value,omitempty"`
	Count *uint64    `json:"count,omitempty"`
	From  *time.Time `json:"from,omitempty"`
	To    *time.Time `json:"to,omitempty"`
}

// Retention

type tsRetentionResponse struct {
	DefaultRetentionDays int                    `json:"default_retention_days"`
	Timelines            []tsTimelineRetention  `json:"timelines"`
}

type tsTimelineRetention struct {
	ID            int    `json:"id"`
	Name          string `json:"name,omitempty"`
	RetentionDays int    `json:"retention_days"`
}

// Stats
type tsStatsResponse struct {
	TenantID  string `json:"tenant_id"`
	Timelines int    `json:"timelines"`
	DiskBytes int64  `json:"disk_bytes"`
}

type tsTimelineStatsResponse struct {
	TimelineID             int        `json:"timeline_id"`
	Name                   string     `json:"name,omitempty"`
	TotalEvents            int64      `json:"total_events"`
	TotalEventsApproximate bool       `json:"total_events_approximate"`
	OldestEvent            *time.Time `json:"oldest_event,omitempty"`
	NewestEvent            *time.Time `json:"newest_event,omitempty"`
}

// --- Handler helpers ---

// tsStore retrieves the timeseries store for the current request's tenant.
// Returns nil and writes the appropriate error response if unavailable.
func (s *Server) tsStore(w http.ResponseWriter, r *http.Request, tenantIDStr string) timeseries.Store {
	if s.tsManager == nil {
		s.writeError(w, http.StatusForbidden, oluerr.Code("OLU-TS002"), "timeseries not enabled")
		return nil
	}
	tid := getTenantIDNumeric(r.Context())
	if tid == 0 {
		s.writeError(w, http.StatusNotFound, oluerr.Code("OLU-TS003"), fmt.Sprintf("tenant %s not found", tenantIDStr))
		return nil
	}
	if !s.tsManager.IsProvisioned(tid) {
		s.writeError(w, http.StatusNotFound, oluerr.Code("OLU-TS003"), fmt.Sprintf("tenant %s not provisioned for timeseries", tenantIDStr))
		return nil
	}
	store, err := s.tsManager.StoreFor(tid)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, oluerr.Code("OLU-TS013"), err.Error())
		return nil
	}
	return store
}

// parseTSTime parses an ISO 8601 timestamp string.
func parseTSTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timestamp %q (OLU-TS005)", s)
	}
	if t.Before(time.Unix(0, 0)) {
		return time.Time{}, fmt.Errorf("timestamp before Unix epoch (OLU-TS005)")
	}
	return t, nil
}

// parseInterval converts a human-readable interval string to time.Duration.
func parseInterval(s string) (time.Duration, error) {
	switch s {
	case "1m":
		return time.Minute, nil
	case "5m":
		return 5 * time.Minute, nil
	case "15m":
		return 15 * time.Minute, nil
	case "30m":
		return 30 * time.Minute, nil
	case "1h":
		return time.Hour, nil
	case "6h":
		return 6 * time.Hour, nil
	case "12h":
		return 12 * time.Hour, nil
	case "1d":
		return 24 * time.Hour, nil
	case "7d":
		return 7 * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("invalid interval %q (OLU-TS010): valid values are 1m 5m 15m 30m 1h 6h 12h 1d 7d", s)
}

// parseDims parses a comma-separated list of uint64 dimension values.
func parseDims(s string) ([]uint64, error) {
	parts := strings.Split(s, ",")
	out := make([]uint64, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.ParseUint(strings.TrimSpace(p), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid dimension value %q: %w", p, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// eventToResponse converts a timeseries.Event to the HTTP response type.
func eventToResponse(e timeseries.Event) tsEventResponse {
	r := tsEventResponse{
		Timeline: int(e.Timeline),
		Dims:     e.Dims,
		Time:     e.Time,
		Nums:     e.Nums,
	}
	if len(e.Payload) > 0 {
		var v any
		if err := json.Unmarshal(e.Payload, &v); err == nil {
			r.Payload = v
		} else {
			r.Payload = string(e.Payload)
		}
	}
	return r
}

// timelineToResponse converts a TimelineConfig to the HTTP response type.
func timelineToResponse(id timeseries.TimelineID, cfg timeseries.TimelineConfig) tsTimelineResponse {
	r := tsTimelineResponse{
		ID:            int(id),
		Name:          cfg.Name,
		Dims:          int(cfg.Dims),
		RetentionDays: cfg.RetentionDays,
		CreatedAt:     cfg.CreatedAt,
	}
	if !cfg.FirstWriteAt.IsZero() {
		fw := cfg.FirstWriteAt
		r.FirstWriteAt = &fw
	}
	return r
}

// encodePayload marshals the payload field from a write request to bytes.
func encodePayload(v any) []byte {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// classifyTSError maps timeseries error messages to OLU-TS error codes.
func classifyTSError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "OLU-TS"):
		// Error already carries a code; extract it.
		for _, code := range []string{
			"OLU-TS001", "OLU-TS002", "OLU-TS003", "OLU-TS004",
			"OLU-TS005", "OLU-TS006", "OLU-TS007", "OLU-TS008",
			"OLU-TS009", "OLU-TS010", "OLU-TS011", "OLU-TS012",
			"OLU-TS013", "OLU-TS014", "OLU-TS015", "OLU-TS016",
			"OLU-TS017", "OLU-TS018", "OLU-TS019",
		} {
			if strings.Contains(msg, code) {
				return code
			}
		}
	}
	return "OLU-TS013"
}

// --- Handlers ---


// --- Limit helpers ---

// tsLimits holds the resolved per-request guardrails for a timeseries query.
type tsLimits struct {
	timeout       time.Duration
	maxEvents     int
	maxScanEvents int
	maxRangeDays  int
	maxBatchSize  int
	maxRespBytes  int
	maxBuckets    int
}

// tsQueryLimits resolves effective limits from config, applying defaults for
// any zero values (matching the pattern used by OQL/Sulpher handlers).
func (s *Server) tsQueryLimits() tsLimits {
	cfg := s.config
	l := tsLimits{
		timeout:       time.Duration(cfg.TSQueryTimeoutSecs) * time.Second,
		maxEvents:     cfg.TSMaxQueryEvents,
		maxScanEvents: cfg.TSMaxScanEvents,
		maxRangeDays:  cfg.TSMaxRangeDays,
		maxBatchSize:  cfg.TSMaxBatchSize,
		maxRespBytes:  cfg.TSMaxResponseBytes,
		maxBuckets:    cfg.TSMaxAggregateBuckets,
	}
	if l.timeout <= 0 {
		l.timeout = 30 * time.Second
	}
	if l.maxEvents <= 0 {
		l.maxEvents = 10000
	}
	if l.maxScanEvents <= 0 {
		l.maxScanEvents = 500000
	}
	if l.maxRangeDays <= 0 {
		l.maxRangeDays = 366
	}
	if l.maxBatchSize <= 0 {
		l.maxBatchSize = 5000
	}
	if l.maxRespBytes <= 0 {
		l.maxRespBytes = 10 * 1024 * 1024
	}
	if l.maxBuckets <= 0 {
		l.maxBuckets = 10000
	}
	return l
}

// tsWriteJSON serialises data and enforces the response byte limit before
// writing to the client. Returns true if the response was written successfully.
func (s *Server) tsWriteJSON(w http.ResponseWriter, status int, data any, maxBytes int) bool {
	encoded, err := json.Marshal(data)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, oluerr.Code("OLU-TS013"), "failed to encode response")
		return false
	}
	if len(encoded) > maxBytes {
		s.writeError(w, http.StatusRequestEntityTooLarge, oluerr.Code("OLU-TS013"),
			fmt.Sprintf("response too large: %d bytes (max %d)", len(encoded), maxBytes))
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
	return true
}

// HandleTSProvision provisions timeseries for a tenant.
//
//	POST /api/v1/tenant/{tenant_id}/ts/provision
func (s *Server) HandleTSProvision(w http.ResponseWriter, r *http.Request) {
	if s.tsManager == nil {
		s.writeError(w, http.StatusForbidden, oluerr.Code("OLU-TS002"), "timeseries not enabled")
		return
	}
	tenantID := chi.URLParam(r, "tenant_id")
	tid := getTenantIDNumeric(r.Context())
	if tid == 0 {
		s.writeError(w, http.StatusNotFound, oluerr.Code("OLU-TS003"), fmt.Sprintf("tenant %s not found", tenantID))
		return
	}
	if err := s.tsManager.Provision(r.Context(), tid); err != nil {
		s.writeError(w, http.StatusInternalServerError, oluerr.Code("OLU-TS015"), err.Error())
		return
	}
	s.writeJSON(w, http.StatusCreated, tsProvisionResponse{TenantID: tenantID, Timeseries: "enabled"})
}

// HandleTSDefineTimeline defines or updates a timeline.
//
//	POST /api/v1/tenant/{tenant_id}/ts/timelines
func (s *Server) HandleTSDefineTimeline(w http.ResponseWriter, r *http.Request) {
	store := s.tsStore(w, r, chi.URLParam(r, "tenant_id"))
	if store == nil {
		return
	}
	var req tsDefineTimelineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS013"), "invalid request body")
		return
	}
	if req.ID <= 0 || req.ID > int(timeseries.MaxTimelineID) {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS018"), fmt.Sprintf("invalid timeline ID %d", req.ID))
		return
	}
	cfg := timeseries.TimelineConfig{
		Name:          req.Name,
		Dims:          uint8(req.Dims),
		RetentionDays: req.RetentionDays,
	}
	if err := store.DefineTimeline(timeseries.TimelineID(req.ID), cfg); err != nil {
		code := classifyTSError(err)
		s.writeError(w, http.StatusConflict, oluerr.Code(code), err.Error())
		return
	}
	cfg, _ = store.Timeline(timeseries.TimelineID(req.ID))
	s.writeJSON(w, http.StatusCreated, timelineToResponse(timeseries.TimelineID(req.ID), cfg))
}

// HandleTSListTimelines returns all defined timelines.
//
//	GET /api/v1/tenant/{tenant_id}/ts/timelines
func (s *Server) HandleTSListTimelines(w http.ResponseWriter, r *http.Request) {
	store := s.tsStore(w, r, chi.URLParam(r, "tenant_id"))
	if store == nil {
		return
	}
	ids := store.Timelines()
	result := make([]tsTimelineResponse, 0, len(ids))
	for _, id := range ids {
		cfg, _ := store.Timeline(id)
		result = append(result, timelineToResponse(id, cfg))
	}
	s.writeJSON(w, http.StatusOK, result)
}

// HandleTSGetTimeline returns a single timeline.
//
//	GET /api/v1/tenant/{tenant_id}/ts/timelines/{timeline_id}
func (s *Server) HandleTSGetTimeline(w http.ResponseWriter, r *http.Request) {
	store := s.tsStore(w, r, chi.URLParam(r, "tenant_id"))
	if store == nil {
		return
	}
	tidStr := chi.URLParam(r, "timeline_id")
	tidInt, err := strconv.ParseUint(tidStr, 10, 16)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS004"), fmt.Sprintf("invalid timeline_id %q", tidStr))
		return
	}
	tid := timeseries.TimelineID(tidInt)
	cfg, ok := store.Timeline(tid)
	if !ok {
		s.writeError(w, http.StatusNotFound, oluerr.Code("OLU-TS004"), fmt.Sprintf("timeline %d not defined", tidInt))
		return
	}
	s.writeJSON(w, http.StatusOK, timelineToResponse(tid, cfg))
}

// HandleTSUpdateTimeline updates a timeline's mutable fields.
//
//	PATCH /api/v1/tenant/{tenant_id}/ts/timelines/{timeline_id}
func (s *Server) HandleTSUpdateTimeline(w http.ResponseWriter, r *http.Request) {
	store := s.tsStore(w, r, chi.URLParam(r, "tenant_id"))
	if store == nil {
		return
	}
	tidStr := chi.URLParam(r, "timeline_id")
	tidInt, err := strconv.ParseUint(tidStr, 10, 16)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS004"), fmt.Sprintf("invalid timeline_id %q", tidStr))
		return
	}
	var req tsDefineTimelineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS013"), "invalid request body")
		return
	}
	tid := timeseries.TimelineID(tidInt)
	cfg := timeseries.TimelineConfig{
		Name:          req.Name,
		RetentionDays: req.RetentionDays,
	}
	if err := store.UpdateTimeline(tid, cfg); err != nil {
		code := classifyTSError(err)
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "OLU-TS004") {
			status = http.StatusNotFound
		} else if strings.Contains(err.Error(), "OLU-TS016") {
			status = http.StatusConflict
		}
		s.writeError(w, status, oluerr.Code(code), err.Error())
		return
	}
	cfg, _ = store.Timeline(tid)
	s.writeJSON(w, http.StatusOK, timelineToResponse(tid, cfg))
}

// HandleTSAppend appends a single event.
//
//	POST /api/v1/tenant/{tenant_id}/ts/events
func (s *Server) HandleTSAppend(w http.ResponseWriter, r *http.Request) {
	store := s.tsStore(w, r, chi.URLParam(r, "tenant_id"))
	if store == nil {
		return
	}
	var req tsAppendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS013"), "invalid request body")
		return
	}
	ts, err := parseTSTime(req.Time)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS005"), err.Error())
		return
	}
	e := timeseries.Event{
		Timeline: timeseries.TimelineID(req.Timeline),
		Dims:     req.Dims,
		Time:     ts,
		Nums:     req.Nums,
		Payload:  encodePayload(req.Payload),
	}
	if err := store.Append(r.Context(), e); err != nil {
		code := classifyTSError(err)
		s.writeError(w, http.StatusBadRequest, oluerr.Code(code), err.Error())
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// HandleTSBatchAppend appends a batch of events atomically.
//
//	POST /api/v1/tenant/{tenant_id}/ts/events/batch
func (s *Server) HandleTSBatchAppend(w http.ResponseWriter, r *http.Request) {
	store := s.tsStore(w, r, chi.URLParam(r, "tenant_id"))
	if store == nil {
		return
	}
	var req tsBatchAppendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS013"), "invalid request body")
		return
	}
	limits := s.tsQueryLimits()
	if len(req.Events) > limits.maxBatchSize {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS006"),
			fmt.Sprintf("batch size %d exceeds max %d", len(req.Events), limits.maxBatchSize))
		return
	}
	events := make([]timeseries.Event, 0, len(req.Events))
	for i, re := range req.Events {
		ts, err := parseTSTime(re.Time)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS005"), fmt.Sprintf("event[%d]: %s", i, err))
			return
		}
		events = append(events, timeseries.Event{
			Timeline: timeseries.TimelineID(re.Timeline),
			Dims:     re.Dims,
			Time:     ts,
			Nums:     re.Nums,
			Payload:  encodePayload(re.Payload),
		})
	}
	accepted, err := store.AppendBatch(r.Context(), events, limits.maxBatchSize)
	if err != nil {
		code := classifyTSError(err)
		s.writeError(w, http.StatusBadRequest, oluerr.Code(code), err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, tsBatchAppendResponse{
		Total:    len(events),
		Accepted: accepted,
		Failed:   len(events) - accepted,
	})
}

// HandleTSQueryRange returns events in a time range.
//
//	GET /api/v1/tenant/{tenant_id}/ts/events
func (s *Server) HandleTSQueryRange(w http.ResponseWriter, r *http.Request) {
	store := s.tsStore(w, r, chi.URLParam(r, "tenant_id"))
	if store == nil {
		return
	}
	q := r.URL.Query()

	tidInt, err := strconv.ParseUint(q.Get("timeline"), 10, 16)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS004"), "missing or invalid timeline parameter")
		return
	}
	dims, err := parseDims(q.Get("dims"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS007"), err.Error())
		return
	}
	from, err := parseTSTime(q.Get("from"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS005"), err.Error())
		return
	}
	to, err := parseTSTime(q.Get("to"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS005"), err.Error())
		return
	}
	limits := s.tsQueryLimits()

	if to.Sub(from) > time.Duration(limits.maxRangeDays)*24*time.Hour {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS011"),
			fmt.Sprintf("query range exceeds %d days", limits.maxRangeDays))
		return
	}

	limit := 1000
	if ls := q.Get("limit"); ls != "" {
		limit, err = strconv.Atoi(ls)
		if err != nil || limit < 1 {
			limit = 1000
		}
	}
	if limit > limits.maxEvents {
		limit = limits.maxEvents
	}
	order := q.Get("order")
	if order == "" {
		order = "asc"
	}

	ctx, cancel := context.WithTimeout(r.Context(), limits.timeout)
	defer cancel()

	rq := timeseries.RangeQuery{
		Timeline:      timeseries.TimelineID(tidInt),
		Dims:          dims,
		From:          from,
		To:            to,
		Limit:         limit,
		Order:         order,
		MaxScanEvents: limits.maxScanEvents,
	}
	events, err := store.QueryRange(ctx, rq)
	if err != nil {
		code := classifyTSError(err)
		status := http.StatusBadRequest
		if ctx.Err() != nil {
			status = http.StatusGatewayTimeout
			code = "OLU-TS013"
		}
		s.writeError(w, status, oluerr.Code(code), err.Error())
		return
	}
	resp := tsRangeResponse{Count: uint64(len(events)), Events: make([]tsEventResponse, len(events))}
	for i, e := range events {
		resp.Events[i] = eventToResponse(e)
	}
	s.tsWriteJSON(w, http.StatusOK, resp, limits.maxRespBytes)
}

// HandleTSLatest returns the N most recent events.
//
//	GET /api/v1/tenant/{tenant_id}/ts/events/latest
func (s *Server) HandleTSLatest(w http.ResponseWriter, r *http.Request) {
	store := s.tsStore(w, r, chi.URLParam(r, "tenant_id"))
	if store == nil {
		return
	}
	q := r.URL.Query()

	tidInt, err := strconv.ParseUint(q.Get("timeline"), 10, 16)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS004"), "missing or invalid timeline parameter")
		return
	}
	dims, err := parseDims(q.Get("dims"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS007"), err.Error())
		return
	}
	limits := s.tsQueryLimits()

	n := 10
	if ns := q.Get("n"); ns != "" {
		n, err = strconv.Atoi(ns)
		if err != nil || n < 1 {
			n = 10
		}
	}
	if n > limits.maxEvents {
		n = limits.maxEvents
	}

	ctx, cancel := context.WithTimeout(r.Context(), limits.timeout)
	defer cancel()

	lq := timeseries.LatestQuery{
		Timeline: timeseries.TimelineID(tidInt),
		Dims:     dims,
		N:        n,
	}
	// Optional time bounds — parse if present, ignore gracefully if absent.
	if fromStr := q.Get("from"); fromStr != "" {
		if t, err := parseTSTime(fromStr); err == nil {
			lq.From = t
		}
	}
	if toStr := q.Get("to"); toStr != "" {
		if t, err := parseTSTime(toStr); err == nil {
			lq.To = t
		}
	}
	events, err := store.Latest(ctx, lq)
	if err != nil {
		code := classifyTSError(err)
		status := http.StatusBadRequest
		if ctx.Err() != nil {
			status = http.StatusGatewayTimeout
			code = "OLU-TS013"
		}
		s.writeError(w, status, oluerr.Code(code), err.Error())
		return
	}
	resp := tsRangeResponse{Count: uint64(len(events)), Events: make([]tsEventResponse, len(events))}
	for i, e := range events {
		resp.Events[i] = eventToResponse(e)
	}
	s.tsWriteJSON(w, http.StatusOK, resp, limits.maxRespBytes)
}

// HandleTSAggregate computes an aggregate over a numeric field.
//
//	POST /api/v1/tenant/{tenant_id}/ts/aggregate
func (s *Server) HandleTSAggregate(w http.ResponseWriter, r *http.Request) {
	store := s.tsStore(w, r, chi.URLParam(r, "tenant_id"))
	if store == nil {
		return
	}
	var req tsAggregateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS013"), "invalid request body")
		return
	}
	from, err := parseTSTime(req.From)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS005"), err.Error())
		return
	}
	to, err := parseTSTime(req.To)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS005"), err.Error())
		return
	}

	var interval time.Duration
	if req.Interval != "" {
		interval, err = parseInterval(req.Interval)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS010"), err.Error())
			return
		}
	}

	limits := s.tsQueryLimits()

	if to.Sub(from) > time.Duration(limits.maxRangeDays)*24*time.Hour {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS011"),
			fmt.Sprintf("query range exceeds %d days", limits.maxRangeDays))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), limits.timeout)
	defer cancel()

	aq := timeseries.AggregateQuery{
		Timeline:      timeseries.TimelineID(req.Timeline),
		Dims:          req.Dims,
		From:          from,
		To:            to,
		NumField:      uint8(req.NumField),
		Function:      req.Function,
		Interval:      interval,
		MaxScanEvents: limits.maxScanEvents,
		MaxBuckets:    limits.maxBuckets,
	}
	buckets, err := store.Aggregate(ctx, aq)
	if err != nil {
		code := classifyTSError(err)
		status := http.StatusBadRequest
		if ctx.Err() != nil {
			status = http.StatusGatewayTimeout
			code = "OLU-TS013"
		}
		s.writeError(w, status, oluerr.Code(code), err.Error())
		return
	}

	resp := tsAggregateResponse{
		Timeline: req.Timeline,
		NumField: req.NumField,
		Function: req.Function,
		Interval: req.Interval,
	}

	if interval > 0 {
		resp.Buckets = make([]tsBucketResponse, len(buckets))
		for i, b := range buckets {
			resp.Buckets[i] = tsBucketResponse{Time: b.Time, Value: b.Value, Count: b.Count}
		}
	} else {
		// Scalar result.
		var val float64
		var count uint64
		if len(buckets) > 0 {
			val = buckets[0].Value
			count = buckets[0].Count
		}
		resp.Value = &val
		resp.Count = &count
		resp.From = &from
		resp.To = &to
	}
	s.tsWriteJSON(w, http.StatusOK, resp, limits.maxRespBytes)
}

// HandleTSGetRetention returns the retention configuration.
//
//	GET /api/v1/tenant/{tenant_id}/ts/retention
func (s *Server) HandleTSGetRetention(w http.ResponseWriter, r *http.Request) {
	store := s.tsStore(w, r, chi.URLParam(r, "tenant_id"))
	if store == nil {
		return
	}
	ids := store.Timelines()
	resp := tsRetentionResponse{
		DefaultRetentionDays: store.DefaultRetentionDays(),
		Timelines:            make([]tsTimelineRetention, 0, len(ids)),
	}
	for _, id := range ids {
		cfg, _ := store.Timeline(id)
		resp.Timelines = append(resp.Timelines, tsTimelineRetention{
			ID:            int(id),
			Name:          cfg.Name,
			RetentionDays: cfg.RetentionDays,
		})
	}
	s.writeJSON(w, http.StatusOK, resp)
}

// HandleTSStats returns store-level diagnostics.
//
//	GET /api/v1/tenant/{tenant_id}/ts/stats
func (s *Server) HandleTSStats(w http.ResponseWriter, r *http.Request) {
	tenantIDStr := chi.URLParam(r, "tenant_id") // for response only
	store := s.tsStore(w, r, tenantIDStr)
	if store == nil {
		return
	}
	stats, err := store.Stats(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, oluerr.Code("OLU-TS013"), err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, tsStatsResponse{
		TenantID:  tenantIDStr,
		Timelines: stats.Timelines,
		DiskBytes: stats.DiskBytes,
	})
}

// HandleTSTimelineStats returns per-timeline diagnostics.
//
//	GET /api/v1/tenant/{tenant_id}/ts/stats/{timeline_id}
func (s *Server) HandleTSTimelineStats(w http.ResponseWriter, r *http.Request) {
	store := s.tsStore(w, r, chi.URLParam(r, "tenant_id"))
	if store == nil {
		return
	}
	tidStr := chi.URLParam(r, "timeline_id")
	tidInt, err := strconv.ParseUint(tidStr, 10, 16)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.Code("OLU-TS004"), fmt.Sprintf("invalid timeline_id %q", tidStr))
		return
	}
	tid := timeseries.TimelineID(tidInt)
	cfg, ok := store.Timeline(tid)
	if !ok {
		s.writeError(w, http.StatusNotFound, oluerr.Code("OLU-TS004"), fmt.Sprintf("timeline %d not defined", tidInt))
		return
	}
	stats, err := store.TimelineStats(r.Context(), tid)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, oluerr.Code("OLU-TS013"), err.Error())
		return
	}
	resp := tsTimelineStatsResponse{
		TimelineID:             int(tid),
		Name:                   cfg.Name,
		TotalEvents:            stats.TotalEvents,
		TotalEventsApproximate: stats.TotalEventsApproximate,
	}
	if !stats.OldestEvent.IsZero() {
		t := stats.OldestEvent
		resp.OldestEvent = &t
	}
	if !stats.NewestEvent.IsZero() {
		t := stats.NewestEvent
		resp.NewestEvent = &t
	}
	s.writeJSON(w, http.StatusOK, resp)
}

