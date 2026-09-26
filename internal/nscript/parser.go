package nscript

import (
	"strconv"
	"strings"
)

// Parse lexes and parses a complete .nsc file.
//
// Two sections are required: `instruction` (the only thing the model sees when
// matching a prompt) and `run` (the implementation). `args` and `confirm` are
// optional.
func Parse(src []byte) (*Program, error) {
	toks, err := Lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	prog, err := p.parseProgram()
	if err != nil {
		return nil, err
	}
	if err := checkCalls(prog); err != nil {
		return nil, err
	}
	return prog, nil
}

type parser struct {
	toks []Token
	i    int
}

func (p *parser) cur() Token { return p.toks[p.i] }

func (p *parser) peek(n int) Token {
	if j := p.i + n; j < len(p.toks) {
		return p.toks[j]
	}
	return p.toks[len(p.toks)-1]
}

func (p *parser) advance() Token {
	t := p.toks[p.i]
	if p.i < len(p.toks)-1 {
		p.i++
	}
	return t
}

func (p *parser) accept(k Kind) bool {
	if p.cur().Kind == k {
		p.advance()
		return true
	}
	return false
}

func (p *parser) expect(k Kind) (Token, error) {
	if p.cur().Kind != k {
		return Token{}, errorf(p.cur().Pos, "expected %s, got %s", k, p.cur().Kind)
	}
	return p.advance(), nil
}

func (p *parser) skipNewlines() {
	for p.cur().Kind == Newline {
		p.advance()
	}
}

// expectName consumes an identifier used as a name (argument, variable,
// function). Reserved words are called out explicitly, because "expected a
// name, got env" leaves the user wondering why `env` is not a name.
func (p *parser) expectName(what string) (Token, error) {
	tok := p.cur()
	if tok.Kind == Ident {
		p.advance()
		return tok, nil
	}
	if isKeyword(tok.Kind) {
		return Token{}, errorf(tok.Pos, "expected %s, got %s; %s is a reserved word", what, tok.Kind, tok.Kind)
	}
	return Token{}, errorf(tok.Pos, "expected %s, got %s", what, tok.Kind)
}

// --- top level -----------------------------------------------------------

func (p *parser) parseProgram() (*Program, error) {
	prog := &Program{Pos: p.cur().Pos}
	p.skipNewlines()

	var instructionPos Pos
	var seenInstruction, seenArgs, seenConfirm, seenRun bool

	for p.cur().Kind != EOF {
		switch p.cur().Kind {
		case KwInstruction:
			if seenInstruction {
				return nil, errorf(p.cur().Pos, "duplicate %s section", KwInstruction)
			}
			seenInstruction = true
			instructionPos = p.cur().Pos
			text, err := p.parseInstruction()
			if err != nil {
				return nil, err
			}
			prog.Instruction = text

		case KwArgs:
			if seenArgs {
				return nil, errorf(p.cur().Pos, "duplicate %s section", KwArgs)
			}
			seenArgs = true
			args, err := p.parseArgsSection()
			if err != nil {
				return nil, err
			}
			prog.Args = args

		case KwConfirm:
			if seenConfirm {
				return nil, errorf(p.cur().Pos, "duplicate %s section", KwConfirm)
			}
			seenConfirm = true
			v, err := p.parseConfirm()
			if err != nil {
				return nil, err
			}
			prog.Confirm = &v

		case KwRun:
			if seenRun {
				return nil, errorf(p.cur().Pos, "duplicate %s section", KwRun)
			}
			seenRun = true
			p.advance() // run
			block, err := p.parseBlock()
			if err != nil {
				return nil, err
			}
			prog.Run = block

		default:
			return nil, errorf(p.cur().Pos,
				"expected %s, %s, %s, or %s at the top level, got %s",
				KwInstruction, KwArgs, KwConfirm, KwRun, p.cur().Kind)
		}
		p.skipNewlines()
	}

	if !seenInstruction {
		return nil, errorf(prog.Pos, "missing an %s section", KwInstruction)
	}
	if prog.Instruction == "" {
		return nil, errorf(instructionPos, "the %s section must not be empty", KwInstruction)
	}
	if !seenRun {
		return nil, errorf(prog.Pos, "missing a %s section", KwRun)
	}
	return prog, nil
}

func (p *parser) parseInstruction() (string, error) {
	p.advance() // instruction
	t := p.cur()
	if t.Kind != RawString && t.Kind != String {
		return "", errorf(t.Pos, "expected a string after %s, got %s", KwInstruction, t.Kind)
	}
	p.advance()
	if t.Kind == RawString {
		// The surrounding whitespace of a triple-quoted block is a formatting
		// artefact of where the delimiters sit, so it is trimmed. A plain
		// string literal is taken exactly as written.
		return strings.TrimSpace(t.Text), nil
	}
	return t.Text, nil
}

func (p *parser) parseConfirm() (bool, error) {
	p.advance() // confirm
	switch p.cur().Kind {
	case KwTrue:
		p.advance()
		return true, nil
	case KwFalse:
		p.advance()
		return false, nil
	}
	return false, errorf(p.cur().Pos, "expected %s or %s after %s, got %s",
		KwTrue, KwFalse, KwConfirm, p.cur().Kind)
}

func (p *parser) parseArgsSection() ([]Arg, error) {
	p.advance() // args
	if _, err := p.expect(LBrace); err != nil {
		return nil, err
	}
	var args []Arg
	seen := map[string]Pos{}
	for {
		p.skipNewlines()
		if p.accept(RBrace) {
			return args, nil
		}
		if p.cur().Kind == EOF {
			return nil, errorf(p.cur().Pos, "unterminated %s block; missing \"}\"", KwArgs)
		}
		a, err := p.parseArgDecl()
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[a.Name]; dup {
			return nil, errorf(a.Pos, "duplicate argument %q (first declared at %s)", a.Name, prev)
		}
		seen[a.Name] = a.Pos
		args = append(args, a)
	}
}

func (p *parser) parseArgDecl() (Arg, error) {
	nameTok, err := p.expectName("a name")
	if err != nil {
		return Arg{}, err
	}
	a := Arg{Pos: nameTok.Pos, Name: nameTok.Text}
	if _, err := p.expect(Colon); err != nil {
		return Arg{}, err
	}
	typ, err := p.parseType()
	if err != nil {
		return Arg{}, err
	}
	a.Type = typ
	if p.accept(Assign) {
		def, err := p.parseExpr()
		if err != nil {
			return Arg{}, err
		}
		if err := checkDefault(a, def); err != nil {
			return Arg{}, err
		}
		a.Default = def
	}
	return a, nil
}

// checkDefault enforces that a default is a literal of the declared type. A
// default may not be an expression: it must be knowable without running the
// script, because it is applied before execution begins.
func checkDefault(a Arg, def Expr) error {
	switch def.(type) {
	case *StringLit:
		if a.Type != TypeString {
			return errorf(def.Position(), "default for %q is a string, but %q is declared %s", a.Name, a.Name, a.Type)
		}
	case *IntLit:
		// An integer literal is accepted for a float, mirroring the way the
		// runtime widens ints to floats.
		if a.Type != TypeInt && a.Type != TypeFloat {
			return errorf(def.Position(), "default for %q is an integer, but %q is declared %s", a.Name, a.Name, a.Type)
		}
	case *FloatLit:
		if a.Type != TypeFloat {
			return errorf(def.Position(), "default for %q is a float, but %q is declared %s", a.Name, a.Name, a.Type)
		}
	case *BoolLit:
		if a.Type != TypeBool {
			return errorf(def.Position(), "default for %q is a boolean, but %q is declared %s", a.Name, a.Name, a.Type)
		}
	default:
		return errorf(def.Position(), "the default for %q must be a literal value", a.Name)
	}
	return nil
}

func (p *parser) parseType() (Type, error) {
	switch p.cur().Kind {
	case KwString:
		p.advance()
		return TypeString, nil
	case KwInt:
		p.advance()
		return TypeInt, nil
	case KwFloat:
		p.advance()
		return TypeFloat, nil
	case KwBool:
		p.advance()
		return TypeBool, nil
	}
	return 0, errorf(p.cur().Pos, "expected a type (string, int, float, bool), got %s", p.cur().Kind)
}

// --- blocks and statements ----------------------------------------------

func (p *parser) parseBlock() (*Block, error) {
	open, err := p.expect(LBrace)
	if err != nil {
		return nil, err
	}
	block := &Block{node: node{Pos: open.Pos}}
	for {
		p.skipNewlines()
		if p.accept(RBrace) {
			return block, nil
		}
		if p.cur().Kind == EOF {
			return nil, errorf(open.Pos, "unterminated block; missing \"}\"")
		}
		stmt, err := p.parseStmt()
		if err != nil {
			return nil, err
		}
		block.Stmts = append(block.Stmts, stmt)

		switch p.cur().Kind {
		case Newline, RBrace, EOF:
			// A statement ends at a newline or at the closing brace.
		default:
			return nil, errorf(p.cur().Pos,
				"expected a newline or \"}\" after the statement, got %s", p.cur().Kind)
		}
	}
}

func (p *parser) parseStmt() (Stmt, error) {
	switch p.cur().Kind {
	case KwLet:
		return p.parseLet()
	case KwIf:
		return p.parseIf()
	case KwFn:
		return p.parseFn()
	case KwReturn:
		t := p.advance()
		return &ReturnStmt{node: node{Pos: t.Pos}}, nil
	case KwEnv:
		return p.parseEnv()
	case KwExec:
		return p.parseExec()
	}

	if !startsExpr(p.cur().Kind) {
		return nil, errorf(p.cur().Pos, "expected a statement, got %s", p.cur().Kind)
	}
	start := p.cur().Pos
	x, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if _, ok := x.(*CallExpr); !ok {
		return nil, errorf(start,
			"expected a statement; an expression on its own has no effect (did you mean %s?)", KwPrint)
	}
	return &ExprStmt{node: node{Pos: start}, X: x}, nil
}

func startsExpr(k Kind) bool {
	switch k {
	case Ident, String, Int, Float, KwTrue, KwFalse, LParen, Bang, Minus,
		KwPrint, KwError, KwString, KwInt, KwFloat, KwBool:
		return true
	}
	return false
}

func (p *parser) parseLet() (Stmt, error) {
	t := p.advance() // let
	nameTok, err := p.expectName("a variable name after \"let\"")
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(Assign); err != nil {
		return nil, err
	}
	v, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	return &LetStmt{node: node{Pos: t.Pos}, Name: nameTok.Text, Value: v}, nil
}

func (p *parser) parseIf() (Stmt, error) {
	t := p.advance() // if
	cond, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	then, err := p.parseBlock()
	if err != nil {
		return nil, err
	}
	stmt := &IfStmt{node: node{Pos: t.Pos}, Cond: cond, Then: then}

	// An `else` may sit on the line after the closing brace. Peek past any
	// newlines, but restore the position when there is no else: the newline
	// terminates the `if` statement and belongs to the caller.
	save := p.i
	p.skipNewlines()
	if p.cur().Kind != KwElse {
		p.i = save
		return stmt, nil
	}
	p.advance() // else

	if p.cur().Kind == KwIf {
		nested, err := p.parseIf()
		if err != nil {
			return nil, err
		}
		// Normalise `else if` into a block holding one statement so that
		// consumers only ever deal with *Block.
		stmt.Else = &Block{node: node{Pos: nested.Position()}, Stmts: []Stmt{nested}}
		return stmt, nil
	}
	block, err := p.parseBlock()
	if err != nil {
		return nil, err
	}
	stmt.Else = block
	return stmt, nil
}

func (p *parser) parseFn() (Stmt, error) {
	t := p.advance() // fn
	nameTok, err := p.expectName("a function name after \"fn\"")
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(LParen); err != nil {
		return nil, err
	}
	var params []Arg
	for {
		if p.accept(RParen) {
			break
		}
		a, err := p.parseArgDecl()
		if err != nil {
			return nil, err
		}
		params = append(params, a)
		if p.accept(Comma) {
			continue
		}
		if _, err := p.expect(RParen); err != nil {
			return nil, err
		}
		break
	}
	body, err := p.parseBlock()
	if err != nil {
		return nil, err
	}
	return &FnStmt{node: node{Pos: t.Pos}, Name: nameTok.Text, Params: params, Body: body}, nil
}

func (p *parser) parseEnv() (Stmt, error) {
	t := p.advance() // env
	if _, err := p.expect(LBrace); err != nil {
		return nil, err
	}
	stmt := &EnvStmt{node: node{Pos: t.Pos}}
	for {
		p.skipNewlines()
		if p.accept(RBrace) {
			return stmt, nil
		}
		if p.cur().Kind == EOF {
			return nil, errorf(t.Pos, "unterminated %s block; missing \"}\"", KwEnv)
		}
		nameTok, err := p.expectName("a variable name in the \"env\" block")
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(Assign); err != nil {
			return nil, err
		}
		v, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		stmt.Vars = append(stmt.Vars, EnvVar{Pos: nameTok.Pos, Name: nameTok.Text, Value: v})
	}
}

func (p *parser) parseExec() (Stmt, error) {
	t := p.advance() // exec
	interp, err := p.expect(Ident)
	if err != nil {
		return nil, errorf(p.cur().Pos, "expected an interpreter after %s, got %s", KwExec, p.cur().Kind)
	}
	body, err := p.expect(RawBlock)
	if err != nil {
		return nil, errorf(p.cur().Pos,
			"expected a <<( ... )<< block after the interpreter %q, got %s", interp.Text, p.cur().Kind)
	}
	return &ExecStmt{
		node:        node{Pos: t.Pos},
		Interpreter: interp.Text,
		Body:        []byte(body.Text),
	}, nil
}

// --- expressions ---------------------------------------------------------

func (p *parser) parseExpr() (Expr, error) { return p.parseOr() }

func (p *parser) parseOr() (Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.cur().Kind == OrOr {
		op := p.advance()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{node: node{Pos: op.Pos}, Op: OrOr, Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (Expr, error) {
	left, err := p.parseEquality()
	if err != nil {
		return nil, err
	}
	for p.cur().Kind == AndAnd {
		op := p.advance()
		right, err := p.parseEquality()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{node: node{Pos: op.Pos}, Op: AndAnd, Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) parseEquality() (Expr, error) {
	left, err := p.parseComparison()
	if err != nil {
		return nil, err
	}
	for {
		k := p.cur().Kind
		if k != Eq && k != Ne {
			return left, nil
		}
		op := p.advance()
		right, err := p.parseComparison()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{node: node{Pos: op.Pos}, Op: k, Left: left, Right: right}
	}
}

func (p *parser) parseComparison() (Expr, error) {
	left, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	for {
		k := p.cur().Kind
		if k != Lt && k != Le && k != Gt && k != Ge {
			return left, nil
		}
		op := p.advance()
		right, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{node: node{Pos: op.Pos}, Op: k, Left: left, Right: right}
	}
}

func (p *parser) parseAdditive() (Expr, error) {
	left, err := p.parseMultiplicative()
	if err != nil {
		return nil, err
	}
	for {
		k := p.cur().Kind
		if k != Plus && k != Minus {
			return left, nil
		}
		op := p.advance()
		right, err := p.parseMultiplicative()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{node: node{Pos: op.Pos}, Op: k, Left: left, Right: right}
	}
}

func (p *parser) parseMultiplicative() (Expr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		k := p.cur().Kind
		if k != Star && k != Slash {
			return left, nil
		}
		op := p.advance()
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{node: node{Pos: op.Pos}, Op: k, Left: left, Right: right}
	}
}

func (p *parser) parseUnary() (Expr, error) {
	if k := p.cur().Kind; k == Bang || k == Minus {
		op := p.advance()
		x, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &UnaryExpr{node: node{Pos: op.Pos}, Op: k, X: x}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (Expr, error) {
	t := p.cur()
	switch t.Kind {
	case String:
		p.advance()
		return &StringLit{node: node{Pos: t.Pos}, Value: t.Text}, nil

	case Int:
		p.advance()
		n, err := strconv.ParseInt(t.Text, 10, 64)
		if err != nil {
			return nil, errorf(t.Pos, "invalid integer %q", t.Text)
		}
		return &IntLit{node: node{Pos: t.Pos}, Value: n}, nil

	case Float:
		p.advance()
		f, err := strconv.ParseFloat(t.Text, 64)
		if err != nil {
			return nil, errorf(t.Pos, "invalid float %q", t.Text)
		}
		return &FloatLit{node: node{Pos: t.Pos}, Value: f}, nil

	case KwTrue, KwFalse:
		p.advance()
		return &BoolLit{node: node{Pos: t.Pos}, Value: t.Kind == KwTrue}, nil

	case LParen:
		p.advance()
		x, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(RParen); err != nil {
			return nil, err
		}
		return x, nil

	case Ident:
		p.advance()
		if p.cur().Kind == LParen {
			return p.parseCall(t.Pos, t.Text)
		}
		return &IdentExpr{node: node{Pos: t.Pos}, Name: t.Text}, nil

	// `print`, `error` and the type names are keywords, but they are also
	// callables: `print(x)`, `string(amount)`.
	case KwPrint, KwError, KwString, KwInt, KwFloat, KwBool:
		p.advance()
		if p.cur().Kind != LParen {
			return nil, errorf(t.Pos, "%s may only be used as a call here", t.Kind)
		}
		return p.parseCall(t.Pos, t.Text)
	}

	return nil, errorf(t.Pos, "expected an expression, got %s", t.Kind)
}

// parseCall parses the argument list; the callee name and the opening paren
// have already been consumed.
func (p *parser) parseCall(pos Pos, name string) (Expr, error) {
	p.advance() // (
	c := &CallExpr{node: node{Pos: pos}, Fn: name}
	if p.accept(RParen) {
		return c, nil
	}
	for {
		arg, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		c.Args = append(c.Args, arg)
		if p.accept(Comma) {
			continue
		}
		if _, err := p.expect(RParen); err != nil {
			return nil, err
		}
		return c, nil
	}
}
