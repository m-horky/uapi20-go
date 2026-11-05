package idl

import (
	"strings"
	"testing"
)

const specExample = `# Interface to jump a spacecraft to another point in space.
# The FTL Drive is the propulsion system to achieve
# faster-than-light travel through space.
interface org.example.ftl

# The current state of the FTL drive and the amount of
# fuel available to jump.
type DriveCondition (
  state: (idle, spooling, busy),
  tylium_level: int
)

# Speed, trajectory and jump duration is calculated prior
# to activating the FTL drive.
type DriveConfiguration (
  speed: int,
  trajectory: int,
  duration: int
)

# The galactic coordinates use the Sun as the origin.
type Coordinate (
  longitude: float,
  latitude: float,
  distance: int
)

# Monitor the drive. The method will reply with an update
# whenever the drive's state changes
method Monitor() -> (condition: DriveCondition)

# Calculate the drive's jump parameters from the current
# position to the target position in the galaxy
method CalculateConfiguration(
  current: Coordinate,
  target: Coordinate
) -> (configuration: DriveConfiguration)

# Jump to the calculated point in space
method Jump(configuration: DriveConfiguration) -> ()

# There is not enough tylium to jump with the given
# parameters
error NotEnoughEnergy ()

# The supplied parameters are outside the supported range
error ParameterOutOfRange (field: string)
`

func TestParseSpecExample(t *testing.T) {
	iface, err := Parse(specExample)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if iface.Name != "org.example.ftl" {
		t.Fatalf("Name = %q", iface.Name)
	}
	if !strings.HasPrefix(iface.Doc, "Interface to jump") {
		t.Fatalf("interface Doc = %q", iface.Doc)
	}
	if got, want := len(iface.Members), 8; got != want {
		t.Fatalf("Members len = %d, want %d", got, want)
	}

	coord, ok := iface.TypeDecl("Coordinate")
	if !ok {
		t.Fatal("TypeDecl(Coordinate) not found")
	}
	if coord.Doc != "The galactic coordinates use the Sun as the origin." {
		t.Fatalf("Coordinate Doc = %q", coord.Doc)
	}
	if got, want := coord.Type.String(), "(longitude: float, latitude: float, distance: int)"; got != want {
		t.Fatalf("Coordinate type = %q, want %q", got, want)
	}

	cond, ok := iface.TypeDecl("DriveCondition")
	if !ok {
		t.Fatal("TypeDecl(DriveCondition) not found")
	}
	state := cond.Type.Fields[0]
	if state.Name != "state" || state.Type.Kind != Enum {
		t.Fatalf("state field = %+v", state)
	}
	if got, want := state.Type.String(), "(idle, spooling, busy)"; got != want {
		t.Fatalf("state enum = %q, want %q", got, want)
	}

	method, ok := iface.Method("CalculateConfiguration")
	if !ok {
		t.Fatal("Method(CalculateConfiguration) not found")
	}
	if got, want := method.In.String(), "(current: Coordinate, target: Coordinate)"; got != want {
		t.Fatalf("CalculateConfiguration in = %q, want %q", got, want)
	}
	if method.Out.Fields[0].Type.Kind != Named || method.Out.Fields[0].Type.Name != "DriveConfiguration" {
		t.Fatalf("CalculateConfiguration out = %+v", method.Out)
	}

	energy, ok := iface.Error("NotEnoughEnergy")
	if !ok {
		t.Fatal("Error(NotEnoughEnergy) not found")
	}
	if energy.Type.Kind != Struct || len(energy.Type.Fields) != 0 {
		t.Fatalf("NotEnoughEnergy type = %+v", energy.Type)
	}
	outOfRange, ok := iface.Error("ParameterOutOfRange")
	if !ok {
		t.Fatal("Error(ParameterOutOfRange) not found")
	}
	if outOfRange.Type.String() != "(field: string)" {
		t.Fatalf("ParameterOutOfRange type = %q", outOfRange.Type.String())
	}
}

func TestParseAllTypes(t *testing.T) {
	description := `interface org.example.types

type MyType (
  example_bool: bool,
  example_int: int,
  example_float: float,
  example_string: string,
  example_object: object,
  example_any: any,
  example_enum: (one, two, three),
  example_struct: (first: int, second: string),
  example_array: []string,
  example_dictionary: [string]int,
  example_stringset: [string](),
  example_nullable: ?string,
  example_nullable_array_struct: ?[](first: int, second: string),
  example_nullable_dict: ?[string]int,
  example_other_type: MyOtherType
)

type MyOtherType (value: int)

method Echo(value: MyType) -> (value: MyType)
`
	iface, err := Parse(description)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	decl, ok := iface.TypeDecl("MyType")
	if !ok {
		t.Fatal("TypeDecl(MyType) not found")
	}
	want := map[string]string{
		"example_bool":                  "bool",
		"example_int":                   "int",
		"example_float":                 "float",
		"example_string":                "string",
		"example_object":                "object",
		"example_any":                   "any",
		"example_enum":                  "(one, two, three)",
		"example_struct":                "(first: int, second: string)",
		"example_array":                 "[]string",
		"example_dictionary":            "[string]int",
		"example_stringset":             "[string]()",
		"example_nullable":              "?string",
		"example_nullable_array_struct": "?[](first: int, second: string)",
		"example_nullable_dict":         "?[string]int",
		"example_other_type":            "MyOtherType",
	}
	for _, field := range decl.Type.Fields {
		if got := field.Type.String(); got != want[field.Name] {
			t.Errorf("field %s = %q, want %q", field.Name, got, want[field.Name])
		}
	}
	if got, want := len(decl.Type.Fields), len(want); got != want {
		t.Fatalf("fields len = %d, want %d", got, want)
	}
}

func TestParseFieldDocComments(t *testing.T) {
	description := `interface org.example.doc

# Method documentation.
method Example(
  # Field documentation.
  # Second line.
  value: string
) -> ()
`
	iface, err := Parse(description)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	method, _ := iface.Method("Example")
	if method.Doc != "Method documentation." {
		t.Fatalf("method Doc = %q", method.Doc)
	}
	if got, want := method.In.Fields[0].Doc, "Field documentation.\nSecond line."; got != want {
		t.Fatalf("field Doc = %q, want %q", got, want)
	}
}

func TestParseTrailingCommentIsNotDocumentation(t *testing.T) {
	description := "interface org.example.trail\n\nmethod First() -> () # trailing note\n\n# real doc\nmethod Second() -> ()\n"
	iface, err := Parse(description)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	first, _ := iface.Method("First")
	if first.Doc != "" {
		t.Fatalf("First Doc = %q, want empty", first.Doc)
	}
	second, _ := iface.Method("Second")
	if second.Doc != "real doc" {
		t.Fatalf("Second Doc = %q", second.Doc)
	}
}

func TestParseBlankLineDetachesDocumentation(t *testing.T) {
	description := "interface org.example.blank\n\n# dangling comment\n\nmethod Foo() -> ()\n"
	iface, err := Parse(description)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	method, _ := iface.Method("Foo")
	if method.Doc != "" {
		t.Fatalf("Foo Doc = %q, want empty", method.Doc)
	}
}

func TestParseInterfaceNames(t *testing.T) {
	valid := []string{
		"org.example",
		"org.example.ftl",
		"org.example7",
		"org.example-with-hyphens",
		"org.example--double",
		"a.b",
		"a1.b2",
	}
	for _, name := range valid {
		if _, err := Parse("interface " + name + "\n\nmethod Foo() -> ()\n"); err != nil {
			t.Errorf("interface name %q: %v", name, err)
		}
	}

	invalid := []string{
		"org",          // single component
		"1org.example", // first component must start with a letter
		"org..example",
		"org.example-",
		"org.-example",
		"org.ex ample",
	}
	for _, name := range invalid {
		if _, err := Parse("interface " + name + "\n\nmethod Foo() -> ()\n"); err == nil {
			t.Errorf("interface name %q unexpectedly accepted", name)
		}
	}
}

func TestParseFieldNames(t *testing.T) {
	valid := []string{"a", "A", "a1", "a_1", "client_id", "a_b_c"}
	for _, name := range valid {
		desc := "interface org.example.f\n\ntype T (" + name + ": int)\n"
		if _, err := Parse(desc); err != nil {
			t.Errorf("field name %q: %v", name, err)
		}
	}
	invalid := []string{"_a", "a_", "a__b", "1a"}
	for _, name := range invalid {
		desc := "interface org.example.f\n\ntype T (" + name + ": int)\n"
		if _, err := Parse(desc); err == nil {
			t.Errorf("field name %q unexpectedly accepted", name)
		}
	}
}

func TestParseRejectsInvalidDescriptions(t *testing.T) {
	tests := []struct {
		name        string
		description string
	}{
		{"empty", ""},
		{"missing interface keyword", "method Foo() -> ()"},
		{"unknown keyword", "interface org.example\n\nstruct Foo ()\n"},
		{"duplicate member", "interface org.example\n\nmethod Foo() -> ()\n\nerror Foo ()\n"},
		{"method without arrow", "interface org.example\n\nmethod Foo ()\n"},
		{"double maybe", "interface org.example\n\ntype T (v: ??int)\n"},
		{"maybe struct alias", "interface org.example\n\ntype T ?int\n"},
		{"unknown type reference", "interface org.example\n\ntype T (v: Missing)\n"},
		{"cyclic type", "interface org.example\n\ntype A (b: B)\n\ntype B (a: A)\n"},
		{"mixed struct enum", "interface org.example\n\ntype T (a: int, b)\n"},
		{"non-struct method input", "interface org.example\n\nmethod Foo (int) -> ()\n"},
		{"lowercase method name", "interface org.example\n\nmethod foo () -> ()\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.description); err == nil {
				t.Fatalf("Parse() unexpectedly succeeded")
			}
		})
	}
}

func TestParseEmptyInterface(t *testing.T) {
	iface, err := Parse("interface org.example.empty\n")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(iface.Members) != 0 {
		t.Fatalf("Members = %v, want none", iface.Members)
	}
}

func TestValidateCall(t *testing.T) {
	description := `interface org.example.validate

type State (name: string, level: ?int)

method Simple(flag: bool, count: int, name: string) -> ()
method Nested(state: State, tags: []string, mapping: [string]int) -> ()
method NoParams() -> ()
method Enums(mode: (one, two, three)) -> ()
method AnyValue(value: any, payload: object) -> ()
method MultiLine(
  a: int,
  b: ?int
) -> ()
`
	iface, err := Parse(description)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	tests := []struct {
		name       string
		method     string
		parameters string
		wantField  string
	}{
		{"valid simple", "Simple", `{"flag":true,"count":1,"name":"x"}`, ""},
		{"absent parameters for required fields", "Simple", ``, "flag"},
		{"null parameters for required fields", "Simple", `null`, "flag"},
		{"empty parameters for required fields", "Simple", `{}`, "flag"},
		{"missing field", "Simple", `{"flag":true,"count":1}`, "name"},
		{"unknown field", "Simple", `{"flag":true,"count":1,"name":"x","extra":1}`, "extra"},
		{"wrong scalar type", "Simple", `{"flag":1,"count":1,"name":"x"}`, "flag"},
		{"int with fraction", "Simple", `{"flag":true,"count":1.5,"name":"x"}`, "count"},
		{"int with exponent", "Simple", `{"flag":true,"count":1e3,"name":"x"}`, "count"},
		{"string as int", "Simple", `{"flag":true,"count":"1","name":"x"}`, "count"},
		{"nullable absent", "MultiLine", `{"a":1}`, ""},
		{"nullable null", "MultiLine", `{"a":1,"b":null}`, ""},
		{"nullable present", "MultiLine", `{"a":1,"b":2}`, ""},
		{"valid nested", "Nested", `{"state":{"name":"x"},"tags":[],"mapping":{}}`, ""},
		{"nested unknown field", "Nested", `{"state":{"name":"x","bogus":1},"tags":[],"mapping":{}}`, "state.bogus"},
		{"nested missing field", "Nested", `{"state":{"level":1},"tags":[],"mapping":{}}`, "state.name"},
		{"nested nullable field", "Nested", `{"state":{"name":"x","level":null},"tags":[],"mapping":{}}`, ""},
		{"array element type", "Nested", `{"state":{"name":"x"},"tags":[1],"mapping":{}}`, "tags"},
		{"map value type", "Nested", `{"state":{"name":"x"},"tags":[],"mapping":{"k":"v"}}`, "mapping.k"},
		{"empty for no params", "NoParams", ``, ""},
		{"extra for no params", "NoParams", `{"x":1}`, "x"},
		{"valid enum", "Enums", `{"mode":"two"}`, ""},
		{"unknown enum value", "Enums", `{"mode":"four"}`, "mode"},
		{"enum not a string", "Enums", `{"mode":2}`, "mode"},
		{"any accepts values", "AnyValue", `{"value":[1,2],"payload":{"a":1}}`, ""},
		{"any rejects null", "AnyValue", `{"value":null,"payload":{}}`, "value"},
		{"object rejects array", "AnyValue", `{"value":1,"payload":[]}`, "payload"},
		{"state not an object", "Nested", `{"state":5,"tags":[],"mapping":{}}`, "state"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := iface.ValidateCall(tt.method, []byte(tt.parameters))
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("ValidateCall() error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateCall() unexpectedly succeeded")
			}
			verr, ok := err.(*ValidationError)
			if !ok {
				t.Fatalf("ValidateCall() error = %T, want *ValidationError", err)
			}
			if verr.Field != tt.wantField {
				t.Fatalf("ValidationError.Field = %q, want %q (reason: %s)", verr.Field, tt.wantField, verr.Reason)
			}
		})
	}

	if err := iface.ValidateCall("Missing", nil); err == nil {
		t.Fatal("ValidateCall(undeclared method) unexpectedly succeeded")
	}
}

func TestValidateCallEmptyCollectionIsNotNull(t *testing.T) {
	description := "interface org.example.nullable\n\ntype T (v: ?[]int)\n\nmethod Call(value: T) -> ()\n"
	iface, err := Parse(description)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if err := iface.ValidateCall("Call", []byte(`{"value":{"v":[]}}`)); err != nil {
		t.Fatalf("empty array as nullable value: %v", err)
	}
}

func TestParseCertificationInterface(t *testing.T) {
	description := `interface org.varlink.certification

type Interface (
  foo: ?[]?[string](foo, bar, baz),
  anon: (foo: bool, bar: bool)
)

type MyType (
  object: object,
  enum: (one, two, three),
  struct: (first: int, second: string),
  array: []string,
  dictionary: [string]string,
  stringset: [string](),
  nullable: ?string,
  nullable_array_struct: ?[](first: int, second: string),
  interface: Interface
)

method Start() -> (client_id: string)

method Test10(client_id: string, mytype: MyType) -> (string: string)

error CertificationError (wants: object, got: object)
`
	iface, err := Parse(description)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	decl, _ := iface.TypeDecl("Interface")
	if got, want := decl.Type.Fields[0].Type.String(), "?[]?[string](foo, bar, baz)"; got != want {
		t.Fatalf("foo type = %q, want %q", got, want)
	}
	myType, _ := iface.TypeDecl("MyType")
	ifaceField := myType.Type.Fields[8]
	if ifaceField.Name != "interface" || ifaceField.Type.Kind != Named {
		t.Fatalf("interface field = %+v", ifaceField)
	}
}
