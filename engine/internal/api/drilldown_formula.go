package api

import (
	"slices"
	"strconv"
	"unicode"
)

// Custom columns on the drill-down report (B5b): arithmetic over a row's
// metrics, such as revenue / visitors or (revenue - cost) / conversions.

// colFormula is a parsed formula; eval gives nil for a missing value or a
// division by zero.
type colFormula interface {
	eval(get func(string) *float64) *float64
}

type colNum float64
type colMetric string
type colOp struct {
	op   byte
	l, r colFormula
}

func (n colNum) eval(func(string) *float64) *float64        { v := float64(n); return &v }
func (m colMetric) eval(get func(string) *float64) *float64 { return get(string(m)) }
func (o colOp) eval(get func(string) *float64) *float64 {
	l, r := o.l.eval(get), o.r.eval(get)
	if l == nil || r == nil {
		return nil
	}
	var v float64
	switch o.op {
	case '+':
		v = *l + *r
	case '-':
		v = *l - *r
	case '*':
		v = *l * *r
	case '/':
		if *r == 0 {
			return nil
		}
		v = *l / *r
	}
	return &v
}

type colParser struct {
	s   []rune
	pos int
}

// parseColumnFormula reads a formula over the report's metric names.
func parseColumnFormula(src string) (colFormula, error) {
	if len(src) == 0 || len(src) > 200 {
		return nil, errInvalid("a column's formula is 1 to 200 characters")
	}
	p := &colParser{s: []rune(src)}
	f, err := p.expr()
	if err != nil {
		return nil, err
	}
	p.skip()
	if p.pos != len(p.s) {
		return nil, errInvalid("formula: unexpected " + string(p.s[p.pos:]))
	}
	return f, nil
}

func (p *colParser) skip() {
	for p.pos < len(p.s) && unicode.IsSpace(p.s[p.pos]) {
		p.pos++
	}
}

func (p *colParser) expr() (colFormula, error) {
	l, err := p.term()
	for err == nil {
		p.skip()
		if p.pos >= len(p.s) || (p.s[p.pos] != '+' && p.s[p.pos] != '-') {
			return l, nil
		}
		op := byte(p.s[p.pos])
		p.pos++
		var r colFormula
		if r, err = p.term(); err == nil {
			l = colOp{op, l, r}
		}
	}
	return nil, err
}

func (p *colParser) term() (colFormula, error) {
	l, err := p.atom()
	for err == nil {
		p.skip()
		if p.pos >= len(p.s) || (p.s[p.pos] != '*' && p.s[p.pos] != '/') {
			return l, nil
		}
		op := byte(p.s[p.pos])
		p.pos++
		var r colFormula
		if r, err = p.atom(); err == nil {
			l = colOp{op, l, r}
		}
	}
	return nil, err
}

func (p *colParser) atom() (colFormula, error) {
	p.skip()
	if p.pos >= len(p.s) {
		return nil, errInvalid("formula ends early: it combines metrics and numbers with + - * / and brackets")
	}
	r := p.s[p.pos]
	switch {
	case r == '(':
		p.pos++
		f, err := p.expr()
		if err != nil {
			return nil, err
		}
		p.skip()
		if p.pos >= len(p.s) || p.s[p.pos] != ')' {
			return nil, errInvalid("formula: a bracket isn't closed")
		}
		p.pos++
		return f, nil
	case r >= 'a' && r <= 'z':
		start := p.pos
		for p.pos < len(p.s) && (p.s[p.pos] >= 'a' && p.s[p.pos] <= 'z' || p.s[p.pos] == '_') {
			p.pos++
		}
		name := string(p.s[start:p.pos])
		if !slices.Contains(drilldownMetrics, name) {
			return nil, errInvalid("formula: " + name + " isn't a metric (visits, visitors, conversions, revenue, cost, profit, roi, roas, cr, epv, cpa)")
		}
		return colMetric(name), nil
	case unicode.IsDigit(r) || r == '.':
		start := p.pos
		for p.pos < len(p.s) && (unicode.IsDigit(p.s[p.pos]) || p.s[p.pos] == '.') {
			p.pos++
		}
		v, err := strconv.ParseFloat(string(p.s[start:p.pos]), 64)
		if err != nil {
			return nil, errInvalid("formula: " + string(p.s[start:p.pos]) + " isn't a number")
		}
		return colNum(v), nil
	}
	return nil, errInvalid("formula: unexpected " + string(r))
}
