package app

import (
	"github.com/go-ap/errors"
	"net/http"

	"github.com/go-chi/chi"
)

// HandleItemRedirect serves /i/{hash} request
func (h *handler) HandleItemRedirect(w http.ResponseWriter, r *http.Request) {
	val := r.Context().Value(RepositoryCtxtKey)
	itemLoader, ok := val.(CanLoadItems)
	if !ok {
		h.logger.Error("could not load item repository from Context")
		return
	}
	p, err := itemLoader.LoadItem(Filters{
		LoadItemsFilter: LoadItemsFilter{
			Key: Hashes{Hash(chi.URLParam(r, "hash"))},
		},
		MaxItems: 1,
	})
	if err != nil {
		h.HandleErrors(w, r, errors.NewNotValid(err, "oops!"))
		return
	}
	url := ItemPermaLink(p)
	h.Redirect(w, r, url, http.StatusMovedPermanently)
}