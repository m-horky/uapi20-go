package idl

import (
	"fmt"
	"strings"
)

// Parse parses a varlink interface description. Documentation comments that
// start on their own line and immediately precede a declaration become the
// Doc field of that declaration.
func Parse(description string) (*Interface, error) {
	p := &parser{input: description}
	return p.parse()
}

type parser struct {
	input string
	pos   int
	// lineStart is the index of the first byte of the current input line.
	lineStart int
	// doc holds pending documentation comment lines.
	doc []string
}

func (p *parser) parse() (*Interface, error) {
	if !p.skip() {
		return nil, fmt.Errorf("idl: empty description")
	}
	if p.keyword() != "interface" {
		return nil, fmt.Errorf("idl: missing interface keyword")
	}
	p.skip()

	iface := &Interface{
		methods: make(map[string]*Method),
		types:   make(map[string]*TypeDecl),
		errors:  make(map[string]*Error),
	}
	iface.Doc = p.takeDoc()
	iface.Name = p.interfaceName()
	if iface.Name == "" {
		return nil, fmt.Errorf("idl: invalid interface name")
	}

	declared := make(map[string]struct{})
	for p.skip() {
		doc := p.takeDoc()
		switch keyword := p.keyword(); keyword {
		case "type":
			member, err := p.parseTypeDecl(doc)
			if err != nil {
				return nil, err
			}
			if err := iface.add(declared, member, member.Name); err != nil {
				return nil, err
			}
		case "method":
			member, err := p.parseMethod(doc)
			if err != nil {
				return nil, err
			}
			if err := iface.add(declared, member, member.Name); err != nil {
				return nil, err
			}
		case "error":
			member, err := p.parseError(doc)
			if err != nil {
				return nil, err
			}
			if err := iface.add(declared, member, member.Name); err != nil {
				return nil, err
			}
		case "":
			return nil, fmt.Errorf("idl: unexpected %q", p.rest())
		default:
			return nil, fmt.Errorf("idl: unknown keyword %q", keyword)
		}
	}

	if err := iface.resolve(); err != nil {
		return nil, err
	}
	return iface, nil
}

func (i *Interface) add(declared map[string]struct{}, member Member, name string) error {
	if _, exists := declared[name]; exists {
		return fmt.Errorf("idl: %q already declared", name)
	}
	declared[name] = struct{}{}
	i.Members = append(i.Members, member)
	switch m := member.(type) {
	case *TypeDecl:
		i.types[m.Name] = m
	case *Method:
		i.methods[m.Name] = m
	case *Error:
		i.errors[m.Name] = m
	}
	return nil
}

// resolve checks that all named type references are declared and that the
// declarations are acyclic.
func (i *Interface) resolve() error {
	var visit func(t *Type, path []string) error
	visit = func(t *Type, path []string) error {
		switch t.Kind {
		case Array, Map, Maybe:
			return visit(t.Elem, path)
		case Struct:
			for _, field := range t.Fields {
				if err := visit(field.Type, path); err != nil {
					return err
				}
			}
		case Named:
			decl, ok := i.types[t.Name]
			if !ok {
				return fmt.Errorf("idl: unknown type %q", t.Name)
			}
			for _, name := range path {
				if name == t.Name {
					return fmt.Errorf("idl: cyclic type %q", t.Name)
				}
			}
			return visit(decl.Type, append(path, t.Name))
		}
		return nil
	}

	for _, member := range i.Members {
		var types []*Type
		switch m := member.(type) {
		case *TypeDecl:
			types = []*Type{m.Type}
		case *Method:
			types = []*Type{m.In, m.Out}
		case *Error:
			types = []*Type{m.Type}
		}
		for _, t := range types {
			if err := visit(t, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *parser) parseTypeDecl(doc string) (*TypeDecl, error) {
	name, err := p.memberName()
	if err != nil {
		return nil, err
	}
	p.skip()
	t, err := p.parseType()
	if err != nil {
		return nil, err
	}
	if t.Kind != Struct && t.Kind != Enum {
		return nil, fmt.Errorf("idl: type %q: only structs and enums can be aliased", name)
	}
	return &TypeDecl{Name: name, Doc: doc, Type: t}, nil
}

func (p *parser) parseMethod(doc string) (*Method, error) {
	name, err := p.memberName()
	if err != nil {
		return nil, err
	}
	p.skip()
	in, err := p.parseType()
	if err != nil {
		return nil, fmt.Errorf("idl: method %s: %w", name, err)
	}
	if in.Kind != Struct {
		return nil, fmt.Errorf("idl: method %s: input parameters must be a struct", name)
	}
	p.skip()
	if !p.consumeString("->") {
		return nil, fmt.Errorf("idl: method %s: missing '->' operator", name)
	}
	p.skip()
	out, err := p.parseType()
	if err != nil {
		return nil, fmt.Errorf("idl: method %s: %w", name, err)
	}
	if out.Kind != Struct {
		return nil, fmt.Errorf("idl: method %s: output parameters must be a struct", name)
	}
	return &Method{Name: name, Doc: doc, In: in, Out: out}, nil
}

func (p *parser) parseError(doc string) (*Error, error) {
	name, err := p.memberName()
	if err != nil {
		return nil, err
	}
	p.skip()
	t, err := p.parseType()
	if err != nil {
		return nil, fmt.Errorf("idl: error %s: %w", name, err)
	}
	if t.Kind != Struct {
		return nil, fmt.Errorf("idl: error %s: parameters must be a struct", name)
	}
	return &Error{Name: name, Doc: doc, Type: t}, nil
}

// memberName reads the name of a type, method or error declaration.
func (p *parser) memberName() (string, error) {
	p.skip()
	name := p.name()
	if name == "" {
		return "", fmt.Errorf("idl: invalid name at %q", p.rest())
	}
	return name, nil
}

// parseType parses a type expression:
//
//	type = element_type / maybe element_type / array type / dict type /
//	       maybe array type / maybe dict type
func (p *parser) parseType() (*Type, error) {
	maybe := p.consumeByte('?')

	var t *Type
	switch {
	case p.consumeString("[string]"):
		elem, err := p.parseType()
		if err != nil {
			return nil, err
		}
		t = &Type{Kind: Map, Elem: elem}
	case p.consumeString("[]"):
		elem, err := p.parseType()
		if err != nil {
			return nil, err
		}
		t = &Type{Kind: Array, Elem: elem}
	default:
		var err error
		t, err = p.parseElementType()
		if err != nil {
			return nil, err
		}
	}

	if maybe {
		return &Type{Kind: Maybe, Elem: t}, nil
	}
	return t, nil
}

// parseElementType parses an element type: a builtin keyword, a named type
// reference, or an inline struct or enum.
func (p *parser) parseElementType() (*Type, error) {
	keyword := p.keyword()
	switch keyword {
	case "bool", "int", "float", "string", "object", "any":
		return &Type{Kind: map[string]Kind{
			"bool":   Bool,
			"int":    Int,
			"float":  Float,
			"string": String,
			"object": Object,
			"any":    Any,
		}[keyword]}, nil
	case "":
		// Not a lowercase keyword; try a name or a struct.
	default:
		return nil, fmt.Errorf("idl: unknown type %q", keyword)
	}

	if p.peekByte('(') {
		return p.parseStructOrEnum()
	}
	if name := p.name(); name != "" {
		return &Type{Kind: Named, Name: name}, nil
	}
	return nil, fmt.Errorf("idl: invalid type at %q", p.rest())
}

// parseStructOrEnum parses `( field: type, ... )` or `( one, two, ... )`.
func (p *parser) parseStructOrEnum() (*Type, error) {
	p.pos++ // '('
	t := &Type{Kind: Struct}
	p.skip()
	if p.consumeByte(')') {
		return t, nil
	}
	for {
		p.skip()
		doc := p.takeDoc()
		field := Field{Doc: doc}

		field.Name = p.fieldName()
		if field.Name == "" {
			return nil, fmt.Errorf("idl: invalid field name at %q", p.rest())
		}
		p.skip()
		if p.consumeByte(':') {
			if t.Kind == Enum {
				return nil, fmt.Errorf("idl: mixed struct and enum fields")
			}
			p.skip()
			var err error
			field.Type, err = p.parseType()
			if err != nil {
				return nil, err
			}
		} else {
			if t.Kind == Struct && len(t.Fields) > 0 {
				return nil, fmt.Errorf("idl: mixed struct and enum fields")
			}
			t.Kind = Enum
		}
		t.Fields = append(t.Fields, field)

		p.skip()
		if p.consumeByte(',') {
			continue
		}
		if p.consumeByte(')') {
			return t, nil
		}
		return nil, fmt.Errorf("idl: expected ',' or ')' at %q", p.rest())
	}
}

// skip advances over whitespace, comments and line breaks. It reports whether
// input remains. Comments that start on their own line accumulate as pending
// documentation; any other input resets it.
func (p *parser) skip() bool {
	for p.pos < len(p.input) {
		switch c := p.input[p.pos]; c {
		case ' ', '\t', '\r':
			p.pos++
		case '\n':
			p.pos++
			p.lineStart = p.pos
			p.doc = p.doc[:0]
		case '#':
			p.comment()
		default:
			return true
		}
	}
	return false
}

// comment consumes a comment and its line break. A comment preceded only by
// whitespace on its line is documentation for the following declaration and
// accumulates in p.doc; a trailing comment is discarded.
func (p *parser) comment() {
	hash := p.pos
	p.pos++ // '#'
	if p.pos < len(p.input) && p.input[p.pos] == ' ' {
		p.pos++ // the conventional separator space is not part of the text
	}
	start := p.pos
	for p.pos < len(p.input) && p.input[p.pos] != '\n' {
		p.pos++
	}
	if strings.TrimSpace(p.input[p.lineStart:hash]) == "" {
		line := strings.TrimRight(p.input[start:p.pos], "\r")
		p.doc = append(p.doc, line)
	} else {
		p.doc = p.doc[:0]
	}
	if p.pos < len(p.input) {
		p.pos++ // '\n'
	}
	p.lineStart = p.pos
}

func (p *parser) takeDoc() string {
	doc := strings.Join(p.doc, "\n")
	p.doc = p.doc[:0]
	return doc
}

// keyword reads a lowercase keyword at the current position. It returns ""
// when the input does not start with a lowercase letter.
func (p *parser) keyword() string {
	start := p.pos
	for p.pos < len(p.input) && p.input[p.pos] >= 'a' && p.input[p.pos] <= 'z' {
		p.pos++
	}
	return p.input[start:p.pos]
}

// name reads a type, method or error name: [A-Z][A-Za-z0-9]*.
func (p *parser) name() string {
	start := p.pos
	if p.pos >= len(p.input) || !isUpper(p.input[p.pos]) {
		return ""
	}
	p.pos++
	for p.pos < len(p.input) && isAlnum(p.input[p.pos]) {
		p.pos++
	}
	return p.input[start:p.pos]
}

// fieldName reads a field name: [A-Za-z](_?[A-Za-z0-9])*.
func (p *parser) fieldName() string {
	start := p.pos
	if p.pos >= len(p.input) || !isLetter(p.input[p.pos]) {
		return ""
	}
	p.pos++
	for p.pos < len(p.input) {
		if p.input[p.pos] == '_' {
			// An underscore must be followed by an alphanumeric character.
			if p.pos+1 >= len(p.input) || !isAlnum(p.input[p.pos+1]) {
				break
			}
			p.pos += 2
			continue
		}
		if !isAlnum(p.input[p.pos]) {
			break
		}
		p.pos++
	}
	return p.input[start:p.pos]
}

// interfaceName reads an interface name in reverse-domain notation with at
// least two components.
func (p *parser) interfaceName() string {
	start := p.pos
	if !p.interfaceNameComponent(true) {
		p.pos = start
		return ""
	}
	components := 1
	for p.peekByte('.') {
		p.pos++
		if !p.interfaceNameComponent(false) {
			p.pos = start
			return ""
		}
		components++
	}
	if components < 2 || p.peekByte('-') {
		p.pos = start
		return ""
	}
	return p.input[start:p.pos]
}

// interfaceNameComponent reads one dot-separated component of an interface
// name. The first component must start with a letter; the others may also
// start with a digit. Within a component, hyphens must be followed by an
// alphanumeric character.
func (p *parser) interfaceNameComponent(first bool) bool {
	if p.pos >= len(p.input) {
		return false
	}
	c := p.input[p.pos]
	if !isLetter(c) && (first || !isDigit(c)) {
		return false
	}
	p.pos++
	for {
		hyphens := p.pos
		for p.pos < len(p.input) && p.input[p.pos] == '-' {
			p.pos++
		}
		if p.pos < len(p.input) && isAlnum(p.input[p.pos]) {
			p.pos++
			continue
		}
		p.pos = hyphens
		return true
	}
}

func (p *parser) peekByte(c byte) bool {
	return p.pos < len(p.input) && p.input[p.pos] == c
}

func (p *parser) consumeByte(c byte) bool {
	if p.peekByte(c) {
		p.pos++
		return true
	}
	return false
}

func (p *parser) consumeString(s string) bool {
	if strings.HasPrefix(p.input[p.pos:], s) {
		p.pos += len(s)
		return true
	}
	return false
}

// rest returns up to 16 bytes of unconsumed input, for error messages.
func (p *parser) rest() string {
	rest := p.input[p.pos:]
	if len(rest) > 16 {
		rest = rest[:16]
	}
	return rest
}

func isUpper(c byte) bool  { return c >= 'A' && c <= 'Z' }
func isLetter(c byte) bool { return isUpper(c) || c >= 'a' && c <= 'z' }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isAlnum(c byte) bool  { return isLetter(c) || isDigit(c) }
