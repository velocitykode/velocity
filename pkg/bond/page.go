package bond

import (
	"encoding/json"
	"html"
)

// Page represents an Inertia page response
type Page struct {
	Component      string              `json:"component"`
	Props          Props               `json:"props"`
	URL            string              `json:"url"`
	Version        string              `json:"version"`
	EncryptHistory bool                `json:"encryptHistory,omitempty"`
	ClearHistory   bool                `json:"clearHistory,omitempty"`
	DeferredProps  map[string][]string `json:"deferredProps,omitempty"`
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
