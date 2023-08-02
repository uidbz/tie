package tiedb

import (
	"bytes"
	"sync/atomic"

	// "encoding/hex"
	"fmt"
	"math/rand"
	"time"
)

var __DEBUG bool = true
var __VERBOSE bool = true

func Debug(value string) {
	if __DEBUG {
		fmt.Println(value)
	}
}

func Info(value string) {
	if __VERBOSE || __DEBUG {
		fmt.Println(value)
	}
}

var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

// Used for testing worst-case scenario
func (t *Collection) WriteRandomDB(filename string) {
	fmt.Println("Writing random database")
	rand.Seed(time.Now().UnixNano())
	for i := 0; i < 100000; i++ {
		t.insert(randSeq(100))
	}
	fmt.Println("Done writing.")
}
func randSeq(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func (ic *Collection) GetTotalEntries() uint64 {
	return ic.totalEntries
}

func (ic *Collection) nextID() uint64 {
	return atomic.AddUint64(&ic.totalEntries, 1)
}

func KeyComparator(o1, o2 interface{}) int {
	k1 := o1.(CollectionKey)
	k2 := o2.(CollectionKey)
	return StringComparator(k1.Collection+k1.Database, k2.Collection+k2.Database)
}

func StringComparator(o1, o2 interface{}) int {
	s1 := o1.(string)
	s2 := o2.(string)
	return bytes.Compare([]byte(s1), []byte(s2))
}

func UInt64Comparator(o1, o2 interface{}) int {
	i1 := o1.(uint64)
	i2 := o2.(uint64)
	switch {
	case i1 > i2:
		return 1
	case i1 < i2:
		return -1
	default:
		return 0
	}
}

func UniqueValueComparator(o1, o2 interface{}) int {
	k1 := o1.(UniqueValue)
	k2 := o2.(UniqueValue)
	switch {
	case k1.ParentId > k2.ParentId:
		return 1
	case k1.ParentId < k2.ParentId:
		return -1
	default:
		return bytes.Compare(k1.Value[:], k2.Value[:])
	}
}

func UniqueAssociationComparator(o1, o2 interface{}) int {
	k1 := o1.(UniqueAssociation)
	k2 := o2.(UniqueAssociation)
	switch {
	case k1.AssociateTo > k2.AssociateTo:
		return 1
	case k1.AssociateTo < k2.AssociateTo:
		return -1
	case k1.Relation > k2.Relation:
		return 1
	case k1.Relation < k2.Relation:
		return -1
	default:
		return 0
	}
}

func AssociationComparator(o1, o2 interface{}) int {
	k1 := o1.(UniqueAssociation)
	k2 := o2.(UniqueAssociation)
	switch {
	case k1.AssociateTo > k2.AssociateTo:
		return 1
	case k1.AssociateTo < k2.AssociateTo:
		return -1
	// case k1.Relation > k2.Relation:
	// 	return 1
	// case k1.Relation < k2.Relation:
	// 	return -1
	default:
		return 0
	}
}

func PointerValueComparator(o1, o2 interface{}) int {
	k1 := o1.(*[]byte)
	k2 := o2.(*[]byte)
	return bytes.Compare(*k1, *k2)
}

func ValueComparator(o1, o2 interface{}) int {
	k1 := o1.([]byte)
	k2 := o2.([]byte)
	return bytes.Compare(k1, k2)
}

// func GetCollectionId(value string) []byte {
// 	b := []byte(value)
// 	// if len(b) == 0 {

// 	// 	return []byte(hex.EncodeToString([]byte{97}))
// 	// }
// 	// return []byte(hex.EncodeToString(b[0:DB_GRANULARITY]))
// 	return b[0:DB_GRANULARITY]
// }
