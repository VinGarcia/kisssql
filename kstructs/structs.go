package kstructs

import (
	"fmt"
	"reflect"
	"strings"
)

// StructInfo stores metainformation of the struct
// parser in order to help the ksql library to work
// efectively and efficiently with reflection.
type StructInfo struct {
	IsNestedStruct bool
	byIndex        map[int]*FieldInfo
	byName         map[string]*FieldInfo
}

// FieldInfo contains reflection and tags
// information regarding a specific field
// of a struct.
type FieldInfo struct {
	Name            string
	Index           int
	Valid           bool
	SerializeAsJSON bool
}

// ByIndex returns either the *FieldInfo of a valid
// empty struct with Valid set to false
func (s StructInfo) ByIndex(idx int) *FieldInfo {
	field, found := s.byIndex[idx]
	if !found {
		return &FieldInfo{}
	}
	return field
}

// ByName returns either the *FieldInfo of a valid
// empty struct with Valid set to false
func (s StructInfo) ByName(name string) *FieldInfo {
	field, found := s.byName[name]
	if !found {
		return &FieldInfo{}
	}
	return field
}

func (s StructInfo) add(field FieldInfo) {
	field.Valid = true
	s.byIndex[field.Index] = &field
	s.byName[field.Name] = &field
}

// NumFields ...
func (s StructInfo) NumFields() int {
	return len(s.byIndex)
}

// This cache is kept as a pkg variable
// because the total number of types on a program
// should be finite. So keeping a single cache here
// works fine.
var tagInfoCache = map[reflect.Type]StructInfo{}

// GetTagInfo efficiently returns the type information
// using a global private cache
//
// In the future we might move this cache inside
// a struct, but for now this accessor is the one
// we are using
func GetTagInfo(key reflect.Type) StructInfo {
	return getCachedTagInfo(tagInfoCache, key)
}

func getCachedTagInfo(tagInfoCache map[reflect.Type]StructInfo, key reflect.Type) StructInfo {
	if info, found := tagInfoCache[key]; found {
		return info
	}

	info := getTagNames(key)
	tagInfoCache[key] = info
	return info
}

// StructToMap converts any struct type to a map based on
// the tag named `ksql`, i.e. `ksql:"map_key_name"`
//
// Valid pointers are dereferenced and copied to the map,
// null pointers are ignored.
//
// This function is efficient in the fact that it caches
// the slower steps of the reflection required to perform
// this task.
func StructToMap(obj interface{}) (map[string]interface{}, error) {
	v := reflect.ValueOf(obj)
	t := v.Type()

	if t.Kind() == reflect.Ptr {
		v = v.Elem()
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("input must be a struct or struct pointer")
	}

	info := getCachedTagInfo(tagInfoCache, t)

	m := map[string]interface{}{}
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		ft := field.Type()
		if ft.Kind() == reflect.Ptr {
			if field.IsNil() {
				continue
			}

			field = field.Elem()
		}

		m[info.ByIndex(i).Name] = field.Interface()
	}

	return m, nil
}

// PtrConverter was created to make it easier
// to handle conversion between ptr and non ptr types, e.g.:
//
// - *type to *type
// - type to *type
// - *type to type
// - type to type
type PtrConverter struct {
	BaseType  reflect.Type
	BaseValue reflect.Value
	ElemType  reflect.Type
	ElemValue reflect.Value
}

// NewPtrConverter instantiates a PtrConverter from
// an empty interface.
//
// The input argument can be of any type, but
// if it is a pointer then its Elem() will be
// used as source value for the PtrConverter.Convert()
// method.
func NewPtrConverter(v interface{}) PtrConverter {
	if v == nil {
		// This is necessary so that reflect.ValueOf
		// returns a valid reflect.Value
		v = (*interface{})(nil)
	}

	baseValue := reflect.ValueOf(v)
	baseType := reflect.TypeOf(v)

	elemType := baseType
	elemValue := baseValue
	if baseType.Kind() == reflect.Ptr {
		elemType = elemType.Elem()
		elemValue = elemValue.Elem()
	}
	return PtrConverter{
		BaseType:  baseType,
		BaseValue: baseValue,
		ElemType:  elemType,
		ElemValue: elemValue,
	}
}

// Convert attempts to convert the ElemValue to the destType received
// as argument and then returns the converted reflect.Value or an error
func (p PtrConverter) Convert(destType reflect.Type) (reflect.Value, error) {
	destElemType := destType
	if destType.Kind() == reflect.Ptr {
		destElemType = destType.Elem()
	}

	// Return 0 valued destType instance:
	if p.BaseType.Kind() == reflect.Ptr && p.BaseValue.IsNil() {
		// Note that if destType is a ptr it will return a nil ptr.
		return reflect.New(destType).Elem(), nil
	}

	if !p.ElemType.ConvertibleTo(destElemType) {
		return reflect.Value{}, fmt.Errorf(
			"cannot convert from type %v to type %v", p.BaseType, destType,
		)
	}

	destValue := p.ElemValue.Convert(destElemType)

	// Get the address of destValue if necessary:
	if destType.Kind() == reflect.Ptr {
		if !destValue.CanAddr() {
			tmp := reflect.New(destElemType)
			tmp.Elem().Set(destValue)
			destValue = tmp
		} else {
			destValue = destValue.Addr()
		}
	}

	return destValue, nil
}

// This function collects only the names
// that will be used from the input type.
//
// This should save several calls to `Field(i).Tag.Get("foo")`
// which improves performance by a lot.
func getTagNames(t reflect.Type) StructInfo {
	info := StructInfo{
		byIndex: map[int]*FieldInfo{},
		byName:  map[string]*FieldInfo{},
	}
	for i := 0; i < t.NumField(); i++ {
		name := t.Field(i).Tag.Get("ksql")
		if name == "" {
			continue
		}

		tags := strings.Split(name, ",")
		serializeAsJSON := false
		if len(tags) > 1 {
			name = tags[0]
			serializeAsJSON = tags[1] == "json"
		}

		info.add(FieldInfo{
			Name:            name,
			Index:           i,
			SerializeAsJSON: serializeAsJSON,
		})
	}

	// If there were `ksql` tags present, then we are finished:
	if len(info.byIndex) > 0 {
		return info
	}

	// If there are no `ksql` tags in the struct, lets assume
	// it is a struct tagged with `tablename` for allowing JOINs
	for i := 0; i < t.NumField(); i++ {
		name := t.Field(i).Tag.Get("tablename")
		if name == "" {
			continue
		}

		info.add(FieldInfo{
			Name:  name,
			Index: i,
		})
	}

	if len(info.byIndex) > 0 {
		info.IsNestedStruct = true
	}

	return info
}

// DecodeAsSliceOfStructs makes several checks
// while decoding an input type and returns
// useful information so that it is easier
// to manipulate the original slice later.
func DecodeAsSliceOfStructs(slice reflect.Type) (
	structType reflect.Type,
	isSliceOfPtrs bool,
	err error,
) {
	if slice.Kind() != reflect.Slice {
		err = fmt.Errorf(
			"expected input kind to be a slice but got %v",
			slice,
		)
		return
	}

	elemType := slice.Elem()
	isPtr := elemType.Kind() == reflect.Ptr

	if isPtr {
		elemType = elemType.Elem()
	}

	if elemType.Kind() != reflect.Struct {
		err = fmt.Errorf(
			"expected input to be a slice of structs but got %v",
			slice,
		)
		return
	}

	return elemType, isPtr, nil
}
