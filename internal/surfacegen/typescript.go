package surfacegen

import (
	"bytes"
	"fmt"
	"strings"

	"partyline.sh/partyline/internal/surface"
)

// genTypeScript emits the vocabularies as TypeScript unions plus runtime guards.
//
// This is the artifact that closes constraint #195. The run-preset allowlist lives in
// web/src/lib/api/runs.ts as an inline array literal, and the same list lives in the
// runs_preset_check constraint; adding a value to one and not the other makes the insert fail with
// 23514 and the endpoint 500. Both become projections of internal/surface, and the drift check
// fails the build if either is edited independently.
//
// The guards matter as much as the types: a union type is erased at runtime, so validating an
// inbound value needs a real array to test against. Emitting both from one declaration is what
// stops a route accepting a value the database will reject.
func genTypeScript() []byte {
	var b bytes.Buffer
	b.WriteString(header("//"))
	b.WriteString("\n")

	for _, v := range surface.All() {
		name := pascal(v.Name)
		b.WriteString("\n// " + wrapComment(v.Doc, "// ") + "\n")
		fmt.Fprintf(&b, "export const %s = [\n", constName(v.Name))
		for _, t := range v.Terms {
			fmt.Fprintf(&b, "  %q, // %s\n", t.Key, oneLine(t.Doc))
		}
		b.WriteString("] as const;\n")
		fmt.Fprintf(&b, "export type %s = (typeof %s)[number];\n", name, constName(v.Name))
		fmt.Fprintf(&b, "export function is%s(v: string): v is %s {\n", name, name)
		fmt.Fprintf(&b, "  return (%s as readonly string[]).includes(v);\n}\n", constName(v.Name))
	}

	// The retry disposition, as data rather than a switch nobody remembers to extend. An unknown
	// code resolves to "hard" for the same reason the Go side does: an outcome we do not recognise
	// is not one we may silently retry.
	b.WriteString("\n// Retry disposition per gate code. An undeclared code is HARD by design —\n")
	b.WriteString("// never retry a failure nobody declared.\n")
	b.WriteString("export const GATE_CODE_CLASS: Record<string, Class> = {\n")
	for _, t := range surface.GateCode.Terms {
		fmt.Fprintf(&b, "  %q: %q,\n", t.Key, string(t.Class))
	}
	b.WriteString("};\n")
	b.WriteString("export type Class = \"none\" | \"transient\" | \"hard\";\n")
	b.WriteString("export function classOf(code: string): Class {\n")
	b.WriteString("  return GATE_CODE_CLASS[code] ?? \"hard\";\n}\n")

	return b.Bytes()
}

// pascal turns run_status into RunStatus.
func pascal(s string) string {
	var b strings.Builder
	for _, part := range strings.Split(s, "_") {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String()
}

// constName turns run_status into RUN_STATUSES — the plural const holding the members.
func constName(s string) string {
	up := strings.ToUpper(s)
	switch {
	case strings.HasSuffix(up, "S"), strings.HasSuffix(up, "X"):
		return up + "ES"
	default:
		return up + "S"
	}
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// wrapComment reflows a doc string to ~96 columns with the given continuation prefix.
func wrapComment(s, prefix string) string {
	words := strings.Fields(s)
	var lines []string
	cur := ""
	for _, w := range words {
		if cur != "" && len(cur)+1+len(w) > 96 {
			lines = append(lines, cur)
			cur = w
			continue
		}
		if cur == "" {
			cur = w
		} else {
			cur += " " + w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return strings.Join(lines, "\n"+prefix)
}
