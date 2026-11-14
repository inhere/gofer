package config

import (
	"reflect"
	"slices"
	"strings"
)

// EnvRefNames lists the names of the environment variables the configuration points
// at through its `*_env` settings (server.token_env, a worker's token_env, a webhook's
// secret_env, a secret's env, an MCP entry's secret_env values, ...): the variables
// that hold gofer's own credentials and secrets. Callers that run a command on the
// server's behalf without the job pipeline's isolation (a schedule's pre-run check)
// drop these from the environment they pass on. The result is sorted and has no
// duplicates; it is derived by reflection over the `yaml` tags, so a new `*_env`
// setting is covered without touching this function.
func (c *Config) EnvRefNames() []string {
	if c == nil {
		return nil
	}
	seen := map[string]bool{}
	collectEnvRefs(reflect.ValueOf(c), seen)
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func yamlFieldName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
	return name
}

func collectEnvRefs(v reflect.Value, seen map[string]bool) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			collectEnvRefs(v.Elem(), seen)
		}
	case reflect.Struct:
		t := v.Type()
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			if strings.HasSuffix(yamlFieldName(f), "_env") {
				addEnvRefNames(v.Field(i), seen)
				continue
			}
			collectEnvRefs(v.Field(i), seen)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			collectEnvRefs(v.Index(i), seen)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			collectEnvRefs(v.MapIndex(k), seen)
		}
	}
}

// addEnvRefNames records the variable names a `*_env` field holds: the string itself,
// the strings of a list, or the values of a map (whose keys are what a child process
// sees, not host variables).
func addEnvRefNames(v reflect.Value, seen map[string]bool) {
	switch v.Kind() {
	case reflect.String:
		if s := strings.TrimSpace(v.String()); s != "" {
			seen[s] = true
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			addEnvRefNames(v.Index(i), seen)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			addEnvRefNames(v.MapIndex(k), seen)
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			addEnvRefNames(v.Elem(), seen)
		}
	}
}
