package tiedb

import (
	"fmt"
	"os"
	"sync"
)

var colMutex sync.Mutex

func NewDB(writeToDisk bool) *Tree {
	return NewTreeWith(KeyComparator, writeToDisk)
}

func Initialize(path string, dbname string, clearExistingDB bool, writeToDisk bool) *InternalCollection {
	ic := InternalCollection{}

	ic.WriteToDisk = writeToDisk
	ic.DBPath = path
	ic.DBName = dbname
	ic.DBFullPath = path + "/" + dbname + ".tie"
	fmt.Println("Initializing", ic.DBFullPath)
	if writeToDisk {
		os.MkdirAll(path, 0777)
		ic.Freespace = make(chan FileEntry, MaxFreespace)
	}
	// ic.Values = NewTreeWith(PointerValueComparator)
	ic.Values = NewTreeWith(ValueComparator, writeToDisk)

	if writeToDisk {
		if clearExistingDB {
			os.Remove(ic.DBFullPath)
		}
		ok, dbFile := OpenDBRead(ic.DBFullPath)
		if ok {
			ic.LoadDB(dbFile)
		}

		ic.DBWriter()
	}
	e := ic.Insert(dbname)

	ic.Level = e.Level
	ic.Id = e.Id

	return &ic
}

func (db *Tree) GetCollection(key CollectionKey) Collection {
	colMutex.Lock()
	defer colMutex.Unlock()

	found, col := db.Get(key)
	if !found {
		col = Initialize(key.Database, key.Collection, false, db.writeToDisk)
		db.Put(key, col)
	}

	return col.(Collection)
}
