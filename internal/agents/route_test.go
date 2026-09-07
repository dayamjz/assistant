package agents_test

import (
	"reflect"
	"sort"

	"github.com/dayamjz/assistant/internal/agents"
)

// This file is the general form of the P4 question, and it exists because the
// specific form is not enough.
//
// Asserting that a value does not assert to agents.Runner answers "is this a
// Runner", and the regression that matters is "can a body get one from this".
// A StageAgent given a Runner() accessor is not a Runner and hands one over on
// request, so every assertion in stage_test.go holds while P4 is gone. That
// was checked by adding the accessor and watching those tests stay green.
//
// So the guarantee is asked of the type graph instead: from a root type, walk
// what a caller in another package can actually reach - exported fields, and
// the results of exported methods - and report anything that is, or yields, a
// route to a fixer session. That catches a route added later by someone who
// never read this file, which an enumerated list of assertions cannot.
//
// It reads types rather than source text, so it is a typed model of the rule
// and not a pattern match over the code.

// fixerRoutes are the three types a stage body must not be able to obtain, and
// why each one is a session.
func fixerRoutes() map[reflect.Type]string {
	return map[reflect.Type]string{
		reflect.TypeOf((*agents.Runner)(nil)).Elem():        "an agents.Runner, which agents.OpenFixer opens a fixer session from",
		reflect.TypeOf((*agents.SessionRunner)(nil)).Elem(): "an agents.SessionRunner, whose Fixer method opens a session directly",
		reflect.TypeOf((*agents.Fixer)(nil)).Elem():         "an agents.Fixer, which is the session itself",
	}
}

// routesToAFixerSession reports every way a caller outside the declaring
// package can reach a fixer session starting from root, each as a path a
// reader can follow. An empty result means there is none.
//
// What it walks is what a caller can reach: exported fields, because a caller
// can read one, and the results of exported methods, because a caller can call
// one. Unexported fields are deliberately not walked - holding a Runner in one
// is exactly how StageAgent keeps its guarantee, and reporting that as a route
// would make the check contradict the mechanism it is checking. That is also
// why this bounds a caller in another package and not code inside the
// declaring one, which agents.StageAgent's own documentation states.
//
// Method parameters are not walked either. A method that takes a Runner is not
// a way to obtain one: a caller would need it already.
func routesToAFixerSession(root reflect.Type) []string {
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
		if t.Kind() == reflect.Interface && t.NumMethod() == 0 {
			found = append(found, path+" is an empty interface, so it can carry any of them")
			return
		}

		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			walk(t.Elem(), path+" element")
		case reflect.Map:
			walk(t.Elem(), path+" value")
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
