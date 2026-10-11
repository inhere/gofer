package configlint

import (
	"reflect"
	"strings"

	"github.com/inhere/gofer/internal/config"
)

// knownPath reports whether a dotted key path names a field of the config model
// (map levels accept any key). An unknown key is silently ignored by the decoder.
func knownPath(kind Target, path string) bool {
	t := reflect.TypeOf(config.Config{})
	if kind == TargetWorker {
		t = reflect.TypeOf(config.WorkerConfig{})
	}
	for _, seg := range strings.Split(path, ".") {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		switch t.Kind() {
		case reflect.Map:
			t = t.Elem()
			continue
		case reflect.Struct:
			ft, ok := fieldByTag(t, seg)
			if !ok {
				return false
			}
			t = ft
		default:
			return true // a scalar / list value: nothing below it to check
		}
	}
	return true
}

// fieldByTag finds the field whose yaml name is name, looking through inline fields.
func fieldByTag(t reflect.Type, name string) (reflect.Type, bool) {
	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		tag, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if strings.Contains(opts, "inline") {
			if ft, ok := fieldByTag(derefStruct(f.Type), name); ok {
				return ft, true
			}
			continue
		}
		if tag == "" {
			tag = strings.ToLower(f.Name)
		}
		if tag == name {
			return f.Type, true
		}
	}
	return nil, false
}

func derefStruct(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}
