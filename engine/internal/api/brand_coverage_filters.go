package api

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// A deliberately small filter language: identifiers come from the resource's
// field catalog and operators from this switch, never directly from SQL input.
type coverageFilters struct {
	Mode  string `json:"mode"`
	Rules []struct {
		Field string `json:"field"`
		Op    string `json:"op"`
		Value string `json:"value"`
	} `json:"rules"`
}

func coverageFilterSQL(raw string, res coverageResource, args *[]any) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > 8000 {
		return "", errInvalid("filters are too large")
	}
	var filters coverageFilters
	if err := json.Unmarshal([]byte(raw), &filters); err != nil {
		return "", errInvalid("invalid filters")
	}
	if (filters.Mode != "all" && filters.Mode != "any") || len(filters.Rules) > 12 {
		return "", errInvalid("use all or any with at most 12 filter rules")
	}
	clauses := []string{}
	for _, rule := range filters.Rules {
		f, ok := res.Fields[rule.Field]
		if rule.Field == "health" && res.Table == "coverage_placements" {
			f = coverageField{Kind: "text"}
			ok = true
		}
		if rule.Field == "created_at" || rule.Field == "updated_at" {
			f = coverageField{Kind: "timestamp"}
			ok = true
		}
		if !ok {
			return "", errInvalid("unsupported filter field")
		}
		if rule.Op == "empty" {
			clauses = append(clauses, "("+rule.Field+" IS NULL OR "+rule.Field+"::text='')")
			continue
		}
		if rule.Op == "contains" {
			if f.Kind != "text" && f.Kind != "long" && f.Kind != "url" && f.Kind != "domain" {
				return "", errInvalid("contains needs a text field")
			}
			*args = append(*args, rule.Value)
			clauses = append(clauses, fmt.Sprintf("%s ILIKE '%%' || $%d || '%%'", rule.Field, len(*args)))
			continue
		}
		op := ""
		switch rule.Op {
		case "eq":
			op = "="
		case "ne":
			op = "<>"
		case "before":
			op = "<"
		case "after":
			op = ">"
		default:
			return "", errInvalid("unsupported filter operator")
		}
		cast := "text"
		switch f.Kind {
		case "uuid":
			cast = "uuid"
			if !validUUID(rule.Value) {
				return "", errInvalid("filter needs a valid record ID")
			}
		case "money", "number", "hours":
			cast = "numeric"
			if value, err := strconv.ParseFloat(rule.Value, 64); err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				return "", errInvalid("filter needs a number")
			}
		case "bool":
			cast = "boolean"
			if rule.Value != "true" && rule.Value != "false" {
				return "", errInvalid("filter needs true or false")
			}
		case "date", "timestamp":
			cast = "date"
			if _, err := time.Parse("2006-01-02", rule.Value); err != nil {
				return "", errInvalid("filter date must be YYYY-MM-DD")
			}
		}
		*args = append(*args, rule.Value)
		clauses = append(clauses, fmt.Sprintf("%s::%s %s $%d::%s", rule.Field, cast, op, len(*args), cast))
	}
	if len(clauses) == 0 {
		return "", nil
	}
	join := " AND "
	if filters.Mode == "any" {
		join = " OR "
	}
	return " AND (" + strings.Join(clauses, join) + ")", nil
}
