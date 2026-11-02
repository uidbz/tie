package server

// The deploy endpoint: the CLI uploads project sources to the filehost,
// and this endpoint downloads them, builds the image, and either runs a
// test job (test) or publishes the image to the registry and the
// deployment state (prod). One request, one transcript back.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"

	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/executor"
	"github.com/uidbz/tie/runner/images"
	"github.com/uidbz/tie/runner/sec"
)

func (s *Server) deployHandler(w http.ResponseWriter, r *http.Request) {
	if s.d.Builder == nil {
		http.Error(w, "image building not enabled on this runner", http.StatusNotImplemented)
		return
	}

	var req api.DeployRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorResponse(w, err.Error())
		return
	}
	deployManager := sec.GetBaseURL(r)

	projectdata, err := s.d.Builder.DownloadProject(req.ProjectHash)
	if err != nil {
		slog.Error("Failed to download project", "project_hash", req.ProjectHash, "error", err)
		jsonResponse(w, api.DeployResponse{ErrorMsg: err.Error()})
		return
	}

	buildOutput, buildErr := s.d.Builder.Build(req.Name, projectdata, req.NoCache, req.Debug)
	if buildErr != nil {
		slog.Error("Failed to build image", "name", req.Name, "error", buildErr)
		jsonResponse(w, api.DeployResponse{ErrorMsg: buildErr.Error()})
		return
	}

	switch req.Command {
	case "test":
		slog.Info("Deploy test requested", "name", req.Name, "job_type", req.JobType)
		jobReq := api.JobRequest{
			JobType:   req.JobType,
			Handler:   req.Name,
			InputHash: req.TestInputHash,
			AppHash:   req.AppHash,
			Debug:     req.Debug,
			Settings:  req.TestSettingsData,
		}

		if req.JobType == executor.WEBSERVICE || req.JobType == executor.TCPSERVICE {
			errorResponse(w, "Services require --prod")
			return
		}

		// A test job is a non-prod job on this runner: it exercises the
		// just-built local image, so it never goes to a remote backend
		// (which could not pull an unpushed image anyway).
		job := s.d.Sched.NewJob(jobReq, false, deployManager)
		s.d.Sched.Submit(job)
		snap, _ := s.d.Sched.LiveCopy(job.JobID)

		jsonResponse(w, api.DeployResponse{
			Message:   buildOutput + "\n" + snap.Log,
			JobStatus: *snap,
			ErrorMsg:  snap.ErrorMsg,
		})

	case "prod":
		slog.Info("Production deploy requested", "name", req.Name, "owner", req.Owner)

		existing, found := s.d.Images.Get(req.Name)
		if found && existing.DeployKey != req.DeployKey {
			slog.Warn("Wrong deploy key provided", "name", req.Name, "owner", existing.Owner)
			jsonResponse(w, api.DeployResponse{ErrorMsg: "Wrong DeployKey provided! " + existing.Name +
				" belongs to: " + existing.Owner +
				"\nTo solve: Either change the project name or update config with correct key."})
			return
		}

		pushOutput, pushErr := s.d.Builder.Push(req.Name)
		if pushErr != nil { // the original tested the build error here; a failed push must abort
			slog.Error("Failed to push image", "name", req.Name, "error", pushErr)
			jsonResponse(w, api.DeployResponse{ErrorMsg: pushErr.Error()})
			return
		}

		isService := req.JobType == executor.WEBSERVICE || req.JobType == executor.TCPSERVICE
		image := images.Image{
			Name:                     req.Name,
			Owner:                    req.Owner,
			Description:              req.Description,
			Documentation:            req.DocumentationLink,
			DeployKey:                req.DeployKey,
			IsService:                isService,
			ServiceInternalPort:      req.ServiceInternalPort,
			ServiceAutostart:         req.ServiceAutostart,
			ServiceStartOnAccess:     req.ServiceStartOnAccess,
			ServiceInactivityTimeout: req.ServiceInactivityTimeout,
		}
		if image.DeployKey == "" {
			image.DeployKey = images.NewDeployKey()
		}

		// A raw-TCP service is reached directly on a stable host port the
		// runner owns. Allocate it once on first deploy and preserve it
		// on re-deploys.
		if req.JobType == executor.TCPSERVICE {
			if found && existing.ServiceExternalPort > 0 {
				image.ServiceExternalPort = existing.ServiceExternalPort
			} else {
				port, err := s.d.Svc.AllocatePersistentPort()
				if err != nil {
					jsonResponse(w, api.DeployResponse{ErrorMsg: err.Error()})
					return
				}
				image.ServiceExternalPort = port
			}
		}

		// ServiceProxyRewrite is an operator setting, not something the
		// deploying project owns, so a re-deploy must not reset it back
		// to the fleet default.
		if found && existing.ServiceProxyRewrite != "" {
			image.ServiceProxyRewrite = existing.ServiceProxyRewrite
		}

		s.d.Images.Upsert(image)

		// Replicate the authorization entry fleet-wide.
		if s.d.Replicator != nil {
			s.d.Replicator.ReplicateImage(image)
		}

		if s.d.ExtDir != nil {
			if dir := s.d.ExtDir(); dir != "" {
				os.MkdirAll(dir+"/"+executor.ContainerName(image.Name), 0o755)
			}
		}

		if isService {
			s.d.Svc.RegisterTask(image.Name, api.TaskTypeService)
		}

		slog.Info("Image deployed successfully", "name", image.Name, "owner", image.Owner,
			"is_service", image.IsService, "external_port", image.ServiceExternalPort)
		jsonResponse(w, api.DeployResponse{
			Message:             buildOutput + "\n" + pushOutput,
			DeployKey:           image.DeployKey,
			ServiceExternalPort: image.ServiceExternalPort,
		})

	default:
		jsonResponse(w, api.DeployResponse{ErrorMsg: "Unknown command"})
	}
}
