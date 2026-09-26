package nscript

import "fmt"

// Error is a lexing or parsing failure with a source position.
type Error struct {
	Pos Pos
	Msg string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Pos, e.Msg) }

func errorf(pos Pos, format string, args ...any) *Error {
	return &Error{Pos: pos, Msg: fmt.Sprintf(format, args...)}
}
