package chat

import (
	"encoding/json"
	"net/http"
	"time"

	"goHTTPS/internal/proto"
)


const pollWait = 25 * time.Second


func (h *Hub) HTTPHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Name == "" {
			body.Name = "anonymous"
		}
		c := h.AddHTTPClient(body.Name)
		writeJSON(w, map[string]string{"id": c.ID})
	})

	mux.HandleFunc("/poll", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		msgs, ok := h.Poll(id, pollWait)
		if !ok {
			http.Error(w, "unknown client", http.StatusNotFound)
			return
		}
		if msgs == nil {
			msgs = []proto.Message{}
		}
		writeJSON(w, map[string]any{"messages": msgs})
	})

	mux.HandleFunc("/send", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		h.FromClient(id, body.Text)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/bye", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id != "" {
			h.Remove(id)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
