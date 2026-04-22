// Package handlers provides HTTP middleware and RFC 7807 problem detail responses.
package handlers

import (
	"encoding/json"
	"net/http"
)

type ProblemDetail struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail"`
	Instance string `json:"instance"`
}

func WriteProblem(w http.ResponseWriter, r *http.Request, status int, detail string) {
	pd := ProblemDetail{
		Type:     "about:blank",
		Title:    http.StatusText(status),
		Status:   status,
		Detail:   detail,
		Instance: r.URL.Path,
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(pd) //nolint:errchkjson // ProblemDetail has only safe fields
}
