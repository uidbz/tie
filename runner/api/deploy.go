package api

import "encoding/json"

// DeployRequest is a build-and-deploy command (POST /api/deploymanager):
// fetch the project tarball from the filehost by ProjectHash, docker build
// it against the runner's registry, and either test-run it or publish it as
// a deployed image. Command selects the mode: "test" or "prod".
//
// Updating an existing image requires that image's DeployKey; a first deploy
// mints one and returns it. Service* fields configure the image as a
// long-running service instead of a batch job handler (JobType
// "WebserviceV1"/"TCPServiceV1").
type DeployRequest struct {
	Command                  string
	Name                     string
	Owner                    string
	Description              string
	DocumentationLink        string
	DeployKey                string // gates deploy access to an existing image
	ProjectHash              string
	TestInputHash            string
	AppHash                  string // injected app hash
	TestSettingsData         json.RawMessage
	Debug                    bool
	NoCache                  bool
	Production               bool
	JobType                  string
	RootPath                 string
	ServiceInternalPort      int
	ServiceAutostart         bool
	ServiceStartOnAccess     bool
	ServiceInactivityTimeout int
}

// DeployResponse answers a deploy. The same shape answers GET /api/filehost:
// Message carries the filehost URL and Store carries the configured upload
// store, so clients upload to the store the runner's jobs read from without
// duplicating the setting client-side.
type DeployResponse struct {
	Message   string
	JobStatus JobStatus
	ErrorMsg  string
	DeployKey string
	Store     string
	// ServiceExternalPort is the stable host port assigned to a raw-TCP
	// service (TCPServiceV1). 0 for jobs and HTTP webservices.
	ServiceExternalPort int
}
