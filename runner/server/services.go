package server

// Deployed-image listing/deletion and the service lifecycle plus the
// /service/* data plane. Deploying an image is the deploy endpoint's.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/images"
)

// deployedImagesHandler lists deployed images. It deliberately omits the
// DeployKey so listing never leaks the credential that gates deletion.
// Clients render their own view; the runner serves no HTML.
func (s *Server) deployedImagesHandler(w http.ResponseWriter, r *http.Request) {
	list := s.d.Images.List()
	out := make([]api.ImageInfo, 0, len(list))
	for _, img := range list {
		out = append(out, images.PublicView(img))
	}
	jsonResponse(w, out)
}

// deleteImageHandler deletes a deployed image (DeployKey-gated at the
// route). Stops the service if running, removes the local Docker image,
// and drops the registry entry.
func (s *Server) deleteImageHandler(w http.ResponseWriter, r *http.Request) {
	imageName := r.PathValue("image")

	img, found := s.d.Images.Get(imageName)
	if !found {
		http.Error(w, "Image not found: "+imageName, http.StatusNotFound)
		return
	}

	slog.Info("Delete request for image", "image", imageName, "owner", img.Owner)

	if img.IsService {
		if _, exists := s.d.Svc.RunningPort(imageName); exists {
			slog.Info("Stopping running service before deletion", "image", imageName)
			if _, err := s.d.Svc.StopTask(imageName); err != nil {
				slog.Warn("Failed to stop service before deletion", "image", imageName, "error", err)
				// Continue anyway - we still want to delete the image.
			}
		}
		s.d.Svc.UnregisterTask(imageName)
	}

	// Remove the local copy of the image; failure is fine (it may simply
	// not exist on this machine).
	if s.d.Builder != nil {
		out, err := s.d.Builder.RemoveLocal(imageName)
		if err != nil {
			slog.Warn("Failed to remove Docker image (continuing anyway)", "image", imageName,
				"error", err, "output", out)
		}
	}

	if !s.d.Images.Delete(imageName) {
		http.Error(w, "Failed to delete image from config", http.StatusInternalServerError)
		return
	}

	slog.Info("Image deleted successfully", "image", imageName)
	w.WriteHeader(http.StatusNoContent)
}

// replicateImageHandler accepts an image entry pushed by a peer runner
// that just deployed it, so DeployKey authorization (job cancel, service
// start/stop) works fleet-wide regardless of which runner executes the
// job. Auth: the admin token, NOT the image DeployKey — the payload
// itself carries that.
func (s *Server) replicateImageHandler(w http.ResponseWriter, r *http.Request) {
	s.adminGate(func(w http.ResponseWriter, r *http.Request) {
		var img images.Image
		if err := json.NewDecoder(r.Body).Decode(&img); err != nil || img.Name == "" {
			http.Error(w, "valid image entry required", http.StatusBadRequest)
			return
		}
		// Host ports are assigned per-runner (raw-TCP services); never
		// adopt a peer's ServiceExternalPort.
		img.ServiceExternalPort = 0
		if !s.d.Images.Upsert(img) {
			http.Error(w, "failed to upsert image entry", http.StatusInternalServerError)
			return
		}
		slog.Info("Image entry replicated from peer runner", "name", img.Name)
		jsonResponse(w, api.DeployResponse{Message: "replicated " + img.Name})
	})(w, r)
}

// listServicesHandler answers the whole task registry (services at rest
// plus running jobs).
func (s *Server) listServicesHandler(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, s.d.Svc.List())
}

// taskStatusHandler answers one service, with container logs attached
// while it runs (stored crash logs survive container removal).
func (s *Server) taskStatusHandler(w http.ResponseWriter, r *http.Request) {
	info, ok := s.d.Svc.Status(r.PathValue("service"))
	if !ok {
		http.Error(w, "Service not registered", http.StatusNotFound)
		return
	}
	jsonResponse(w, info)
}

// startServiceHandler starts a deployed service (DeployKey-gated at the
// route).
func (s *Server) startServiceHandler(w http.ResponseWriter, r *http.Request) {
	msg, err := s.d.Svc.StartService(r.PathValue("service"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	fmt.Fprintln(w, msg)
}

// stopTaskHandler stops a running service (DeployKey-gated).
func (s *Server) stopTaskHandler(w http.ResponseWriter, r *http.Request) {
	msg, err := s.d.Svc.StopTask(r.PathValue("service"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	fmt.Fprintln(w, msg)
}

// serviceHandler is the data plane: a stateless reverse proxy (including
// WebSocket tunnels) to the running service container named in the path.
// A service that is not running answers with the historic 200 +
// {"ErrorMsg": ...} body, which the proxy package preserves.
func (s *Server) serviceHandler(w http.ResponseWriter, r *http.Request) {
	if s.d.Proxy == nil {
		errorResponse(w, "Service not running: "+r.PathValue("service"))
		return
	}
	s.d.Proxy.ServeService(w, r, r.PathValue("service"))
}
