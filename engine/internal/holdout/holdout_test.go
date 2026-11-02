package holdout

import (
	"fmt"
	"testing"
)

func TestIn(t *testing.T) {
	if In("send", "p", 0) {
		t.Fatal("0% holds nobody out")
	}
	n := 0
	for i := range 10000 {
		p := fmt.Sprint(i)
		if In("send", p, 20) {
			n++
		}
		if In("send", p, 20) != In("send", p, 20) {
			t.Fatal("not stable")
		}
	}
	if n < 1800 || n > 2200 {
		t.Fatalf("20%% held out %d of 10000", n)
	}
	// Another send picks other people.
	same := 0
	for i := range 1000 {
		p := fmt.Sprint(i)
		if In("a", p, 50) == In("b", p, 50) {
			same++
		}
	}
	if same > 600 || same < 400 {
		t.Fatalf("sends agree on %d of 1000", same)
	}
}
