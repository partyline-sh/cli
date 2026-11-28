package main

import (
	"fmt"
	"reflect"
	"testing"
)

// testLauncherHostActions records deferred operations without running tmux or forms.
func testLauncherHostActions(t *testing.T) {
	oldMerge, oldFocus, oldOpen := launcherMerge, launcherFocus, launcherOpen
	oldQuick, oldNew, oldSplit, oldRecover := launcherQuickNew, launcherNewMenu, launcherSplit, launcherRecover
	t.Cleanup(func() {
		launcherMerge, launcherFocus, launcherOpen = oldMerge, oldFocus, oldOpen
		launcherQuickNew, launcherNewMenu, launcherSplit, launcherRecover = oldQuick, oldNew, oldSplit, oldRecover
	})
	var calls []string
	launcherMerge = func(sp []Spec) (string, error) {
		calls = append(calls, fmt.Sprintf("merge:%d", len(sp)))
		return "%1", nil
	}
	launcherFocus = func(s string) error { calls = append(calls, "focus:"+s); return nil }
	launcherOpen = func(s aiSession, _ map[string]sessMeta) error { calls = append(calls, "open:"+s.ID); return nil }
	launcherQuickNew = func(b bool) error { calls = append(calls, fmt.Sprint("quick:", b)); return nil }
	launcherNewMenu = func() { calls = append(calls, "new") }
	launcherSplit = func(s []aiSession, _ map[string]sessMeta) error {
		calls = append(calls, fmt.Sprintf("split:%d", len(s)))
		return nil
	}
	launcherRecover = func(s aiSession, _ string, f func(aiSession)) { calls = append(calls, "recover"); f(s) }
	for _, tc := range []struct {
		action string
		want   []string
	}{
		{"open", []string{"open:24"}}, {"terminal", []string{"merge:1", "focus:%1"}}, {"open-many", []string{"merge:2", "focus:%1"}},
		{"recover", []string{"recover", "open:24"}}, {"new", []string{"new"}}, {"quick-new", []string{"quick:false"}}, {"quick-bypass", []string{"quick:true"}}, {"split", []string{"split:2"}},
	} {
		m := launcherActionFixture(t)
		m.action = tc.action
		cp := *m.menu.selected()
		m.picked = &cp
		m.sessions = m.menu.view[:2]
		calls = nil
		if err := m.finish(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(calls, tc.want) {
			t.Fatalf("%s: %v", tc.action, calls)
		}
	}
}
