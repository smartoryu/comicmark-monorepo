package bookmark

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"comicmark/go-modular/internal/httpx"
	"comicmark/go-modular/internal/modules/auth"
	"comicmark/go-modular/internal/pkg/scraper"
	"comicmark/go-modular/internal/pkg/validation"
)

// handler turns HTTP requests into use case calls and domain errors back into
// responses.
type handler struct {
	service   *Service
	validator *validation.Validator
}

func newHandler(service *Service) *handler {
	return &handler{service: service, validator: validation.New()}
}

// --- preview ----------------------------------------------------------------

// Parse handles POST /api/v1/bookmarks/parse.
//
// Requiring authentication keeps the fetch endpoint from being an open proxy:
// every request it serves is one the server makes on someone's behalf.
func (h *handler) Parse(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, httpx.MsgUnauthorized)
		return
	}

	var req parseRequest
	if !httpx.DecodeJSONOrFail(w, r, &req) {
		return
	}
	if fields := h.validator.FieldErrors(req); fields != nil {
		httpx.WriteFieldErrors(w, r, fields)
		return
	}

	preview, err := h.service.Parse(r.Context(), req.URL)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	_ = principal
	httpx.WriteJSON(w, r, http.StatusOK, newPreviewResponse(preview))
}

// --- create -----------------------------------------------------------------

// Create handles POST /api/v1/bookmarks.
func (h *handler) Create(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, httpx.MsgUnauthorized)
		return
	}

	var req createRequest
	if !httpx.DecodeJSONOrFail(w, r, &req) {
		return
	}
	if fields := h.validator.FieldErrors(req); fields != nil {
		httpx.WriteFieldErrors(w, r, fields)
		return
	}

	bookmark, err := h.service.Create(r.Context(), principal.UserID, CreateInput{
		Title:         req.Title,
		CurrentURL:    req.CurrentURL,
		Chapter:       req.Chapter,
		ChapterNumber: req.ChapterNumber,
		CoverURL:      req.CoverURL,
		Status:        req.Status,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusCreated, newBookmarkResponse(bookmark))
}

// --- read -------------------------------------------------------------------

// List handles GET /api/v1/bookmarks.
func (h *handler) List(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, httpx.MsgUnauthorized)
		return
	}

	filter, err := filterFromQuery(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	bookmarks, err := h.service.List(r.Context(), principal.UserID, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	total, err := h.service.Count(r.Context(), principal.UserID, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	out := make([]bookmarkResponse, 0, len(bookmarks))
	for _, b := range bookmarks {
		out = append(out, newBookmarkResponse(b))
	}

	httpx.WriteJSON(w, r, http.StatusOK, listResponse{
		Bookmarks: out,
		Total:     total,
		Limit:     filter.Limit,
		Offset:    filter.Offset,
	})
}

// Get handles GET /api/v1/bookmarks/{id}.
func (h *handler) Get(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, httpx.MsgUnauthorized)
		return
	}

	bookmark, err := h.service.Get(r.Context(), principal.UserID, r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, newBookmarkResponse(bookmark))
}

// --- edit -------------------------------------------------------------------

// Update handles PATCH /api/v1/bookmarks/{id}.
func (h *handler) Update(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, httpx.MsgUnauthorized)
		return
	}

	var req updateRequest
	if !httpx.DecodeJSONOrFail(w, r, &req) {
		return
	}
	if fields := h.validator.FieldErrors(req); fields != nil {
		httpx.WriteFieldErrors(w, r, fields)
		return
	}

	in := UpdateInput{
		Chapter:  req.Chapter,
		CoverURL: req.CoverURL,
		Status:   req.Status,
	}
	if strings.TrimSpace(req.Title) != "" {
		in.Title = &req.Title
	}

	// An absent chapter_number leaves the stored one alone; an explicit null
	// clears it; a number sets it.
	switch {
	case len(req.ChapterNumber) == 0:
		// Left as it is.
	case string(req.ChapterNumber) == "null":
		in.ClearChapterNumber = true
	default:
		var number float64
		if err := json.Unmarshal(req.ChapterNumber, &number); err != nil {
			httpx.WriteFieldError(w, r, "chapter_number", "Must be a number, or null to clear it.")
			return
		}
		in.ChapterNumber = &number
	}

	bookmark, err := h.service.Update(r.Context(), principal.UserID, r.PathValue("id"), in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, newBookmarkResponse(bookmark))
}

// ReadNext handles PATCH /api/v1/bookmarks/{id}/read-next.
func (h *handler) ReadNext(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, httpx.MsgUnauthorized)
		return
	}

	bookmark, err := h.service.ReadNext(r.Context(), principal.UserID, r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, newBookmarkResponse(bookmark))
}

// MarkRead handles POST /api/v1/bookmarks/{id}/read.
func (h *handler) MarkRead(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, httpx.MsgUnauthorized)
		return
	}

	bookmark, err := h.service.MarkRead(r.Context(), principal.UserID, r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, newBookmarkResponse(bookmark))
}

// Delete handles DELETE /api/v1/bookmarks/{id}.
func (h *handler) Delete(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, httpx.CodeUnauthorized, httpx.MsgUnauthorized)
		return
	}

	if err := h.service.Delete(r.Context(), principal.UserID, r.PathValue("id")); err != nil {
		h.writeError(w, r, err)
		return
	}

	httpx.NoContent(w)
}

// --- helpers ----------------------------------------------------------------

// filterFromQuery reads the library filters from the query string.
//
// The page size is resolved here rather than left to the repository, so the
// response can echo the size that was actually applied instead of the zero the
// client did not send.
func filterFromQuery(r *http.Request) (Filter, error) {
	query := r.URL.Query()

	filter := Filter{
		Status: Status(strings.TrimSpace(query.Get("status"))),
		Query:  strings.TrimSpace(query.Get("q")),
		Limit:  DefaultPageSize,
	}

	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 {
			return Filter{}, newFieldError("limit", "Limit must be a positive number.", nil)
		}
		if limit > MaxPageSize {
			limit = MaxPageSize
		}
		filter.Limit = limit
	}

	if raw := strings.TrimSpace(query.Get("offset")); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return Filter{}, newFieldError("offset", "Offset must be zero or more.", nil)
		}
		filter.Offset = offset
	}

	return filter, nil
}

// writeError maps a domain error onto the API's error envelope.
//
// The specific sentinels are checked before the generic field-error branch on
// purpose: a field error carries a sentinel, so testing for the generic shape
// first would report every specific case as a plain 400 and make codes such as
// chapter_not_numeric unreachable.
func (h *handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var fieldErr *FieldError

	switch {
	case errors.Is(err, ErrChapterNotNumeric):
		// 409 rather than an error: the client is expected to open an editor and
		// try again, which is a normal part of the flow rather than a failure.
		httpx.WriteError(w, r, http.StatusConflict, "chapter_not_numeric",
			"This chapter has no number to advance. Edit it to set the next one.")

	case errors.Is(err, ErrDuplicate):
		httpx.WriteError(w, r, http.StatusConflict, "duplicate_bookmark",
			"You have already saved this link.")

	case errors.Is(err, ErrNothingToUpdate):
		httpx.WriteError(w, r, http.StatusBadRequest, httpx.CodeBadRequest,
			"There was nothing to change in that request.")

	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, httpx.MsgNotFound)

	case errors.Is(err, scraper.ErrURLNotAllowed):
		// The reader gave us a link we will not fetch, and they can fix it.
		httpx.WriteError(w, r, http.StatusBadRequest, "url_not_allowed",
			"That link cannot be opened. Check it and try again.")

	case errors.Is(err, scraper.ErrResolutionFailed):
		httpx.WriteError(w, r, http.StatusBadRequest, "url_not_found",
			"That address could not be found. Check it and try again.")

	case errors.As(err, &fieldErr):
		httpx.WriteFieldError(w, r, fieldErr.Field, fieldErr.Message)

	default:
		httpx.WriteError(w, r, http.StatusInternalServerError, httpx.CodeInternal, httpx.MsgInternal)
	}
}
