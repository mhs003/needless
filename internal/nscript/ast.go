package nscript

// node carries the source position shared by every AST node. Embedding it
// promotes both the Pos field and the Position method, which is what satisfies
// the Stmt and Expr interfaces without repeating a method on each type.
type node struct{ Pos Pos }

// Position returns the source position of the node.
func (n node) Position() Pos { return n.Pos }

// Program is a parsed .nsc file.
//
// It is the whole of a command's meaning: the instruction is what the model
// matches a prompt against, the args are the slots the model may fill, and the
// run block is what actually executes.
type Program struct {
	Pos Pos

	// Instruction is the command's natural-language description, with
	// surrounding whitespace trimmed. It is never empty in a parsed program.
	Instruction string

	// Args are the declared argument slots, in source order.
	Args []Arg

	// Confirm is the script's explicit confirmation policy, or nil when the
	// script does not declare one. The runtime supplies the default (D12).
	Confirm *bool

	// Run is the implementation.
	Run *Block
}

// Arg is one declared argument slot.
type Arg struct {
	Pos     Pos
	Name    string
	Type    Type
	Default Expr // nil when the declaration has no default
}

// Type is an nscript primitive type.
type Type uint8

const (
	TypeString Type = iota
	TypeInt
	TypeFloat
	TypeBool
)

func (t Type) String() string {
	switch t {
	case TypeString:
		return "string"
	case TypeInt:
		return "int"
	case TypeFloat:
		return "float"
	case TypeBool:
		return "bool"
	}
	return "invalid"
}

// Block is a brace-delimited sequence of statements.
type Block struct {
	node
	Stmts []Stmt
}

// Stmt is a statement inside a run block or function body.
type Stmt interface {
	stmtNode()
	Position() Pos
}

// Expr is an expression.
type Expr interface {
	exprNode()
	Position() Pos
}

// --- statements ----------------------------------------------------------

// LetStmt binds a name: `let x = expr`.
type LetStmt struct {
	node
	Name  string
	Value Expr
}

// IfStmt is `if cond { ... } else { ... }`. An `else if` chain is represented
// as a Block holding a single IfStmt, so the shape stays uniform.
type IfStmt struct {
	node
	Cond Expr
	Then *Block
	Else *Block // nil when there is no else clause
}

// FnStmt declares a script-local helper: `fn name(a: string) { ... }`.
type FnStmt struct {
	node
	Name   string
	Params []Arg
	Body   *Block
}

// ReturnStmt leaves the enclosing run block early.
type ReturnStmt struct {
	node
}

// ExprStmt is an expression evaluated for its effect, e.g. `print(x)` or
// `error("...")`.
type ExprStmt struct {
	node
	X Expr
}

// EnvStmt exposes nscript values to external scripts by name: `env { X = expr }`.
type EnvStmt struct {
	node
	Vars []EnvVar
}

// EnvVar is one `NAME = expr` binding inside an env block.
type EnvVar struct {
	Pos   Pos
	Name  string
	Value Expr
}

// ExecStmt runs an external interpreter over an opaque body.
//
// Body is exactly the bytes that appeared between `<<(` and `)<<`. It is
// never trimmed, re-indented, or interpolated into (D2).
type ExecStmt struct {
	node
	Interpreter string
	Body        []byte
}

func (*LetStmt) stmtNode()    {}
func (*IfStmt) stmtNode()     {}
func (*FnStmt) stmtNode()     {}
func (*ReturnStmt) stmtNode() {}
func (*ExprStmt) stmtNode()   {}
func (*EnvStmt) stmtNode()    {}
func (*ExecStmt) stmtNode()   {}

// --- expressions ---------------------------------------------------------

// StringLit is a string literal.
type StringLit struct {
	node
	Value string
}

// IntLit is an integer literal.
type IntLit struct {
	node
	Value int64
}

// FloatLit is a floating-point literal.
type FloatLit struct {
	node
	Value float64
}

// BoolLit is `true` or `false`.
type BoolLit struct {
	node
	Value bool
}

// IdentExpr is a reference to a let binding, an argument, or a parameter.
type IdentExpr struct {
	node
	Name string
}

// BinaryExpr is `left op right`.
type BinaryExpr struct {
	node
	Op          Kind
	Left, Right Expr
}

// UnaryExpr is `op x`.
type UnaryExpr struct {
	node
	Op Kind
	X  Expr
}

// CallExpr is a call to a builtin: `string(x)`, `print(x)`, `error(x)`.
type CallExpr struct {
	node
	Fn   string
	Args []Expr
}

func (*StringLit) exprNode()  {}
func (*IntLit) exprNode()     {}
func (*FloatLit) exprNode()   {}
func (*BoolLit) exprNode()    {}
func (*IdentExpr) exprNode()  {}
func (*BinaryExpr) exprNode() {}
func (*UnaryExpr) exprNode()  {}
func (*CallExpr) exprNode()   {}
