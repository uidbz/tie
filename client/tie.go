package client

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"git.sr.ht/~uid/tie/api"
	"git.sr.ht/~uid/tie/tiedb"
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
	Webservice:       "http://localhost:1161",
	Namespace:        "Collections",
	Collection:       "Main",
	DefaultFileHosts: []string{"default"},
	FileHosts:        map[string]FileHost{"default": {URL: "http://localhost:1162"}},
	PrevVersions:     3,
	key:              InitKey(),
}

const (
	tieKey            = "A00102030405060708090A0B0C0D0E0FF0E0D0C0B0A090807060504030201000"
	defaultConfigFile = "config"
)

type AssociatedReply = *api.AssociatedReply
type DumpReply = *api.DumpReply
type AddReply = *api.AddReply
type DeleteReply = *api.DeleteReply
type UpdateReply = *api.UpdateReply
type BatchReply = *api.BatchReply
type DropReply = *api.DropReply
type Update = api.Update

// Row is the flat query result unit: a key plus its attributes (relation ->
// values). Re-exported from tiedb so callers and future bindings need only the
// client package.
type Row = tiedb.Row

// QuerySpec describes a tag/association query. Terms is the full AND-list of
// values a match must be associated with (no positional seed). See api.Query.
type QuerySpec struct {
	Terms   []string
	Exclude []string
	Scope   string
	Filter  string
	Reverse bool
	Expand  bool
	Offset  int
	Limit   int
	SortBy  string
}

// RowValues returns all values a row holds under relation, or nil.
func RowValues(r Row, relation string) []string {
	return r.Attributes[relation]
}

// RowFirst returns the first value under relation, or "" when absent.
func RowFirst(r Row, relation string) string {
	if v := r.Attributes[relation]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// RowHas reports whether relation holds value.
func RowHas(r Row, relation, value string) bool {
	for _, v := range r.Attributes[relation] {
		if v == value {
			return true
		}
	}
	return false
}

type TieClient struct {
	client *ws.Client
	Config Config
}

type Config struct {
	configPath string
	Username   string
	Password   string
	Namespace  string
	Collection string
	Webservice string
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
	// PrevVersions bounds how many superseded versions of a file are kept when a
	// directory is re-imported. When re-import replaces a file (same name, new
	// bytes) or drops one (renamed/deleted on disk), the old content's edge is
	// moved into a "<filename>_prev" history directory instead of being deleted;
	// only the newest PrevVersions are retained there, oldest dropped first. A
	// value of 0 keeps no history (the old edge is removed directly). NOTE:
	// LoadConfig fills a zero-value Config from TOML, so a config file that omits
	// this key gets 0 (no history), not the defaultConfig value.
	PrevVersions int
	// Queries maps a friendly name to a saved tag query, e.g.
	// "chill-jazz" = "jazz mellow -live". Each entry appears as a directory
	// under the mount's query/ tree, so "ls query/chill-jazz" runs the stored
	// query. The query string uses the same syntax as an ad-hoc query dir
	// (space-ANDed terms, "-" excludes, optional "type:" scope).
	Queries map[string]string
	key     []byte
	verbose bool
}

// Path returns the filesystem path the config was loaded from (empty for a
// default config that was never read from disk).
func (c Config) Path() string {
	return c.configPath
}

// FileHost is a filehost endpoint. Insecure enables TLS InsecureSkipVerify
// (accept self-signed certificates); the scheme lives in URL. Username/Password
// are optional HTTP Basic Auth credentials sent with every request to a filehost
// that requires authentication; leave them empty for an open filehost.
type FileHost struct {
	URL      string
	Insecure bool
	Username string
	Password string
}

// basicAuthTransport injects HTTP Basic Auth on every request. Wrapping the
// transport (rather than each call site) lets credentials ride along both the
// download path (getlib, which uses client.Get) and the upload path without
// threading them through every function signature.
type basicAuthTransport struct {
	base           http.RoundTripper
	username, pass string
}

func (t *basicAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone before mutating: RoundTrip must not modify the caller's request.
	r := req.Clone(req.Context())
	r.SetBasicAuth(t.username, t.pass)
	return t.base.RoundTrip(r)
}

// HTTPClientFor returns an *http.Client honoring the host's Insecure flag and
// credentials. A plain, credential-free secure host reuses http.DefaultClient;
// anything needing a custom TLS config or Basic Auth gets a dedicated client.
func HTTPClientFor(host FileHost) *http.Client {
	if !host.Insecure && host.Username == "" {
		return http.DefaultClient
	}
	var base http.RoundTripper = http.DefaultTransport
	if host.Insecure {
		base = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	if host.Username != "" {
		base = &basicAuthTransport{base: base, username: host.Username, pass: host.Password}
	}
	return &http.Client{Transport: base}
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
	col := api.CollectionInfo{Namespace: tc.Config.Namespace, CollectionId: tc.Config.Collection}
	request := col.NewAddRequest(key, value1, value2)

	reply, err := run[api.AddReply](tc, request)
	if err != nil {
		return nil, err
	}
	return reply, replyError(reply.ReplyStatus)
}

// Wait until all changes has been committed to the collection
func (tc *TieClient) Sync() error {
	col := api.CollectionInfo{Namespace: tc.Config.Namespace, CollectionId: tc.Config.Collection}
	request := col.NewSyncRequest()

	if _, err := tc.client.Run(request); err != nil {
		return err
	}
	return nil
}

// Get a TripleSet with all triples that are associated with 'key'.
// Returns ErrNotFound if nothing is associated with the key.
func (tc *TieClient) Associated(key string) (AssociatedReply, error) {
	col := api.CollectionInfo{Namespace: tc.Config.Namespace, CollectionId: tc.Config.Collection}
	request := col.NewAssociatedRequest(key)

	reply, err := run[api.AssociatedReply](tc, request)
	if err != nil {
		return nil, err
	}
	return reply, replyError(reply.ReplyStatus)
}

// Query runs a tag/association query and returns the matching rows (ordered and
// paginated per spec), the total match count before pagination, and an error.
// Returns ErrNotFound (with nil rows) when nothing matches.
func (tc *TieClient) Query(spec QuerySpec) ([]Row, int, error) {
	col := api.CollectionInfo{Namespace: tc.Config.Namespace, CollectionId: tc.Config.Collection}
	request := col.NewQueryRequest()
	request.Terms = spec.Terms
	request.Exclude = spec.Exclude
	request.Scope = spec.Scope
	request.Filter = spec.Filter
	request.Reverse = spec.Reverse
	request.Expand = spec.Expand
	request.Sort = tiedb.SortOptions{Offset: spec.Offset, Limit: spec.Limit, SortBy: spec.SortBy}

	reply, err := run[api.QueryReply](tc, request)
	if err != nil {
		return nil, 0, err
	}
	if e := replyError(reply.ReplyStatus); e != nil {
		return nil, 0, e
	}
	return reply.Rows, reply.TotalCount, nil
}

// Get fetches the forward attributes of a single key. Returns ErrNotFound if
// the key has no associated values. It is Expand for one key; use Query to
// search for keys by their associations.
func (tc *TieClient) Get(key string) (Row, error) {
	rows, err := tc.Expand([]string{key})
	if err != nil {
		return Row{}, err
	}
	if len(rows) == 0 {
		return Row{}, ErrNotFound
	}
	return rows[0], nil
}

// Expand fetches the forward attributes of many keys in one round trip, one Row
// per key that exists (missing keys are omitted). Order follows keys.
func (tc *TieClient) Expand(keys []string) ([]Row, error) {
	col := api.CollectionInfo{Namespace: tc.Config.Namespace, CollectionId: tc.Config.Collection}
	request := col.NewExpandRequest(keys, "")

	reply, err := run[api.ExpandReply](tc, request)
	if err != nil {
		return nil, err
	}
	if e := replyError(reply.ReplyStatus); e != nil {
		return nil, e
	}
	return reply.Rows, nil
}

// Set makes (key, relation) hold exactly values, replacing any existing values
// for that relation in one server-side op. An empty values slice clears it.
func (tc *TieClient) Set(key, relation string, values []string) error {
	col := api.CollectionInfo{Namespace: tc.Config.Namespace, CollectionId: tc.Config.Collection}
	request := col.NewSetRequest(key, relation, values)

	reply, err := run[api.SetReply](tc, request)
	if err != nil {
		return err
	}
	return replyError(reply.ReplyStatus)
}

// Delete a triple from the collection
func (tc *TieClient) Delete(key, value1, value2 string) (DeleteReply, error) {
	col := api.CollectionInfo{Namespace: tc.Config.Namespace, CollectionId: tc.Config.Collection}
	request := col.NewDeleteRequest(key, value1, value2)

	reply, err := run[api.DeleteReply](tc, request)
	if err != nil {
		return nil, err
	}
	return reply, replyError(reply.ReplyStatus)
}

// Update a triple in the collection
func (tc *TieClient) Update(update api.Update) (UpdateReply, error) {
	col := api.CollectionInfo{Namespace: tc.Config.Namespace, CollectionId: tc.Config.Collection}
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

// DumpStream streams every forward triple in the current collection to fn, one
// at a time, without ever buffering the whole collection on the client. Order
// is unspecified. If fn returns an error, streaming stops and that error is
// returned.
func (tc *TieClient) DumpStream(fn func(tiedb.StringTriple) error) error {
	col := api.CollectionInfo{Namespace: tc.Config.Namespace, CollectionId: tc.Config.Collection}
	request := col.NewDumpRequest()

	return tc.client.RunStream(request, func(r io.Reader) error {
		dec := json.NewDecoder(r)
		for dec.More() {
			var t tiedb.StringTriple
			if err := dec.Decode(&t); err != nil {
				return err
			}
			if err := fn(t); err != nil {
				return err
			}
		}
		return nil
	})
}

// Dump returns every forward triple in the current collection, for backup or
// interop. Order is unspecified. It collects the streamed dump into a slice;
// callers that must not hold the whole collection in memory should use
// DumpStream instead.
func (tc *TieClient) Dump() (DumpReply, error) {
	reply := &api.DumpReply{}
	err := tc.DumpStream(func(t tiedb.StringTriple) error {
		reply.Triples = append(reply.Triples, t)
		return nil
	})
	if err != nil {
		return nil, err
	}
	reply.Success = true
	return reply, nil
}

// DropCollection deletes the entire current collection — its on-disk .tie file
// and in-memory index — server-side. The collection reloads empty on next
// access. Unlike Delete (one triple) this discards the whole collection, so it
// is the destructive setup for an overwriting Restore.
func (tc *TieClient) DropCollection() error {
	col := api.CollectionInfo{Namespace: tc.Config.Namespace, CollectionId: tc.Config.Collection}
	request := col.NewDropRequest()

	reply, err := run[api.DropReply](tc, request)
	if err != nil {
		return err
	}
	return replyError(reply.ReplyStatus)
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
	if _, err := tc.Batch(b); err != nil {
		return err
	}
	return nil
}

// Check if the key exists
func (tc *TieClient) Exists(key string) bool {
	_, err := tc.Get(key)
	return err == nil
}
