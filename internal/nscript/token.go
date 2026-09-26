package nscript

import "fmt"

// Kind is the lexical class of a Token.
type Kind uint8

const (
	EOF Kind = iota
	Illegal
	Newline

	Ident
	Int
	Float
	String    // "..."      — Text holds the decoded value
	RawString // """..."""  — Text holds the content verbatim
	RawBlock  // <<( )<<    — Text holds the content verbatim

	KwInstruction
	KwArgs
	KwConfirm
	KwRun
	KwLet
	KwIf
	KwElse
	KwFn
	KwReturn
	KwPrint
	KwError
	KwEnv
	KwExec
	KwTrue
	KwFalse

	// Argument type names.
	KwString
	KwInt
	KwFloat
	KwBool

	LBrace
	RBrace
	LParen
	RParen
	Colon
	Comma
	Dot

	Assign
	Eq
	Ne
	Lt
	Le
	Gt
	Ge
	Plus
	Minus
	Star
	Slash
	AndAnd
	OrOr
	Bang
)

var kindNames = map[Kind]string{
	EOF:     "end of file",
	Illegal: "illegal token",
	Newline: "newline",

	Ident:     "identifier",
	Int:       "integer",
	Float:     "float",
	String:    "string",
	RawString: "triple-quoted string",
	RawBlock:  "external script block",

	KwInstruction: "\"instruction\"",
	KwArgs:        "\"args\"",
	KwConfirm:     "\"confirm\"",
	KwRun:         "\"run\"",
	KwLet:         "\"let\"",
	KwIf:          "\"if\"",
	KwElse:        "\"else\"",
	KwFn:          "\"fn\"",
	KwReturn:      "\"return\"",
	KwPrint:       "\"print\"",
	KwError:       "\"error\"",
	KwEnv:         "\"env\"",
	KwExec:        "\"exec\"",
	KwTrue:        "\"true\"",
	KwFalse:       "\"false\"",

	KwString: "\"string\"",
	KwInt:    "\"int\"",
	KwFloat:  "\"float\"",
	KwBool:   "\"bool\"",

	LBrace: "\"{\"",
	RBrace: "\"}\"",
	LParen: "\"(\"",
	RParen: "\")\"",
	Colon:  "\":\"",
	Comma:  "\",\"",
	Dot:    "\".\"",

	Assign: "\"=\"",
	Eq:     "\"==\"",
	Ne:     "\"!=\"",
	Lt:     "\"<\"",
	Le:     "\"<=\"",
	Gt:     "\">\"",
	Ge:     "\">=\"",
	Plus:   "\"+\"",
	Minus:  "\"-\"",
	Star:   "\"*\"",
	Slash:  "\"/\"",
	AndAnd: "\"&&\"",
	OrOr:   "\"||\"",
	Bang:   "\"!\"",
}

func (k Kind) String() string {
	if s, ok := kindNames[k]; ok {
		return s
	}
	return fmt.Sprintf("Kind(%d)", uint8(k))
}

// keywords maps reserved words to their token kind. Type names are reserved
// too, so `string` cannot be used as an argument name.
var keywords = map[string]Kind{
	"instruction": KwInstruction,
	"args":        KwArgs,
	"confirm":     KwConfirm,
	"run":         KwRun,
	"let":         KwLet,
	"if":          KwIf,
	"else":        KwElse,
	"fn":          KwFn,
	"return":      KwReturn,
	"print":       KwPrint,
	"error":       KwError,
	"env":         KwEnv,
	"exec":        KwExec,
	"true":        KwTrue,
	"false":       KwFalse,

	"string": KwString,
	"int":    KwInt,
	"float":  KwFloat,
	"bool":   KwBool,
}

// Pos is a location in the source. Line and Column are 1-based; Column counts
// runes, so it lines up with editor columns for non-ASCII source.
type Pos struct {
	Line   int
	Column int
	Offset int // 0-based byte offset
}

func (p Pos) String() string { return fmt.Sprintf("%d:%d", p.Line, p.Column) }

// Token is one lexical unit. Text carries the decoded value for strings and
// the verbatim content for triple-quoted strings and external script blocks.
type Token struct {
	Kind Kind
	Text string
	Pos  Pos
}
