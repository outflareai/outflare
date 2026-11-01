package support

import (
	"testing"
	"time"
)

func TestSLADue(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	nine := [][2]int{{9 * 60, 17 * 60}}
	week := Week{nil, nine, nine, nine, nine, nine, nil} // Monday to Friday, 9 to 5
	p := Policy{Location: ny, Hours: &week}
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, ny)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		name, from string
		mins       int
		want       string
	}{
		{"inside the day", "2026-11-02 10:00", 60, "2026-11-02 11:00"},
		{"runs into the next day", "2026-11-02 16:30", 60, "2026-11-03 09:30"},
		{"before opening", "2026-11-02 07:00", 30, "2026-11-02 09:30"},
		{"Friday evening to Monday", "2026-11-06 18:00", 120, "2026-11-09 11:00"},
		{"Saturday", "2026-11-07 12:00", 8 * 60, "2026-11-09 17:00"},
		{"a full working week", "2026-11-02 09:00", 5 * 8 * 60, "2026-11-06 17:00"},
	} {
		if got := p.Due(at(c.from), c.mins); !got.Equal(at(c.want)) {
			t.Errorf("%s: due %v, want %s", c.name, got.In(ny), c.want)
		}
	}
	// Around the clock, minutes are minutes.
	if got := (Policy{}).Due(at("2026-11-07 12:00"), 90); !got.Equal(at("2026-11-07 13:30")) {
		t.Errorf("around the clock: %v", got)
	}
	// A split day (lunch closed) and a clock change: 1 November 2026 is the
	// end of daylight saving in New York.
	split := Week{nil, {{9 * 60, 12 * 60}, {13 * 60, 17 * 60}}}
	p = Policy{Location: ny, Hours: &split}
	if got := p.Due(at("2026-11-02 11:30"), 60); !got.Equal(at("2026-11-02 13:30")) {
		t.Errorf("lunch: %v", got.In(ny))
	}
	if got := p.Due(at("2026-10-31 12:00"), 30); !got.Equal(at("2026-11-02 09:30")) {
		t.Errorf("over the clock change: %v", got.In(ny))
	}
	// Open on the Sunday the clocks go back: 9 to 5 is still 9 to 5.
	sunday := Week{{{9 * 60, 17 * 60}}}
	p = Policy{Location: ny, Hours: &sunday}
	if got := p.Due(at("2026-11-01 09:00"), 60); !got.Equal(at("2026-11-01 10:00")) {
		t.Errorf("clock change day: %v", got.In(ny))
	}
}

func TestParseWeek(t *testing.T) {
	for _, bad := range []string{`{}`, `[[[60,30]],[],[],[],[],[],[]]`, `[[[0,1500]],[],[],[],[],[],[]]`, `[[],[],[],[],[],[],[]]`,
		`[[[540,720],[700,800]],[],[],[],[],[],[]]`} {
		if _, err := ParseWeek([]byte(bad)); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
	if w, err := ParseWeek([]byte(`null`)); w != nil || err != nil {
		t.Errorf("null: %v %v", w, err)
	}
	if w, err := ParseWeek([]byte(`[[],[[540,1020]],[[540,1020]],[[540,1020]],[[540,1020]],[[540,1020]],[]]`)); err != nil || w[1][0] != [2]int{540, 1020} {
		t.Errorf("weekdays: %v %v", w, err)
	}
}
