package brands

import "testing"

func TestAddress(t *testing.T) {
	a := Address{Line1: " Level 2, 10 Example Street ", City: "Sydney", Region: "NSW", PostalCode: "2000", Country: "au"}
	a.Clean()
	if err := a.Check(); err != nil {
		t.Fatal(err)
	}
	if got := a.String(); got != "Level 2, 10 Example Street, Sydney NSW 2000, Australia" {
		t.Fatalf("one line = %q", got)
	}
	b := Business{Address: a}
	if got := b.Footer("Northpeak"); got != "Northpeak, Level 2, 10 Example Street, Sydney NSW 2000, Australia" {
		t.Fatalf("footer without a legal name = %q", got)
	}
	b.LegalName = "Northpeak Example Pty Ltd"
	if got := b.Footer("Northpeak"); got != "Northpeak Example Pty Ltd, Level 2, 10 Example Street, Sydney NSW 2000, Australia" {
		t.Fatalf("footer = %q", got)
	}
	sg := Address{Line1: "1 Example Road", City: "Singapore", PostalCode: "000000", Country: "SG"}
	if got := sg.String(); got != "1 Example Road, Singapore 000000" {
		t.Fatalf("city-state = %q", got)
	}
	if (Business{LegalName: "X"}).Footer("Y") != "" || (Address{}).String() != "" || (Address{}).Check() != nil {
		t.Fatal("an empty address shows nothing and clears")
	}
	for _, bad := range []Address{
		{Line1: "1 Example St", City: "Sydney"},
		{Line1: "1 Example St", City: "Sydney", Country: "ZZ"},
		{City: "Sydney", Country: "AU"},
		{Line1: string(make([]byte, MaxAddress+1)), City: "Sydney", Country: "AU"},
	} {
		if bad.Check() == nil {
			t.Errorf("%+v passed", bad)
		}
	}
}
