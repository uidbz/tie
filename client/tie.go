package client

import (
	"git.sr.ht/~uid/tie/api"
	ws "git.sr.ht/~uid/tie/webservice"
)

// Default values
var defaultConfig = Config{
	Username:         "defaultuser",
	Password:         "defaultpassword",
	Webservice:       "https://localhost:1161",
	Namespace:        "Collections",
	Collection:       "Main",
	DefaultFileHosts: []string{"default"},
	FileHosts:        map[string]string{"default": "https://localhost:1162"},
	key:              InitKey(),
}

const (
	objectUid         = "Uid"
	tieKey            = "A00102030405060708090A0B0C0D0E0FF0E0D0C0B0A090807060504030201000"
	defaultConfigFile = "config"
)

type AssociatedReply = *api.AssociatedReply
type AddReply = *api.AddReply
type GetReply = *api.GetReply
type DeleteReply = *api.DeleteReply
type UpdateReply = *api.UpdateReply
type BatchReply = *api.BatchReply
type Update = api.Update
type GetOptions = api.GetOptions
type AssociatedOptions = api.AssociatedOptions

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
	DefaultFileHosts []string
	Import           ImportConfig
	FileHosts        map[string]string
	key              []byte
	verbose          bool
}

type ImportConfig struct {
	ImageCollection    string
	VideoCollection    string
	AudioCollection    string
	DocumentCollection string
	GeneralCollection  string
}

type TagOptions struct {
	AddOriginalPath               bool
	PutlibForceGenerateThumbnails bool
}

func NewTieClient(config Config) (client *TieClient) {
	client = &TieClient{
		Config: config,
		client: ws.NewClient(config.Webservice, config.Username, config.Password),
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

func (tc *TieClient) CollectionInfo() api.CollectionInfo {
	return api.CollectionInfo{tc.Config.Namespace, tc.Config.Collection}
}

// Add a triple to the collection
func (tc *TieClient) Add(key, value1, value2 string, handler func(reply AddReply)) {
	col := api.CollectionInfo{tc.Config.Namespace, tc.Config.Collection}
	request := col.NewAddRequest(key, value1, value2)

	if genericReply, err := tc.client.Run(request); err != nil {
		reply := &api.AddReply{}
		reply.Success = false
		reply.Message = err.Error()
		handler(reply)
	} else {
		reply := ws.ReadReply[api.AddReply](genericReply)
		handler(reply)
	}
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

// Get a TripleSet with all triples that are associated with 'key'
func (tc *TieClient) Associated(key string, handler func(reply AssociatedReply)) {
	col := api.CollectionInfo{tc.Config.Namespace, tc.Config.Collection}
	request := col.NewAssociatedRequest(key)

	if genericReply, err := tc.client.Run(request); err != nil {
		reply := &api.AssociatedReply{}
		reply.Success = false
		reply.Message = err.Error()
		handler(reply)
	} else {
		reply := ws.ReadReply[api.AssociatedReply](genericReply)
		handler(reply)
	}
}

// Get a TripleSet with all triples that are associated with 'key'
func (tc *TieClient) AssociatedWith(key string, o AssociatedOptions, handler func(reply AssociatedReply)) {
	col := api.CollectionInfo{tc.Config.Namespace, tc.Config.Collection}
	request := col.NewAssociatedRequest(key)
	request.MatchValue1 = o.MatchValue1

	if genericReply, err := tc.client.Run(request); err != nil {
		reply := &api.AssociatedReply{}
		reply.Success = false
		reply.Message = err.Error()
		handler(reply)
	} else {
		reply := ws.ReadReply[api.AssociatedReply](genericReply)
		handler(reply)
	}
}

// Get a TripleSet from a key
func (tc *TieClient) SimpleGet(key string, handler func(reply GetReply)) {
	col := api.CollectionInfo{tc.Config.Namespace, tc.Config.Collection}
	request := col.NewGetRequest(key)

	if genericReply, err := tc.client.Run(request); err != nil {
		reply := &api.GetReply{}
		reply.Success = false
		reply.Message = err.Error()
		handler(reply)
	} else {
		reply := ws.ReadReply[api.GetReply](genericReply)
		handler(reply)
	}
}

// Get a TripleSet from a key with options
func (tc *TieClient) Get(key string, o GetOptions, handler func(reply GetReply)) {
	col := api.CollectionInfo{tc.Config.Namespace, tc.Config.Collection}
	request := col.NewGetRequest(key)
	request.Options = o

	if genericReply, err := tc.client.Run(request); err != nil {
		reply := &api.GetReply{}
		reply.Success = false
		reply.Message = err.Error()
		handler(reply)
	} else {
		reply := ws.ReadReply[api.GetReply](genericReply)
		handler(reply)
	}

}

// Delete a triple from the collection
func (tc *TieClient) Delete(key, value1, value2 string, handler func(reply DeleteReply)) {
	col := api.CollectionInfo{tc.Config.Namespace, tc.Config.Collection}
	request := col.NewDeleteRequest(key, value1, value2)

	if genericReply, err := tc.client.Run(request); err != nil {
		reply := &api.DeleteReply{}
		reply.Success = false
		reply.Message = err.Error()
		handler(reply)
	} else {
		reply := ws.ReadReply[api.DeleteReply](genericReply)
		handler(reply)
	}
}

// Update a triple in the collection
func (tc *TieClient) Update(update api.Update, handler func(reply UpdateReply)) {
	col := api.CollectionInfo{tc.Config.Namespace, tc.Config.Collection}
	request := col.NewUpdateRequest(update)

	if genericReply, err := tc.client.Run(request); err != nil {
		reply := &api.UpdateReply{}
		reply.Success = false
		reply.Message = err.Error()
		handler(reply)
	} else {
		reply := ws.ReadReply[api.UpdateReply](genericReply)
		handler(reply)
	}
}

// Run a batch - make new Batch with NewBatch
func (tc *TieClient) Batch(batch *api.Batch, handler func(reply BatchReply)) {
	request := api.NewBatchRequest(batch)

	if genericReply, err := tc.client.Run(request); err != nil {
		reply := &api.BatchReply{}
		reply.Success = false
		reply.Message = err.Error()
		handler(reply)
	} else {
		reply := ws.ReadReply[api.BatchReply](genericReply)
		handler(reply)
	}
}

// Check if the key exists
func (tc *TieClient) Exists(key string) bool {
	result := false
	tc.SimpleGet(key, func(reply GetReply) {
		if reply.Success {
			result = true
		}
	})
	return result
}

// func (tc *TieClient) Tag(path string, tags []string, options TagOptions, addHandler func(json.RawMessage)) {
// 	pc := putlib.PutConfig{}
// 	pc.JsonOutput = true
// 	pc.ForceGenerateThumbnails = options.PutlibForceGenerateThumbnails
// 	status := putlib.Upload(config.State.ServeUrl, path, pc)
// 	if status.ErrorMsg != "" {
// 		fmt.Println("Error:", status.ErrorMsg)
// 	}
// 	if status.LastItem.Hash != "" {
// 		info := status.LastItem
// 		// uid := metalib.HashFunction + "/" + info.MediaType + "/" + info.Hash
// 		base := filepath.Base(path)
// 		TieAdd("file", "highway-hash", info.Hash, addHandler)
// 		TieAdd(info.Hash, "context", "tiehashv1", addHandler)
// 		TieAdd(info.Hash, "filename", base, addHandler)
// 		TieAdd(info.Hash, "media-type", info.MediaType, addHandler)
// 		if options.AddOriginalPath {
// 			TieAdd(info.Hash, "original-path", path, addHandler)
// 		}
// 		for _, x := range tags {
// 			TieAdd(info.Hash, "tag", x, addHandler)
// 		}
// 		if len(tags) != 0 {
// 			fmt.Println(info.Hash, "tag", tags)
// 		} else {
// 			fmt.Println(info.Hash)
// 		}
// 		p := strings.Split(info.MediaType, "/")
// 		if len(p) == 2 {
// 			switch p[0] {
// 			case "video":
// 				if height := GetVideoHeight(path); height != "" {
// 					TieAdd(info.Hash, "tag", height, addHandler)
// 				}

// 			case "image":

// 			case "audio":
// 				// FIXME
// 				// audio := metadata.Audio{}
// 				// if err := json.Unmarshal([]byte(output), &audio); err == nil {
// 				// 	if audio.Album != "" {
// 				// 		TieAdd(audio.Hash, "album", audio.Album, addHandler)
// 				// 	}
// 				// 	if audio.Artist != "" {
// 				// 		TieAdd(audio.Hash, "artist", audio.Artist, addHandler)
// 				// 	}
// 				// 	if audio.Title != "" {
// 				// 		TieAdd(audio.Hash, "title", audio.Title, addHandler)
// 				// 	}
// 				// 	if audio.Track != 0 {
// 				// 		TieAdd(audio.Hash, "track", strconv.Itoa(audio.Track), addHandler)
// 				// 	}
// 				// 	if audio.Year != 0 {
// 				// 		TieAdd(audio.Hash, "year", strconv.Itoa(audio.Year), addHandler)
// 				// 	}
// 				// }
// 			}
// 		}
// 	}
// }
