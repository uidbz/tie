package client

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/uidbz/tie/api"
	"github.com/uidbz/tie/tiedb"
	ws "github.com/uidbz/tie/webservice"
)

// ErrNotFound is returned when a key exists in no triple (the server reports
// "Key has no associated values"). Use errors.Is to distinguish "no data" from
// a real transport or server failure.
var ErrNotFound = errors.New("key has no associated values")

// Default values
var defaultConfig = Config{
	Username:         "defaultuser",
	Password:         "defaultpassword",
	TripleStoreURL:   "http://localhost:1161",
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
	// MissingRelation keeps only matches that carry no triple under this
	// relation (e.g. "tag" to list untagged items). It is the negation of
	// existence the tag algebra cannot express with Exclude, resolved
	// server-side so only the qualifying rows cross the wire.
	MissingRelation string
	Filter          string
	Reverse         bool
	Expand          bool
	Offset          int
	Limit           int
	SortBy          string
	// SortByValue orders matched keys by the value each holds under this
	// relation (e.g. "gendb-imported-at" for chronological order). Empty = none.
	SortByValue string
	// SortByValueNumeric reads those values as numbers instead of strings, so 9
	// precedes 10. Values that do not parse sort last in either direction. See
	// ValueTypes for declaring which relations hold numbers.
	SortByValueNumeric bool
	// Descending reverses the final ordering.
	Descending bool
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
	// active is the resolved collection this client operates on (triplestore,
	// namespace, collection id, credentials, filehosts), chosen at construction.
	active ResolvedCollection
}

// firstNonEmpty returns the first non-empty string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ResolveCollection resolves a collection name to the concrete triplestore,
// namespace, collection id, credentials and filehosts to use, applying
// top-level fallbacks for any field the named entry leaves unset. An empty name
// selects DefaultCollection; a name matching no entry is treated as a bare
// collection id on the top-level triplestore/namespace.
func (c Config) ResolveCollection(name string) ResolvedCollection {
	if name == "" {
		name = c.DefaultCollection
	}
	store := firstNonEmpty(c.TripleStoreURL, c.Webservice)
	entry, ok := c.Collections[name]
	if !ok {
		return ResolvedCollection{
			Namespace:      c.Namespace,
			Collection:     firstNonEmpty(name, c.Collection),
			TripleStoreURL: store,
			Username:       c.Username,
			Password:       c.Password,
			Insecure:       c.WebserviceInsecure,
			FileHosts:      c.DefaultFileHosts,
		}
	}
	user, pass := c.Username, c.Password
	if entry.Username != "" {
		user, pass = entry.Username, entry.Password
	}
	hosts := entry.FileHosts
	if len(hosts) == 0 {
		hosts = c.DefaultFileHosts
	}
	return ResolvedCollection{
		Namespace:      firstNonEmpty(entry.Namespace, c.Namespace),
		Collection:     firstNonEmpty(entry.Collection, name, c.Collection),
		TripleStoreURL: firstNonEmpty(entry.TripleStoreURL, store),
		Username:       user,
		Password:       pass,
		Insecure:       entry.Insecure || c.WebserviceInsecure,
		FileHosts:      hosts,
	}
}

type Config struct {
	configPath string
	Username   string
	Password   string
	Namespace  string
	Collection string
	// Webservice is the triplestore URL. Deprecated: use TripleStoreURL. It is
	// still honored (LoadConfig copies it into TripleStoreURL when the latter is
	// empty) so pre-existing configs keep working.
	Webservice string
	// TripleStoreURL is the tie-triplestore endpoint. It supersedes Webservice;
	// when unset LoadConfig falls back to Webservice.
	TripleStoreURL string
	// WebserviceInsecure enables TLS InsecureSkipVerify for the triplestore
	// connection (accept self-signed certificates).
	WebserviceInsecure bool
	DefaultFileHosts   []string
	FileHosts          map[string]FileHost
	// DefaultCollection names the entry in Collections used when no --collection
	// is given. Empty falls back to the flat Collection field.
	DefaultCollection string
	// Collections holds named collection bindings so one config can address
	// several collections. Each entry may override the top-level triplestore,
	// namespace, credentials and filehosts; unset fields fall back to the
	// top-level values. When empty, LoadConfig synthesizes a single entry from
	// the flat Namespace/Collection/DefaultFileHosts fields.
	Collections map[string]CollectionEntry
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

// CollectionEntry is one named collection binding in Config.Collections. Every
// field is optional; an unset field falls back to the top-level Config value
// during resolution. DefaultRetention, when set, is a Go duration (or
// "infinite") the client may stamp on uploads for this collection.
type CollectionEntry struct {
	Namespace        string
	Collection       string
	TripleStoreURL   string
	Username         string
	Password         string
	Insecure         bool
	FileHosts        []string
	DefaultRetention string
}

// ResolvedCollection is a CollectionEntry with all top-level fallbacks applied:
// the concrete triplestore/namespace/collection/credentials/filehosts to use
// for one operation.
type ResolvedCollection struct {
	Namespace      string
	Collection     string
	TripleStoreURL string
	Username       string
	Password       string
	Insecure       bool
	FileHosts      []string
}

// ResolveHosts picks the filehost names an upload/import targets. An explicit
// list (e.g. one or more --host flags) wins. Otherwise the hosts come from the
// resolved entry of the named collection — the client's bound collection (the
// global -c selection) when collection is empty — so a collection's own
// FileHosts override the top-level DefaultFileHosts.
func (tc *TieClient) ResolveHosts(collection string, explicit []string) []string {
	if len(explicit) > 0 {
		return explicit
	}
	if collection == "" {
		return tc.active.FileHosts
	}
	return tc.Config.ResolveCollection(collection).FileHosts
}

// FileHost is a filehost endpoint. Insecure enables TLS InsecureSkipVerify
// (accept self-signed certificates); the scheme lives in URL. Username/Password
// are optional HTTP Basic Auth credentials sent with every request to a filehost
// that requires authentication; leave them empty for an open filehost. Store,
// when set, names a physical store on a multi-store filehost and rides uploads
// as the Tie-Store header; leave empty for a single-store filehost.
type FileHost struct {
	URL      string
	Insecure bool
	Username string
	Password string
	Store    string
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

// NewTieClient builds a client for the config's default collection.
func NewTieClient(config Config) (client *TieClient) {
	return NewTieClientFor(config, "")
}

// NewTieClientFor builds a client bound to the named collection (empty selects
// the default). The transport targets that collection's resolved triplestore and
// credentials.
func NewTieClientFor(config Config, collection string) (client *TieClient) {
	active := config.ResolveCollection(collection)
	client = &TieClient{
		Config: config,
		active: active,
		client: ws.NewClient(active.TripleStoreURL, active.Username, active.Password, active.Insecure),
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
		collection = tc.active.Collection
	}
	return api.CollectionInfo{Namespace: tc.active.Namespace, CollectionId: collection}
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
	col := tc.collectionInfo("")
	request := col.NewAddRequest(key, value1, value2)

	reply, err := run[api.AddReply](tc, request)
	if err != nil {
		return nil, err
	}
	return reply, replyError(reply.ReplyStatus)
}

// Wait until all changes has been committed to the collection
func (tc *TieClient) Sync() error {
	return tc.SyncIn("")
}

// SyncIn waits until all changes have been committed to a specific collection.
// An empty collection falls back to the configured default.
func (tc *TieClient) SyncIn(collection string) error {
	request := tc.collectionInfo(collection).NewSyncRequest()

	if _, err := tc.client.Run(request); err != nil {
		return err
	}
	return nil
}

// Get a TripleSet with all triples that are associated with 'key'.
// Returns ErrNotFound if nothing is associated with the key.
func (tc *TieClient) Associated(key string) (AssociatedReply, error) {
	col := tc.collectionInfo("")
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
	return tc.QueryIn("", spec)
}

// QueryIn runs a Query against a specific collection. An empty collection falls
// back to the configured default.
func (tc *TieClient) QueryIn(collection string, spec QuerySpec) ([]Row, int, error) {
	request := tc.collectionInfo(collection).NewQueryRequest()
	request.Terms = spec.Terms
	request.Exclude = spec.Exclude
	request.Scope = spec.Scope
	request.MissingRelation = spec.MissingRelation
	request.Filter = spec.Filter
	request.Reverse = spec.Reverse
	request.Expand = spec.Expand
	request.Sort = tiedb.SortOptions{
		Offset:             spec.Offset,
		Limit:              spec.Limit,
		SortBy:             spec.SortBy,
		SortByValue:        spec.SortByValue,
		SortByValueNumeric: spec.SortByValueNumeric,
		Descending:         spec.Descending,
	}

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
	return tc.GetIn("", key)
}

// GetIn fetches the forward attributes of a single key from a specific
// collection. An empty collection falls back to the configured default.
func (tc *TieClient) GetIn(collection, key string) (Row, error) {
	rows, err := tc.ExpandIn(collection, []string{key})
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
	return tc.ExpandIn("", keys)
}

// ExpandIn fetches the forward attributes of many keys from a specific
// collection in one round trip. An empty collection falls back to the
// configured default.
func (tc *TieClient) ExpandIn(collection string, keys []string) ([]Row, error) {
	request := tc.collectionInfo(collection).NewExpandRequest(keys, "")

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
	col := tc.collectionInfo("")
	request := col.NewSetRequest(key, relation, values)

	reply, err := run[api.SetReply](tc, request)
	if err != nil {
		return err
	}
	return replyError(reply.ReplyStatus)
}

// Delete a triple from the collection
func (tc *TieClient) Delete(key, value1, value2 string) (DeleteReply, error) {
	col := tc.collectionInfo("")
	request := col.NewDeleteRequest(key, value1, value2)

	reply, err := run[api.DeleteReply](tc, request)
	if err != nil {
		return nil, err
	}
	return reply, replyError(reply.ReplyStatus)
}

// Update a triple in the collection
func (tc *TieClient) Update(update api.Update) (UpdateReply, error) {
	col := tc.collectionInfo("")
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
	col := tc.collectionInfo("")
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

// CheckIndex asks the triplestore to cross-check a collection's forward and
// reverse association indexes (tiedb.Collection.CheckIndex). An empty
// collection falls back to the bound one. deep also validates each forward
// position against its on-disk record (slow on a large collection); repair
// fixes the divergences in memory and reports how many it fixed. The counts in
// the report describe the state before any repair.
func (tc *TieClient) CheckIndex(collection string, deep, repair bool) (*tiedb.IndexReport, error) {
	col := tc.collectionInfo(collection)
	request := col.NewCheckIndexRequest(deep, repair)

	reply, err := run[api.CheckIndexReply](tc, request)
	if err != nil {
		return nil, err
	}
	if err := replyError(reply.ReplyStatus); err != nil {
		return nil, err
	}
	return &reply.Report, nil
}

// DropCollection deletes the entire current collection — its on-disk .tie file
// and in-memory index — server-side. The collection reloads empty on next
// access. Unlike Delete (one triple) this discards the whole collection, so it
// is the destructive setup for an overwriting Restore.
func (tc *TieClient) DropCollection() error {
	col := tc.collectionInfo("")
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
