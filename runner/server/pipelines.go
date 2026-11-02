package server

// Pipelines are not forwarded to a backend the way /api/run is: the
// sequencing state lives in the accepting runner's job database, so the
// runner that takes the submission owns the run for its whole life.
// Individual steps still reach an external backend when their job type
// says so — that is where cross-machine capacity comes from.

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/pipeline"
	"github.com/uidbz/tie/runner/sec"
)

func (s *Server) submitPipelineHandler(w http.ResponseWriter, r *http.Request) {
	var req api.PipelineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "malformed pipeline request: "+err.Error(), http.StatusBadRequest)
		slog.Error("Failed to decode pipeline request", "error", err)
		return
	}

	p, err := s.d.Pipe.Submit(&req, sec.GetBaseURL(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonResponse(w, p)
}

func (s *Server) pipelinesHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pipelines, err := s.d.Store.ListPipelines(parseSince(q.Get("since")), q.Get("user"), q.Get("status"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if pipelines == nil {
		pipelines = []*api.Pipeline{}
	}
	jsonResponse(w, pipelines)
}

func (s *Server) pipelineStatusHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, ok := s.d.Store.GetPipeline(id)
	if !ok {
		http.Error(w, "no pipeline with ID "+id, http.StatusNotFound)
		return
	}
	jsonResponse(w, p)
}

// authorizePipeline loads the pipeline the request names and checks the
// caller's DeployKey against its entry step, writing the response and
// returning nil if the caller may not proceed. Both mutating endpoints
// act on a run somebody else started — cancel kills its jobs, resume
// starts new ones — so both need the same key the jobs themselves are
// gated on (see pipeline.EntryHandler for why that step's key).
func (s *Server) authorizePipeline(w http.ResponseWriter, r *http.Request) *api.Pipeline {
	id := r.PathValue("id")
	p, ok := s.d.Store.GetPipeline(id)
	if !ok {
		http.Error(w, "no pipeline with ID "+id, http.StatusNotFound)
		return nil
	}

	user, pass, hasAuth := r.BasicAuth()
	if !s.d.Auth.ValidateDeployKey(s.d.Images, pipeline.EntryHandler(p), user, pass, hasAuth) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return nil
	}
	return p
}

func (s *Server) cancelPipelineHandler(w http.ResponseWriter, r *http.Request) {
	p := s.authorizePipeline(w, r)
	if p == nil {
		return
	}

	if err := s.d.Pipe.Cancel(p.PipelineID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	jsonResponse(w, map[string]string{"status": api.StatusCancelled})
}

// resumeRequest reruns part of a finished pipeline. From names the first
// step to rerun; everything downstream of it reruns too. With reads a
// step's output from a hash the caller supplies instead of rerunning it.
type resumeRequest struct {
	From string
	With map[string]string
}

func (s *Server) resumePipelineHandler(w http.ResponseWriter, r *http.Request) {
	p := s.authorizePipeline(w, r)
	if p == nil {
		return
	}

	var req resumeRequest
	// An empty body is the common case ("retry what did not finish"), so
	// a decode failure on no content is not an error.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed resume request: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if from := r.URL.Query().Get("from"); from != "" {
		req.From = from
	}

	resumed, err := s.d.Pipe.Resume(p.PipelineID, req.From, req.With)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	jsonResponse(w, resumed)
}
