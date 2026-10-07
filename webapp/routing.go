package main

import (
	"encoding/json"
	"net/http"
)

// A route says where a link's segments run when it was moved by hand: the
// coordinates of its segments between the ends, alternately across and along
// its source's axis. The frontend draws it; the server only keeps it.
type route []float64

const maxRouteLanes = 64

// Reads {"route": [...] | null} from a request. A nil result clears the
// route. Writes the error and returns false if the body is invalid.
func decodeRoute(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	var body struct {
		Route *route `json:"route"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return nil, false
	}
	if body.Route == nil {
		return nil, true
	}
	if len(*body.Route) > maxRouteLanes {
		http.Error(w, "route has too many segments", http.StatusBadRequest)
		return nil, false
	}
	b, err := json.Marshal(body.Route)
	if err != nil {
		http.Error(w, "invalid route", http.StatusBadRequest)
		return nil, false
	}
	return b, true
}

// A stored route as JSON, null when there is none.
func routeJSON(stored []byte) json.RawMessage {
	if len(stored) == 0 {
		return json.RawMessage("null")
	}
	return stored
}
