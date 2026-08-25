package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestVisibleLenSingleWidth pins visibleLen to the terminal's actual rendering.
//
// The target environment was measured directly with a cursor-position probe
// (DSR ESC[6n): every glyph the statusline uses renders as a single column —
// the Nerd Font powerline caps, the ctx circles, the effort gauge dots,
// the vertical ellipsis, the pill icons. Only genuine East Asian Wide code
// points (e.g. CJK) occupy two columns. visibleLen must reflect that, because
// over-counting shrinks the bars (right-edge gap) and under-counting wraps the
// rightmost label.
func TestVisibleLenSingleWidth(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"ctx icons", "●◑◕◔", 4},
		{"effort gauge dots", "●●●○○", 5},
		{"bar separator", "⋮", 1},
		{"pill icons", "⏱✎", 2},
		{"powerline caps", "", 2},
		{"compacted ctx icon", "↻", 1},
		{"sync/branch/stash", "↑⇅⎇⚑", 4},
		{"bar dashes", "━━━", 3},
		{"ascii", "abc", 3},
	}
	for _, c := range cases {
		if got := visibleLen(c.in); got != c.want {
			t.Errorf("visibleLen(%q) [%s] = %d, want %d", c.in, c.name, got, c.want)
		}
	}
}

// TestVisibleLenStripsANSI ensures color codes never count toward width.
func TestVisibleLenStripsANSI(t *testing.T) {
	if got := visibleLen("\x1b[31m●\x1b[0m"); got != 1 {
		t.Errorf("visibleLen with ANSI wrapper = %d, want 1", got)
	}
}

// captureStdout runs f and returns everything it wrote to os.Stdout.
func captureStdout(f func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	f()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

// TestLine2NoOverflowLongBranch guards against an over-long branch name pushing
// the right-side pills off the edge. The branch is the one unbounded field on
// line 2 (the path is length-capped), so without truncation a long ref (e.g. a
// dependabot branch) overflows targetW on narrower widths. Probe-verified
// single-width glyphs mean visibleLen == true rendered width here.
func TestLine2NoOverflowLongBranch(t *testing.T) {
	cases := []int{80, 90, 100, 120, 160}
	for _, cols := range cases {
		t.Setenv("COLUMNS", strconv.Itoa(cols))
		git := &GitInfo{
			Branch:   "dependabot/composer/dev-dependencies-cc3e4e928d",
			ModCount: 1,
			Age:      "18h",
			Sync:     "=",
		}
		out := captureStdout(func() {
			renderStatusLine(git, WindowInfo{Pct: 11, TimeLeft: "2h 21m left"}, 11,
				"high", "Claude Opus 4.8", "/tmp/focus-service-insights-reports", false)
		})
		targetW := cols - renderSafetyMargin
		for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			if w := visibleLen(line); w > targetW {
				t.Errorf("COLUMNS=%d: line exceeds targetW=%d (width=%d): %q",
					cols, targetW, w, ansiRe.ReplaceAllString(line, ""))
			}
		}
	}
}

// TestMaxWidthCap verifies the statusline caps its rendered width on wide
// terminals (default 120), honors the CLAUDE_STATUSLINE_MAX_WIDTH override, and
// does not shrink a narrow terminal below its available width.
func TestMaxWidthCap(t *testing.T) {
	git := &GitInfo{Branch: "main", Age: "5m", Sync: "="}
	render := func() string {
		return captureStdout(func() {
			renderStatusLine(git, WindowInfo{Pct: 50, TimeLeft: "2h left"}, 50,
				"high", "Claude Opus 4.8", "/tmp", false)
		})
	}
	maxLineWidth := func(out string) int {
		m := 0
		for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			if w := visibleLen(line); w > m {
				m = w
			}
		}
		return m
	}

	// Wide terminal, default cap: no rendered line may exceed defaultMaxWidth.
	t.Setenv("COLUMNS", "250")
	t.Setenv("CLAUDE_STATUSLINE_MAX_WIDTH", "")
	if w := maxLineWidth(render()); w > defaultMaxWidth {
		t.Errorf("wide terminal, default cap: max line width %d exceeds %d", w, defaultMaxWidth)
	}

	// Env override raises the cap.
	t.Setenv("CLAUDE_STATUSLINE_MAX_WIDTH", "160")
	if w := maxLineWidth(render()); w <= defaultMaxWidth || w > 160 {
		t.Errorf("override 160: expected max line width in (%d, 160], got %d", defaultMaxWidth, w)
	}

	// Narrow terminal: the cap must not shrink it below the available width.
	t.Setenv("COLUMNS", "90")
	t.Setenv("CLAUDE_STATUSLINE_MAX_WIDTH", "")
	if w := maxLineWidth(render()); w != 90-renderSafetyMargin {
		t.Errorf("narrow terminal: expected full width %d, got %d", 90-renderSafetyMargin, w)
	}
}

// TestEffortFromPayload pins the field that broke before: Claude Code sends the
// effort level as a nested object ("effort": {"level": ...}), not a flat
// "effortLevel" string, and omits the object entirely when the model has no
// effort parameter. A nil Input.Effort must mean "no effort", not a default.
func TestEffortFromPayload(t *testing.T) {
	var withEffort Input
	if err := json.Unmarshal([]byte(`{"effort":{"level":"xhigh"}}`), &withEffort); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if withEffort.Effort == nil || withEffort.Effort.Level != "xhigh" {
		t.Errorf("nested effort.level not parsed: got %+v", withEffort.Effort)
	}

	var noEffort Input
	if err := json.Unmarshal([]byte(`{"model":"Claude Haiku 4.5"}`), &noEffort); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if noEffort.Effort != nil {
		t.Errorf("absent effort object should yield nil Effort, got %+v", noEffort.Effort)
	}
}

// TestBuildModelCluster verifies the model pill plus its effort dot gauge. The
// gauge is five slots wide at every level — filled slots count out the level,
// empty ones state the denominator — and carries no level word, so the count and
// hue are the only cues. With no effort the gauge is absent entirely rather than
// drawn all-empty, since an all-empty gauge would read as "level 0 of 5" for a
// model that has no effort scale at all.
func TestBuildModelCluster(t *testing.T) {
	modelBg := modelPillBg("Claude Sonnet 4.6")

	// No effort → the pill alone: no gauge, and none of the old pill's seam divider.
	plain := ansiRe.ReplaceAllString(buildModelCluster(modelBg, "sonnet", ""), "")
	if strings.ContainsAny(plain, "●○") {
		t.Errorf("no-effort cluster drew a gauge: %q", plain)
	}
	if strings.Contains(plain, "▕") {
		t.Errorf("no-effort cluster still has the effort seam divider: %q", plain)
	}
	if !strings.Contains(plain, "sonnet") {
		t.Errorf("no-effort cluster lost the model name: %q", plain)
	}

	for lvl, filled := range map[string]int{"low": 1, "medium": 2, "high": 3, "xhigh": 4, "maximum": 5} {
		got := ansiRe.ReplaceAllString(buildModelCluster(modelBg, "sonnet", lvl), "")

		if !strings.Contains(got, "sonnet") {
			t.Errorf("%s cluster lost the model name: %q", lvl, got)
		}
		if n := strings.Count(got, "●"); n != filled {
			t.Errorf("%s cluster has %d filled dots, want %d: %q", lvl, n, filled, got)
		}
		if n := strings.Count(got, "○"); n != 5-filled {
			t.Errorf("%s cluster has %d empty dots, want %d: %q", lvl, n, 5-filled, got)
		}
		// The gauge replaced the level word; none of the old labels may survive.
		for _, word := range []string{"low", "med", "high", "xhigh", "max"} {
			if strings.Contains(got, word) {
				t.Errorf("%s cluster still carries the level word %q: %q", lvl, word, got)
			}
		}
	}
}

// TestRenderNoEffortGauge verifies renderStatusLine omits the dot gauge when no
// effort applies, rather than defaulting to a level or drawing an all-empty gauge.
//
// The dot count is asserted against the whole rendered line, which is only safe
// because the window and context gauges pick "●" as their own icon at >= 75%.
// Both fixtures here sit at 20%, where those icons are "⧉" and "◔", so every "●"
// in the line belongs to the effort gauge. Do not raise these percentages.
func TestRenderNoEffortGauge(t *testing.T) {
	t.Setenv("COLUMNS", "120")
	render := func(effort, model string) string {
		return ansiRe.ReplaceAllString(captureStdout(func() {
			renderStatusLine(&GitInfo{Branch: "main", Age: "5m", Sync: "="},
				WindowInfo{Pct: 20, TimeLeft: "1h left"}, 20, effort, model, "/tmp", false)
		}), "")
	}

	noEffort := render("", "Claude Haiku 4.5")
	if strings.ContainsAny(noEffort, "●○") {
		t.Errorf("no-effort render drew an effort gauge: %q", noEffort)
	}

	withEffort := render("high", "Claude Opus 4.8")
	if n := strings.Count(withEffort, "●"); n != 3 {
		t.Errorf("high render has %d filled dots, want 3: %q", n, withEffort)
	}
	if n := strings.Count(withEffort, "○"); n != 2 {
		t.Errorf("high render has %d empty dots, want 2: %q", n, withEffort)
	}
}

// TestEffortDots pins the level → (hue, filled-slot count) mapping the dot gauge
// renders from. The count is what carries the level: five discrete slots let a
// reader take "3 of 5" off the gauge without resolving the hue at all, so the
// green→red hue is a redundant cue rather than the only one — which is what keeps
// the gauge legible under red-green color vision deficiency now that no level word
// accompanies it. A model with no effort knob must map to zero slots rather than a
// default, or Haiku would render a phantom "medium".
func TestEffortDots(t *testing.T) {
	if fg, filled := effortDots(""); fg != "" || filled != 0 {
		t.Errorf(`effortDots("") = (%q,%d), want ("",0)`, fg, filled)
	}

	want := map[string]int{"low": 1, "medium": 2, "high": 3, "xhigh": 4, "maximum": 5}
	for lvl, n := range want {
		fg, got := effortDots(lvl)
		if got != n {
			t.Errorf("effortDots(%q) filled = %d, want %d", lvl, got, n)
		}
		if fg == "" {
			t.Errorf("effortDots(%q) returned no color", lvl)
		}
	}

	// Two levels sharing a hue would make the hue ambiguous, leaving the count as
	// the only cue instead of a redundant second one.
	seen := make(map[string]string, len(want))
	for lvl := range want {
		fg, _ := effortDots(lvl)
		if prev, dup := seen[fg]; dup {
			t.Errorf("effortDots(%q) reuses the color of effortDots(%q)", lvl, prev)
		}
		seen[fg] = lvl
	}
}
