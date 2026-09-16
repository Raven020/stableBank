package rules

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Raven020/stableBank/internal/httpx"
	"github.com/Raven020/stableBank/internal/store"
)

// RegisterRoutes wires every /rules HTTP endpoint described in
// CONTRACTS.md onto mux.
func (e *Engine) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /rules", e.handleList)
	mux.HandleFunc("GET /rules/applications", e.handleApplicationsAll)
	mux.HandleFunc("GET /rules/{id}", e.handleGet)
	mux.HandleFunc("GET /rules/{id}/versions", e.handleVersions)
	mux.HandleFunc("GET /rules/{id}/versions/{v}", e.handleVersion)
	mux.HandleFunc("GET /rules/{id}/applications", e.handleApplications)
	mux.HandleFunc("GET /rules/{id}/applications/{entity_id}", e.handleApplicationsByEntity)
	mux.HandleFunc("PUT /rules/{id}", e.handleSave)
	mux.HandleFunc("POST /rules/{id}/validate", e.handleValidateDryRun)
	mux.HandleFunc("POST /rules/{id}/reactivate", e.handleReactivate)
}

type versionDetail struct {
	Version       int            `json:"version"`
	RuleType      string         `json:"rule_type"`
	Content       map[string]any `json:"content,omitempty"`
	RawYAML       string         `json:"raw_yaml"`
	ContentHash   string         `json:"content_hash"`
	SourceCommit  string         `json:"source_commit"`
	Author        string         `json:"author"`
	ChangeNote    string         `json:"change_note"`
	EffectiveFrom time.Time      `json:"effective_from"`
	EffectiveTo   *time.Time     `json:"effective_to,omitempty"`
}

type versionSummary struct {
	Version       int        `json:"version"`
	EffectiveFrom time.Time  `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to,omitempty"`
	ChangeNote    string     `json:"change_note"`
	Author        string     `json:"author"`
}

func (e *Engine) toVersionDetail(v store.RuleVersion) versionDetail {
	d := versionDetail{
		Version:       v.Version,
		RuleType:      v.RuleType,
		RawYAML:       string(v.Content),
		ContentHash:   v.ContentHash,
		SourceCommit:  v.SourceCommit,
		Author:        v.Author,
		ChangeNote:    v.ChangeNote,
		EffectiveFrom: v.EffectiveFrom,
		EffectiveTo:   v.EffectiveTo,
	}
	if content, err := canonicalize(v.Content); err == nil {
		d.Content = content
	}
	return d
}

func (e *Engine) handleList(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"rules": e.List()})
}

func (e *Engine) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rule, err := e.Get(id)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	versions, err := e.store.ListRuleVersions(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	allVersions := make([]versionSummary, 0, len(versions))
	for _, v := range versions {
		allVersions = append(allVersions, versionSummary{
			Version:       v.Version,
			EffectiveFrom: v.EffectiveFrom,
			EffectiveTo:   v.EffectiveTo,
			ChangeNote:    v.ChangeNote,
			Author:        v.Author,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"meta":           rule.Meta,
		"version":        rule.Version,
		"effective_from": rule.EffectiveFrom,
		"content":        rule.Content,
		"raw_yaml":       string(rule.Raw),
		"examples":       rule.Examples,
		"all_versions":   allVersions,
	})
}

func (e *Engine) handleVersions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	versions, err := e.store.ListRuleVersions(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]versionDetail, 0, len(versions))
	for _, v := range versions {
		out = append(out, e.toVersionDetail(v))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"versions": out})
}

func (e *Engine) handleVersion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	vStr := r.PathValue("v")
	vNum, err := strconv.Atoi(vStr)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "version must be an integer")
		return
	}
	v, err := e.store.GetRuleVersion(r.Context(), id, vNum)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, e.toVersionDetail(v))
}

func parseLimit(r *http.Request) int {
	limit := 0
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			limit = n
		}
	}
	return limit
}

func (e *Engine) handleApplications(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	apps, err := e.store.ListRuleApplications(r.Context(), id, "", parseLimit(r))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"applications": apps})
}

func (e *Engine) handleApplicationsByEntity(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	entityID := r.PathValue("entity_id")
	apps, err := e.store.ListRuleApplications(r.Context(), id, entityID, parseLimit(r))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"applications": apps})
}

func (e *Engine) handleApplicationsAll(w http.ResponseWriter, r *http.Request) {
	entityID := r.URL.Query().Get("entity_id")
	apps, err := e.store.ListRuleApplications(r.Context(), "", entityID, parseLimit(r))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"applications": apps})
}

type saveRequest struct {
	YAML                 string `json:"yaml"`
	ChangeNote           string `json:"change_note"`
	AllowFailingExamples bool   `json:"allow_failing_examples"`
}

type reactivateRequest struct {
	Version    int    `json:"version"`
	ChangeNote string `json:"change_note"`
}

func (e *Engine) handleSave(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req saveRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := e.Save(r.Context(), id, []byte(req.YAML), SaveOptions{
		ChangeNote:           req.ChangeNote,
		AllowFailingExamples: req.AllowFailingExamples,
	})
	if err != nil {
		writeSaveError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (e *Engine) handleValidateDryRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req saveRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	raw := []byte(req.YAML)
	rule, err := e.Validate(raw)
	if err != nil {
		problems := []string{err.Error()}
		var ve *ValidationError
		if errors.As(err, &ve) {
			problems = ve.Problems
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"valid":    false,
			"problems": problems,
			"examples": []ExampleResult{},
			"diff":     "",
		})
		return
	}
	if rule.RuleID != id {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"valid":    false,
			"problems": []string{"rule_id in document does not match target rule_id"},
			"examples": []ExampleResult{},
			"diff":     "",
		})
		return
	}

	examples := e.RunExamples(rule)
	var prevRaw []byte
	if live, err := e.Get(id); err == nil {
		prevRaw = live.Raw
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"valid":    true,
		"problems": []string{},
		"examples": examples,
		"diff":     lineDiff(string(prevRaw), string(raw)),
	})
}

func (e *Engine) handleReactivate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req reactivateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := e.Reactivate(r.Context(), id, req.Version, req.ChangeNote)
	if err != nil {
		writeSaveError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

// writeSaveError maps Save/Reactivate errors onto the status codes fixed
// by CONTRACTS.md: 422 for schema validation failures, 409 when examples
// fail, 404 when the rule/version doesn't exist, 500 otherwise.
func writeSaveError(w http.ResponseWriter, err error) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", ve.Problems)
		return
	}
	var ef *ExamplesFailedError
	if errors.As(err, &ef) {
		httpx.WriteError(w, http.StatusConflict, "examples_failed", map[string]any{"examples": ef.Results})
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
}
