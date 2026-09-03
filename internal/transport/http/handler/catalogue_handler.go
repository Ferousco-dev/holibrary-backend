package handler

import (
	"net/http"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/response"
)

type CatalogueHandler struct{ catalogue *service.CatalogueService }

func NewCatalogueHandler(c *service.CatalogueService) *CatalogueHandler {
	return &CatalogueHandler{catalogue: c}
}

// Search queries the catalogue (REQ-028..035).
//
// The query parameters mirror the access points of the card catalogue and of
// HOL's own OPAC: a free-text q, plus title, author and subject (DOM-007).
// Search is open to visitors who are not signed in, because the catalogue is
// public information; nothing personal is reachable here (REQ-037).
func (h *CatalogueHandler) Search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset, page := pagination(r)

	books, total, err := h.catalogue.Search(r.Context(), postgres.SearchParams{
		Query:         q.Get("q"),
		Title:         q.Get("title"),
		Author:        q.Get("author"),
		Subject:       q.Get("subject"),
		ISBN:          q.Get("isbn"),
		CallNumber:    q.Get("call_number"),
		LCCClass:      q.Get("class"),
		OnlyAvailable: boolParam(r, "available"),
		Limit:         limit,
		Offset:        offset,
	})
	if err != nil {
		response.FromError(w, err)
		return
	}

	// Each result carries its derived wing and availability, so a reader knows
	// both whether to come in and which way to walk (DOM-003).
	views := make([]service.BookView, 0, len(books))
	for _, b := range books {
		views = append(views, service.NewBookView(b))
	}

	response.JSON(w, http.StatusOK, views, &response.Meta{
		Page: page, PerPage: limit, Total: total,
	})
}

// Get returns one title with its copies (REQ-036, REQ-038).
func (h *CatalogueHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	book, copies, err := h.catalogue.Get(r.Context(), id)
	if err != nil {
		response.FromError(w, err)
		return
	}

	response.JSON(w, http.StatusOK, map[string]any{
		"book":   book,
		"copies": copies,
	}, nil)
}

type createBookRequest struct {
	Title              string   `json:"title"`
	Subtitle           string   `json:"subtitle"`
	ISBN13             string   `json:"isbn13"`
	ISBN10             string   `json:"isbn10"`
	Publisher          string   `json:"publisher"`
	PlaceOfPublication string   `json:"place_of_publication"`
	PublishedYear      *int     `json:"published_year"`
	CallNumber         string   `json:"call_number"`
	Description        string   `json:"description"`
	Authors            []string `json:"authors"`
	Subjects           []string `json:"subjects"`
}

// Create adds a bibliographic record (REQ-016).
func (h *CatalogueHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createBookRequest
	if !decode(w, r, &req) {
		return
	}

	book, err := h.catalogue.CreateBook(r.Context(), postgres.CreateBookParams{
		Title:              req.Title,
		Subtitle:           req.Subtitle,
		ISBN13:             req.ISBN13,
		ISBN10:             req.ISBN10,
		Publisher:          req.Publisher,
		PlaceOfPublication: req.PlaceOfPublication,
		PublishedYear:      req.PublishedYear,
		CallNumber:         req.CallNumber,
		Description:        req.Description,
		Authors:            req.Authors,
		Subjects:           req.Subjects,
	})
	if err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, service.NewBookView(book), nil)
}

// Archive removes a title from the catalogue without erasing its history
// (REQ-020, DOM-008).
func (h *CatalogueHandler) Archive(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	if err := h.catalogue.Archive(r.Context(), id); err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "archived"}, nil)
}

type addCopyRequest struct {
	AccessionNumber string `json:"accession_number"`
	LoanPolicy      string `json:"loan_policy"`
}

// AddCopy registers one physical volume (REQ-022).
//
// The accession number is the library's own per-item identifier and is unique
// across the collection, unlike the call number the copy inherits from its
// title (DOM-002).
func (h *CatalogueHandler) AddCopy(w http.ResponseWriter, r *http.Request) {
	bookID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var req addCopyRequest
	if !decode(w, r, &req) {
		return
	}

	policy := domain.LoanPolicy(req.LoanPolicy)
	if req.LoanPolicy != "" && !validLoanPolicy(policy) {
		response.ValidationError(w,
			"loan_policy must be circulating, reference_only, on_display or restricted.", nil)
		return
	}

	copy, err := h.catalogue.AddCopy(r.Context(), bookID, req.AccessionNumber, policy)
	if err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, copy, nil)
}

type updateCopyRequest struct {
	LoanPolicy *string `json:"loan_policy"`
	Status     *string `json:"status"`
}

// UpdateCopy marks a volume lost, damaged, withdrawn, or changes its loan
// policy (REQ-024..026).
func (h *CatalogueHandler) UpdateCopy(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var req updateCopyRequest
	if !decode(w, r, &req) {
		return
	}

	var policy *domain.LoanPolicy
	if req.LoanPolicy != nil {
		p := domain.LoanPolicy(*req.LoanPolicy)
		if !validLoanPolicy(p) {
			response.ValidationError(w, "That is not a valid loan policy.", nil)
			return
		}
		policy = &p
	}

	var status *domain.CopyStatus
	if req.Status != nil {
		s := domain.CopyStatus(*req.Status)
		if !validCopyStatus(s) {
			response.ValidationError(w, "That is not a valid copy status.", nil)
			return
		}
		status = &s
	}

	if err := h.catalogue.UpdateCopy(r.Context(), id, policy, status); err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "updated"}, nil)
}

// Enum values are checked here rather than left to the database, so a bad value
// becomes a clear 400 instead of a driver error (NFR-007).
func validLoanPolicy(p domain.LoanPolicy) bool {
	switch p {
	case domain.PolicyCirculating, domain.PolicyReferenceOnly,
		domain.PolicyOnDisplay, domain.PolicyRestricted:
		return true
	}
	return false
}

func validCopyStatus(s domain.CopyStatus) bool {
	switch s {
	case domain.CopyAvailable, domain.CopyOnLoan, domain.CopyLost,
		domain.CopyDamaged, domain.CopyWithdrawn:
		return true
	}
	return false
}
