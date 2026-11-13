package match

import (
	"embed"
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

//go:embed rules/ps4.yaml rules/ps5.yaml
var defaultRulesFS embed.FS

type ruleFile struct {
	Platform string `yaml:"platform"`
	Rules    []Rule `yaml:"rules"`
}

// LoadDefaults returns a RuleSet built from the embedded rule packs.
// The flags control which platforms' rules are loaded.
func LoadDefaults(enablePS4, enablePS5 bool) (*RuleSet, error) {
	rs := &RuleSet{}
	if enablePS4 {
		if err := rs.appendFromFS(defaultRulesFS, "rules/ps4.yaml"); err != nil {
			return nil, err
		}
	}
	if enablePS5 {
		if err := rs.appendFromFS(defaultRulesFS, "rules/ps5.yaml"); err != nil {
			return nil, err
		}
	}
	return rs, nil
}

func (rs *RuleSet) appendFromFS(fsys embed.FS, path string) error {
	data, err := fsys.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read embedded %q: %w", path, err)
	}
	return rs.appendBytes(path, data)
}

func (rs *RuleSet) appendBytes(source string, data []byte) error {
	var rf ruleFile
	if err := yaml.Unmarshal(data, &rf); err != nil {
		return fmt.Errorf("parse %q: %w", source, err)
	}
	for i, r := range rf.Rules {
		re, err := regexp.Compile(r.PathRegex)
		if err != nil {
			return fmt.Errorf("%s rule %d (%s): invalid path_regex %q: %w",
				source, i, r.Kind, r.PathRegex, err)
		}
		rs.rules = append(rs.rules, compiledRule{
			kind:       r.Kind,
			hostSuffix: r.HostSuffix,
			pathRegex:  re,
		})
	}
	return nil
}

// Len reports the number of compiled rules — useful for tests and diagnostics.
func (rs *RuleSet) Len() int { return len(rs.rules) }
