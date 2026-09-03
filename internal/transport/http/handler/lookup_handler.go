package handler

import (
	"errors"
	"net/http"

	"github.com/Ferousco-dev/holibrary-backend/internal/books"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/response"
)

// LookupHandler pre-fills a catalogue record from an external source.
type LookupHandler struct{ catalogue *books.OpenLibrary }

func NewLookupHandler(c *books.OpenLibrary) *LookupHandler { return &LookupHandler{catalogue: c} }

// Lookup fetches bibliographic metadata for a librarian cataloguing a book
// they are physically holding (REQ-017, REQ-018).
//
// What comes back is a SUGGESTION, not a holding. It fills in title, author and
// publication details so the librarian does not retype what is printed on the
// title page. It creates nothing: the librarian reviews it, corrects it, saves
// it, and then adds copies with their accession numbers. An external catalogue
// knowing a book exists says nothing about whether HOL owns one (I-10, DEC-007).
func (h *LookupHandler) Lookup(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	if isbn := query.Get("isbn"); isbn != "" {
		meta, err := h.catalogue.ByISBN(r.Context(), isbn)
		if err != nil {
			if errors.Is(err, books.ErrNotFound) {
				// Not an error the librarian should be stopped by. Africana,
				// OAU Publications and older Nigerian imprints are largely
				// absent from public catalogues and are catalogued by hand.
				response.JSON(w, http.StatusOK, map[string]any{
					"results": []books.Metadata{},
					"note":    "No external record. Enter the details by hand; many Africana and OAU Publications titles are not in public catalogues.",
				}, nil)
				return
			}
			// The external catalogue being unreachable must not read as our
			// system failing, and must not block cataloguing.
			response.Error(w, http.StatusBadGateway, "CATALOGUE_UNAVAILABLE",
				"The external catalogue could not be reached. You can still enter the book by hand.", nil)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{
			"results": []books.Metadata{meta},
		}, nil)
		return
	}

	q := query.Get("q")
	if q == "" {
		response.ValidationError(w, "Supply either an isbn or a q parameter.", nil)
		return
	}

	results, err := h.catalogue.Search(r.Context(), q, 10)
	if err != nil {
		response.Error(w, http.StatusBadGateway, "CATALOGUE_UNAVAILABLE",
			"The external catalogue could not be reached. You can still enter the book by hand.", nil)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"results": results}, nil)
}
