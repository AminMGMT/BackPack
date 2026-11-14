package manage

import (
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
)

// Keeping a person's comments through an edit.
//
// A direct tunnel's file is rendered from a spec, so an Edit used to replace
// the whole file: the header, the notes somebody wrote above a key, the
// "# changed on purpose" at the end of a line were all gone. preserveComments
// instead patches the OLD text so that it says what the freshly rendered body
// says: a changed value is replaced in place, a removed key's line is dropped,
// a new key is added after the key that precedes it in the render, and
// everything else — comments, blank lines, spacing, the order a person left —
// stays as it was.
//
// It is line-based and deliberately small. It understands the shapes these
// files really have: [table] headers, `bare_key = value` lines with a string,
// number, bool or one-line array value, and trailing or full-line comments.
// Anything else (inline tables, multi-line strings or arrays, dotted or quoted
// keys, [[array tables]], a repeated table) makes it give up, and the caller
// writes the rendered body as before. It also gives up when the patched text
// does not decode to exactly what the rendered body decodes to, so a bug here
// can cost the comments but never the config.

type tomlLineKind int

const (
	lineBlank tomlLineKind = iota
	lineComment
	lineHeader
	lineKV
)

type tomlLine struct {
	kind     tomlLineKind
	text     string
	table    string // the table this line is in; "" before the first header
	key      string // lineKV only
	valStart int    // lineKV: offsets of the value inside text
	valEnd   int
}

// scanTOML classifies every line, or reports false for a file it will not edit
// in place.
func scanTOML(text string) ([]tomlLine, bool) {
	raw := strings.Split(text, "\n")
	out := make([]tomlLine, 0, len(raw))
	table := ""
	for _, line := range raw {
		l := tomlLine{text: line, table: table}
		t := strings.TrimSpace(line)
		switch {
		case t == "":
			l.kind = lineBlank
		case strings.HasPrefix(t, "#"):
			l.kind = lineComment
		case strings.HasPrefix(t, "[["):
			return nil, false
		case strings.HasPrefix(t, "["):
			end := strings.Index(t, "]")
			if end < 0 {
				return nil, false
			}
			if rest := strings.TrimSpace(t[end+1:]); rest != "" && !strings.HasPrefix(rest, "#") {
				return nil, false
			}
			inner := t[1:end]
			if strings.ContainsAny(inner, `"'`) {
				return nil, false
			}
			parts := strings.Split(inner, ".")
			for i := range parts {
				parts[i] = strings.TrimSpace(parts[i])
				if parts[i] == "" {
					return nil, false
				}
			}
			table = strings.Join(parts, ".")
			l.kind, l.table = lineHeader, table
		default:
			if !scanKV(&l) {
				return nil, false
			}
		}
		out = append(out, l)
	}
	return out, true
}

func bareKeyByte(c byte) bool {
	return c == '_' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// scanKV reads `key = value [# comment]` into l.
func scanKV(l *tomlLine) bool {
	s := l.text
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	k := i
	for i < len(s) && bareKeyByte(s[i]) {
		i++
	}
	if i == k {
		return false
	}
	l.key = s[k:i]
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	if i >= len(s) || s[i] != '=' {
		return false
	}
	i++
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	if i >= len(s) {
		return false
	}
	l.valStart = i
	end, ok := scanValue(s, i)
	if !ok {
		return false
	}
	l.valEnd = end
	if rest := strings.TrimSpace(s[end:]); rest != "" && !strings.HasPrefix(rest, "#") {
		return false
	}
	l.kind = lineKV
	return true
}

// scanValue returns where the value that starts at s[i] ends, for the value
// shapes the editor handles.
func scanValue(s string, i int) (int, bool) {
	switch {
	case strings.HasPrefix(s[i:], `"""`), strings.HasPrefix(s[i:], `'''`), s[i] == '{':
		return 0, false
	case s[i] == '"' || s[i] == '\'':
		return scanString(s, i)
	case s[i] == '[':
		depth := 0
		for j := i; j < len(s); j++ {
			switch s[j] {
			case '"', '\'':
				end, ok := scanString(s, j)
				if !ok {
					return 0, false
				}
				j = end - 1
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					return j + 1, true
				}
			case '{', '#':
				return 0, false
			}
		}
		return 0, false // continues on the next line
	}
	j := i
	for j < len(s) && s[j] != ' ' && s[j] != '\t' && s[j] != '#' && s[j] != '\r' {
		j++
	}
	return j, j > i
}

// scanString returns the end of the one-line string whose opening quote is at
// s[i]. A '#' inside it is part of the string, which is the point.
func scanString(s string, i int) (int, bool) {
	q := s[i]
	for j := i + 1; j < len(s); j++ {
		switch {
		case q == '"' && s[j] == '\\':
			j++
		case s[j] == q:
			return j + 1, true
		}
	}
	return 0, false
}

// inStyleOf writes a new key's line the way the old file writes its own: the
// same indent and the "=" in the same column, so a hand-written file does not
// gain one line in the renderer's padded style. old is the line to copy from,
// or negative when there is none.
func inStyleOf(lines []tomlLine, old int, n tomlLine) string {
	value := strings.TrimSpace(n.text[n.valStart:])
	if old < 0 {
		return strings.TrimSpace(n.text)
	}
	o := lines[old]
	eq := strings.Index(o.text, "=")
	indent := o.text[:len(o.text)-len(strings.TrimLeft(o.text, " \t"))]
	head := indent + n.key
	pad := " "
	// Aligned only where the old line is itself padded; a plain `key = value`
	// file stays plain.
	if eq-len(indent+o.key) > 1 && eq > len(head) {
		pad = strings.Repeat(" ", eq-len(head))
	}
	return head + pad + o.text[eq:o.valStart] + value
}

// dropNoKeyNote marks for removal the note a GRE file carries where a key is
// left out ("# No mtu: it is worked out at start ..."), once an edit gives that
// key a value and the note would say the opposite.
func dropNoKeyNote(lines []tomlLine, key string, drop map[int]bool) {
	for i, l := range lines {
		if l.kind != lineComment || !strings.HasPrefix(strings.TrimSpace(l.text), "# No "+key+":") {
			continue
		}
		for j := i; j < len(lines) && lines[j].kind == lineComment; j++ {
			drop[j] = true
		}
	}
}

// sameTOMLValue reports whether two value texts decode to the same thing, so a
// value the edit did not change is left in the spelling the person gave it.
func sameTOMLValue(a, b string) bool {
	var x, y map[string]any
	if _, err := toml.Decode("v = "+a, &x); err != nil {
		return false
	}
	if _, err := toml.Decode("v = "+b, &y); err != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// preserveComments returns old patched to say what rendered says, keeping old's
// comments; ok is false when that is not safe and rendered should be written.
func preserveComments(old, rendered string) (string, bool) {
	oldLines, ok := scanTOML(old)
	if !ok {
		return "", false
	}
	newLines, ok := scanTOML(rendered)
	if !ok {
		return "", false
	}

	// Where each old table and key sits.
	type oldTable struct {
		header int // -1 for the top level
		keys   map[string]int
		last   int // the last key line, -1 when there is none
	}
	tables := map[string]*oldTable{"": {header: -1, keys: map[string]int{}, last: -1}}
	firstHeader := -1
	for i, l := range oldLines {
		switch l.kind {
		case lineHeader:
			if _, dup := tables[l.table]; dup {
				return "", false
			}
			tables[l.table] = &oldTable{header: i, keys: map[string]int{}, last: -1}
			if firstHeader < 0 {
				firstHeader = i
			}
		case lineKV:
			t := tables[l.table]
			if _, dup := t.keys[l.key]; dup {
				return "", false
			}
			t.keys[l.key] = i
			t.last = i
		}
	}

	// The new body, table by table, in its own order.
	type newTable struct {
		name string
		kvs  []int // indexes into newLines
	}
	var order []*newTable
	byName := map[string]*newTable{}
	get := func(name string) *newTable {
		if t := byName[name]; t != nil {
			return t
		}
		t := &newTable{name: name}
		byName[name] = t
		order = append(order, t)
		return t
	}
	get("")
	for i, l := range newLines {
		switch l.kind {
		case lineHeader:
			if byName[l.table] != nil {
				return "", false // a table named twice
			}
			get(l.table)
		case lineKV:
			t := get(l.table)
			t.kvs = append(t.kvs, i)
		}
	}
	for name := range tables {
		if _, kept := byName[name]; !kept {
			return "", false
		}
	}

	replace := map[int]string{}  // old line -> its new value text
	keep := map[int]bool{}       // old key lines that survive
	after := map[int][]string{}  // lines to add after an old line
	before := map[int][]string{} // lines to add before an old line
	drop := map[int]bool{}       // comment lines the edit makes false
	var appended []string        // whole tables the old file has no header for
	oldComments := map[string]bool{}
	for _, l := range oldLines {
		if l.kind == lineComment {
			oldComments[strings.TrimSpace(l.text)] = true
		}
	}

	// The comment block the renderer put directly above new line j, unless the
	// old file already says it.
	blockAbove := func(j int) []string {
		var block []string
		for k := j - 1; k >= 0 && newLines[k].kind == lineComment; k-- {
			block = append([]string{strings.TrimSpace(newLines[k].text)}, block...)
		}
		if len(block) == 0 || oldComments[block[0]] {
			return nil
		}
		return append([]string{""}, block...)
	}

	for _, nt := range order {
		ot, exists := tables[nt.name]
		if !exists {
			appended = append(appended, "", "["+nt.name+"]")
			for _, j := range nt.kvs {
				appended = append(appended, strings.TrimSpace(newLines[j].text))
			}
			continue
		}
		anchor := -2
		for _, j := range nt.kvs {
			n := newLines[j]
			if idx, found := ot.keys[n.key]; found {
				o := oldLines[idx]
				newVal := n.text[n.valStart:n.valEnd]
				if !sameTOMLValue(o.text[o.valStart:o.valEnd], newVal) {
					replace[idx] = newVal
				}
				keep[idx] = true
				anchor = idx
				continue
			}
			dropNoKeyNote(oldLines, n.key, drop)
			style := anchor
			if style < 0 {
				style = ot.last
			}
			add := append(blockAbove(j), inStyleOf(oldLines, style, n))
			switch {
			case anchor >= 0:
				after[anchor] = append(after[anchor], add...)
			case ot.header >= 0:
				after[ot.header] = append(after[ot.header], add...)
			case ot.last >= 0:
				after[ot.last] = append(after[ot.last], add...)
			case firstHeader >= 0:
				// Top-level keys must precede the first table; keep clear of a
				// comment that belongs to it.
				at := firstHeader
				for at > 0 && oldLines[at-1].kind == lineComment {
					at--
				}
				before[at] = append(before[at], add...)
			default:
				appended = append(appended, add...)
			}
		}
	}

	var out []string
	for i, l := range oldLines {
		out = append(out, before[i]...)
		if drop[i] {
			out = append(out, after[i]...)
			continue
		}
		switch {
		case l.kind != lineKV, keep[i]:
			if v, changed := replace[i]; changed {
				l.text = l.text[:l.valStart] + v + l.text[l.valEnd:]
			}
			out = append(out, l.text)
		}
		out = append(out, after[i]...)
	}
	result := strings.Join(out, "\n")
	if len(appended) > 0 {
		result = strings.TrimRight(result, "\n") + "\n" + strings.Join(appended, "\n") + "\n"
	}

	// The proof: it has to be the same config the render describes.
	var got, want map[string]any
	if _, err := toml.Decode(result, &got); err != nil {
		return "", false
	}
	if _, err := toml.Decode(rendered, &want); err != nil {
		return "", false
	}
	if !reflect.DeepEqual(got, want) {
		return "", false
	}
	return result, true
}

// carryOver is what an edit writes: the rendered body, with the keys of the old
// file the renderer has no field for put back (withForeignKeys), written into
// the old text so its comments survive (preserveComments). Panel and menu
// both finish through it, so the same edit gives the same file from either.
func carryOver(rendered, raw string, md toml.MetaData) (string, error) {
	body, err := withForeignKeys(rendered, raw, md)
	if err != nil {
		return "", err
	}
	if merged, ok := preserveComments(raw, body); ok {
		return merged, nil
	}
	return body, nil
}
