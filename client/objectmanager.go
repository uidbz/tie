package client

import (
	"errors"
	"reflect"
	"strconv"

	"git.sr.ht/~uid/tie/tiedb"
)

const (
	defaultLimit int = 1000
)

type ObjectManager[T any] struct {
	client        *TieClient
	collectionUid string
	data          T
	Limit         int
	Offset        int
}

func NewObjectManager[T any](client *TieClient, collectionUid string) ObjectManager[T] {
	return ObjectManager[T]{
		client:        client,
		collectionUid: collectionUid,
		Limit:         defaultLimit,
		Offset:        0,
	}
}

func (om *ObjectManager[T]) Get(uid string) (T, error) {
	var obj T
	var err error
	om.client.SimpleGet(uid, func(reply GetReply) {
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

	o := GetOptions{
		Sort: tiedb.SortOptions{
			Limit:  om.Limit,
			Offset: om.Offset,
		},
	}

	errorMsg := ""
	om.client.Get(om.collectionUid, o, func(reply GetReply) {
		if reply.Success {
			objectIds = reply.Result[om.collectionUid][str(TieUid)]
		} else {
			errorMsg = reply.Message
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

func getPropertyName(field reflect.StructField) string {
	var property string
	if tag, ok := field.Tag.Lookup("tie"); ok {
		property = tag
	} else {
		property = field.Name
	}

	return property
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
	batch.Add(om.collectionUid, str(TieUid), uid)

	for i := 0; i < v.NumField(); i++ {
		property := getPropertyName(t.Field(i))

		if property == objectUid {
			continue
		}
		valueField := v.Field(i)
		switch valueField.Kind() {
		case reflect.String:
			value := valueField.Interface().(string)
			if value != "" {
				batch.Add(uid, property, value)
			}

		case reflect.Int:
			intValue := valueField.Interface().(int)
			value := strconv.Itoa(intValue)

			if value != "" {
				batch.Add(uid, property, value)
			}

		case reflect.Float32:
			float32Value := valueField.Interface().(float32)
			value := strconv.FormatFloat(float64(float32Value), 'e', -1, 32)

			if value != "" {
				batch.Add(uid, property, value)
			}

		case reflect.Float64:
			float64Value := valueField.Interface().(float64)
			value := strconv.FormatFloat(float64Value, 'e', -1, 64)

			if value != "" {
				batch.Add(uid, property, value)
			}

		case reflect.Slice:
			for j := 0; j < valueField.Len(); j++ {
				value := valueField.Index(j).Interface().(string)
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
	om.client.SimpleGet(uid, func(reply GetReply) {
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
	batch.Add(om.collectionUid, str(TieUid), uid)

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
		property := getPropertyName(t.Field(i))
		if property == objectUid {
			continue
		}

		valueField := v.Field(i)
		switch valueField.Kind() {
		case reflect.String:
			value := valueField.Interface().(string)
			if value != "" {
				if !excludeUnchangedFromDeletion(uid, property, value) {
					batch.Add(uid, property, value)
				}
			}

		case reflect.Int:
			intValue := valueField.Interface().(int)
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
			float64Value := valueField.Interface().(float64)
			value := strconv.FormatFloat(float64Value, 'e', -1, 64)

			if value != "" {
				if !excludeUnchangedFromDeletion(uid, property, value) {
					batch.Add(uid, property, value)
				}
			}

		case reflect.Slice:
			for j := 0; j < valueField.Len(); j++ {
				value := valueField.Index(j).Interface().(string)
				if value != "" {
					if !excludeUnchangedFromDeletion(uid, property, value) {
						batch.Add(uid, property, value)
					}
				}
			}
		}
	}

	origObject.ForEachValue2(func(key, val1, val2 string) {
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
	om.client.SimpleGet(uid, func(reply GetReply) {
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
	batch.Delete(om.collectionUid, str(TieUid), uid)

	for i := 0; i < v.NumField(); i++ {
		property := getPropertyName(t.Field(i))
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
