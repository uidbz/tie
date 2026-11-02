// Package images keeps the runner's deployed-image registry: which
// handler names this runner knows, their owners, per-image access keys,
// and their service settings. Entries are the unit that deploys create,
// auth gates check, the reverse proxy routes to, and services start
// from.
//
// The registry is in-memory with a persistence callback: deployments
// decide where entries live (a config file, a directory) and receive the
// whole list on save. Concurrency-safe for the API handlers.
package images

import (
	"crypto/rand"
	"math/big"
	"sync"

	"github.com/uidbz/tie/runner/api"
)

// Image is one deployed image. Names and per-image keys are the unit of
// authorization for service management, deletion, and job cancellation.
type Image struct {
	Name                string
	Owner               string
	Description         string
	Documentation       string
	DeployKey           string // gates access to this image; never listed publicly
	IsService           bool
	ServiceInternalPort int
	// ServiceExternalPort is the stable host port for a raw-TCP service.
	// It is runner-assigned once at deploy and persists across restarts.
	// 0 means dynamic (HTTP webservice, reached via the reverse proxy).
	ServiceExternalPort      int
	ServiceAutostart         bool
	ServiceStartOnAccess     bool
	ServiceInactivityTimeout int
	// ServiceProxyRewrite limits how far the reverse proxy rewrites this
	// service's response bodies: "" or "all" rewrites HTML, XML, CSS and
	// JS; "html" rewrites only HTML/XML; "none" leaves bodies untouched.
	// Prefix-aware backends (they emit their own service URLs in their
	// JS and CSS) need "html" — rewriting those literals again doubles
	// the prefix at runtime.
	ServiceProxyRewrite string
}

// Registry is the deployed-image table with copy-on-change semantics:
// every Get returns a copy, so callers can never mutate live entries.
type Registry struct {
	mu     sync.Mutex
	images []Image
	// save persists the current list; a failed save rolls the change
	// back, so the in-memory table never lies about what is stored.
	save func(images []Image) error
}

// New builds a registry over an optional persistence callback (nil means
// in-memory only). initial seeds the table.
func New(save func(images []Image) error, initial ...Image) *Registry {
	r := &Registry{save: save, images: append([]Image{}, initial...)}
	return r
}

// Get returns a copy of the named image.
func (r *Registry) Get(name string) (Image, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.images {
		if x.Name == name {
			return x, true
		}
	}
	return Image{}, false
}

// List returns copies of all entries in insertion order.
func (r *Registry) List() []Image {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Image{}, r.images...)
}

// Replace swaps in a whole new table (config reload); persisted through
// the save callback like any other change.
func (r *Registry) Replace(newImages []Image) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	prev := r.images
	r.images = append([]Image{}, newImages...)
	if err := r.persist(); err != nil {
		r.images = prev
		return err
	}
	return nil
}

// Upsert inserts or replaces an entry, persisting the table; a save
// failure rolls back. The bool reports whether the stored table changed.
func (r *Registry) Upsert(image Image) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, x := range r.images {
		if x.Name == image.Name {
			if image == x {
				return true // no change; not an error
			}
			r.images[i] = image
			if err := r.persist(); err != nil {
				r.images[i] = x
				return false
			}
			return true
		}
	}
	r.images = append(r.images, image)
	if err := r.persist(); err != nil {
		r.images = r.images[:len(r.images)-1]
		return false
	}
	return true
}

// Delete removes an entry by name, persisting; false when absent or when
// persistence failed (rolled back).
func (r *Registry) Delete(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, x := range r.images {
		if x.Name == name {
			r.images = append(r.images[:i], r.images[i+1:]...)
			if err := r.persist(); err != nil {
				r.images = append(r.images[:i], append([]Image{x}, r.images[i:]...)...)
				return false
			}
			return true
		}
	}
	return false
}

func (r *Registry) persist() error {
	if r.save == nil {
		return nil
	}
	return r.save(r.images)
}

// deployKeyAlphabet and -Length match what existing deployments mint;
// keys are opaque bearer secrets.
const (
	deployKeyAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	deployKeyLength   = 16
)

// NewDeployKey mints a fresh per-image access key (first deploy of an
// image without a caller-supplied key).
func NewDeployKey() string {
	out := make([]byte, deployKeyLength)
	max := big.NewInt(int64(len(deployKeyAlphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil { // crypto/rand does not fail; belt and braces
			panic("images: no randomness for deploy keys: " + err.Error())
		}
		out[i] = deployKeyAlphabet[n.Int64()]
	}
	return string(out)
}

// PublicView strips the image to its public listing shape: the api view
// never carries the DeployKey or port internals.
func PublicView(image Image) api.ImageInfo {
	return api.ImageInfo{
		Name:          image.Name,
		Owner:         image.Owner,
		Description:   image.Description,
		Documentation: image.Documentation,
		IsService:     image.IsService,
	}
}
