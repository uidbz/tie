package client

import (
	"errors"
	"reflect"
	"strconv"

	"git.sr.ht/~uid/tie/tiedb"

	"git.sr.ht/~uid/tie/api"
	ws "git.sr.ht/~uid/tie/webservice"
)

// Default values
var config = Config{
	Username:      "defaultuser",
	Password:      "defaultpassword",
	Webservice:    "https://localhost:1161",
	Namespace:     "Collections",
	Collection:    "Main",
	ServeUrl:      "https://localhost:1162",
	DataHost:      "/data",
	ThumbnailHost: "/mnt/thumbnails",
	Key:           InitKey(),
}

const (
	tieUid            = "tie-uid"
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

type TieClient struct {
	client *ws.Client
	Config Config
}

type ObjectManager[T any] struct {
	client        *TieClient
	collectionUid string
	data          T
}

type Config struct {
	configDir     string
	configPath    string
	Username      string
	Password      string
	Namespace     string
	Collection    string
	Webservice    string
	ServeUrl      string
	DataHost      string
	ThumbnailHost string
	Key           []byte
	Verbose       bool
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

func NewObjectManager[T any](client *TieClient, collectionUid string) ObjectManager[T] {
	return ObjectManager[T]{
		client:        client,
		collectionUid: collectionUid,
	}
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

// Get a TripleSet from a key
func (tc *TieClient) Get(key string, handler func(reply GetReply)) {
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
	tc.Get(key, func(reply GetReply) {
		if reply.Success {
			result = true
		}
	})
	return result
}

func (om *ObjectManager[T]) Get(uid string) (T, error) {
	var obj T
	var err error
	om.client.Get(uid, func(reply GetReply) {
		if reply.Success {
			obj = om.ResultToObject(uid, reply.Result)
		} else {
			err = errors.New(reply.Message)
		}
	})
	return obj, err
}

func (om *ObjectManager[T]) GetAll() ([]T, error) {
	result := make([]T, 0)
	var objectIds tiedb.Value2

	errorMsg := ""
	om.client.Get(om.collectionUid, func(reply GetReply) {
		if reply.Success {
			objectIds = reply.Result[om.collectionUid][tieUid]
		} else {
			errorMsg = reply.GetMessage()
		}
	})
	if errorMsg != "" {
		return result, errors.New(errorMsg)
	}
	batch := om.client.NewBatch()
	for key, _ := range objectIds {
		batch.Get(key)
	}
	om.client.Batch(batch, func(reply BatchReply) {
		for _, x := range reply.GetReplys {
			if x.Success {
				obj := om.ResultToObject(x.OrigKey, x.Result)
				result = append(result, obj)
			}
		}
	})

	return result, nil
}

// Return value of string field from struct
func getStringFieldValue(fieldName string, something any) string {
	v := reflect.ValueOf(something)
	t := reflect.TypeOf(something)

	for i := 0; i < v.NumField(); i++ {
		if t.Field(i).Name == objectUid {
			if v.Field(i).Kind() == reflect.String {
				return v.Field(i).Interface().(string)
			}
		}
	}
	return ""
}

func (om *ObjectManager[T]) ResultToMap(result tiedb.TripleSet) (objects map[string]T) {
	objects = make(map[string]T, len(result))

	result.ForEachKey(func(key string) {
		o := om.ResultToObject(key, result)
		objects[key] = o
	})

	return objects
}

func (om *ObjectManager[T]) ResultToSlice(result tiedb.TripleSet) (objects []T) {
	objects = make([]T, 0, len(result))

	result.ForEachKey(func(key string) {
		o := om.ResultToObject(key, result)
		objects = append(objects, o)
	})

	return objects
}

func (om *ObjectManager[T]) ResultToObject(uid string, result tiedb.TripleSet) (obj T) {
	v := reflect.ValueOf(&obj).Elem()
	field := v.FieldByName(objectUid)
	field.SetString(uid)

	result[uid].ForEachValue2(func(val1, val2 string) {
		field := v.FieldByName(val1)
		if field.IsValid() {
			switch field.Kind() {
			case reflect.String:
				field.SetString(val2)

			case reflect.Int:
				intValue, _ := strconv.Atoi(val2)
				field.SetInt(int64(intValue))

			case reflect.Float32:
				float64Value, _ := strconv.ParseFloat(val2, 32)
				field.SetFloat(float64Value)

			case reflect.Float64:
				float64Value, _ := strconv.ParseFloat(val2, 64)
				field.SetFloat(float64Value)

			case reflect.Slice:
				field.Set(reflect.Append(field, reflect.ValueOf(val2)))
			}
		}
	})

	return obj
}

func (om *ObjectManager[T]) Associated(property string) (objects []T, err error) {
	om.client.Associated(property, func(reply AssociatedReply) {
		if reply.Success {
			objects = om.ResultToSlice(reply.Result)
		} else {
			err = errors.New(reply.Message)
		}
	})

	return objects, err
}

func (om *ObjectManager[T]) Add(object any) error {
	uid := getStringFieldValue(objectUid, object)
	if uid == "" {
		return errors.New("Need Uid field")
	}
	if exists := om.client.Exists(uid); exists {
		return errors.New("Entry with '" + uid + "' already exists")
	}

	v := reflect.ValueOf(object)
	t := reflect.TypeOf(object)

	batch := om.client.NewBatch()
	batch.Add(om.collectionUid, tieUid, uid)

	for i := 0; i < v.NumField(); i++ {
		property := t.Field(i).Name
		if property == objectUid {
			continue
		}

		switch v.Field(i).Kind() {
		case reflect.String:
			value := v.Field(i).Interface().(string)
			if value != "" {
				batch.Add(uid, property, value)
			}

		case reflect.Int:
			intValue := v.Field(i).Interface().(int)
			value := strconv.Itoa(intValue)

			if value != "" {
				batch.Add(uid, property, value)
			}

		case reflect.Float32:
			float32Value := v.Field(i).Interface().(float32)
			value := strconv.FormatFloat(float64(float32Value), 'e', -1, 32)

			if value != "" {
				batch.Add(uid, property, value)
			}

		case reflect.Float64:
			float64Value := v.Field(i).Interface().(float64)
			value := strconv.FormatFloat(float64Value, 'e', -1, 64)

			if value != "" {
				batch.Add(uid, property, value)
			}

		case reflect.Slice:
			for j := 0; j < v.Field(i).Len(); j++ {
				value := v.Field(i).Index(j).Interface().(string)
				if value != "" {
					batch.Add(uid, property, value)
				}
			}
		}
	}
	err := ""
	om.client.Batch(batch, func(reply BatchReply) {
		for _, x := range reply.AddReplys {
			if !x.Success {
				err += reply.Message + "\n"
			}
		}
	})
	if err != "" {
		return errors.New(err)
	}

	return nil
}

func (om *ObjectManager[T]) Upsert(object any) error {
	uid := getStringFieldValue(objectUid, object)
	if uid == "" {
		return errors.New("Need Uid field")
	}
	var origObject tiedb.TripleSet
	om.client.Get(uid, func(reply GetReply) {
		if reply.Success {
			origObject = reply.Result
		}
	})
	if origObject == nil {
		return om.Add(object)
	}

	v := reflect.ValueOf(object)
	t := reflect.TypeOf(object)

	batch := om.client.NewBatch()
	batch.Add(om.collectionUid, tieUid, uid)

	excludeUnchangedFromDeletion := func(uid, property, value string) bool {
		if origObject[uid][property].Has(value) {
			delete(origObject[uid][property], value)
			if len(origObject[uid][property]) == 0 {
				delete(origObject[uid], property)
			}
			if len(origObject[uid]) == 0 {
				delete(origObject, uid)
			}
			return true
		}
		return false
	}

	for i := 0; i < v.NumField(); i++ {
		property := t.Field(i).Name
		if property == objectUid {
			continue
		}

		switch v.Field(i).Kind() {
		case reflect.String:
			value := v.Field(i).Interface().(string)
			if value != "" {
				if !excludeUnchangedFromDeletion(uid, property, value) {
					batch.Add(uid, property, value)
				}
			}

		case reflect.Int:
			intValue := v.Field(i).Interface().(int)
			value := strconv.Itoa(intValue)

			if value != "" {
				if !excludeUnchangedFromDeletion(uid, property, value) {
					batch.Add(uid, property, value)
				}
			}

		case reflect.Float32:
			float32Value := v.Field(i).Interface().(float32)
			value := strconv.FormatFloat(float64(float32Value), 'e', -1, 32)

			if value != "" {
				if !excludeUnchangedFromDeletion(uid, property, value) {
					batch.Add(uid, property, value)
				}
			}

		case reflect.Float64:
			float64Value := v.Field(i).Interface().(float64)
			value := strconv.FormatFloat(float64Value, 'e', -1, 64)

			if value != "" {
				if !excludeUnchangedFromDeletion(uid, property, value) {
					batch.Add(uid, property, value)
				}
			}

		case reflect.Slice:
			for j := 0; j < v.Field(i).Len(); j++ {
				value := v.Field(i).Index(j).Interface().(string)
				if value != "" {
					if !excludeUnchangedFromDeletion(uid, property, value) {
						batch.Add(uid, property, value)
					}
				}
			}
		}
	}

	origObject.ForEachValue2(func(key, val1, val2 string) {
		if val1 == tiedb.ASSOCIATED { // Do not delete associated value
			return
		}
		batch.Delete(key, val1, val2)
	})

	err := ""
	om.client.Batch(batch, func(reply BatchReply) {
		for _, x := range reply.DeleteReplys {
			if !x.Success {
				err += x.Message + "\n"
			}
		}
		for _, x := range reply.AddReplys {
			if !x.Success {
				err += x.Message + "\n"
			}
		}
	})

	if err != "" {
		// Todo re-add deleted values, or better yet - implement transactions
		return errors.New(err)
	}

	return nil
}

func (om *ObjectManager[T]) Delete(object any) error {
	uid := getStringFieldValue(objectUid, object)
	if uid == "" {
		return errors.New("Need Uid field")
	}
	var origObject tiedb.TripleSet
	om.client.Get(uid, func(reply GetReply) {
		if reply.Success {
			origObject = reply.Result
		}
	})
	if origObject == nil {
		return om.Add(object)
	}

	v := reflect.ValueOf(object)
	t := reflect.TypeOf(object)

	batch := om.client.NewBatch()
	batch.Delete(om.collectionUid, tieUid, uid)

	for i := 0; i < v.NumField(); i++ {
		property := t.Field(i).Name
		if property == objectUid {
			continue
		}

		switch v.Field(i).Kind() {
		case reflect.String:
			value := v.Field(i).Interface().(string)
			if value != "" {
				batch.Delete(uid, property, value)
			}

		case reflect.Int:
			intValue := v.Field(i).Interface().(int)
			value := strconv.Itoa(intValue)

			if value != "" {
				batch.Delete(uid, property, value)
			}

		case reflect.Float32:
			float32Value := v.Field(i).Interface().(float32)
			value := strconv.FormatFloat(float64(float32Value), 'e', -1, 32)

			if value != "" {
				batch.Delete(uid, property, value)
			}

		case reflect.Float64:
			float64Value := v.Field(i).Interface().(float64)
			value := strconv.FormatFloat(float64Value, 'e', -1, 64)

			if value != "" {
				batch.Delete(uid, property, value)
			}

		case reflect.Slice:
			for j := 0; j < v.Field(i).Len(); j++ {
				value := v.Field(i).Index(j).Interface().(string)
				if value != "" {
					batch.Delete(uid, property, value)
				}
			}
		}
	}

	err := ""
	om.client.Batch(batch, func(reply BatchReply) {
		for _, x := range reply.DeleteReplys {
			if !x.Success {
				err += x.Message + "\n"
			}
		}
	})

	if err != "" {
		// Todo re-add deleted values, or better yet - implement transactions
		return errors.New(err)
	}

	return nil
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
