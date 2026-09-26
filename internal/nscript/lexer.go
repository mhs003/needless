package nscript

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Lexer turns .nsc source into tokens. It is not safe for concurrent use.
//
// Two productions are deliberately opaque and are handed through byte-for-byte
// (AGENTS.md §2, ARCHITECTURE.md D2):
//
//   - triple-quoted strings, """ ... """
//   - external script blocks, <<( ... )<<
//
// Neither is unescaped, re-indented, or trimmed: the block body is source for
// another interpreter, and any normalisation would corrupt it.
type Lexer struct {
	src  []byte
	pos  int
	line int
	col  int

	// wantInterpreter is set right after the `exec` keyword. The interpreter
	// that follows may be a bare name or a path, and a path contains '/' which
	// is otherwise the division operator. Rather than guess from the shape of
	// the text, the lexer uses the preceding keyword as context.
	wantInterpreter bool
}

func NewLexer(src []byte) *Lexer {
	return &Lexer{src: src, line: 1, col: 1}
}

// Lex lexes src in full. It returns every token including the trailing EOF.
func Lex(src []byte) ([]Token, error) {
	l := NewLexer(src)
	var toks []Token
	for {
		t := l.Next()
		if t.Kind == Illegal {
			return nil, errorf(t.Pos, "%s", t.Text)
		}
		toks = append(toks, t)
		if t.Kind == EOF {
			return toks, nil
		}
	}
}

// Next returns the next token. It returns an Illegal token rather than an
// error so callers that want to resynchronise can; Lex converts it to *Error.
func (l *Lexer) Next() Token {
	l.skipSpaceAndComments()

	start := l.mark()
	if l.pos >= len(l.src) {
		return Token{Kind: EOF, Pos: start}
	}

	if l.src[l.pos] == '\n' {
		l.wantInterpreter = false
		l.advance()
		return Token{Kind: Newline, Text: "\n", Pos: start}
	}

	if l.wantInterpreter {
		return l.lexInterpreter(start)
	}

	switch c := l.src[l.pos]; {
	case c == '"':
		return l.lexQuoted(start)
	case c == '<' && l.peekAt(1) == '<' && l.peekAt(2) == '(':
		return l.lexRawBlock(start)
	case isDigit(c):
		return l.lexNumber(start)
	case isIdentStart(c):
		return l.lexIdent(start)
	}
	return l.lexOperator(start)
}

func (l *Lexer) mark() Pos {
	return Pos{Line: l.line, Column: l.col, Offset: l.pos}
}

func (l *Lexer) peekAt(n int) byte {
	if l.pos+n >= len(l.src) {
		return 0
	}
	return l.src[l.pos+n]
}

// advance consumes one rune and maintains the line/column counters.
func (l *Lexer) advance() rune {
	if l.pos >= len(l.src) {
		return 0
	}
	r, size := utf8.DecodeRune(l.src[l.pos:])
	l.pos += size
	if r == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return r
}

// skipSpaceAndComments consumes spaces, tabs, carriage returns, and // comments.
// Newlines are left alone: they are statement separators and are significant.
func (l *Lexer) skipSpaceAndComments() {
	for l.pos < len(l.src) {
		switch c := l.src[l.pos]; {
		case c == ' ' || c == '\t' || c == '\r':
			l.advance()
		case c == '/' && l.peekAt(1) == '/':
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.advance()
			}
		default:
			return
		}
	}
}

func (l *Lexer) lexIdent(start Pos) Token {
	begin := l.pos
	for l.pos < len(l.src) && isIdentPart(l.src[l.pos]) {
		l.advance()
	}
	text := string(l.src[begin:l.pos])
	if k, ok := keywords[text]; ok {
		if k == KwExec {
			l.wantInterpreter = true
		}
		return Token{Kind: k, Text: text, Pos: start}
	}
	return Token{Kind: Ident, Text: text, Pos: start}
}

// lexInterpreter scans the word after `exec`. It stops at whitespace and at
// '<', so `exec bash <<( ... )<<` works with or without a space before `<<(`.
func (l *Lexer) lexInterpreter(start Pos) Token {
	l.wantInterpreter = false
	begin := l.pos
	for l.pos < len(l.src) {
		switch c := l.src[l.pos]; c {
		case ' ', '\t', '\r', '\n', '<':
			goto done
		}
		l.advance()
	}
done:
	if l.pos == begin {
		return Token{Kind: Illegal, Text: "expected an interpreter after \"exec\"", Pos: start}
	}
	return Token{Kind: Ident, Text: string(l.src[begin:l.pos]), Pos: start}
}

func (l *Lexer) lexNumber(start Pos) Token {
	begin := l.pos
	for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
		l.advance()
	}
	kind := Int
	// A '.' only continues the number when a digit follows, so `1.foo` lexes
	// as `1` `.` `foo` rather than a malformed float.
	if l.pos < len(l.src) && l.src[l.pos] == '.' && isDigit(l.peekAt(1)) {
		kind = Float
		l.advance() // '.'
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.advance()
		}
	}
	return Token{Kind: kind, Text: string(l.src[begin:l.pos]), Pos: start}
}

func (l *Lexer) lexQuoted(start Pos) Token {
	if l.peekAt(1) == '"' && l.peekAt(2) == '"' {
		return l.lexTripleQuoted(start)
	}
	l.advance() // opening quote

	var b strings.Builder
	for l.pos < len(l.src) {
		switch c := l.src[l.pos]; c {
		case '"':
			l.advance()
			return Token{Kind: String, Text: b.String(), Pos: start}
		case '\n':
			return Token{Kind: Illegal, Text: "unterminated string literal", Pos: start}
		case '\\':
			l.advance()
			if l.pos >= len(l.src) {
				return Token{Kind: Illegal, Text: "unterminated string literal", Pos: start}
			}
			switch esc := l.src[l.pos]; esc {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			default:
				return Token{Kind: Illegal, Text: "unknown escape \\" + string(esc), Pos: start}
			}
			l.advance()
		default:
			b.WriteRune(l.advance())
		}
	}
	return Token{Kind: Illegal, Text: "unterminated string literal", Pos: start}
}

// lexTripleQuoted consumes a """ ... """ string verbatim. No escapes are
// processed: the content is usually prose handed to the model.
func (l *Lexer) lexTripleQuoted(start Pos) Token {
	l.advance()
	l.advance()
	l.advance()

	begin := l.pos
	for l.pos < len(l.src) {
		if l.src[l.pos] == '"' && l.peekAt(1) == '"' && l.peekAt(2) == '"' {
			text := string(l.src[begin:l.pos])
			l.advance()
			l.advance()
			l.advance()
			return Token{Kind: RawString, Text: text, Pos: start}
		}
		l.advance()
	}
	return Token{Kind: Illegal, Text: "unterminated triple-quoted string", Pos: start}
}

// lexRawBlock consumes <<( ... )<< verbatim. The first `)<<` at or after the
// opening delimiter terminates it (NSCRIPT spec §8); nscript never inspects
// what is inside.
func (l *Lexer) lexRawBlock(start Pos) Token {
	l.advance()
	l.advance()
	l.advance()

	begin := l.pos
	for l.pos < len(l.src) {
		if l.src[l.pos] == ')' && l.peekAt(1) == '<' && l.peekAt(2) == '<' {
			text := string(l.src[begin:l.pos])
			l.advance()
			l.advance()
			l.advance()
			return Token{Kind: RawBlock, Text: text, Pos: start}
		}
		l.advance()
	}
	return Token{Kind: Illegal, Text: "unterminated external script block", Pos: start}
}

func (l *Lexer) lexOperator(start Pos) Token {
	if l.pos+1 < len(l.src) {
		switch string(l.src[l.pos : l.pos+2]) {
		case "==":
			return l.twoChar(start, Eq, "==")
		case "!=":
			return l.twoChar(start, Ne, "!=")
		case "<=":
			return l.twoChar(start, Le, "<=")
		case ">=":
			return l.twoChar(start, Ge, ">=")
		case "&&":
			return l.twoChar(start, AndAnd, "&&")
		case "||":
			return l.twoChar(start, OrOr, "||")
		}
	}

	c := l.src[l.pos]
	kind, ok := singleCharKinds[c]
	if !ok {
		l.advance()
		return Token{Kind: Illegal, Text: fmt.Sprintf("unexpected character %q", string(rune(c))), Pos: start}
	}
	l.advance()
	return Token{Kind: kind, Text: string(c), Pos: start}
}

func (l *Lexer) twoChar(start Pos, kind Kind, text string) Token {
	l.advance()
	l.advance()
	return Token{Kind: kind, Text: text, Pos: start}
}

var singleCharKinds = map[byte]Kind{
	'{': LBrace,
	'}': RBrace,
	'(': LParen,
	')': RParen,
	':': Colon,
	',': Comma,
	'.': Dot,
	'=': Assign,
	'<': Lt,
	'>': Gt,
	'+': Plus,
	'-': Minus,
	'*': Star,
	'/': Slash,
	'!': Bang,
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool { return isIdentStart(c) || isDigit(c) }
