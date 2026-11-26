// tie-runner is the execution daemon: one process owning jobs,
// pipelines, services, deploy builds, fleet membership, and the
// /service/* reverse proxy — the assembled runner behind the protocol in
// docs/runner-api.md. Everything protocol-shaped lives in the runner/*
// engine packages; this binary owns only the configuration document and
// the process lifecycle.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/uidbz/tie/runner/api"
	"github.com/uidbz/tie/runner/deploy"
	"github.com/uidbz/tie/runner/dispatch"
	"github.com/uidbz/tie/runner/executor"
	"github.com/uidbz/tie/runner/fleet"
	"github.com/uidbz/tie/runner/imagecheck"
	"github.com/uidbz/tie/runner/images"
	"github.com/uidbz/tie/runner/pipeline"
	"github.com/uidbz/tie/runner/proxy"
	"github.com/uidbz/tie/runner/scheduler"
	"github.com/uidbz/tie/runner/sec"
	"github.com/uidbz/tie/runner/server"
	"github.com/uidbz/tie/runner/service"
	"github.com/uidbz/tie/runner/store"
	"github.com/uidbz/tie/version"

	"github.com/uidbz/conf"
)

var (
	jobman     *scheduler.Manager
	svc        *service.Manager
	imgReg     *images.Registry
	fleetReg   *fleet.Registry
	replicator *fleet.Replicator
	certs      = &sec.CertManager{}
)

func main() {
	// One-shot fleet join: must run before the normal config load because
	// it WRITES the config.
	if len(os.Args) > 1 && os.Args[1] == "--register" {
		os.Exit(registerWithSeed(os.Args[2:]))
	}

	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		info := version.Get()
		fmt.Printf("tie-runner version %s\nGit commit: %s\nBuilt: %s\n", info.Version, info.Commit, info.Date)
		return
	}

	info := version.Get()
	applyDebugLogging(false) // INFO-level JSON logging from here on

	if _, err := conf.LoadFromCurrentDir(configFileName, &config); err != nil {
		slog.Error("Error reading config file", "error", err)
		os.Exit(1)
	}
	for _, w := range validateConfig(&config) {
		slog.Warn("Config warning", "warning", w)
	}
	applyDebugLogging(config.DebugLogging)

	// SIGHUP re-reads the config file and swaps the runtime-mutable
	// fields (capabilities, backends, job-type specs, capacity) without
	// a restart.
	startConfigReloadSignal()

	// Fleet registry (this host is a seed when FleetJoinToken is set);
	// the file lives beside the config.
	fleetReg = fleet.Load(conf.PathCurrentDir(fleetRegistryFile))

	// Deployed-image table; every change is written back to the config
	// file, so deploys survive a restart.
	imgReg = images.New(saveDeployedImages, config.DeployedImages...)

	// Services: stable host ports reserved from the persisted table,
	// then task registration and autostart.
	var reserved []int
	for _, img := range config.DeployedImages {
		if img.ServiceExternalPort > 0 {
			reserved = append(reserved, img.ServiceExternalPort)
		}
	}
	svc = service.New(service.Config{
		Images:   imgReg,
		Registry: dockerRegistry(),
		ExtDir:   extDirAccess(),
		Ports:    service.NewPortPool(2000, 65535, reserved...),
	})
	svc.Restore()

	// Durable job/pipeline history (embedded tiedb).
	storeDir := config.JobDBPath
	if storeDir == "" {
		storeDir = "jobs"
	}
	st, err := store.NewStore(storeDir)
	if err != nil {
		slog.Error("Failed to open job store", "path", storeDir, "error", err)
		os.Exit(1)
	}
	if n, err := st.ReconcileInterrupted(); err != nil {
		slog.Error("Failed to reconcile interrupted jobs", "path", storeDir, "error", err)
		os.Exit(1)
	} else if n > 0 {
		slog.Info("Marked interrupted jobs failed", "count", n)
	}
	slog.Info("Job store ready", "path", storeDir)

	// The pipeline sequencer wires into the same submit/validate path as
	// /api/run, via a late-bound server pointer: the closures run long
	// after srv is assigned.
	var srv *server.Server
	pipe := pipeline.New(pipeline.Config{
		Store:     st,
		Filehost:  filehostAccess(),
		StoreName: storeNameAccess(),
		SubmitJob: func(req api.JobRequest, deployManager string) (*api.JobStatus, error) {
			return srv.SubmitJob(req, deployManager)
		},
		CancelJob: func(jobID string) error { return jobman.Cancel(jobID) },
		ValidateStep: func(req api.JobRequest) error {
			return srv.ValidateStep(req)
		},
	})

	// Scheduler: the admission pool, queue, and finalize path. The
	// local runtime re-reads its container settings from the config per
	// job, so a hot reload takes effect on the next container.
	cpu, ramMB, gpu := scheduler.ResolveCapacity(cfgPool())
	jobman = scheduler.New(scheduler.Config{
		Local:       liveLocal{},
		Store:       st,
		TotalCPU:    cpu,
		TotalRAMMB:  ramMB,
		TotalGPU:    gpu,
		ResolveSpec: cfgJobTypeSpec,
		Filehost:    filehostAccess(),
		OnFinished:  pipe.JobFinished,
		OnStart: func(job *api.JobStatus) {
			svc.RegisterTask(job.Handler, api.TaskTypeJob)
			svc.Activate(job.Handler, api.TaskTypeJob, 0)
		},
		OnEnd: func(job *api.JobStatus) {
			svc.Deactivate(job.Handler)
			svc.UnregisterTask(job.Handler)
		},
	})
	jobman.StartCleanUp(time.Hour)

	// After the scheduler is up, since resuming a pipeline submits its
	// next steps to it.
	pipe.ResumeAll()

	// Fleet machinery.
	replicator = &fleet.Replicator{
		Backends:   backendsAccess,
		AdminUser:  authUserAccess,
		AdminToken: adminTokenAccess,
	}

	// startTLS is assigned below (it needs the TLS listener, which needs
	// the assembled mux); the OnCertInstalled hook only fires over HTTP,
	// well after startup.
	var startTLS func()

	srv = server.New(server.Deps{
		Store:      st,
		Sched:      jobman,
		Pipe:       pipe,
		Svc:        svc,
		Images:     imgReg,
		Auth:       &sec.Auth{User: authUserAccess},
		Certs:      certs,
		Fleet:      fleetReg,
		Replicator: replicator,
		Dispatch: &dispatch.Router{
			Capabilities: capabilitiesAccess,
			Backends:     backendsAccess,
			ResolveSpec:  jobman.SpecFor,
			CanFit:       jobman.CanFit,
		},
		Proxy: proxy.New(proxy.Config{
			ExternalPort: svc.RunningPort,
			RewriteScope: func(name string) string {
				img, ok := imgReg.Get(name)
				if !ok {
					return ""
				}
				return img.ServiceProxyRewrite
			},
		}),
		Builder: &deploy.Builder{
			Registry:  dockerRegistry,
			Filehost:  filehostAccess,
			BuildArgs: buildArgsAccess,
		},
		Filehost:        filehostAccess,
		StoreName:       storeNameAccess,
		AdminToken:      adminTokenAccess,
		FleetJoinToken:  fleetJoinToken,
		FleetSeed:       fleetSeedAccess,
		Backends:        backendsAccess,
		Capabilities:    capabilitiesAccess,
		ExtDir:          extDirAccess,
		TLSCertPath:     tlsCertPathAccess,
		TLSKeyPath:      tlsKeyPathAccess,
		ImageCheck:      func(handler string) error { return imagecheck.Check(dockerRegistry(), handler) },
		BuildHostConfig: buildHostConfig,
		Reload:          reloadConfig,
		OnCertInstalled: func() { startTLS() },
		Version:         info.Version,
		GitCommit:       info.Commit,
		BuildDate:       info.Date,
	})

	// Outer mux: static publishing (client binaries, CA certs) coexists
	// with the frozen route table. The explicit /files/ prefix beats the
	// "/" catch-all.
	mux := http.NewServeMux()
	server.MountFileServer(mux, config.FileServer)
	server.StartPlainFileServer(config.FileServer)
	mux.Handle("/", srv.Handler())

	// One listener and one server for the whole process. Without a
	// loadable cert the listener serves plain HTTP; a cert push (the
	// OnCertInstalled hook) switches new connections to TLS in place.
	// Starting a separate TLS server on the same address used to fail
	// with "address already in use" and exit the runner. Serving through
	// the cert manager lets POST /api/cert hot-swap the certificate
	// without a restart (service containers keep running).
	ln, err := net.Listen("tcp", config.Address)
	if err != nil {
		slog.Error("Listen failed", "address", config.Address, "error", err)
		os.Exit(1)
	}
	listener := sec.NewUpgradableListener(ln, &tls.Config{
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(hi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return certs.GetCertificate(hi)
		},
	})
	httpServer := &http.Server{Handler: mux}
	startTLS = func() {
		if listener.EnableTLS() {
			slog.Info("HTTPS enabled", "address", config.Address)
		}
	}

	// TLS availability: a loadable keypair means HTTPS from startup;
	// otherwise plain HTTP, and the cert-push hook flips this host over
	// when a cert arrives.
	if config.TLSCert != "" && config.TLSKey != "" {
		if err := certs.Load(config.TLSCert, config.TLSKey); err != nil {
			slog.Warn("TLS keypair not loadable, serving plain HTTP", "error", err)
		}
	}
	_, certErr := os.Stat(config.TLSCert)
	_, keyErr := os.Stat(config.TLSKey)
	_, certLiveErr := certs.GetCertificate(nil) // nil when a cert is loaded
	if certErr == nil && keyErr == nil && certLiveErr == nil {
		startTLS()
	} else {
		slog.Info("HTTP server starting (TLS disabled until a cert is pushed)", "address", config.Address)
	}
	serveErr := make(chan error, 1)
	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	// Graceful shutdown.
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)
	select {
	case <-stopChan:
		slog.Info("Shutting down gracefully")
	case err := <-serveErr:
		// Not os.Exit: the graceful path below still stops services and
		// closes the store.
		slog.Error("HTTP server error", "error", err)
	}

	// Stop accepting requests first, so nothing reaches the managers
	// while they shut down.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpServer.Shutdown(ctx)

	svc.StopAll()
	replicator.Wait()
	jobman.Stop()

	if err := st.Close(); err != nil {
		slog.Error("Error closing job store", "error", err)
	}
	slog.Info("Server stopped")
}

// saveDeployedImages is the images.Registry persistence hook: the
// deployed-images table lives in the config file, so a deploy (or an
// image delete, or fleet replication) rewrites that section atomically.
func saveDeployedImages(list []images.Image) error {
	config.Lock()
	defer config.Unlock()
	config.DeployedImages = list
	return config.Save()
}

// liveLocal adapts the Docker runtime to hot reload: the container
// settings (registry, filehost, store, scratch dir, memory cap) are read
// from the live config for every Run/Stop, so a config reload takes
// effect on the next container. The runtime struct is stateless apart
// from those settings, which is what makes one-per-call construction
// cheap and race-free.
type liveLocal struct{}

func (liveLocal) Run(ctx context.Context, job *api.JobStatus, opts executor.RunOptions) executor.ExitInfo {
	return dockerRuntime().Run(ctx, job, opts)
}

func (liveLocal) Stop(containerName string) error {
	return dockerRuntime().Stop(containerName)
}

func dockerRuntime() *executor.Docker {
	config.Lock()
	defer config.Unlock()
	return &executor.Docker{
		Registry:     config.DockerRegistry,
		Filehost:     config.Filehost,
		Store:        config.Store,
		ExtDir:       config.ExtDir,
		DefaultMemMB: config.DefaultJobMemoryMB,
		// The zero protocol accepts the canonical marker and the
		// historical one (D6), so old container images keep working.
		Protocol: executor.Protocol{},
	}
}
