package bot

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed builtins/*.yml
var builtinFS embed.FS

// BuiltinInfo describes one embedded sub-agent template.
type BuiltinInfo struct {
	Name        string
	Description string
}

var (
	builtinsOnce  sync.Once
	builtinsErr   error
	builtinsBytes map[string][]byte
	builtinsList  []BuiltinInfo
)

// builtinHeader matches the registry-only fields we read from each embedded
// YAML. The Bot struct ignores the top-level `description` (yaml.v3 is
// non-strict in non-KnownFields mode), so it's free to use as the
// list-builtins description without polluting the runtime schema.
type builtinHeader struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func loadBuiltins() {
	bytes := map[string][]byte{}
	list := []BuiltinInfo{}

	entries, err := fs.ReadDir(builtinFS, "builtins")
	if err != nil {
		builtinsErr = fmt.Errorf("read embedded builtins dir: %w", err)
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}
		data, err := fs.ReadFile(builtinFS, "builtins/"+entry.Name())
		if err != nil {
			builtinsErr = fmt.Errorf("read embedded builtin %s: %w", entry.Name(), err)
			return
		}
		var hdr builtinHeader
		err = yaml.Unmarshal(data, &hdr)
		if err != nil {
			builtinsErr = fmt.Errorf("parse embedded builtin %s: %w", entry.Name(), err)
			return
		}
		hdr.Name = strings.TrimSpace(hdr.Name)
		hdr.Description = strings.TrimSpace(hdr.Description)
		if hdr.Name == "" {
			builtinsErr = fmt.Errorf("embedded builtin %s: name is required", entry.Name())
			return
		}
		if _, dup := bytes[hdr.Name]; dup {
			builtinsErr = fmt.Errorf("embedded builtin %q is declared in more than one file", hdr.Name)
			return
		}
		bytes[hdr.Name] = data
		list = append(list, BuiltinInfo(hdr))
	}

	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	builtinsBytes = bytes
	builtinsList = list
}

// LookupBuiltin returns the raw YAML bytes and registry metadata for the
// named embedded sub-agent. The bool is false when the name is unknown or
// when the registry failed to initialize.
func LookupBuiltin(name string) ([]byte, BuiltinInfo, bool) {
	builtinsOnce.Do(loadBuiltins)
	if builtinsErr != nil {
		return nil, BuiltinInfo{}, false
	}
	data, ok := builtinsBytes[name]
	if !ok {
		return nil, BuiltinInfo{}, false
	}
	for _, info := range builtinsList {
		if info.Name == name {
			return data, info, true
		}
	}
	return data, BuiltinInfo{Name: name}, true
}

// ListBuiltins returns metadata for every embedded sub-agent, sorted by name.
func ListBuiltins() ([]BuiltinInfo, error) {
	builtinsOnce.Do(loadBuiltins)
	if builtinsErr != nil {
		return nil, builtinsErr
	}
	out := make([]BuiltinInfo, len(builtinsList))
	copy(out, builtinsList)
	return out, nil
}
