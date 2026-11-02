package value

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// field is one document field of a struct type.
type field struct {
	name      string
	index     []int
	omitEmpty bool
	omitZero  bool
}

// structInfo describes how a struct type maps to a document.
type structInfo struct {
	fields []field
	byName map[string]int // document field name to index in fields
	key    *field         // the `kdoq:",key"` field, kept out of the body
	err    error
}

var structCache sync.Map // reflect.Type -> *structInfo

func infoOf(t reflect.Type) *structInfo {
	if cached, ok := structCache.Load(t); ok {
		return cached.(*structInfo)
	}
	info := &structInfo{}
	names := map[string]bool{}
	collectFields(t, nil, info, names, 0)
	info.byName = make(map[string]int, len(info.fields))
	for i, f := range info.fields {
		info.byName[f.name] = i
	}
	actual, _ := structCache.LoadOrStore(t, info)
	return actual.(*structInfo)
}

func collectFields(t reflect.Type, prefix []int, info *structInfo, names map[string]bool, depth int) {
	if depth > 16 {
		info.err = fmt.Errorf("struct %s embeds structs too deeply", t)
		return
	}
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		index := append(append([]int(nil), prefix...), i)
		kdoqTag, _ := sf.Tag.Lookup("kdoq")
		jsonTag, _ := sf.Tag.Lookup("json")
		kdoqName, kdoqOpts, _ := strings.Cut(kdoqTag, ",")
		jsonName, jsonOpts, _ := strings.Cut(jsonTag, ",")
		if kdoqTag == "-" || (jsonTag == "-" && kdoqName == "" && !hasOption(kdoqOpts, "key")) {
			continue
		}
		name := kdoqName
		if name == "" {
			name = jsonName
		}
		if sf.Anonymous && name == "" && !hasOption(kdoqOpts, "key") {
			embedded := sf.Type
			if embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct && !isValueStruct(embedded) {
				collectFields(embedded, index, info, names, depth+1)
				continue
			}
		}
		if !sf.IsExported() {
			continue
		}
		if name == "" {
			name = sf.Name
		}
		f := field{
			name:      name,
			index:     index,
			omitEmpty: hasOption(kdoqOpts, "omitempty") || hasOption(jsonOpts, "omitempty"),
			omitZero:  hasOption(kdoqOpts, "omitzero") || hasOption(jsonOpts, "omitzero"),
		}
		if hasOption(kdoqOpts, "key") {
			if sf.Type.Kind() != reflect.String {
				info.err = fmt.Errorf("key field %s must be a string type, not %s", sf.Name, sf.Type)
				return
			}
			if info.key != nil {
				info.err = fmt.Errorf("struct %s has more than one key field", t)
				return
			}
			info.key = &f
			continue
		}
		if names[name] {
			info.err = fmt.Errorf("struct %s has more than one field named %q", t, name)
			return
		}
		names[name] = true
		info.fields = append(info.fields, f)
	}
}

func hasOption(options, want string) bool {
	for options != "" {
		var option string
		option, options, _ = strings.Cut(options, ",")
		if option == want {
			return true
		}
	}
	return false
}

// fieldValue walks index through v. Nil embedded pointers end the walk with
// false, or are allocated when alloc is set and the pointer is settable.
func fieldValue(v reflect.Value, index []int, alloc bool) (reflect.Value, bool) {
	for n, i := range index {
		if n > 0 {
			for v.Kind() == reflect.Pointer {
				if v.IsNil() {
					if !alloc || !v.CanSet() {
						return reflect.Value{}, false
					}
					v.Set(reflect.New(v.Type().Elem()))
				}
				v = v.Elem()
			}
		}
		v = v.Field(i)
	}
	return v, true
}
