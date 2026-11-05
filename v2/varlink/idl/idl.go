// Package idl implements the varlink interface definition language.
//
// An [Interface] is a parsed interface description as defined by the UAPI
// varlink specification: a named collection of types, methods and errors,
// optionally documented with comments preceding each declaration.
package idl

import "strings"

// Kind identifies the kind of a [Type].
type Kind uint8

const (
	Invalid Kind = iota
	// Bool is the `bool` keyword.
	Bool
	// Int is the `int` keyword.
	Int
	// Float is the `float` keyword.
	Float
	// String is the `string` keyword.
	String
	// Object is the `object` keyword, a foreign untyped JSON object.
	Object
	// Any is the `any` keyword, any JSON value except null.
	Any
	// Array is `[]T`, an ordered list of values of one type.
	Array
	// Map is `[string]T`, a dictionary keyed by unique strings.
	Map
	// Maybe is `?T`, a nullable type.
	Maybe
	// Struct is `(a: T, ...)`, a structure of named, typed fields.
	Struct
	// Enum is `(one, two, ...)`, an enumeration of names.
	Enum
	// Named is a reference to a type declared with the `type` keyword.
	Named
)

// Type is a varlink type expression.
type Type struct {
	Kind Kind
	// Elem is the element type of Array, Map and Maybe types.
	Elem *Type
	// Name is the referenced declaration of Named types.
	Name string
	// Fields are the fields of Struct and Enum types. Enum fields have a
	// nil Type.
	Fields []Field
}

// Field is a named field of a Struct type, or a value of an Enum type.
type Field struct {
	Name string
	Doc  string
	Type *Type
}

// Member is one declared member of an interface: a [TypeDecl], [Method] or
// [Error]. Types, methods and errors share one namespace within an interface.
type Member interface {
	member()
}

// TypeDecl is a named type declared with the `type` keyword.
type TypeDecl struct {
	Name string
	Doc  string
	Type *Type
}

// Method is a method declared with the `method` keyword.
type Method struct {
	Name string
	Doc  string
	In   *Type
	Out  *Type
}

// Error is an error declared with the `error` keyword.
type Error struct {
	Name string
	Doc  string
	Type *Type
}

func (*TypeDecl) member() {}
func (*Method) member()   {}
func (*Error) member()    {}

// Interface is a parsed varlink interface description. Members appear in
// declaration order.
type Interface struct {
	Name    string
	Doc     string
	Members []Member

	methods map[string]*Method
	types   map[string]*TypeDecl
	errors  map[string]*Error
}

// Method returns the declared method with the given unqualified name.
func (i *Interface) Method(name string) (*Method, bool) {
	m, ok := i.methods[name]
	return m, ok
}

// TypeDecl returns the type declared with the given name.
func (i *Interface) TypeDecl(name string) (*TypeDecl, bool) {
	t, ok := i.types[name]
	return t, ok
}

// Error returns the declared error with the given unqualified name.
func (i *Interface) Error(name string) (*Error, bool) {
	e, ok := i.errors[name]
	return e, ok
}

// String returns the interface definition language representation of the
// type.
func (t *Type) String() string {
	switch t.Kind {
	case Bool:
		return "bool"
	case Int:
		return "int"
	case Float:
		return "float"
	case String:
		return "string"
	case Object:
		return "object"
	case Any:
		return "any"
	case Array:
		return "[]" + t.Elem.String()
	case Map:
		return "[string]" + t.Elem.String()
	case Maybe:
		return "?" + t.Elem.String()
	case Named:
		return t.Name
	case Struct:
		if len(t.Fields) == 0 {
			return "()"
		}
		parts := make([]string, len(t.Fields))
		for i, field := range t.Fields {
			parts[i] = field.Name + ": " + field.Type.String()
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case Enum:
		names := make([]string, len(t.Fields))
		for i, field := range t.Fields {
			names[i] = field.Name
		}
		return "(" + strings.Join(names, ", ") + ")"
	}
	return "<invalid>"
}
