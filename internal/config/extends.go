package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	yaml "github.com/goccy/go-yaml"
)

// ExtendsBuiltin is the only accepted value of an agent's `extends`: start from the
// built-in template of the same name and override just the keys written (gofer-arsj).
const ExtendsBuiltin = "builtin"

// BuiltinAgentTemplate looks up the built-in agent template named name (a deep copy).
// The agent package registers it at init — templates live there, and config cannot
// import agent (G022). Nil means no templates are linked in, so `extends` fails.
var BuiltinAgentTemplate func(name string) (AgentConfig, bool)

// BuiltinAgentTemplateNames lists the template names for error messages (registered
// with BuiltinAgentTemplate).
var BuiltinAgentTemplateNames func() []string

// agentFieldIndex maps an agent yaml key to its AgentConfig field index.
var agentFieldIndex = func() map[string]int {
	t := reflect.TypeOf(AgentConfig{})
	out := make(map[string]int, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name != "" && name != "-" {
			out[name] = i
		}
	}
	return out
}()

// ApplyAgentExtends resolves every agent declared with `extends: builtin`: the
// built-in template of the same name is the base and each key WRITTEN in the
// declaration replaces that whole field (lists and maps are replaced, not merged).
// Whether a key was written is read from raw — the yaml document holding the
// agents mapping — never from zero values, so `interactive: false` or
// `interactive_args: []` really override the template. Used for config.yaml (via
// Load) and worker.yaml.
func ApplyAgentExtends(agents map[string]AgentConfig, raw []byte) error {
	need := false
	for _, a := range agents {
		if a.Extends != "" {
			need = true
			break
		}
	}
	if !need {
		return nil
	}
	var doc struct {
		Agents map[string]map[string]any `yaml:"agents"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("decode agents: %w", err)
	}
	for name, a := range agents {
		if a.Extends == "" {
			continue
		}
		if a.Extends != ExtendsBuiltin {
			return fmt.Errorf("agents.%s.extends: %q is not supported (only %q)", name, a.Extends, ExtendsBuiltin)
		}
		if BuiltinAgentTemplate == nil {
			return fmt.Errorf("agents.%s.extends: no built-in agent templates are available", name)
		}
		tpl, ok := BuiltinAgentTemplate(name)
		if !ok {
			avail := ""
			if BuiltinAgentTemplateNames != nil {
				avail = " (built-in templates: " + strings.Join(BuiltinAgentTemplateNames(), ", ") + ")"
			}
			return fmt.Errorf("agents.%s.extends: builtin needs a built-in template named %q%s", name, name, avail)
		}
		declared := map[string]bool{}
		for k := range doc.Agents[name] {
			if k != "extends" {
				declared[k] = true
			}
		}
		base := cloneAgent(tpl)
		merged := cloneAgent(tpl)
		mv, av := reflect.ValueOf(&merged).Elem(), reflect.ValueOf(a)
		for k := range declared {
			if idx, ok := agentFieldIndex[k]; ok {
				mv.Field(idx).Set(av.Field(idx))
			}
		}
		merged.Extends = ExtendsBuiltin
		merged.declared = declared
		merged.base = &base
		agents[name] = merged
	}
	return nil
}

// DeclaredKeys lists the keys an `extends` agent wrote itself (sorted); nil for an
// agent without extends.
func (a AgentConfig) DeclaredKeys() []string {
	if a.Extends == "" {
		return nil
	}
	keys := make([]string, 0, len(a.declared))
	for k := range a.declared {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// MarshalYAML writes an `extends` agent as its DIFFERENCE from the template: the keys
// it declared plus any field an edit moved away from the template value. Template
// values are never materialised into the file, so the agent keeps following the
// built-in template across upgrades. Any other agent marshals field by field as before.
func (a AgentConfig) MarshalYAML() (any, error) {
	type plain AgentConfig
	if a.Extends == "" || a.base == nil {
		return plain(a), nil
	}
	out := yaml.MapSlice{{Key: "extends", Value: a.Extends}}
	av, bv := reflect.ValueOf(a), reflect.ValueOf(*a.base)
	t := av.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		key, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if key == "" || key == "-" || key == "extends" {
			continue
		}
		if !a.declared[key] && reflect.DeepEqual(av.Field(i).Interface(), bv.Field(i).Interface()) {
			continue
		}
		out = append(out, yaml.MapItem{Key: key, Value: av.Field(i).Interface()})
	}
	return out, nil
}

// cloneAgent deep-copies an agent definition (slices, maps and pointers included) so
// a template handed out to several configs can never be mutated through one of them.
func cloneAgent(a AgentConfig) AgentConfig {
	return deepCopy(reflect.ValueOf(a)).Interface().(AgentConfig)
}

func deepCopy(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(deepCopy(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), deepCopy(iter.Value()))
		}
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(deepCopy(v.Elem()))
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v) // unexported fields keep their (immutable) values
		for i := range v.NumField() {
			if v.Type().Field(i).PkgPath == "" {
				out.Field(i).Set(deepCopy(v.Field(i)))
			}
		}
		return out
	default:
		return v
	}
}
