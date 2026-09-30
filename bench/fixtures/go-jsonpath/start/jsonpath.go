// Package jsonpath evaluates a small subset of JSONPath over documents that were
// decoded with encoding/json into an any.
//
// README.md specifies the syntax, the order of the results and the errors.
package jsonpath

import "errors"

// Eval returns the values of doc that path selects, in the order defined in
// README.md. A valid path that selects nothing gives an empty result and a nil
// error; an invalid path gives a nil result and a non-nil error.
func Eval(doc any, path string) ([]any, error) {
	// TODO: implement, following README.md.
	return nil, errors.New("jsonpath: not implemented")
}
