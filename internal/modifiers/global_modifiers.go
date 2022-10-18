package modifiers

import (
	"fmt"
	"sync"

	"github.com/vingarcia/ksql/kmodifiers"
)

// Here we keep all the registered modifiers
var modifiers sync.Map

func init() {
	// Here we expose the registration function in a public package,
	// so users can use it:
	kmodifiers.RegisterAttrModifier = RegisterAttrModifier

	// These are the builtin modifiers:

	// This one is useful for serializing/desserializing structs:
	modifiers.Store("json", jsonModifier)

	// This next two are useful for the UpdatedAt and Created fields respectively:
	// They only work on time.Time attributes and will set the attribute to time.Now().
	modifiers.Store("timeNowUTC", timeNowUTCModifier)
	modifiers.Store("timeNowUTC/skipUpdates", timeNowUTCSkipUpdatesModifier)

	// These are mostly example modifiers and they are also used
	// to test the feature of skipping updates, inserts and queries.
	modifiers.Store("skipUpdates", skipUpdatesModifier)
	modifiers.Store("skipInserts", skipInsertsModifier)
}

// RegisterAttrModifier allow users to add custom modifiers on startup
// it is recommended to do this inside an init() function.
func RegisterAttrModifier(key string, modifier kmodifiers.AttrModifier) {
	_, found := modifiers.Load(key)
	if found {
		panic(fmt.Errorf("KSQL: cannot register modifier '%s' name is already in use", key))
	}

	modifiers.Store(key, modifier)
}

// LoadGlobalModifier is used internally by KSQL to load
// modifiers during runtime.
func LoadGlobalModifier(key string) (kmodifiers.AttrModifier, error) {
	rawModifier, _ := modifiers.Load(key)
	modifier, ok := rawModifier.(kmodifiers.AttrModifier)
	if !ok {
		return kmodifiers.AttrModifier{}, fmt.Errorf("no modifier found with name '%s'", key)
	}

	return modifier, nil
}
