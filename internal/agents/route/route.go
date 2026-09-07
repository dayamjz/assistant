package route

import (
	"reflect"
	"sort"

	"github.com/dayamjz/assistant/internal/agents"
)

// Package route answers one question about a type: can a caller holding it
// reach a fixer session. It is the general form of the P4 check, and it is a
// package rather than a helper in one test because two packages ask it -
// internal/agents of its StageAgent and internal/stages of the dependencies it
// hands a stage body - and two copies of a rule drift.
//
// It exists because the specific form of the check is not enough.
//
// Asserting that a value does not assert to agents.Runner answers "is this a
// Runner", and the regression that matters is "can a body get one from this".
// An agents.StageAgent given a Runner() accessor is not a Runner and hands one
// over on request, so every such assertion holds while P4 is entirely gone.
// That is not a hypothetical: the accessor was added and those assertions were
// watched passing.
//
// So the guarantee is asked of the type graph instead: from a root type, walk
// what a caller in another package can actually reach - exported fields, and
// the results of exported methods - and report anything that is, or yields, a
// route to a fixer session. That catches a route added later by someone who
// never read this file, which an enumerated list of assertions cannot.
//
// It reads types rather than source text, so it is a typed model of the rule
// and not a pattern match over the code.
//
// A caller must pair a "finds nothing" assertion with one over a type that
// really does expose a route, or a walk that stopped inspecting anything would
// report the guarantee forever. agents.Resolution is that control.

// fixerRoutes are the three types a stage body must not be able to obtain, and
// why each one is a session.
func fixerRoutes() map[reflect.Type]string {
	return map[reflect.Type]string{
		reflect.TypeOf((*agents.Runner)(nil)).Elem():        "an agents.Runner, which agents.OpenFixer opens a fixer session from",
		reflect.TypeOf((*agents.SessionRunner)(nil)).Elem(): "an agents.SessionRunner, whose Fixer method opens a session directly",
		reflect.TypeOf((*agents.Fixer)(nil)).Elem():         "an agents.Fixer, which is the session itself",
	}
}

// ToFixerSession reports every way a caller outside the declaring package can
// reach a fixer session starting from root, each as a path a reader can
// follow. An empty result means there is none.
//
// A caller pairs it with a walk over a type that really does expose a route,
// because a walk that stopped inspecting anything would report an empty result
// and so report the guarantee forever.
//
// What it walks is what a caller can reach: exported fields, because a caller
// can read one, and the results of exported methods, because a caller can call
// one. Unexported fields are deliberately not walked - holding a Runner in one
// is exactly how StageAgent keeps its guarantee, and reporting that as a route
// would make the check contradict the mechanism it is checking. That is also
// why this bounds a caller in another package and not code inside the
// declaring one, which agents.StageAgent's own documentation states.
//
// A value that is not itself a session may still yield one, so every shape a
// Go value can be held in is followed: a pointer, slice, array or channel
// element, a map's key as well as its value, and a function's results. The
// function case is not decoration - a constructor-valued field is how a
// service already hands a fixer over, so a deps struct is one field away from
// it. A value's address is reachable too, so a method set only the pointer has
// counts as the caller's.
//
// Two of those overstate slightly and do so on purpose. A send-only channel
// cannot be received from and an unbuffered one may never carry a value, and
// both are walked anyway: a guard that overstates fails loudly at the shape
// that has to be argued about, while one that understates passes in silence.
//
// Method and function parameters are not walked. A method that takes a Runner
// is not a way to obtain one: a caller would need it already.
func ToFixerSession(root reflect.Type) []string {
	forbidden := fixerRoutes()
	var found []string
	seen := map[reflect.Type]bool{}

	var walk func(t reflect.Type, path string)
	walk = func(t reflect.Type, path string) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true

		for iface, why := range forbidden {
			if t.Implements(iface) {
				found = append(found, path+" is "+why)
				return
			}
		}
		// A caller can take the address of anything it holds, so a method set
		// carried by the pointer is a method set the caller has. Asking this of
		// an interface or a pointer would ask it of a type with no methods.
		if t.Kind() != reflect.Interface && t.Kind() != reflect.Pointer {
			for iface, why := range forbidden {
				if reflect.PointerTo(t).Implements(iface) {
					found = append(found, "a pointer to "+path+" is "+why)
					return
				}
			}
		}
		if t.Kind() == reflect.Interface && t.NumMethod() == 0 {
			found = append(found, path+" is an empty interface, so it can carry any of them")
			return
		}

		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
			walk(t.Elem(), path+" element")
		case reflect.Map:
			walk(t.Key(), path+" key")
			walk(t.Elem(), path+" value")
		case reflect.Func:
			for out := range t.NumOut() {
				walk(t.Out(out), path+"()")
			}
		case reflect.Struct:
			for i := range t.NumField() {
				if field := t.Field(i); field.IsExported() {
					walk(field.Type, path+"."+field.Name)
				}
			}
		}

		// Methods are asked of the value and of the pointer, so a route behind
		// a pointer receiver is not missed. A pointer to an interface has no
		// method set worth asking, so it is skipped rather than walked.
		method := []reflect.Type{t}
		if t.Kind() != reflect.Interface && t.Kind() != reflect.Pointer {
			method = append(method, reflect.PointerTo(t))
		}
		for _, mt := range method {
			for i := range mt.NumMethod() {
				m := mt.Method(i)
				for out := range m.Type.NumOut() {
					walk(m.Type.Out(out), path+"."+m.Name+"()")
				}
			}
		}
	}

	walk(root, root.String())
	sort.Strings(found)
	return found
}
