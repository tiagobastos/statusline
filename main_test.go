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
// the Nerd Font powerline caps, the ctx circles, the effort chip label,
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

// TestBuildEffortChipNoEffort verifies an empty level renders nothing (a model
// without effort support shows no effort chip, not a phantom "medium"), while
// every real level renders a chip containing its uppercase label.
func TestBuildEffortChipNoEffort(t *testing.T) {
	if got := buildEffortChip(""); got != "" {
		t.Errorf(`buildEffortChip("") = %q, want empty`, got)
	}
	want := map[string]string{
		"low": "low", "medium": "med", "high": "high", "xhigh": "xhigh", "maximum": "max",
	}
	for lvl, label := range want {
		got := buildEffortChip(lvl)
		if got == "" {
			t.Errorf("buildEffortChip(%q) is empty, want a chip", lvl)
		}
		if !strings.Contains(ansiRe.ReplaceAllString(got, ""), label) {
			t.Errorf("buildEffortChip(%q) missing label %q: %q", lvl, label,
				ansiRe.ReplaceAllString(got, ""))
		}
	}
}

// TestRenderNoEffortSegment verifies renderStatusLine omits the effort chip when
// no effort applies — the uppercase level labels are effort-only, so their absence
// proves the chip (and its leading space) was dropped rather than defaulted to
// medium. A "high" render is checked as the positive control.
func TestRenderNoEffortSegment(t *testing.T) {
	t.Setenv("COLUMNS", "120")
	render := func(effort, model string) string {
		return ansiRe.ReplaceAllString(captureStdout(func() {
			renderStatusLine(&GitInfo{Branch: "main", Age: "5m", Sync: "="},
				WindowInfo{Pct: 20, TimeLeft: "1h left"}, 20, effort, model, "/tmp", false)
		}), "")
	}

	noEffort := render("", "Claude Haiku 4.5")
	for _, label := range []string{"low", "med", "high", "xhigh", "max"} {
		if strings.Contains(noEffort, label) {
			t.Errorf("no-effort render contains effort label %q: %q", label, noEffort)
		}
	}

	if withEffort := render("high", "Claude Opus 4.8"); !strings.Contains(withEffort, "high") {
		t.Errorf("high-effort render missing high chip: %q", withEffort)
	}
}
