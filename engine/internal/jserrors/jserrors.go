// Package jserrors reads errors that websites send (docs/error-tracking.md):
// it groups the same bug into one issue whatever the browser or the build,
// spots errors that aren't the site's own (browser extensions, link
// scanners, copies of the site), and expands the short codes production
// builds of React and Redux throw into their full message.
//
// Everything here is a pure function of what the browser sent, so the
// same error always lands in the same issue.
package jserrors

import (
	"crypto/md5"
	"embed"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

//go:embed data/*.json
var data embed.FS

// The message tables: error code to message, with %s where an argument
// goes. Published by each project under the MIT licence (data/*.LICENSE).
var codes = map[string]map[string]string{}

func init() {
	for lib, file := range map[string]string{"react": "react.json", "redux": "redux.json", "redux-toolkit": "redux-toolkit.json"} {
		raw, err := data.ReadFile("data/" + file)
		if err != nil {
			panic(err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(raw, &m); err != nil {
			panic(err)
		}
		codes[lib] = m
	}
}

// minified finds a production build's short error: React's "Minified
// React error #418; visit https://react.dev/errors/418?args[]=..." (and the
// older error-decoder address), Redux's and Redux Toolkit's.
var minified = regexp.MustCompile(`Minified (React|Redux Toolkit|Redux) error #(\d+)`)

// Code is the library and code of a minified React or Redux error, or ""
// when the message isn't one.
func Code(message string) (lib, code string) {
	m := minified.FindStringSubmatch(message)
	if m == nil {
		return "", ""
	}
	return strings.ToLower(strings.ReplaceAll(m[1], " ", "-")), m[2]
}

// Expand is the full message of a minified React or Redux error, with the
// arguments the address carried put in; "" when the message isn't one or
// the code is unknown.
func Expand(message string) string {
	lib, code := Code(message)
	if lib == "" {
		return ""
	}
	text, ok := codes[lib][code]
	if !ok {
		return ""
	}
	var args []string
	if i := strings.Index(message, "?"); i >= 0 {
		q := message[i+1:]
		if j := strings.IndexAny(q, " \n"); j >= 0 {
			q = q[:j]
		}
		if v, err := url.ParseQuery(q); err == nil {
			args = v["args[]"]
		}
	}
	var b strings.Builder
	for n := 0; ; n++ {
		i := strings.Index(text, "%s")
		if i < 0 {
			b.WriteString(text)
			break
		}
		b.WriteString(text[:i])
		if n < len(args) {
			b.WriteString(args[n])
		} else {
			b.WriteString("…")
		}
		text = text[i+2:]
	}
	return b.String()
}

// chunkLoad is a script or style the page asked for that never arrived,
// which every browser words its own way. After a deploy it names every old
// file, so it is one issue, whatever the file.
var chunkLoad = regexp.MustCompile(`(?i)failed to fetch dynamically imported module|error loading dynamically imported module|importing a module script failed|loading (css )?chunk [^\s]+ failed|unable to preload css|failed to load module script`)

var (
	urlRe    = regexp.MustCompile(`(?:https?|file|webpack|blob)://[^\s'"()<>]+`)
	uuidRe   = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	hexRe    = regexp.MustCompile(`(?i)\b[0-9a-f]{8,}\b`)
	numRe    = regexp.MustCompile(`\d+`)
	frameRe  = regexp.MustCompile(`((?:[a-z][a-z0-9+.-]*)://[^\s()]+?|/[^\s():]+?):(\d+):(\d+)`)
	segSplit = regexp.MustCompile(`[-.~]`)
)

// Group is the issue a browser error belongs to: its type, its message
// with numbers, ids and addresses taken out, and the file it was thrown
// in, with the build's hash taken out of the file's name. The function
// name and the line are left out, since a new build changes both, and so
// is how the browser writes its stack. 16 lowercase hex characters.
func Group(typ, message, stack string) string {
	key := typ + "|"
	switch lib, code := Code(message); {
	case lib != "":
		// The same React or Redux error wherever it was thrown.
		key += lib + "#" + code
	case chunkLoad.MatchString(message):
		key = "chunk-load"
	default:
		key += Message(message) + "|" + TopFile(stack)
	}
	sum := md5.Sum([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// Message is an error message as it is grouped: addresses become their
// file's name without the build hash, and ids and numbers become 0.
func Message(m string) string {
	m = urlRe.ReplaceAllStringFunc(m, File)
	m = uuidRe.ReplaceAllString(m, "id")
	m = hexRe.ReplaceAllString(m, "0")
	m = numRe.ReplaceAllString(m, "0")
	return strings.TrimSpace(m)
}

// TopFile is the first file in a stack that isn't the browser's or an
// extension's, as File writes it; "" when the stack names none. Chrome
// (at f (https://x/app.js:1:2)), Firefox and Safari (f@https://x/app.js:1:2)
// read the same.
func TopFile(stack string) string {
	for _, line := range strings.Split(stack, "\n") {
		m := frameRe.FindStringSubmatch(line)
		if m == nil || foreign(m[1]) {
			continue
		}
		if f := File(m[1]); f != "" {
			return f
		}
	}
	return ""
}

// File is a script's address as it is grouped: its file name only, with
// any part of the name that looks like a build hash left out, so
// app-B6vn08tg.js and app-mZtfGkFb.js are both app.js.
func File(addr string) string {
	if u, err := url.Parse(addr); err == nil && u.Path != "" {
		addr = u.Path
	} else if i := strings.IndexAny(addr, "?#"); i >= 0 {
		addr = addr[:i]
	}
	base := path.Base(addr)
	if base == "." || base == "/" {
		return ""
	}
	ext := path.Ext(base)
	name := strings.TrimSuffix(base, ext)
	var keep []string
	for _, s := range segSplit.Split(name, -1) {
		if s != "" && !looksHashed(s) {
			keep = append(keep, s)
		}
	}
	return strings.Join(keep, "-") + ext
}

// looksHashed says whether part of a file name is a build hash: letters
// and digits mixed (B6vn08tg), mixed case without digits (mZtfGkFb), long
// hex, or a number.
func looksHashed(s string) bool {
	var digit, lower, upper bool
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		}
	}
	if _, err := strconv.Atoi(s); err == nil {
		return len(s) >= 3
	}
	switch {
	case len(s) >= 6 && digit && (lower || upper):
		return true
	case len(s) >= 8 && lower && upper:
		return true
	}
	return false
}

// foreign is an address that isn't the site's own code: a browser
// extension, Safari's hidden extension addresses, or the browser itself.
func foreign(addr string) bool {
	a := strings.ToLower(addr)
	for _, p := range []string{"chrome-extension:", "moz-extension:", "safari-extension:", "safari-web-extension:", "webkit-masked-url:", "chrome:", "about:", "edge:", "extension:"} {
		if strings.HasPrefix(a, p) {
			return true
		}
	}
	return false
}

// Noise reasons: errors that aren't the site's own bugs.
const (
	// Thrown in a browser extension's code.
	NoiseExtension = "extension"
	// Microsoft's link scanner (Outlook Safe Links) opening the page.
	NoiseLinkScanner = "link_scanner"
	// Code a wallet or the browser put on the page (window.ethereum,
	// __firefox__) and its own mistakes.
	NoiseInjected = "injected"
	// The page was saved and opened from a file.
	NoiseLocalFile = "local_file"
	// A copy of the site served from a bare IP address.
	NoiseIPHost = "ip_host"
)

// NoiseReasons are every reason, in the order they are checked.
var NoiseReasons = []string{NoiseExtension, NoiseLinkScanner, NoiseInjected, NoiseLocalFile, NoiseIPHost}

var (
	foreignAnywhere = regexp.MustCompile(`(?i)(chrome|moz|safari|safari-web)-extension://|webkit-masked-url://`)
	injected        = regexp.MustCompile(`\b(ethereum|__firefox__|__gCrWeb|__gchrome_uniqueid)\b`)
)

// Noise is why an error isn't the site's own bug, or "" when it may be:
// every frame of its stack is in a browser extension, it is the message
// Outlook's link scanner causes, it is about code a wallet or the browser
// injects, or the page was opened from a file or a bare IP address.
func Noise(message, stack, source, pageURL string) string {
	if foreignAnywhere.MatchString(source) || (stack != "" && onlyForeign(stack)) {
		return NoiseExtension
	}
	if strings.Contains(message, "Object Not Found Matching Id") {
		return NoiseLinkScanner
	}
	if injected.MatchString(message) {
		return NoiseInjected
	}
	if strings.HasPrefix(strings.ToLower(pageURL), "file:") {
		return NoiseLocalFile
	}
	if u, err := url.Parse(pageURL); err == nil && u.Hostname() != "" && net.ParseIP(u.Hostname()) != nil {
		return NoiseIPHost
	}
	return ""
}

// onlyForeign says whether a stack names files and all of them are an
// extension's or the browser's.
func onlyForeign(stack string) bool {
	seen := false
	for _, line := range strings.Split(stack, "\n") {
		m := frameRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if !foreign(m[1]) {
			return false
		}
		seen = true
	}
	return seen
}
