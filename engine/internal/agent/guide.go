package agent

import (
	"bufio"
	"bytes"
	"strings"
)

// Guide lists the workspace operations in an OpenAPI document, one line
// each, as "METHOD /path: summary", with paths relative to the workspace.
// It reads the document's layout (paths at two spaces, methods at four,
// summary at six) rather than parsing YAML, which is all it needs.
func Guide(spec []byte) string {
	const prefix = "/v1/workspaces/{ws}"
	var b strings.Builder
	var path, method string
	sc := bufio.NewScanner(bytes.NewReader(spec))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	inPaths := false
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && len(line) > 0 && line[0] != ' ':
			inPaths = false
		case !inPaths:
		case strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":"):
			path, method = strings.TrimSuffix(strings.TrimSpace(line), ":"), ""
		case strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     "):
			m := strings.TrimSuffix(strings.TrimSpace(line), ":")
			switch m {
			case "get", "post", "patch", "put", "delete":
				method = strings.ToUpper(m)
			default:
				method = ""
			}
		case method != "" && strings.HasPrefix(line, "      summary:"):
			if p, ok := strings.CutPrefix(path, prefix); ok && p != "" {
				b.WriteString(method + " " + p + ": " + strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "      summary:")), `"`) + "\n")
			}
			method = ""
		}
	}
	return b.String()
}

// Docs returns the OpenAPI text for one workspace path (all its methods)
// and the schemas it references, so the agent can see request bodies.
func Docs(spec []byte, path string) string {
	full := "/v1/workspaces/{ws}" + path
	lines := strings.Split(string(spec), "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		if lines[i] == "  "+full+":" {
			out = append(out, lines[i])
			for j := i + 1; j < len(lines) && (strings.HasPrefix(lines[j], "   ") || lines[j] == ""); j++ {
				out = append(out, lines[j])
			}
			break
		}
	}
	if len(out) == 0 {
		return ""
	}
	// Add the schemas it names, one level deep.
	seen := map[string]bool{}
	text := strings.Join(out, "\n")
	for _, part := range strings.Split(text, "#/components/schemas/")[1:] {
		name := part
		if k := strings.IndexAny(name, "\" }\n,"); k >= 0 {
			name = name[:k]
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		for i := 0; i < len(lines); i++ {
			if lines[i] == "    "+name+":" {
				out = append(out, "", lines[i])
				for j := i + 1; j < len(lines) && strings.HasPrefix(lines[j], "      "); j++ {
					out = append(out, lines[j])
				}
				break
			}
		}
	}
	return strings.Join(out, "\n")
}
