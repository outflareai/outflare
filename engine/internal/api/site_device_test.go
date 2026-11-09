package api

import "testing"

func TestParseDevice(t *testing.T) {
	cases := []struct {
		name, ua string
		want     deviceDetail
	}{
		{"iPhone Safari",
			"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
			deviceDetail{"Apple", "iPhone", "17.5", "17"}},
		{"iPad Chrome",
			"Mozilla/5.0 (iPad; CPU OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/131.0.6778.73 Mobile/15E148 Safari/604.1",
			deviceDetail{"Apple", "iPad", "18.1", "131"}},
		{"Samsung with its own browser",
			"Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/25.0 Chrome/121.0.0.0 Mobile Safari/537.36",
			deviceDetail{"Samsung", "SM-S918B", "14", "25"}},
		{"Pixel with Build",
			"Mozilla/5.0 (Linux; Android 13; Pixel 7 Build/TQ3A.230805.001) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/118.0.0.0 Mobile Safari/537.36",
			deviceDetail{"Google", "Pixel 7", "13", "118"}},
		{"Chrome's frozen Android model",
			"Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36",
			deviceDetail{"", "", "10", "131"}},
		{"Xiaomi in an app's web view",
			"Mozilla/5.0 (Linux; Android 12; Redmi Note 11 Build/SKQ1.211103.001; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/120.0.6099.230 Mobile Safari/537.36",
			deviceDetail{"Xiaomi", "Redmi Note 11", "12", "120"}},
		{"An unknown maker keeps its model",
			"Mozilla/5.0 (Linux; Android 11; ZX-Phone 3) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/119.0.0.0 Mobile Safari/537.36",
			deviceDetail{"", "ZX-Phone 3", "11", "119"}},
		{"Windows Edge",
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.2903.70",
			deviceDetail{"", "", "10", "131"}},
		{"Windows 7 Firefox",
			"Mozilla/5.0 (Windows NT 6.1; Win64; x64; rv:115.0) Gecko/20100101 Firefox/115.0",
			deviceDetail{"", "", "7", "115"}},
		{"Mac Safari",
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15",
			deviceDetail{"Apple", "Mac", "10.15.7", "18"}},
		{"Linux Firefox",
			"Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0",
			deviceDetail{"", "", "", "132"}},
		{"Nothing", "", deviceDetail{}},
	}
	for _, c := range cases {
		_, browser, os := parseUA(c.ua)
		if got := parseDevice(c.ua, os, browser); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestVisitLanguage(t *testing.T) {
	for in, want := range map[string]string{
		"en-US":                   "en",
		"pt_BR":                   "pt",
		"de":                      "de",
		"FR-ca,fr;q=0.9,en;q=0.8": "fr",
		"fil":                     "fil",
		"":                        "",
		"*":                       "",
		"x-pig-latin":             "",
		"english":                 "",
		"zh-Hant-TW, zh;q=0.9":    "zh",
		"  es-419 ":               "es",
	} {
		if got := visitLanguage(in); got != want {
			t.Errorf("visitLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}
