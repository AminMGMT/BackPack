package webui

import (
	"bytes"
	"regexp"
	"strings"
)

// Shrinking the panel's files as they are served.
//
// The sources are written to be read: most of their length is the comments
// that say why a rule or a branch is the way it is, and those comments are the
// documentation of this front end, so they stay in the files. What the browser
// gets has them taken out, once, when a file is first prepared (assetFor),
// before it is gzipped: about a quarter of the CSS and a third of the
// JavaScript, compressed, that every visit used to download over a route from
// Iran for nothing.
//
// This removes comments and collapses whitespace and nothing else. It does not
// rename, reorder or rewrite code, so what runs is what is in the source.
// Vendored files are left alone: they are shipped as their authors built them.

// minifyAsset returns the served form of a panel file.
func minifyAsset(name string, raw []byte) []byte {
	if strings.Contains(name, "/vendor/") {
		return raw
	}
	switch {
	case strings.HasSuffix(name, ".js"):
		return minifyJS(raw)
	case strings.HasSuffix(name, ".css"):
		return minifyCSS(raw)
	case strings.HasSuffix(name, ".html"):
		return minifyHTML(raw)
	}
	return raw
}

// minifyJS strips comments and folds whitespace outside strings, template
// literals and regular expressions. A comment that spanned lines leaves a
// newline behind, and so does any run of whitespace that held one, so
// automatic semicolon insertion sees the same line breaks it saw before.
func minifyJS(src []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(src))
	n := len(src)
	// prev is the last significant byte written outside a literal, and word
	// the identifier or keyword that ended there, if one did: together they
	// decide whether a '/' starts a regular expression or divides.
	var prev byte
	word := ""
	pendingSpace, pendingNL := false, false
	// Template literals nest through ${ }: each open one records the brace
	// depth its expression started at.
	var tmpl []int
	depth := 0

	flush := func() {
		if out.Len() == 0 {
			pendingSpace, pendingNL = false, false
			return
		}
		if pendingNL {
			out.WriteByte('\n')
		} else if pendingSpace {
			out.WriteByte(' ')
		}
		pendingSpace, pendingNL = false, false
	}
	regexAllowed := func() bool {
		if prev == 0 {
			return true
		}
		if word != "" {
			switch word {
			case "return", "typeof", "case", "do", "else", "in", "of", "new", "delete",
				"void", "throw", "instanceof", "yield", "await":
				return true
			}
			return false
		}
		return strings.IndexByte("(,=:[!&|?{};+-*%~^<>", prev) >= 0
	}
	// copyTemplate copies template text from i (just after a ` or a closing }
	// of an expression) to the next ${ or the closing `, and reports where it
	// stopped and whether the template ended.
	copyTemplate := func(i int) (int, bool) {
		for i < n {
			c := src[i]
			switch {
			case c == '\\':
				out.WriteByte(c)
				if i+1 < n {
					out.WriteByte(src[i+1])
				}
				i += 2
				continue
			case c == '`':
				out.WriteByte(c)
				return i + 1, true
			case c == '$' && i+1 < n && src[i+1] == '{':
				out.WriteString("${")
				return i + 2, false
			}
			out.WriteByte(c)
			i++
		}
		return i, true
	}

	i := 0
	for i < n {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			pendingSpace = true
			i++
		case c == '\n':
			pendingNL = true
			i++
		case c == '/' && i+1 < n && src[i+1] == '/':
			for i < n && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && src[i+1] == '*':
			end := bytes.Index(src[i+2:], []byte("*/"))
			body := src[i:]
			if end >= 0 {
				body = src[i : i+2+end+2]
			}
			if bytes.IndexByte(body, '\n') >= 0 {
				pendingNL = true
			} else {
				pendingSpace = true
			}
			i += len(body)
		case c == '"' || c == '\'':
			flush()
			j := i + 1
			for j < n && src[j] != c && src[j] != '\n' {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			if j < n {
				j++
			}
			out.Write(src[i:j])
			i, prev, word = j, c, ""
		case c == '`':
			flush()
			out.WriteByte('`')
			j, done := copyTemplate(i + 1)
			if !done {
				tmpl = append(tmpl, depth)
				depth++
			}
			i, prev, word = j, '`', ""
		case c == '/' && regexAllowed():
			flush()
			j, inClass := i+1, false
			for j < n && src[j] != '\n' {
				ch := src[j]
				if ch == '\\' {
					j += 2
					continue
				}
				if ch == '[' {
					inClass = true
				} else if ch == ']' {
					inClass = false
				} else if ch == '/' && !inClass {
					break
				}
				j++
			}
			j++ // the closing slash
			for j < n && (src[j] >= 'a' && src[j] <= 'z') {
				j++ // flags
			}
			if j > n {
				j = n
			}
			out.Write(src[i:j])
			i, prev, word = j, '/', ""
		default:
			// A space is kept only where it separates two things that would
			// otherwise join into one token.
			if pendingSpace && !pendingNL && out.Len() > 0 && !(isIdent(prev) && isIdent(c)) &&
				!((prev == '+' || prev == '-') && (c == '+' || c == '-')) {
				pendingSpace = false
			}
			flush()
			if c == '{' {
				depth++
			} else if c == '}' {
				depth--
				if len(tmpl) > 0 && depth == tmpl[len(tmpl)-1] {
					// The end of a ${ } expression: back into the template.
					tmpl = tmpl[:len(tmpl)-1]
					out.WriteByte('}')
					j, done := copyTemplate(i + 1)
					if !done {
						tmpl = append(tmpl, depth)
						depth++
					}
					i, prev, word = j, '`', ""
					continue
				}
			}
			if isIdent(c) {
				j := i
				for j < n && isIdent(src[j]) {
					j++
				}
				out.Write(src[i:j])
				word = string(src[i:j])
				if c >= '0' && c <= '9' {
					word = "" // a number divides like a name, but is not a keyword
				}
				prev = src[j-1]
				i = j
				continue
			}
			out.WriteByte(c)
			prev, word = c, ""
			if c == ')' || c == ']' {
				word = ")" // a value: what follows a closing bracket divides
			}
			i++
		}
	}
	return out.Bytes()
}

func isIdent(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}

var (
	cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	cssSpace   = regexp.MustCompile(`\s+`)
	cssPunct   = regexp.MustCompile(`\s*([{};])\s*`)
	htmlNote   = regexp.MustCompile(`(?s)<!--.*?-->`)
	blankLines = regexp.MustCompile(`\n[ \t]*\n+`)
)

// minifyCSS strips comments and folds whitespace. Spaces are only dropped
// around braces and semicolons; anywhere else one stays, since a space in a
// selector is the descendant combinator. No stylesheet here puts "/*" inside a
// string.
func minifyCSS(src []byte) []byte {
	s := cssComment.ReplaceAll(src, nil)
	s = cssSpace.ReplaceAll(s, []byte(" "))
	s = cssPunct.ReplaceAll(s, []byte("$1"))
	return bytes.TrimSpace(s)
}

// minifyHTML strips comments and empty lines. Whitespace between elements is
// left as it is: between inline elements it is a space on the page.
func minifyHTML(src []byte) []byte {
	s := htmlNote.ReplaceAll(src, nil)
	return blankLines.ReplaceAll(s, []byte("\n"))
}
