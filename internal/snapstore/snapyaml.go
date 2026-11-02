package snapstore

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// SnapYAML is what thothdock-snap needs from a snap's meta/snap.yaml.
type SnapYAML struct {
	Name, Version, Type, Base, Confinement string
	Environment                            map[string]string
	Apps                                   map[string]App
	// Plugs are the interface names the apps and hooks declare, for
	// snapctl is-connected.
	Plugs []string
}

// App is one entry of snap.yaml's apps.
type App struct {
	Command      string
	CommandChain []string
	Environment  map[string]string
	Daemon       string // simple, forking, oneshot, notify, ... or "" for a command
	Timer        string // snapd runs a daemon with a timer on that schedule, not continuously
	Plugs        []string
}

// ParseSnapYAML reads the YAML subset snapcraft writes: block maps by
// indentation, "- item" and [a, b] sequences, quoted and plain scalars, and
// | / > block scalars (kept as text). It is not a general YAML parser.
func ParseSnapYAML(data []byte) (*SnapYAML, error) {
	p := &yamlParser{}
	for _, raw := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		p.lines = append(p.lines, raw)
	}
	root, err := p.mapping(0)
	if err != nil {
		return nil, err
	}
	m, _ := root.(map[string]any)
	if m == nil {
		return nil, fmt.Errorf("snap.yaml: not a mapping")
	}
	y := &SnapYAML{
		Name: str(m["name"]), Version: str(m["version"]), Type: str(m["type"]), Base: str(m["base"]),
		Confinement: str(m["confinement"]), Environment: strMap(m["environment"]), Apps: map[string]App{},
	}
	if y.Type == "" {
		y.Type = "app"
	}
	if y.Confinement == "" {
		y.Confinement = "strict"
	}
	if apps, ok := m["apps"].(map[string]any); ok {
		for name, v := range apps {
			a, _ := v.(map[string]any)
			y.Apps[name] = App{Command: str(a["command"]), CommandChain: strList(a["command-chain"]),
				Environment: strMap(a["environment"]), Daemon: str(a["daemon"]), Timer: str(a["timer"]),
				Plugs: strList(a["plugs"])}
			y.Plugs = append(y.Plugs, strList(a["plugs"])...)
		}
	}
	if hooks, ok := m["hooks"].(map[string]any); ok {
		for _, v := range hooks {
			h, _ := v.(map[string]any)
			y.Plugs = append(y.Plugs, strList(h["plugs"])...)
		}
	}
	sort.Strings(y.Plugs)
	y.Plugs = slices.Compact(y.Plugs)
	if y.Name == "" {
		return nil, fmt.Errorf("snap.yaml: no name")
	}
	return y, nil
}

type yamlParser struct {
	lines []string
	pos   int
}

func indentOf(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

func blank(s string) bool {
	t := strings.TrimSpace(s)
	return t == "" || strings.HasPrefix(t, "#")
}

// next skips blank lines and returns the next line's indent, or -1 at the end.
func (p *yamlParser) next() int {
	for p.pos < len(p.lines) && blank(p.lines[p.pos]) {
		p.pos++
	}
	if p.pos >= len(p.lines) {
		return -1
	}
	return indentOf(p.lines[p.pos])
}

// mapping reads key: value lines at exactly indent.
func (p *yamlParser) mapping(indent int) (any, error) {
	out := map[string]any{}
	for {
		ind := p.next()
		if ind < indent || ind == -1 {
			return out, nil
		}
		if ind > indent {
			return nil, fmt.Errorf("snap.yaml line %d: unexpected indentation", p.pos+1)
		}
		line := strings.TrimSpace(p.lines[p.pos])
		if strings.HasPrefix(line, "- ") || line == "-" {
			return nil, fmt.Errorf("snap.yaml line %d: a list item where a key was expected", p.pos+1)
		}
		key, rest, ok := cutKey(line)
		if !ok {
			return nil, fmt.Errorf("snap.yaml line %d: expected key: value", p.pos+1)
		}
		p.pos++
		v, err := p.value(rest, indent)
		if err != nil {
			return nil, err
		}
		out[key] = v
	}
}

// value parses what follows "key:" (rest), reading nested lines if needed.
func (p *yamlParser) value(rest string, indent int) (any, error) {
	rest = stripComment(rest)
	switch {
	case rest == "|" || rest == ">" || strings.HasPrefix(rest, "|") || strings.HasPrefix(rest, ">"):
		var b []string
		for p.pos < len(p.lines) && (blank(p.lines[p.pos]) || indentOf(p.lines[p.pos]) > indent) {
			b = append(b, strings.TrimSpace(p.lines[p.pos]))
			p.pos++
		}
		return strings.Join(b, "\n"), nil
	case unterminatedQuote(rest):
		// A quoted scalar over several lines: read on to its closing quote,
		// folding line breaks as YAML does (a blank line is a newline).
		parts := []string{rest}
		for p.pos < len(p.lines) {
			l := strings.TrimSpace(p.lines[p.pos])
			p.pos++
			parts = append(parts, l)
			if !unterminatedQuote(strings.Join(parts, " ")) {
				break
			}
		}
		joined := strings.Join(parts, " ")
		if unterminatedQuote(joined) {
			return nil, fmt.Errorf("snap.yaml: unterminated quoted value")
		}
		return scalar(joined), nil
	case rest != "":
		// A plain scalar continues on more-indented lines (redis's long
		// command, mosquitto's description); YAML folds them with spaces.
		if !strings.HasPrefix(rest, "[") && !strings.HasPrefix(rest, "'") && !strings.HasPrefix(rest, `"`) {
			parts := []string{rest}
			for p.pos < len(p.lines) && !blank(p.lines[p.pos]) && indentOf(p.lines[p.pos]) > indent {
				l := stripComment(strings.TrimSpace(p.lines[p.pos]))
				if _, _, isKey := cutKey(l); isKey {
					// ": " cannot occur in a plain scalar: a key here is bad indentation.
					return nil, fmt.Errorf("snap.yaml line %d: unexpected indentation", p.pos+1)
				}
				parts = append(parts, l)
				p.pos++
			}
			rest = strings.Join(parts, " ")
		}
		return scalar(rest), nil
	}
	ind := p.next()
	if ind == -1 || ind < indent || (ind == indent && !strings.HasPrefix(strings.TrimSpace(p.lines[p.pos]), "-")) {
		return "", nil
	}
	if strings.HasPrefix(strings.TrimSpace(p.lines[p.pos]), "-") {
		return p.sequence(ind)
	}
	return p.mapping(ind)
}

// sequence reads "- item" lines at indent (scalars only: what snap.yaml uses).
func (p *yamlParser) sequence(indent int) (any, error) {
	var out []any
	for {
		ind := p.next()
		if ind != indent || !strings.HasPrefix(strings.TrimSpace(p.lines[p.pos]), "-") {
			return out, nil
		}
		item := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p.lines[p.pos]), "-"))
		p.pos++
		out = append(out, scalar(stripComment(item)))
	}
}

func cutKey(line string) (string, string, bool) {
	if strings.HasPrefix(line, `"`) || strings.HasPrefix(line, "'") {
		q := line[:1]
		end := strings.Index(line[1:], q)
		if end < 0 {
			return "", "", false
		}
		k, rest := line[1:1+end], line[2+end:]
		r, ok := strings.CutPrefix(rest, ":")
		return k, strings.TrimSpace(r), ok
	}
	i := strings.Index(line, ": ")
	if i < 0 {
		if strings.HasSuffix(line, ":") {
			return line[:len(line)-1], "", true
		}
		return "", "", false
	}
	return line[:i], strings.TrimSpace(line[i+2:]), true
}

func stripComment(s string) string {
	if strings.HasPrefix(s, `"`) || strings.HasPrefix(s, "'") {
		return s
	}
	if i := strings.Index(s, " #"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func scalar(s string) any {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		var out []any
		for _, e := range strings.Split(s[1:len(s)-1], ",") {
			if e = strings.TrimSpace(e); e != "" {
				out = append(out, scalar(e))
			}
		}
		return out
	}
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		r := strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\n`, "\n", `\t`, "\t")
		return r.Replace(s[1 : len(s)-1])
	}
	return s
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func strList(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, e := range l {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func strMap(v any) map[string]string {
	m, _ := v.(map[string]any)
	out := map[string]string{}
	for k, e := range m {
		if s, ok := e.(string); ok {
			out[k] = s
		}
	}
	return out
}

// unterminatedQuote reports a scalar that opens a quote it does not close.
func unterminatedQuote(s string) bool {
	if s == "" || (s[0] != '\'' && s[0] != '"') {
		return false
	}
	q := s[0]
	for i := 1; i < len(s); i++ {
		switch {
		case q == '"' && s[i] == '\\':
			i++
		case s[i] == q && q == '\'' && i+1 < len(s) && s[i+1] == '\'':
			i++
		case s[i] == q:
			return false
		}
	}
	return true
}
