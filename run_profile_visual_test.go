package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// flagValue returns the value following flag in argv (or "", true when the flag is present without a
// following value), and whether the flag appears at all.
func flagValue(argv []string, flag string) (val string, present bool) {
	for i, a := range argv {
		if a == flag {
			if i+1 < len(argv) {
				return argv[i+1], true
			}
			return "", true
		}
	}
	return "", false
}

// resolveRun is the daemon-side chokepoint. When the web toggles visual verify on, it must append a
// FIXED --visual flag and a --visual-routes DATA file — and that file must contain ONLY the routes
// that survive safeVisualRoutes (a flag-smuggling route is dropped, never written).
func TestResolveRunVisual(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", t.TempDir()) // isolate stateDir() (worklist + routes files)
	reg := daemonRegistry{Projects: []daemonProject{{Label: "proj", Path: tmp, Preset: "spec"}}}

	// Toggle ON with a mix of safe + poison routes.
	ref := runRef{
		ProjectLabel: "proj",
		ThreadID:     "plt-thr-1",
		Tasks:        []string{"do x"},
		VisualVerify: true,
		VisualRoutes: []string{"/dashboard", "-rf", "/settings", "/$(id)"},
	}
	argv, _, err := resolveRun(reg, ref)
	if err != nil {
		t.Fatalf("resolveRun: %v", err)
	}
	if _, ok := flagValue(argv, "--visual"); !ok {
		t.Fatalf("VisualVerify=true → --visual expected, argv=%v", argv)
	}
	routesFile, ok := flagValue(argv, "--visual-routes")
	if !ok || routesFile == "" {
		t.Fatalf("expected --visual-routes <file>, argv=%v", argv)
	}
	b, err := os.ReadFile(routesFile)
	if err != nil {
		t.Fatalf("read routes file: %v", err)
	}
	got := strings.Fields(strings.TrimSpace(string(b)))
	want := []string{"/dashboard", "/settings"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routes file = %q, want only the safe routes %q (poison dropped)", got, want)
	}

	// Toggle OFF → no --visual, no --visual-routes (parity with the pre-T2d behaviour).
	off := runRef{ProjectLabel: "proj", ThreadID: "plt-thr-1", Tasks: []string{"do x"}}
	argv, _, err = resolveRun(reg, off)
	if err != nil {
		t.Fatalf("resolveRun (off): %v", err)
	}
	if _, ok := flagValue(argv, "--visual"); ok {
		t.Fatalf("VisualVerify=false → no --visual, argv=%v", argv)
	}
}

// safeVisualRoutes is the daemon-side gate on web-supplied screenshot routes (T2d). It is the load-
// bearing wall between "the web picks which pages to shoot" (allowed DATA) and "the web smuggles a
// flag or command into the render" (forbidden). These cases pin that only unambiguously-safe app
// paths survive — anything a shell or argv could reinterpret is dropped.
func TestSafeVisualRoutes(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"plain path kept", []string{"/dashboard"}, []string{"/dashboard"}},
		{"root kept", []string{"/"}, []string{"/"}},
		{"nested + query kept", []string{"/app/board?tab=todo&q=x"}, []string{"/app/board?tab=todo&q=x"}},
		{"trimmed", []string{"  /settings  "}, []string{"/settings"}},
		{"multiple kept in order", []string{"/a", "/b"}, []string{"/a", "/b"}},

		// --- dropped (unsafe) ---
		{"leading dash (flag smuggle) dropped", []string{"-rf"}, nil},
		{"no leading slash dropped", []string{"dashboard"}, nil},
		{"parent traversal dropped", []string{"/../etc/passwd"}, nil},
		{"dot segment dropped", []string{"/./x"}, nil},
		{"whitespace-injection dropped", []string{"/a b"}, nil},
		{"shell subst dropped", []string{"/$(whoami)"}, nil},
		{"backtick dropped", []string{"/`id`"}, nil},
		{"semicolon dropped", []string{"/a;rm -rf /"}, nil},
		{"pipe dropped", []string{"/a|b"}, nil},
		{"newline injection dropped", []string{"/a\n--visual"}, nil},
		{"quote dropped", []string{"/a\"b"}, nil},
		{"empty dropped", []string{""}, nil},

		// mixed: survivors preserved, poison dropped
		{"mixed keeps only safe", []string{"/ok", "-evil", "/also-ok", "/$(x)"}, []string{"/ok", "/also-ok"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := safeVisualRoutes(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("safeVisualRoutes(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
