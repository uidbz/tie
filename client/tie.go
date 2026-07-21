package client

import (
	"crypto/tls"
	"errors"
	"net/http"

	"git.sr.ht/~uid/tie/api"
	ws "git.sr.ht/~uid/tie/webservice"
)

// ErrNotFound is returned when a key exists in no triple (the server reports
// "Key has no associated values"). Use errors.Is to distinguish "no data" from
// a real transport or server failure.
var ErrNotFound = errors.New("key has no associated values")

// Default values
var defaultConfig = Config{
	Username:         "defaultuser",
	Password:         "defaultpassword",
	Webservice:       "https://localhost:1161",
	Namespace:        "Collections",
	Collection:       "Main",
	DefaultFileHosts: []string{"default"},
	FileHosts:        map[string]FileHost{"default": {URL: "https://localhost:1162"}},
	key:              InitKey(),
}

const (
	tieKey            = "A00102030405060708090A0B0C0D0E0FF0E0D0C0B0A090807060504030201000"
	defaultConfigFile = "config"
)

type AssociatedReply = *api.AssociatedReply
type DumpReply = *api.DumpReply
type AddReply = *api.AddReply
type GetReply = *api.GetReply
type DeleteReply = *api.DeleteReply
type UpdateReply = *api.UpdateReply
type BatchReply = *api.BatchReply
type Update = api.Update
type GetOptions = api.GetOptions

type TieClient struct {
	client *ws.Client
	Config Config
}

type Config struct {
	configPath       string
	Username         string
	Password         string
	Namespace        string
	Collection       string
	Webservice       string
	// WebserviceInsecure enables TLS InsecureSkipVerify for the Webservice
	// connection (accept self-signed certificates).
	WebserviceInsecure bool
	DefaultFileHosts   []string
	FileHosts          map[string]FileHost
	// ImportDest maps a dir-type name (e.g. "audio-dir") to a virtual-path
	// template rendered from a directory's aggregated metadata, e.g.
	// "/music/{artist}/{year}. {album}". When a dir-type has an entry, imports
	// of that type are rooted at the rendered path instead of the source's
	// absolute on-disk path. Supported variables: artist, album, year, title,
	// track.
	ImportDest map[string]string
	// Queries maps a friendly name to a saved tag query, e.g.
	// "chill-jazz" = "jazz mellow -live". Each entry appears as a directory
	// under the mount's query/ tree, so "ls query/chill-jazz" runs the stored
	// query. The query string uses the same syntax as an ad-hoc query dir
	// (space-ANDed terms, "-" excludes, optional "type:" scope).
	Queries map[string]string
	key        []byte
	verbose    bool
}

// FileHost is a filehost endpoint. Insecure enables TLS InsecureSkipVerify
// (accept self-signed certificates); the scheme lives in URL.
type FileHost struct {
	URL      string
	Insecure bool
}

// httpClientFor returns an *http.Client honoring the host's Insecure flag.
// A secure host reuses http.DefaultClient; an insecure one gets a client that
// skips TLS certificate verification.
func httpClientFor(host FileHost) *http.Client {
	if !host.Insecure {
		return http.DefaultClient
	}
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
}

type TagOptions struct {
	AddOriginalPath bool
}

func NewTieClient(config Config) (client *TieClient) {
	client = &TieClient{
		Config: config,
		client: ws.NewClient(config.Webservice, config.Username, config.Password, config.WebserviceInsecure),
	}

	return client
}

func (tc *TieClient) NewUpdate(key, value1, value2, newValue2 string) api.Update {
	return api.Update{
		Key:          key,
		Value1:       value1,
		Value2:       value2,
		NewValue2:    newValue2,
		AddOnFailure: false,
	}
}

// Make a new Batch - run with Batch function
func (tc *TieClient) NewBatch() *api.Batch {
	return &api.Batch{
		Collection: tc.CollectionInfo(),
	}
}

// NewBatchIn makes a Batch targeting a specific collection. An empty collection
// falls back to the configured default.
func (tc *TieClient) NewBatchIn(collection string) *api.Batch {
	return &api.Batch{
		Collection: tc.collectionInfo(collection),
	}
}

func (tc *TieClient) CollectionInfo() api.CollectionInfo {
	return tc.collectionInfo("")
}

func (tc *TieClient) collectionInfo(collection string) api.CollectionInfo {
	if collection == "" {
		collection = tc.Config.Collection
	}
	return api.CollectionInfo{Namespace: tc.Config.Namespace, CollectionId: collection}
}

// run sends a request and returns the typed reply. err is non-nil on transport
// failure; the API-level Success flag is checked by each method via replyError.
func run[T any](tc *TieClient, request ws.RequestInterface) (*T, error) {
	generic, err := tc.client.Run(request)
	if err != nil {
		return nil, err
	}
	return ws.ReadReply[T](generic), nil
}

// replyError turns an unsuccessful reply into an error: ErrNotFound for the
// empty-key case, otherwise a generic error carrying the server message.
func replyError(status ws.ReplyStatus) error {
	if status.Success {
		return nil
	}
	if status.Message == "Key has no associated values" {
		return ErrNotFound
	}
	return errors.New(status.Message)
}

// Add a triple to the collection
func (tc *TieClient) Add(key, value1, value2 string) (AddReply, error) {
	col := api.CollectionInfo{tc.Config.Namespace, tc.Config.Collection}
	request := col.NewAddRequest(key, value1, value2)

	reply, err := run[api.AddReply](tc, request)
	if err != nil {
		return nil, err
	}
	return reply, replyError(reply.ReplyStatus)
}

// Wait until all changes has been committed to the collection
func (tc *TieClient) Sync() error {
	col := api.CollectionInfo{tc.Config.Namespace, tc.Config.Collection}
	request := col.NewSyncRequest()

	if _, err := tc.client.Run(request); err != nil {
		return err
	}
	return nil
}

// Get a TripleSet with all triples that are associated with 'key'.
// Returns ErrNotFound if nothing is associated with the key.
func (tc *TieClient) Associated(key string) (AssociatedReply, error) {
	col := api.CollectionInfo{tc.Config.Namespace, tc.Config.Collection}
	request := col.NewAssociatedRequest(key)

	reply, err := run[api.AssociatedReply](tc, request)
	if err != nil {
		return nil, err
	}
	return reply, replyError(reply.ReplyStatus)
}

// Get a TripleSet from a key.
// Returns ErrNotFound if the key has no associated values.
func (tc *TieClient) SimpleGet(key string) (GetReply, error) {
	col := api.CollectionInfo{tc.Config.Namespace, tc.Config.Collection}
	request := col.NewGetRequest(key)

	reply, err := run[api.GetReply](tc, request)
	if err != nil {
		return nil, err
	}
	return reply, replyError(reply.ReplyStatus)
}

// Get a TripleSet from a key with options.
// Returns ErrNotFound if the key has no associated values.
func (tc *TieClient) Get(key string, o GetOptions) (GetReply, error) {
	col := api.CollectionInfo{tc.Config.Namespace, tc.Config.Collection}
	request := col.NewGetRequest(key)
	request.Options = o

	reply, err := run[api.GetReply](tc, request)
	if err != nil {
		return nil, err
	}
	return reply, replyError(reply.ReplyStatus)
}

// Delete a triple from the collection
func (tc *TieClient) Delete(key, value1, value2 string) (DeleteReply, error) {
	col := api.CollectionInfo{tc.Config.Namespace, tc.Config.Collection}
	request := col.NewDeleteRequest(key, value1, value2)

	reply, err := run[api.DeleteReply](tc, request)
	if err != nil {
		return nil, err
	}
	return reply, replyError(reply.ReplyStatus)
}

// Update a triple in the collection
func (tc *TieClient) Update(update api.Update) (UpdateReply, error) {
	col := api.CollectionInfo{tc.Config.Namespace, tc.Config.Collection}
	request := col.NewUpdateRequest(update)

	reply, err := run[api.UpdateReply](tc, request)
	if err != nil {
		return nil, err
	}
	return reply, replyError(reply.ReplyStatus)
}

// Run a batch - make new Batch with NewBatch. The reply is always returned so
// callers can inspect per-request sub-replies; err is non-nil on transport
// failure or if the batch as a whole reports failure.
func (tc *TieClient) Batch(batch *api.Batch) (BatchReply, error) {
	request := api.NewBatchRequest(batch)

	reply, err := run[api.BatchReply](tc, request)
	if err != nil {
		return nil, err
	}
	return reply, replyError(reply.ReplyStatus)
}

// Dump returns every forward triple in the current collection, for backup or
// interop. Order is unspecified.
func (tc *TieClient) Dump() (DumpReply, error) {
	col := api.CollectionInfo{tc.Config.Namespace, tc.Config.Collection}
	request := col.NewDumpRequest()

	reply, err := run[api.DumpReply](tc, request)
	if err != nil {
		return nil, err
	}
	return reply, replyError(reply.ReplyStatus)
}

// Restore adds every (key, value1, value2) triple into the current collection
// via a single batch. It is additive and idempotent: re-adding an existing
// triple is a no-op, so restoring a dump merges rather than replaces.
func (tc *TieClient) Restore(triples [][3]string) error {
	if len(triples) == 0 {
		return nil
	}
	b := tc.NewBatch()
	for _, t := range triples {
		b.Add(t[0], t[1], t[2])
	}
	reply, err := tc.Batch(b)
	if err != nil {
		return err
	}
	for _, a := range reply.AddReplys {
		if !a.Success {
			return errors.New("restore failed for key '" + a.OrigKey + "': " + a.Message)
		}
	}
	return nil
}

// Check if the key exists
func (tc *TieClient) Exists(key string) bool {
	reply, err := tc.SimpleGet(key)
	return err == nil && reply.Success
}
