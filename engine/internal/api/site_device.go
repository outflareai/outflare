package api

import (
	"regexp"
	"strings"
)

// deviceDetail is what a user agent says beyond parseUA's families: the
// device's maker and model, and the system's and browser's versions.
// Browsers now freeze much of this (Chrome reports Android 10 and model "K",
// every browser on a Mac reports macOS 10.15.7), so a field is left empty
// rather than guessed, and versions are what the browser says.
type deviceDetail struct {
	brand, model, osVersion, browserVersion string
}

var (
	iosVersionRe     = regexp.MustCompile(`(?:iPhone|CPU) OS (\d+)(?:_(\d+))?`)
	androidVersionRe = regexp.MustCompile(`Android (\d+(?:\.\d+)?)`)
	windowsVersionRe = regexp.MustCompile(`Windows NT (\d+\.\d+)`)
	macVersionRe     = regexp.MustCompile(`Mac OS X (\d+)[_.](\d+)(?:[_.](\d+))?`)
	// The model is the last part of the Android section: "Linux; Android
	// 14; SM-S918B)" or "... Pixel 8 Build/AP2A)".
	androidModelRe = regexp.MustCompile(`Android [^;)]*;(?:\s*[a-z]{2}[-_][A-Za-z]{2};)?\s*([^;)]+?)(?:\s+Build/[^;)]*)?(?:;\s*wv)?\)`)
	browserMajorRe = map[string]*regexp.Regexp{
		"edge":    regexp.MustCompile(`Edg(?:A|iOS)?/(\d+)`),
		"opera":   regexp.MustCompile(`(?:OPR|Opera)[/ ](\d+)`),
		"samsung": regexp.MustCompile(`SamsungBrowser/(\d+)`),
		"firefox": regexp.MustCompile(`(?:Firefox|FxiOS)/(\d+)`),
		"chrome":  regexp.MustCompile(`(?:CriOS|Chrome)/(\d+)`),
		"safari":  regexp.MustCompile(`Version/(\d+)`),
	}
)

// windowsVersions names the Windows NT versions people know. Windows 11
// still says NT 10.0, so 10 covers both.
var windowsVersions = map[string]string{"10.0": "10", "6.3": "8.1", "6.2": "8", "6.1": "7"}

// modelBrands finds the maker from how its models are named, first match
// wins. Unknown models keep their model and get no brand.
var modelBrands = []struct {
	re    *regexp.Regexp
	brand string
}{
	{regexp.MustCompile(`^(?:SM-|SAMSUNG|Galaxy|GT-|SC-|SCV)`), "Samsung"},
	{regexp.MustCompile(`^Pixel`), "Google"},
	{regexp.MustCompile(`^(?:Redmi|POCO|Mi |MI |Xiaomi|M2\d{3}|2\d{6,}[A-Z]{1,3}$)`), "Xiaomi"},
	{regexp.MustCompile(`(?i)^(?:HUAWEI|[A-Z]{3}-(?:L|AL|TL|LX)\d)`), "Huawei"},
	{regexp.MustCompile(`(?i)^honor`), "Honor"},
	{regexp.MustCompile(`^(?:ONEPLUS|OnePlus|IN20|KB20|LE21|NE22|CPH25)`), "OnePlus"},
	{regexp.MustCompile(`^(?:CPH|OPPO|PD)`), "OPPO"},
	{regexp.MustCompile(`^RMX`), "realme"},
	{regexp.MustCompile(`(?i)^(?:vivo|V2\d{3})`), "vivo"},
	{regexp.MustCompile(`(?i)^(?:moto|Motorola|XT\d{4})`), "Motorola"},
	{regexp.MustCompile(`(?i)^nokia`), "Nokia"},
	{regexp.MustCompile(`^(?:LM-|LG-)`), "LG"},
	{regexp.MustCompile(`^(?:SO-|XQ-|Xperia)`), "Sony"},
	{regexp.MustCompile(`(?i)^(?:TECNO|Infinix|itel)`), "Transsion"},
}

// parseDevice reads ua's device details; os and browser are parseUA's.
func parseDevice(ua, os, browser string) deviceDetail {
	var d deviceDetail
	switch os {
	case "ios":
		d.brand = "Apple"
		switch {
		case strings.Contains(ua, "iPad"):
			d.model = "iPad"
		case strings.Contains(ua, "iPod"):
			d.model = "iPod"
		default:
			d.model = "iPhone"
		}
		if m := iosVersionRe.FindStringSubmatch(ua); m != nil {
			d.osVersion = m[1]
			if m[2] != "" {
				d.osVersion += "." + m[2]
			}
		}
	case "macos":
		d.brand, d.model = "Apple", "Mac"
		if m := macVersionRe.FindStringSubmatch(ua); m != nil {
			d.osVersion = m[1] + "." + m[2]
			if m[3] != "" {
				d.osVersion += "." + m[3]
			}
		}
	case "android":
		if m := androidVersionRe.FindStringSubmatch(ua); m != nil {
			d.osVersion = m[1]
		}
		if m := androidModelRe.FindStringSubmatch(ua); m != nil {
			model := strings.TrimSpace(m[1])
			// "K" is Chrome's frozen model; "Mobile" and "Tablet" aren't
			// models.
			if model != "K" && model != "Mobile" && model != "Tablet" && !strings.HasPrefix(model, "Android") {
				d.model = cutUTF8(model, 60)
				for _, b := range modelBrands {
					if b.re.MatchString(model) {
						d.brand = b.brand
						break
					}
				}
			}
		}
	case "windows":
		if m := windowsVersionRe.FindStringSubmatch(ua); m != nil {
			d.osVersion = windowsVersions[m[1]]
		}
	}
	if re := browserMajorRe[browser]; re != nil {
		if m := re.FindStringSubmatch(ua); m != nil {
			d.browserVersion = m[1]
		}
	}
	return d
}

var visitLanguageRe = regexp.MustCompile(`^[a-zA-Z]{2,3}(?:[-_]|$)`)

// visitLanguage is the language part of a locale or an Accept-Language header,
// lowercase ("en" from "en-US" or "en-GB,en;q=0.9"), or "".
func visitLanguage(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, ",;"); i >= 0 {
		s = s[:i]
	}
	if !visitLanguageRe.MatchString(s) {
		return ""
	}
	if i := strings.IndexAny(s, "-_"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(s)
}
