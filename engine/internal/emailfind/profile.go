package emailfind

import "math"

// tally counts how many observed addresses at a domain fit a shape (hits) and
// how many addresses built from that shape turned out undeliverable (misses).
type tally struct {
	Hits   int `json:"hits"`
	Misses int `json:"misses,omitempty"`
}

// Profile is a domain's learned address shape, as stored in email_patterns.
type Profile struct {
	Domain     string           `json:"domain"`
	Shapes     map[string]tally `json:"shapes"`
	TopShape   string           `json:"top_shape"`
	Confidence int              `json:"confidence"`
	Provider   string           `json:"provider"`
	CatchAll   bool             `json:"catch_all"`
	SampleSize int              `json:"sample_size"`
}

// observe records that an address built from one of shapes fit (valid) or
// bounced (!valid). An address that matches several shapes credits each; the
// dominant-shape maths weighs them out across the domain.
func (p *Profile) observe(shapeNames []string, valid bool) {
	if p.Shapes == nil {
		p.Shapes = map[string]tally{}
	}
	for _, name := range shapeNames {
		t := p.Shapes[name]
		if valid {
			t.Hits++
		} else {
			t.Misses++
		}
		p.Shapes[name] = t
	}
}

// shapeScore is our confidence (0-1) that an address of this shape is right,
// blending the domain's own evidence with the shape's prior. Evidence weighs
// more as the shape is seen more often (Laplace-smoothed success rate); with
// no evidence it falls back to the prior.
func (p Profile) shapeScore(name string) float64 {
	t := p.Shapes[name]
	obs := t.Hits + t.Misses
	rate := (float64(t.Hits) + 1) / (float64(obs) + 2) // smoothed success rate
	prior := 0.01
	if s, ok := shapeByName[name]; ok {
		prior = s.prior
	}
	w := float64(obs) / (float64(obs) + 3) // trust evidence more as it grows
	return w*rate + (1-w)*prior
}

// recompute picks the domain's winning shape and overall confidence from the
// tallies, and refreshes SampleSize. The winner is the shape with the most net
// hits; confidence is its smoothed success rate scaled by how dominant it is
// over the runner-up, so a domain seen to use one clear shape scores high and
// a mixed or thin domain scores low. A catch-all domain is capped, because
// acceptance there proves nothing.
func (p *Profile) recompute() {
	p.SampleSize = 0
	var best, runner string
	var bestNet, runnerNet int
	for name, t := range p.Shapes {
		p.SampleSize += t.Hits + t.Misses
		net := t.Hits - t.Misses
		switch {
		case best == "" || net > bestNet:
			runner, runnerNet = best, bestNet
			best, bestNet = name, net
		case runner == "" || net > runnerNet:
			runner, runnerNet = name, net
		}
	}
	if best == "" || bestNet <= 0 {
		p.TopShape, p.Confidence = "", 0
		return
	}
	p.TopShape = best
	rate := p.shapeScore(best)
	// Dominance: how far the winner is ahead of the runner-up, 0.5 (tied) to
	// 1 (uncontested).
	dom := 1.0
	if runnerNet > 0 {
		dom = float64(bestNet) / float64(bestNet+runnerNet)
	}
	conf := rate * dom
	if p.CatchAll {
		conf *= 0.6
	}
	p.Confidence = int(math.Round(conf * 100))
}
