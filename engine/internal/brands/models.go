// Package brands holds data about brands that is the same for every
// workspace: the business models a brand can pick in its setup ("What does
// this brand do?") and the ways its customers pay. Each model names the
// report packs that suit it, most suitable first. Adding a model is a change
// to business_models.json, not to code.
package brands

import (
	_ "embed"
	"encoding/json"
	"slices"
)

//go:embed business_models.json
var raw []byte

// Group is a heading in the dropdown.
type Group struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Model is one business model.
type Model struct {
	ID    string `json:"id"`
	Group string `json:"group"`
	Label string `json:"label"`
	// Packs are the report packs that suit it, the first one primary.
	Packs []string `json:"packs"`
}

// Revenue is one way customers pay.
type Revenue struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Catalog is the whole list.
type Catalog struct {
	Groups        []Group   `json:"groups"`
	Models        []Model   `json:"models"`
	RevenueModels []Revenue `json:"revenue_models"`
}

var catalog = func() Catalog {
	var c Catalog
	if err := json.Unmarshal(raw, &c); err != nil {
		panic("brands: business_models.json: " + err.Error())
	}
	return c
}()

// All returns the catalog.
func All() Catalog { return catalog }

// ModelByID returns a business model by id.
func ModelByID(id string) (Model, bool) {
	i := slices.IndexFunc(catalog.Models, func(m Model) bool { return m.ID == id })
	if i < 0 {
		return Model{}, false
	}
	return catalog.Models[i], true
}

// ValidRevenue reports whether id is a way customers pay.
func ValidRevenue(id string) bool {
	return slices.ContainsFunc(catalog.RevenueModels, func(r Revenue) bool { return r.ID == id })
}
