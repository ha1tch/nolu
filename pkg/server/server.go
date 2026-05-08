// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Package server implements the nolu HTTP API.
//
// Routes (all under base URL, default :7070):
//
//   GET  /health
//   GET  /version
//
//   POST   /registry
//   GET    /registry/{globalID}
//   GET    /registry/{globalID}/resolve
//   POST   /registry/{globalID}/transfer
//   POST   /registry/{globalID}/retire
//   GET    /registry?instance_url=...
//   GET    /registry?entity_type=...
//
//   GET    /tenants/{name}/locate
//
//   POST   /transfers
//   GET    /transfers/{id}
//   POST   /transfers/{id}/accept
//   POST   /transfers/{id}/reject
//   POST   /transfers/{id}/cancel
//   POST   /transfers/{id}/complete
//   GET    /transfers?global_id=...
//   GET    /transfers?instance_url=...&state=...
//
//   POST   /hotswaps
//   GET    /hotswaps/{id}
//   GET    /hotswaps/{id}/status
//   POST   /hotswaps/{id}/confirm
//   POST   /hotswaps/{id}/abort
//   GET    /hotswaps?state=...
//
//   /proxy/tenant/{name}/...  (reverse proxy to xolu)
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"

	"github.com/ha1tch/nolu/pkg/hotswap"
	"github.com/ha1tch/nolu/pkg/proxy"
	"github.com/ha1tch/nolu/pkg/registry"
	"github.com/ha1tch/nolu/pkg/transfer"
	"github.com/ha1tch/nolu/pkg/version"
)

// Server holds all dependencies and implements http.Handler.
type Server struct {
	reg        registry.Registry
	neg        transfer.Negotiator
	hs         hotswap.Manager  // may be nil if hotswap disabled
	prx        *proxy.ReverseProxy
	dir        *registry.TenantDirectory // may be nil if directory not started
	host       string
	mux        *chi.Mux
}

// New creates and configures a Server. All dependencies are required except
// hs (hotswap manager) which may be nil.
func New(
	reg registry.Registry,
	neg transfer.Negotiator,
	hs hotswap.Manager,
	prx *proxy.ReverseProxy,
	dir *registry.TenantDirectory,
	host string,
) *Server {
	s := &Server{
		reg:  reg,
		neg:  neg,
		hs:   hs,
		prx:  prx,
		dir:  dir,
		host: host,
	}
	s.mux = s.buildRoutes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) buildRoutes() *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.RequestID)
	r.Use(logMiddleware)
	r.Use(middleware.Recoverer)

	// Utility
	r.Get("/health", s.handleHealth)
	r.Get("/version", s.handleVersion)

	// Registry
	r.Post("/registry", s.handleRegister)
	r.Get("/registry", s.handleRegistryList)
	r.Get("/registry/{globalID}", s.handleGet)
	r.Get("/registry/{globalID}/resolve", s.handleResolve)
	r.Post("/registry/{globalID}/transfer", s.handleTransfer)
	r.Post("/registry/{globalID}/retire", s.handleRetire)

	// Tenant location (for proxy and discovery)
	r.Get("/tenants/{name}/locate", s.handleLocate)

	// Transfer negotiation
	r.Post("/transfers", s.handlePropose)
	r.Get("/transfers", s.handleTransferList)
	r.Get("/transfers/{id}", s.handleTransferGet)
	r.Post("/transfers/{id}/accept", s.handleAccept)
	r.Post("/transfers/{id}/reject", s.handleReject)
	r.Post("/transfers/{id}/cancel", s.handleCancel)
	r.Post("/transfers/{id}/complete", s.handleComplete)

	// Hotswap
	if s.hs != nil {
		r.Post("/hotswaps", s.handleHotswapRequest)
		r.Get("/hotswaps", s.handleHotswapList)
		r.Get("/hotswaps/{id}", s.handleHotswapGet)
		r.Get("/hotswaps/{id}/status", s.handleHotswapStatus)
		r.Post("/hotswaps/{id}/confirm", s.handleHotswapConfirm)
		r.Post("/hotswaps/{id}/abort", s.handleHotswapAbort)
	}

	// Proxy
	if s.prx != nil {
		r.Handle("/proxy/*", http.StripPrefix("/proxy", s.prx.Handler()))
	}

	return r
}

// ── Utility ───────────────────────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"version": version.Version,
	})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version":       version.Version,
		"registry_host": s.host,
		"hotswap":       s.hs != nil,
		"proxy":         s.prx != nil,
	})
}

// ── Registry ──────────────────────────────────────────────────────────────────

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EntityType string              `json:"entity_type"`
		Owner      localRefJSON        `json:"owner"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.EntityType == "" {
		writeError(w, http.StatusBadRequest, "NOLU-XX001", "entity_type is required")
		return
	}

	rec, err := s.reg.Register(r.Context(), s.host, req.EntityType, req.Owner.toRef())
	if err != nil {
		if errors.Is(err, registry.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "NOLU-RG002", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "NOLU-XX002", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, recordToJSON(rec))
}

func (s *Server) handleRegistryList(w http.ResponseWriter, r *http.Request) {
	instanceURL := r.URL.Query().Get("instance_url")
	entityType  := r.URL.Query().Get("entity_type")

	if instanceURL == "" && entityType == "" {
		writeError(w, http.StatusBadRequest, "NOLU-XX001", "provide instance_url or entity_type query parameter")
		return
	}

	var gids []interface{}
	var filterVal string

	if instanceURL != "" {
		ids, err := s.reg.ListByInstance(r.Context(), instanceURL)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "NOLU-XX002", err.Error())
			return
		}
		for _, id := range ids {
			gids = append(gids, string(id))
		}
		filterVal = instanceURL
	} else {
		ids, err := s.reg.ListByEntityType(r.Context(), entityType)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "NOLU-XX002", err.Error())
			return
		}
		for _, id := range ids {
			gids = append(gids, string(id))
		}
		filterVal = entityType
	}

	if gids == nil {
		gids = []interface{}{}
	}
	key := "instance_url"
	if entityType != "" {
		key = "entity_type"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		key:        filterVal,
		"count":     len(gids),
		"global_ids": gids,
	})
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	gid, ok := parseGlobalID(w, r)
	if !ok {
		return
	}
	rec, err := s.reg.Get(r.Context(), gid)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, recordToJSON(rec))
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	gid, ok := parseGlobalID(w, r)
	if !ok {
		return
	}
	ref, err := s.reg.Resolve(r.Context(), gid)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"global_id": string(gid),
		"current":   refToJSON(ref),
	})
}

func (s *Server) handleTransfer(w http.ResponseWriter, r *http.Request) {
	gid, ok := parseGlobalID(w, r)
	if !ok {
		return
	}
	var req struct {
		From        localRefJSON `json:"from"`
		To          localRefJSON `json:"to"`
		Protocol    string       `json:"protocol"`
		HistoryFrom string       `json:"history_from"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	rec, err := s.reg.Transfer(r.Context(), registry.TransferRequest{
		GlobalID:    gid,
		From:        req.From.toRef(),
		To:          req.To.toRef(),
		Protocol:    req.Protocol,
		HistoryFrom: req.HistoryFrom,
	})
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, recordToJSON(rec))
}

func (s *Server) handleRetire(w http.ResponseWriter, r *http.Request) {
	gid, ok := parseGlobalID(w, r)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	_ = readJSON(w, r, &req) // reason is optional
	if err := s.reg.Retire(r.Context(), gid, req.Reason); err != nil {
		writeRegistryError(w, err)
		return
	}
	rec, _ := s.reg.Get(r.Context(), gid)
	writeJSON(w, http.StatusOK, recordToJSON(rec))
}

// ── Tenant locate (for proxy discovery) ──────────────────────────────────────

func (s *Server) handleLocate(w http.ResponseWriter, r *http.Request) {
	tenantName := chi.URLParam(r, "name")
	if tenantName == "" {
		writeError(w, http.StatusBadRequest, "NOLU-XX001", "tenant name is required")
		return
	}

	if s.dir == nil {
		writeError(w, http.StatusServiceUnavailable, "NOLU-RG005",
			"tenant directory not initialised — nolu may still be starting up")
		return
	}

	entry, ok := s.dir.Locate(tenantName)
	if !ok {
		writeError(w, http.StatusNotFound, "NOLU-RG001",
			fmt.Sprintf("tenant %q not found in directory — no entities registered yet", tenantName))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"tenant":       entry.TenantName,
		"instance_url": entry.InstanceURL,
		"tenant_id":    entry.TenantID,
		"entity_count": entry.EntityCount,
		"stable_until": entry.StableUntil.UTC().Format(time.RFC3339),
		"first_seen":   entry.FirstSeen.UTC().Format(time.RFC3339),
		"last_seen":    entry.LastSeen.UTC().Format(time.RFC3339),
	})
}

// ── Transfer negotiation ──────────────────────────────────────────────────────

func (s *Server) handlePropose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GlobalID     string       `json:"global_id"`
		From         localRefJSON `json:"from"`
		To           localRefJSON `json:"to"`
		Protocol     string       `json:"protocol"`
		HistoryOffer struct {
			Mode string `json:"mode"`
			Note string `json:"note"`
		} `json:"history_offer"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.GlobalID == "" {
		writeError(w, http.StatusBadRequest, "NOLU-XX001", "global_id is required")
		return
	}

	p, err := s.neg.Propose(r.Context(), transfer.Proposal{
		GlobalID: toGlobalID(req.GlobalID),
		From:     req.From.toRef(),
		To:       req.To.toRef(),
		Protocol: req.Protocol,
		HistoryOffer: transfer.HistoryOffer{
			Mode: req.HistoryOffer.Mode,
			Note: req.HistoryOffer.Note,
		},
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "NOLU-XX001", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, proposalToJSON(p))
}

func (s *Server) handleTransferGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := s.neg.Get(r.Context(), id)
	if err != nil {
		writeTransferError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, proposalToJSON(p))
}

func (s *Server) handleTransferList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	globalID := q.Get("global_id")
	instanceURL := q.Get("instance_url")
	stateStr := q.Get("state")

	if globalID != "" {
		proposals, err := s.neg.ListByGlobalID(r.Context(), toGlobalID(globalID))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "NOLU-XX002", err.Error())
			return
		}
		out := make([]interface{}, len(proposals))
		for i, p := range proposals {
			pp := p
			out[i] = proposalToJSON(&pp)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"global_id": globalID,
			"count":     len(out),
			"proposals": out,
		})
		return
	}

	if instanceURL != "" {
		var statePtr *transfer.State
		if stateStr != "" {
			s := transfer.State(stateStr)
			statePtr = &s
		}
		proposals, err := s.neg.ListByInstance(r.Context(), instanceURL, statePtr)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "NOLU-XX002", err.Error())
			return
		}
		out := make([]interface{}, len(proposals))
		for i, p := range proposals {
			pp := p
			out[i] = proposalToJSON(&pp)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"instance_url": instanceURL,
			"count":        len(out),
			"proposals":    out,
		})
		return
	}

	writeError(w, http.StatusBadRequest, "NOLU-XX001", "provide global_id or instance_url query parameter")
}

func (s *Server) handleAccept(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		HistorySpec struct {
			Mode string    `json:"mode"`
			From time.Time `json:"from,omitempty"`
		} `json:"history_spec"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p, err := s.neg.Accept(r.Context(), id, transfer.HistorySpec{
		Mode: req.HistorySpec.Mode,
		From: req.HistorySpec.From,
	})
	if err != nil {
		writeTransferError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, proposalToJSON(p))
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Reason string `json:"reason"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p, err := s.neg.Reject(r.Context(), id, req.Reason)
	if err != nil {
		writeTransferError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, proposalToJSON(p))
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := s.neg.Cancel(r.Context(), id)
	if err != nil {
		writeTransferError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, proposalToJSON(p))
}

func (s *Server) handleComplete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := s.neg.Complete(r.Context(), id)
	if err != nil {
		writeTransferError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, proposalToJSON(p))
}

// ── Hotswap ───────────────────────────────────────────────────────────────────

func (s *Server) handleHotswapRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source  instanceRefJSON    `json:"source"`
		Target  instanceRefJSON    `json:"target"`
		Options hotswap.HotswapOptions `json:"options"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	h, err := s.hs.Request(r.Context(), req.Source.toRef(), req.Target.toRef(), req.Options)
	if err != nil {
		switch {
		case errors.Is(err, hotswap.ErrAlreadyExists):
			writeError(w, http.StatusConflict, "NOLU-HS001", err.Error())
		case errors.Is(err, hotswap.ErrSourceUnreachable), errors.Is(err, hotswap.ErrTargetUnreachable):
			writeError(w, http.StatusBadGateway, "NOLU-HS002", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "NOLU-XX002", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusCreated, h)
}

func (s *Server) handleHotswapGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h, err := s.hs.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, hotswap.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOLU-HS003", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "NOLU-XX002", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleHotswapStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	st, err := s.hs.Status(r.Context(), id)
	if err != nil {
		if errors.Is(err, hotswap.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOLU-HS003", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "NOLU-XX002", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleHotswapList(w http.ResponseWriter, r *http.Request) {
	stateStr := r.URL.Query().Get("state")
	var statePtr *hotswap.State
	if stateStr != "" {
		s := hotswap.State(stateStr)
		statePtr = &s
	}
	hs, err := s.hs.List(r.Context(), statePtr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "NOLU-XX002", err.Error())
		return
	}
	if hs == nil {
		hs = []*hotswap.Hotswap{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count":    len(hs),
		"hotswaps": hs,
	})
}

func (s *Server) handleHotswapConfirm(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	h, err := s.hs.Confirm(r.Context(), id)
	if err != nil {
		writeHotswapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleHotswapAbort(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Reason string `json:"reason"`
	}
	_ = readJSON(w, r, &req)
	h, err := s.hs.Abort(r.Context(), id, req.Reason)
	if err != nil {
		writeHotswapError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

// ── JSON helpers ──────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Error().Err(err).Msg("server: encode response")
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "NOLU-XX001", fmt.Sprintf("invalid JSON: %v", err))
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
			"status":  status,
		},
	})
}

func writeRegistryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, registry.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOLU-RG001", err.Error())
	case errors.Is(err, registry.ErrAlreadyExists):
		writeError(w, http.StatusConflict, "NOLU-RG002", err.Error())
	case errors.Is(err, registry.ErrRetired):
		writeError(w, http.StatusGone, "NOLU-RG003", err.Error())
	case errors.Is(err, registry.ErrInvalidTransfer):
		writeError(w, http.StatusConflict, "NOLU-RG004", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "NOLU-XX002", err.Error())
	}
}

func writeTransferError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, transfer.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOLU-TX001", err.Error())
	case errors.Is(err, transfer.ErrWrongState):
		writeError(w, http.StatusConflict, "NOLU-TX002", err.Error())
	case errors.Is(err, transfer.ErrNotAuthorised):
		writeError(w, http.StatusForbidden, "NOLU-TX003", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "NOLU-XX002", err.Error())
	}
}

func writeHotswapError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, hotswap.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOLU-HS003", err.Error())
	case errors.Is(err, hotswap.ErrWrongState):
		writeError(w, http.StatusConflict, "NOLU-HS004", err.Error())
	case errors.Is(err, hotswap.ErrAlreadyExists):
		writeError(w, http.StatusConflict, "NOLU-HS001", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "NOLU-XX002", err.Error())
	}
}

// ── Path parameter helpers ────────────────────────────────────────────────────

func parseGlobalID(w http.ResponseWriter, r *http.Request) (identity_GlobalID, bool) {
	raw := chi.URLParam(r, "globalID")
	decoded, err := url.PathUnescape(raw)
	if err != nil || decoded == "" {
		writeError(w, http.StatusBadRequest, "NOLU-XX001", "invalid or missing GlobalID in path")
		return "", false
	}
	gid := identity_GlobalID(decoded)
	if err := gid.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "NOLU-XX001", fmt.Sprintf("malformed GlobalID: %v", err))
		return "", false
	}
	return gid, true
}

// ── JSON shape types (request/response) ──────────────────────────────────────

type localRefJSON struct {
	InstanceURL string `json:"instance_url"`
	TenantName  string `json:"tenant_name,omitempty"`
	TenantID    uint16 `json:"tenant_id"`
	EntityType  string `json:"entity_type"`
	LocalID     int    `json:"local_id"`
}

type instanceRefJSON struct {
	InstanceURL string `json:"instance_url"`
	TenantName  string `json:"tenant_name"`
	TenantID    uint16 `json:"tenant_id"`
}

// ── Logging middleware ────────────────────────────────────────────────────────

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		log.Debug().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", ww.Status()).
			Dur("elapsed", time.Since(start)).
			Msg("request")
	})
}

// ── Type bridges (avoid importing identity package directly) ──────────────────
// These keep the server package from having to re-import every package just
// to call interface methods. We use type aliases resolved at compile time.

func (l localRefJSON) toRef() localRef_type {
	return localRef_type{
		InstanceURL: l.InstanceURL,
		TenantName:  l.TenantName,
		TenantID:    l.TenantID,
		EntityType:  l.EntityType,
		LocalID:     l.LocalID,
	}
}

func (i instanceRefJSON) toRef() hotswap.InstanceRef {
	return hotswap.InstanceRef{
		InstanceURL: i.InstanceURL,
		TenantName:  i.TenantName,
		TenantID:    i.TenantID,
	}
}
