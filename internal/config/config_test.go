package config

import "testing"

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		site    Site
		wantErr bool
	}{
		{"html with pagination", Site{Selector: Selector{Pagination: "a.next::attr(href)"}}, false},
		{"json-ld", Site{Format: FormatJSONLD}, false},
		{"json", Site{Format: FormatJSON, Selector: Selector{Item: "data", Link: "/{id}"}}, false},
		{"unknown format", Site{Format: "xml"}, true},
		{"json with pagination", Site{Format: FormatJSON, Selector: Selector{Pagination: "next"}}, true},
		{"json-ld with pagination", Site{Format: FormatJSONLD, Selector: Selector{Pagination: "a"}}, true},
	}

	for _, tt := range tests {
		err := tt.site.validate()
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: got error %v, want error %t", tt.name, err, tt.wantErr)
		}
	}
}
