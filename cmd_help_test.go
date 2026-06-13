package velocity

import (
	"reflect"
	"strings"
	"testing"
)

// printHelp writes straight to stdout through the velocity-cli helpers, so
// these tests exercise the underlying data and pure functions the renderer
// consumes (section layout, command placement, pad width) rather than
// capturing styled stdout.

// wantHelpSections is the help layout printHelp renders, in order. The empty
// title at the end is the hidden group (help aliases + internal serve:run):
// registered for dispatch but omitted from help output.
var wantHelpSections = []commandSection{
	{title: "Server", cmds: []command{
		serveCmd{}, buildCmd{}, downCmd{}, upCmd{},
	}},
	{title: "Database", cmds: []command{
		migrateCmd{}, migrateFreshCmd{}, migrateRollbackCmd{}, migrateStatusCmd{}, dbWipeCmd{},
	}},
	{title: "Queue & Scheduler", cmds: []command{
		queueWorkCmd{}, scheduleWorkCmd{},
	}},
	{title: "Cache", cmds: []command{
		cacheClearCmd{},
	}},
	{title: "Code Generation", cmds: []command{
		makeHandlerCmd{}, makeModelCmd{}, makeMigrationCmd{}, makeMiddlewareCmd{},
		makeEventCmd{}, makeListenerCmd{}, makeJobCmd{}, makeMailCmd{},
		makeNotificationCmd{}, makeResourceCmd{}, makePolicyCmd{}, makeProviderCmd{},
		makeCommandCmd{}, makeGRPCServiceCmd{}, makeGRPCRPCCmd{}, makeGRPCGenCmd{},
	}},
	{title: "Custom Commands", cmds: []command{
		runCmd{},
	}},
	{title: "Other", cmds: []command{
		routeListCmd{}, keyGenerateCmd{},
	}},
	{title: "", cmds: []command{
		helpCmd{name_: "help"}, helpCmd{name_: "--help"}, helpCmd{name_: "-h"}, serveRunCmd{},
	}},
}

// TestHelpSections_TitlesAndOrder asserts the registry's section titles and
// per-section command order exactly match the documented help layout.
func TestHelpSections_TitlesAndOrder(t *testing.T) {
	got := newCommandRegistry().sections

	if len(got) != len(wantHelpSections) {
		t.Fatalf("section count = %d, want %d", len(got), len(wantHelpSections))
	}
	for i, want := range wantHelpSections {
		if got[i].title != want.title {
			t.Errorf("section[%d] title = %q, want %q", i, got[i].title, want.title)
		}
		gotNames := tokenNames(got[i].cmds)
		wantNames := tokenNames(want.cmds)
		if !reflect.DeepEqual(gotNames, wantNames) {
			t.Errorf("section %q commands = %v, want %v", want.title, gotNames, wantNames)
		}
	}
}

// TestRegistryOrder_FlattensSections pins reg.order to the flattened section
// sequence, proving the sections are the single source of truth for registry
// iteration (no separate order list to keep in sync).
func TestRegistryOrder_FlattensSections(t *testing.T) {
	want := []string{
		"serve", "build", "down", "up",
		"migrate", "migrate:fresh", "migrate:rollback", "migrate:status",
		"db:wipe",
		"queue:work", "schedule:work",
		"cache:clear",
		"make:handler", "make:model", "make:migration", "make:middleware",
		"make:event", "make:listener", "make:job", "make:mail",
		"make:notification", "make:resource", "make:policy", "make:provider",
		"make:command", "make:grpc:service", "make:grpc:rpc", "make:grpc:gen",
		"run",
		"route:list", "key:generate",
		"help", "--help", "-h",
		"serve:run",
	}
	got := tokenNames(newCommandRegistry().order)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reg.order = %v, want %v", got, want)
	}
}

// TestHelpSections_VisibleCommandPlacement asserts every command with a
// non-empty description appears in exactly one titled section, and every
// hidden command (empty description) appears in no titled section.
func TestHelpSections_VisibleCommandPlacement(t *testing.T) {
	reg := newCommandRegistry()

	titledCount := map[string]int{} // command name -> # titled sections containing it
	for _, sec := range reg.sections {
		if sec.title == "" {
			continue
		}
		for _, c := range sec.cmds {
			titledCount[c.name()]++
		}
	}

	for _, c := range reg.order {
		visible := strings.TrimSpace(c.description()) != ""
		n := titledCount[c.name()]
		switch {
		case visible && n != 1:
			t.Errorf("visible command %q appears in %d titled sections, want exactly 1", c.name(), n)
		case !visible && n != 0:
			t.Errorf("hidden command %q appears in %d titled sections, want 0", c.name(), n)
		}
	}
}

// TestHelpSections_EveryCommandInExactlyOneSection asserts the section list is
// a complete, non-overlapping partition of reg.order - no command is dropped
// from help-layout coverage and none is double-listed.
func TestHelpSections_EveryCommandInExactlyOneSection(t *testing.T) {
	reg := newCommandRegistry()

	count := map[string]int{}
	for _, sec := range reg.sections {
		for _, c := range sec.cmds {
			count[c.name()]++
		}
	}
	for _, c := range reg.order {
		if count[c.name()] != 1 {
			t.Errorf("command %q appears in %d sections, want exactly 1", c.name(), count[c.name()])
		}
	}
	if len(count) != len(reg.order) {
		t.Errorf("sections cover %d distinct commands, order has %d", len(count), len(reg.order))
	}
}

// TestHelpPadWidth asserts helpPadWidth equals the longest help-visible token
// plus two, and that the current longest token (make:notification /
// make:grpc:service, 17 chars) yields width 19.
func TestHelpPadWidth(t *testing.T) {
	reg := newCommandRegistry()

	max := 0
	for _, sec := range reg.sections {
		if sec.title == "" {
			continue
		}
		for _, c := range sec.cmds {
			if c.description() == "" {
				continue
			}
			if n := len(usageToken(c)); n > max {
				max = n
			}
		}
	}

	if got := reg.helpPadWidth(); got != max+2 {
		t.Errorf("helpPadWidth() = %d, want maxlen+2 = %d", got, max+2)
	}
	if max != 17 {
		t.Errorf("longest visible token = %d chars, want 17 (make:grpc:service/make:notification)", max)
	}
	if got := reg.helpPadWidth(); got != 19 {
		t.Errorf("helpPadWidth() = %d, want 19", got)
	}
}

// TestRunCmd_UsageToken asserts the run command renders "run <command>" in
// help while keeping "run" as its dispatch name.
func TestRunCmd_UsageToken(t *testing.T) {
	if got := usageToken(runCmd{}); got != "run <command>" {
		t.Errorf("usageToken(runCmd) = %q, want %q", got, "run <command>")
	}
	if got := (runCmd{}).name(); got != "run" {
		t.Errorf("runCmd.name() = %q, want %q", got, "run")
	}
}

// TestUsageToken_FallsBackToName asserts commands not implementing
// usageTokener render their name() as the help token.
func TestUsageToken_FallsBackToName(t *testing.T) {
	if got := usageToken(serveCmd{}); got != "serve" {
		t.Errorf("usageToken(serveCmd) = %q, want %q", got, "serve")
	}
}

func tokenNames(cmds []command) []string {
	names := make([]string, len(cmds))
	for i, c := range cmds {
		names[i] = c.name()
	}
	return names
}
