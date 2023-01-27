package tiedb

import (
	"fmt"
	"os"
	"sync"
)

var colMutex sync.Mutex

func NewDB(writeToDisk bool) *Tree {
	return newTreeWith(KeyComparator, writeToDisk)
}

func initialize(path string, dbname string, clearExistingDB bool, writeToDisk bool) *Collection {
	ic := Collection{}

	ic.writeToDisk = writeToDisk
	ic.dBPath = path
	ic.dBName = dbname
	ic.dBFullPath = path + "/" + dbname + ".tie"
	fmt.Println("Initializing", ic.dBFullPath)
	if writeToDisk {
		os.MkdirAll(path, 0777)
		ic.freespace = make(chan FileEntry, MaxFreespace)
	}
	// ic.Values = NewTreeWith(PointerValueComparator)
	ic.values = newTreeWith(ValueComparator, writeToDisk)

	if writeToDisk {
		if clearExistingDB {
			os.Remove(ic.dBFullPath)
		}
		ok, dbFile := openDBRead(ic.dBFullPath)
		if ok {
			ic.loadDB(dbFile)
		}

		ic.dBWriter()
	}
	e := ic.insert(dbname)

	ic.level = e.Level
	ic.id = e.Id

	return &ic
}

func (db *Tree) GetCollection(key CollectionKey) *Collection {
	colMutex.Lock()
	defer colMutex.Unlock()

	found, col := db.Get(key)
	if !found {
		col = initialize(key.Database, key.Collection, false, db.writeToDisk)
		db.put(key, col)
	}

	return col.(*Collection)
}
