// Package sample is a fixture for surface extraction tests.
package sample

import "context"

// Version is an exported const.
const Version = 1

// Hidden is unexported and must not appear.
const hidden = 2

// Thing is an exported struct.
type Thing struct {
	// Name is exported.
	Name string
	// secret is unexported.
	secret string
}

// Doer is an exported interface.
type Doer interface {
	// Do is exported.
	Do(ctx context.Context, n int) (string, error)
	// hidden is unexported.
	hidden()
}

// Callback is an exported func type.
type Callback func(a, b string) error

// Exported is a package-level function.
func Exported(a, b string, opts ...int) (string, error) { return "", nil }

func unexported() {}

// Method on an exported type is part of the surface.
func (t Thing) Method() string { return t.Name }

// methods on unexported types are not.
type internalThing struct{}

func (i internalThing) Method() string { return "" }
