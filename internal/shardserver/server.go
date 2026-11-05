// Package shardserver exposes stateless banking and internal participant APIs.
package shardserver

import (
	"context"
	"net/http"
	"time"

	"github.com/azare242/gobank/internal/httpbank"
	"github.com/azare242/gobank/internal/participant"
	"github.com/azare242/gobank/internal/repository"
	"github.com/azare242/gobank/internal/service"
	"github.com/azare242/gobank/internal/serviceerr"
	"github.com/azare242/gobank/internal/shardmap"
)

type Server struct {
	Store                      *repository.SQLStore
	Map                        *shardmap.Map
	ShardID, ReplicaID, Secret string
	Participant                *participant.Participant
}

func (s *Server) owned(id string) error {
	owner, e := s.Map.Owner(id)
	if e != nil {
		return serviceerr.New(400, "invalid_account")
	}
	if owner.ID != s.ShardID {
		return serviceerr.New(400, "wrong_shard")
	}
	return nil
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		httpbank.JSON(w, 200, map[string]string{"status": "alive"})
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if e := s.Participant.Ready(ctx); e != nil {
			httpbank.Error(w, e)
			return
		}
		httpbank.JSON(w, 200, map[string]string{"status": "ready"})
	})
	internal := http.NewServeMux()
	internal.HandleFunc("POST /internal/balance", func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			UserID  uint   `json:"user_id"`
			Account string `json:"account"`
		}
		if e := httpbank.Decode(w, r, &m); e != nil {
			httpbank.Error(w, e)
			return
		}
		if e := s.owned(m.Account); e != nil {
			httpbank.Error(w, e)
			return
		}
		out, e := s.Store.Balance(r.Context(), m.UserID, m.Account)
		if e != nil {
			httpbank.Error(w, e)
			return
		}
		httpbank.JSON(w, 200, out)
	})
	internal.HandleFunc("POST /internal/mutate", func(w http.ResponseWriter, r *http.Request) {
		var m repository.Mutation
		if e := httpbank.Decode(w, r, &m); e != nil {
			httpbank.Error(w, e)
			return
		}
		if e := s.owned(m.Source); e != nil {
			httpbank.Error(w, e)
			return
		}
		if m.Action == "transfer" {
			if e := s.owned(m.Destination); e != nil {
				httpbank.Error(w, e)
				return
			}
		}
		out, e := service.NewSQLBank(s.Store).Mutate(r.Context(), m)
		if e != nil {
			httpbank.Error(w, e)
			return
		}
		httpbank.JSON(w, 200, out)
	})
	internal.HandleFunc("POST /internal/prepare", func(w http.ResponseWriter, r *http.Request) {
		var cmd participant.Command
		if e := httpbank.Decode(w, r, &cmd); e != nil {
			httpbank.Error(w, e)
			return
		}
		out, e := s.Participant.Prepare(r.Context(), cmd)
		if e != nil {
			httpbank.Error(w, e)
			return
		}
		httpbank.JSON(w, 200, out)
	})
	internal.HandleFunc("POST /internal/decide", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Command  participant.Command `json:"command"`
			Decision string              `json:"decision"`
		}
		if e := httpbank.Decode(w, r, &body); e != nil {
			httpbank.Error(w, e)
			return
		}
		out, e := s.Participant.Decide(r.Context(), body.Command, body.Decision)
		if e != nil {
			httpbank.Error(w, e)
			return
		}
		httpbank.JSON(w, 200, out)
	})
	internal.HandleFunc("POST /internal/status", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID string `json:"id"`
		}
		if e := httpbank.Decode(w, r, &body); e != nil {
			httpbank.Error(w, e)
			return
		}
		out, e := s.Participant.Status(r.Context(), body.ID)
		if e != nil {
			httpbank.Error(w, e)
			return
		}
		httpbank.JSON(w, 200, out)
	})
	withEpoch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		internal.ServeHTTP(w, r.WithContext(repository.WithEpoch(r.Context(), r.Header.Get("X-Shard-Epoch"))))
	})
	mux.Handle("/internal/", httpbank.Internal(s.Secret, withEpoch))
	return httpbank.Log(s.ShardID, s.ReplicaID, mux)
}
