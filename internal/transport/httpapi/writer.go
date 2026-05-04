package httpapi

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	// Encoding errors after the status header has been written are
	// typically transient broken-pipe failures; nothing useful to do.
	_ = json.NewEncoder(w).Encode(body)
}
