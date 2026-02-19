package bond

import (
	"encoding/json"
	"html"
)

// OnceMeta tracks a once-prop for the client so it knows which props
// have already been delivered and should not be sent again.
type OnceMeta struct {
	Prop string `json:"prop"`
}

// Page represents an Inertia page response
type Page struct {
	Component      string                `json:"component"`
	Props          Props                 `json:"props"`
	URL            string                `json:"url"`
	Version        string                `json:"version"`
	EncryptHistory bool                  `json:"encryptHistory,omitempty"`
	ClearHistory   bool                  `json:"clearHistory,omitempty"`
	DeferredProps  map[string][]string   `json:"deferredProps,omitempty"`
	MergeProps     []string              `json:"mergeProps,omitempty"`
	PrependProps   []string              `json:"prependProps,omitempty"`
	DeepMergeProps []string              `json:"deepMergeProps,omitempty"`
	MatchPropsOn   map[string][]string   `json:"matchPropsOn,omitempty"`
	ScrollProps    map[string]ScrollMeta `json:"scrollProps,omitempty"`
	OnceProps      map[string]OnceMeta   `json:"onceProps,omitempty"`
}

// ToJSON returns the JSON representation of the page
func (p Page) ToJSON() (string, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ToHTMLAttr returns JSON escaped for use in HTML data attributes
// Single quotes in the JSON are escaped since we use data-page='...'
func (p Page) ToHTMLAttr() (string, error) {
	jsonStr, err := p.ToJSON()
	if err != nil {
		return "", err
	}
	// Escape HTML entities and single quotes for safe embedding in data-page='...'
	escaped := html.EscapeString(jsonStr)
	return escaped, nil
}
