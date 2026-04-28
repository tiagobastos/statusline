package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var bgWg sync.WaitGroup

// --- ANSI color constants (truecolor) ---

const (
	rst  = "\033[0m"
	bold = "\033[1m"

	fgWhite  = "\033[38;2;255;255;255m"
	fgGray   = "\033[38;2;128;128;128m"
	fgDkGray = "\033[38;2;80;80;80m"

	// Pill backgrounds
	bgGreen  = "\033[48;2;80;190;100m"
	bgBlue   = "\033[48;2;100;150;235m"
	bgTeal   = "\033[48;2;70;185;165m"
	bgPurple = "\033[48;2;160;110;200m"
	bgAmber  = "\033[48;2;230;155;60m"
	bgDark   = "\033[48;2;80;88;105m"
	bgRed    = "\033[48;2;200;60;60m" // cost pill: expensive session

	// Context bar colors
	fgBarGreen  = "\033[38;2;120;200;120m"
	fgBarYellow = "\033[38;2;229;192;80m"
	fgBarRed    = "\033[38;2;255;100;100m"

	// Window bar colors
	fgWinGreen = "\033[38;2;120;200;160m"
	fgWinAmber = "\033[38;2;230;175;80m"
	fgWinRed   = "\033[38;2;255;120;80m"

	// Effort bar gold
	fgGold = "\033[38;2;218;165;32m"

	// Effort bolt yellow (maximum effort)
	fgBolt = "\033[38;2;255;220;50m"

	// Effort glyph red-orange (xHigh effort)
	fgGlyph = "\033[38;2;255;100;60m"

	// Auto-compact threshold marker
	fgAutoCompact = "\033[38;2;255;70;70m"

	// Auto-compact threshold position (% of context)
	autoCompactThreshold = 83

	// Nerd Font half-circle caps
	capLeft  = "\xee\x82\xb6"
	capRight = "\xee\x82\xb4"

	// Cache
	cachePath    = "/tmp/claude-statusline-usage.json"
	cacheTTL     = 60 // seconds
	gitCacheTTL  = 3  // seconds — short enough to feel live, long enough to absorb rapid renders

	// Git call timeout
	gitTimeout = 2 * time.Second
)

// --- Input types ---

type Input struct {
	Model         json.RawMessage `json:"model"`
	CWD           string          `json:"cwd"`
	EffortLevel   string          `json:"effortLevel"`

	ContextWindow CtxInfo         `json:"context_window"`
	Workspace     WorkspaceInfo   `json:"workspace"`
}


type CtxInfo struct {
	UsedPercentage    int `json:"used_percentage"`
	ContextWindowSize int `json:"context_window_size"`
}

type WorkspaceInfo struct {
	CurrentDir string `json:"current_dir"`
}

type ModelObj struct {
	DisplayName string `json:"display_name"`
	ID          string `json:"id"`
}

type Settings struct {
	EffortLevel string            `json:"effortLevel"`
	Env         map[string]string `json:"env"`
}

type GitInfo struct {
	Branch     string
	WorktreeOf string // repo basename if in a linked worktree, empty otherwise
	ModCount   int
	Sync       string
	Age        string
	StashCount int
}

type UsageData struct {
	FiveHour struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    string  `json:"resets_at"`
	} `json:"five_hour"`
}

type WindowInfo struct {
	Pct      int
	TimeLeft string
}

type SessionData struct {
	StartedAt time.Time `json:"started_at"`
	MaxCtxPct int       `json:"max_ctx_pct"`
	Compacted bool      `json:"compacted"`
}

// --- ANSI strip regex (compiled once) ---

var ansiRe = regexp.MustCompile(`\033\[[0-9;]*m`)

// --- Main ---

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--demo" {
		renderDemo()
		return
	}

	data, _ := io.ReadAll(os.Stdin)
	if len(data) == 0 {
		data = []byte("{}")
	}

	var input Input
	json.Unmarshal(data, &input)

	// Resolve CWD
	cwd := input.CWD
	if cwd == "" {
		cwd = input.Workspace.CurrentDir
	}
	if cwd == "" {
		cwd = "."
	}

	ctxPct := input.ContextWindow.UsedPercentage

	// Gather data concurrently
	gitCh := make(chan *GitInfo, 1)
	winCh := make(chan WindowInfo, 1)

	go func() { gitCh <- getGitInfo(cwd) }()
	go func() { winCh <- getWindowInfo() }()

	modelName := parseModelName(input.Model)
	effort := clampEffortForModel(readEffortLevel(cwd, input.EffortLevel), modelName)

	sess := getSessionData(cwd, ctxPct)

	gitInfo := <-gitCh
	windowInfo := <-winCh

	renderStatusLine(gitInfo, windowInfo, ctxPct, effort, modelName, cwd, sess.Compacted)
	bgWg.Wait()
}

// --- Model pill color ---

func modelPillBg(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "opus"):
		return bgBlue
	case strings.Contains(lower, "sonnet"):
		return bgPurple
	case strings.Contains(lower, "haiku"):
		return bgTeal
	default:
		return bgBlue
	}
}


// --- Terminal width ---

func terminalWidth() int {
	if cols := os.Getenv("COLUMNS"); cols != "" {
		var w int
		n, _ := fmt.Sscanf(cols, "%d", &w)
		if n == 1 && w > 40 {
			return w
		}
	}
	out, err := exec.Command("tput", "cols").Output()
	if err == nil {
		var w int
		n, _ := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &w)
		if n == 1 && w > 40 {
			return w
		}
	}
	return 120
}

// --- Session tracking ---

func sessionFilePath(cwd string) string {
	h := fnv.New32a()
	h.Write([]byte(cwd))
	return fmt.Sprintf("/tmp/claude-statusline-sess-%08x.json", h.Sum32())
}

func getSessionData(cwd string, ctxPct int) SessionData {
	path := sessionFilePath(cwd)
	now := time.Now()

	var sess SessionData

	if data, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(data, &sess) != nil {
			sess = SessionData{StartedAt: now, MaxCtxPct: ctxPct}
		}
	} else {
		sess = SessionData{StartedAt: now, MaxCtxPct: ctxPct}
	}

	if ctxPct > sess.MaxCtxPct {
		sess.MaxCtxPct = ctxPct
	} else if sess.MaxCtxPct > 30 && ctxPct < sess.MaxCtxPct-20 {
		if ctxPct <= 3 {
			// Fresh session (/new) — ctx resets to ~0; reset state
			sess = SessionData{StartedAt: now, MaxCtxPct: ctxPct}
		} else {
			sess.Compacted = true
		}
	}

	if data, err := json.Marshal(sess); err == nil {
		os.WriteFile(path, data, 0644)
	}

	return sess
}


// --- Data gathering ---

func parseModelName(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "?"
	}
	var obj ModelObj
	if err := json.Unmarshal(raw, &obj); err == nil {
		if obj.DisplayName != "" {
			return obj.DisplayName
		}
		if obj.ID != "" {
			return obj.ID
		}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return "?"
}

func normalizeEffort(e string) string {
	switch strings.ToLower(e) {
	case "max", "maximum":
		return "maximum"
	case "xhigh", "x-high", "extra-high", "extrahigh":
		return "xhigh"
	case "high":
		return "high"
	case "low":
		return "low"
	case "medium":
		return "medium"
	default:
		return e
	}
}

// clampEffortForModel downgrades xhigh→max for models that don't support it.
// Only Opus 4.7 supports xhigh; Opus 4.6 and Sonnet 4.6 top out at max.
func clampEffortForModel(effort, modelName string) string {
	if effort != "xhigh" {
		return effort
	}
	lower := strings.ToLower(modelName)
	opusWithXHigh := strings.Contains(lower, "opus") &&
		(strings.Contains(lower, "4.7") || strings.Contains(lower, "4-7"))
	if opusWithXHigh {
		return effort
	}
	return "maximum"
}

func readEffortLevel(cwd string, stdinEffort string) string {
	effort := "medium"
	home, _ := os.UserHomeDir()
	globalPath := filepath.Join(home, ".claude", "settings.json")
	if data, err := os.ReadFile(globalPath); err == nil {
		var s Settings
		if json.Unmarshal(data, &s) == nil {
			if s.EffortLevel != "" {
				effort = normalizeEffort(s.EffortLevel)
			} else if v := s.Env["CLAUDE_CODE_EFFORT_LEVEL"]; v != "" {
				effort = normalizeEffort(v)
			}
		}
	}
	projPath := filepath.Join(cwd, ".claude", "settings.json")
	if data, err := os.ReadFile(projPath); err == nil {
		var s Settings
		if json.Unmarshal(data, &s) == nil {
			if s.EffortLevel != "" {
				effort = normalizeEffort(s.EffortLevel)
			} else if v := s.Env["CLAUDE_CODE_EFFORT_LEVEL"]; v != "" {
				effort = normalizeEffort(v)
			}
		}
	}
	// Session-level override (from Claude Code's runtime state via stdin)
	if stdinEffort != "" {
		effort = normalizeEffort(stdinEffort)
	}
	return effort
}

// --- Git info ---

func gitCachePath(cwd string) string {
	h := fnv.New32a()
	h.Write([]byte(cwd))
	return fmt.Sprintf("/tmp/claude-statusline-git-%08x.json", h.Sum32())
}

type GitCache struct {
	Info     *GitInfo `json:"info"`
	CachedAt int64    `json:"cached_at"`
}

func runGit(ctx context.Context, cwd string, args ...string) ([]byte, error) {
	fullArgs := append([]string{"-C", cwd}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	return cmd.Output()
}

func getGitInfo(cwd string) *GitInfo {
	// Short-TTL cache: absorbs rapid successive renders without re-spawning 8 git processes.
	cachefile := gitCachePath(cwd)
	if info, err := os.Stat(cachefile); err == nil {
		if time.Now().Unix()-info.ModTime().Unix() < gitCacheTTL {
			if data, err := os.ReadFile(cachefile); err == nil {
				var gc GitCache
				if json.Unmarshal(data, &gc) == nil {
					return gc.Info
				}
			}
		}
	}

	result := fetchGitInfo(cwd)

	gc := GitCache{Info: result, CachedAt: time.Now().Unix()}
	if data, err := json.Marshal(gc); err == nil {
		os.WriteFile(cachefile, data, 0644)
	}
	return result
}

func fetchGitInfo(cwd string) *GitInfo {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	if out, err := runGit(ctx, cwd, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(string(out)) != "true" {
		// Double-check: error means not a repo
		if err != nil {
			return nil
		}
	}

	info := &GitInfo{}

	type strResult struct{ val string }
	type intResult struct{ val int }

	branchCh := make(chan strResult, 1)
	worktreeCh := make(chan strResult, 1)
	modCh := make(chan intResult, 1)
	aheadCh := make(chan intResult, 1)
	behindCh := make(chan intResult, 1)
	ageCh := make(chan strResult, 1)
	stashCh := make(chan intResult, 1)

	go func() {
		out, err := runGit(ctx, cwd, "branch", "--show-current")
		if err != nil {
			branchCh <- strResult{"detached"}
		} else {
			branchCh <- strResult{strings.TrimSpace(string(out))}
		}
	}()

	go func() {
		out, err := runGit(ctx, cwd, "rev-parse", "--git-dir")
		if err != nil {
			worktreeCh <- strResult{""}
			return
		}
		gitDir := strings.TrimSpace(string(out))
		idx := strings.Index(gitDir, "/.git/worktrees/")
		if idx < 0 {
			worktreeCh <- strResult{""}
			return
		}
		worktreeCh <- strResult{filepath.Base(gitDir[:idx])}
	}()

	go func() {
		out, _ := runGit(ctx, cwd, "status", "--porcelain")
		t := strings.TrimSpace(string(out))
		if t == "" {
			modCh <- intResult{0}
		} else {
			modCh <- intResult{len(strings.Split(t, "\n"))}
		}
	}()

	go func() {
		out, err := runGit(ctx, cwd, "rev-list", "--count", "@{upstream}..HEAD")
		n := 0
		if err == nil {
			fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
		}
		aheadCh <- intResult{n}
	}()

	go func() {
		out, err := runGit(ctx, cwd, "rev-list", "--count", "HEAD..@{upstream}")
		n := 0
		if err == nil {
			fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
		}
		behindCh <- intResult{n}
	}()

	go func() {
		out, err := runGit(ctx, cwd, "log", "-1", "--format=%ct")
		age := ""
		if err == nil {
			var ts int64
			fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &ts)
			if ts > 0 {
				diff := time.Now().Unix() - ts
				switch {
				case diff < 60:
					age = fmt.Sprintf("%ds", diff)
				case diff < 3600:
					age = fmt.Sprintf("%dm", diff/60)
				case diff < 86400:
					age = fmt.Sprintf("%dh", diff/3600)
				default:
					age = fmt.Sprintf("%dd", diff/86400)
				}
			}
		}
		ageCh <- strResult{age}
	}()

	go func() {
		out, _ := runGit(ctx, cwd, "stash", "list")
		t := strings.TrimSpace(string(out))
		if t == "" {
			stashCh <- intResult{0}
		} else {
			stashCh <- intResult{len(strings.Split(t, "\n"))}
		}
	}()

	info.Branch = (<-branchCh).val
	info.WorktreeOf = (<-worktreeCh).val
	info.ModCount = (<-modCh).val
	ahead := (<-aheadCh).val
	behind := (<-behindCh).val
	info.Age = (<-ageCh).val
	info.StashCount = (<-stashCh).val

	if ahead > 0 && behind > 0 {
		info.Sync = fmt.Sprintf("↑%d↓%d", ahead, behind)
	} else if ahead > 0 {
		info.Sync = fmt.Sprintf("↑%d", ahead)
	} else if behind > 0 {
		info.Sync = fmt.Sprintf("↓%d", behind)
	} else {
		info.Sync = "="
	}

	return info
}

// --- Usage API ---

func getWindowInfo() WindowInfo {
	result := WindowInfo{Pct: 0, TimeLeft: "idle"}
	var usage UsageData
	cacheValid := false

	if info, err := os.Stat(cachePath); err == nil {
		if data, err := os.ReadFile(cachePath); err == nil && json.Unmarshal(data, &usage) == nil {
			cacheValid = true
			if time.Since(info.ModTime()).Seconds() >= cacheTTL {
				// Stale — serve immediately, refresh in background
				bgWg.Add(1)
				go func() {
					defer bgWg.Done()
					var u UsageData
					fetchAndCacheUsage(&u)
				}()
			}
		}
	}

	if !cacheValid {
		// No cache at all — fetch synchronously (first run only)
		if fetchAndCacheUsage(&usage) {
			cacheValid = true
		}
	}

	if !cacheValid {
		return result
	}

	result.Pct = int(usage.FiveHour.Utilization)
	if usage.FiveHour.ResetsAt != "" {
		if resetTime, err := time.Parse(time.RFC3339, usage.FiveHour.ResetsAt); err == nil {
			delta := time.Until(resetTime)
			if delta > 0 {
				h := int(delta.Hours())
				m := int(delta.Minutes()) % 60
				if h > 0 {
					result.TimeLeft = fmt.Sprintf("%dh %02dm left", h, m)
				} else {
					result.TimeLeft = fmt.Sprintf("%dm left", m)
				}
			} else {
				result.TimeLeft = "reset"
			}
		}
	}

	return result
}

func fetchAndCacheUsage(usage *UsageData) bool {
	out, err := exec.Command("security", "find-generic-password",
		"-s", "Claude Code-credentials", "-w").Output()
	if err != nil {
		return false
	}
	var creds struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(out, &creds) != nil {
		return false
	}
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", "https://api.anthropic.com/api/oauth/usage", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+creds.ClaudeAiOauth.AccessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if json.Unmarshal(body, usage) != nil {
		return false
	}
	os.WriteFile(cachePath, body, 0644)
	return true
}

// --- Rendering ---

func pill(bg, content string) string {
	fgCap := strings.Replace(bg, "48;", "38;", 1)
	return fgCap + capLeft + bg + fgWhite + bold + " " + content + " " + rst + fgCap + capRight + rst
}

func visibleLen(s string) int {
	stripped := ansiRe.ReplaceAllString(s, "")
	return utf8.RuneCountInString(stripped)
}

func alignedLine(left, right string, width int) string {
	lLen := visibleLen(left)
	rLen := visibleLen(right)
	pad := width - lLen - rLen
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right + "\n"
}

func separator(width int) string {
	return fgDkGray + strings.Repeat("─", width) + rst + "\n"
}

func buildBars(targetW, leftPillsLen, totalFixed, windowPct, ctxPct int, winBarColor, ctxBarColor, winIcon, ctxIcon, winLabel, winTimeLabel, ctxLabel string, compactThreshold int) string {
	barsTotal := targetW - leftPillsLen - 2 - totalFixed
	if barsTotal < 10 {
		barsTotal = 10
	}
	winBarW := barsTotal * 40 / 100
	ctxBarW := barsTotal - winBarW
	if winBarW < 4 {
		winBarW = 4
	}
	if ctxBarW < 4 {
		ctxBarW = 4
	}

	winFilled := winBarW * windowPct / 100
	winEmpty := winBarW - winFilled

	var b strings.Builder

	b.WriteString(winBarColor + winIcon + rst + " ")
	for i := 0; i < winFilled; i++ {
		b.WriteString(winBarColor + "━" + rst)
	}
	for i := 0; i < winEmpty; i++ {
		b.WriteString(fgDkGray + "━" + rst)
	}
	b.WriteString(" " + fgGray + winLabel + " " + winTimeLabel + rst)
	b.WriteString(" " + fgDkGray + "⋮" + rst + " ")

	ctxFilled := ctxBarW * ctxPct / 100
	thresholdPos := ctxBarW * compactThreshold / 100

	b.WriteString(ctxBarColor + ctxIcon + rst + " ")
	for i := 0; i < ctxBarW; i++ {
		isThreshold := i >= thresholdPos
		if i < ctxFilled {
			b.WriteString(ctxBarColor + "━" + rst)
		} else if isThreshold {
			b.WriteString(fgAutoCompact + "━" + rst)
		} else {
			b.WriteString(fgDkGray + "━" + rst)
		}
	}
	b.WriteString(" " + fgGray + ctxLabel + rst)

	return b.String()
}

func buildEffortBars(effort string) string {
	switch effort {
	case "low":
		return fgGold + "▰" + fgDkGray + "▱▱" + fgWhite
	case "medium":
		return fgGold + "▰▰" + fgDkGray + "▱" + fgWhite
	case "high":
		return fgGold + "▰▰▰" + fgWhite
	case "xhigh":
		return fgGold + "▰▰▰ " + fgGlyph + "✦" + fgWhite
	case "maximum":
		return fgGold + "▰▰▰ " + fgBolt + "⚡" + fgWhite
	default:
		return fgGold + "▰▰" + fgDkGray + "▱" + fgWhite
	}
}

func stripTrailingVersion(s string) string {
	re := regexp.MustCompile(`\s+[0-9.]+$`)
	return re.ReplaceAllString(s, "")
}

func tildeCollapsePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+"/") {
		return "~" + path[len(home):]
	}
	return path
}

// --- Rendering ---

func renderStatusLine(gitInfo *GitInfo, win WindowInfo, ctxPct int, effort, modelName, cwd string, compacted bool) {
	targetW := terminalWidth()
	pwdDisplay := tildeCollapsePath(cwd)

	modelShort := strings.TrimPrefix(modelName, "Claude ")
	modelShort = stripTrailingVersion(modelShort)
	modelShort = strings.ToLower(modelShort)

	effortBars := buildEffortBars(effort)
	modelLabel := modelShort + " " + effortBars
	modelBg := modelPillBg(modelName)

	winBarColor := fgWinGreen
	if win.Pct >= 75 {
		winBarColor = fgWinRed
	} else if win.Pct >= 50 {
		winBarColor = fgWinAmber
	}

	ctxBarColor := fgBarGreen
	if ctxPct >= 75 {
		ctxBarColor = fgBarRed
	} else if ctxPct >= 50 {
		ctxBarColor = fgBarYellow
	}

	winIcon := "⧉"
	if win.Pct >= 75 {
		winIcon = "●"
	} else if win.Pct >= 50 {
		winIcon = "◕"
	} else if win.Pct >= 25 {
		winIcon = "◑"
	}

	ctxIcon := "◔"
	if ctxPct >= 75 {
		ctxIcon = "●"
	} else if ctxPct >= 50 {
		ctxIcon = "◕"
	} else if ctxPct >= 25 {
		ctxIcon = "◑"
	}
	if compacted {
		ctxIcon = "↻"
	}

	winLabel := fmt.Sprintf("%d%%", win.Pct)
	winTimeLabel := win.TimeLeft
	ctxLabel := fmt.Sprintf("%d%%", ctxPct)

	winFixed := 1 + 1 + 1 + len(winLabel) + 1 + len(winTimeLabel)
	ctxFixed := 1 + 1 + 1 + len(ctxLabel)
	totalFixed := winFixed + ctxFixed + 3

	compactThreshold := autoCompactThreshold
	if v := os.Getenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 1 && n <= 100 {
			compactThreshold = n
		}
	}

	if gitInfo != nil {
		l1Pills := pill(modelBg, modelLabel)
		l1PillsLen := visibleLen(l1Pills)
		bars := fgDkGray + "⋮" + rst + " " + buildBars(targetW, l1PillsLen, totalFixed, win.Pct, ctxPct, winBarColor, ctxBarColor, winIcon, ctxIcon, winLabel, winTimeLabel, ctxLabel, compactThreshold)
		fmt.Print(alignedLine(l1Pills, bars, targetW))

		pwdLabel := pwdDisplay
		branchIcon := "⎇  "
		if gitInfo.WorktreeOf != "" {
			pwdLabel = "~/" + gitInfo.WorktreeOf + "/../" + filepath.Base(cwd)
			branchIcon = "⎇⎇ "
		}
		l2Left := pill(bgGreen, pwdLabel) + " " + pill(bgPurple, branchIcon+gitInfo.Branch)
		var l2RightParts []string
		if gitInfo.Age != "" {
			l2RightParts = append(l2RightParts, pill(bgDark, "⏱ "+gitInfo.Age))
		}
		if gitInfo.ModCount > 0 {
			l2RightParts = append(l2RightParts, pill(bgAmber, fmt.Sprintf("✎ %d", gitInfo.ModCount)))
		}
		if gitInfo.StashCount > 0 {
			l2RightParts = append(l2RightParts, pill(bgAmber, fmt.Sprintf("⚑ %d", gitInfo.StashCount)))
		}
		if gitInfo.Sync != "" && gitInfo.Sync != "=" {
			l2RightParts = append(l2RightParts, pill(bgDark, "⇅ "+gitInfo.Sync))
		}
		l2Right := strings.Join(l2RightParts, " ")

		sepW := targetW
		if l2W := visibleLen(l2Left) + visibleLen(l2Right) + 1; l2W > sepW {
			sepW = l2W
		}
		fmt.Print(separator(sepW))
		fmt.Print(alignedLine(l2Left, l2Right, targetW))
		fmt.Print(rst + "\n")
	} else {
		l1Pills := pill(bgGreen, pwdDisplay) + " " + pill(modelBg, modelLabel)
		l1PillsLen := visibleLen(l1Pills)
		bars := fgDkGray + "⋮" + rst + " " + buildBars(targetW, l1PillsLen, totalFixed, win.Pct, ctxPct, winBarColor, ctxBarColor, winIcon, ctxIcon, winLabel, winTimeLabel, ctxLabel, compactThreshold)
		fmt.Print(alignedLine(l1Pills, bars, targetW))
		fmt.Print(rst + "\n")
	}
}

// --- Demo mode ---

func renderDemo() {
	home, _ := os.UserHomeDir()
	cwd := home + "/code/my-project"
	model := "Claude Sonnet 4.6"
	mockWin := WindowInfo{Pct: 35, TimeLeft: "2h 15m left"}
	mockGit := &GitInfo{Branch: "main", ModCount: 3, Sync: "↑2", Age: "5m", StashCount: 1}

	section := func(title string) {
		fmt.Println()
		fmt.Println(fgGray + "  " + title + rst)
		fmt.Println()
	}

	section("── git layout — effort levels ─────────────────────────────────────")
	for _, eff := range []string{"low", "medium", "high", "xhigh", "maximum"} {
		fmt.Println(fgDkGray + "  " + eff + rst)
		renderStatusLine(mockGit, mockWin, 45, eff, model, cwd, false)
	}

	section("── non-git layout ──────────────────────────────────────────────────")
	renderStatusLine(nil, mockWin, 45, "medium", model, cwd, false)

	section("── context window levels (medium effort) ───────────────────────────")
	for _, pct := range []int{15, 50, 82, 95} {
		fmt.Printf(fgDkGray+"  ctx %d%%\n"+rst, pct)
		renderStatusLine(mockGit, mockWin, pct, "medium", model, cwd, pct >= 90)
	}

	section("── rate-limit window levels (medium effort) ────────────────────────")
	for _, wpct := range []int{0, 40, 75, 90} {
		tl := fmt.Sprintf("%dm left", 270-wpct*2)
		if wpct == 0 {
			tl = "idle"
		}
		fmt.Printf(fgDkGray+"  window %d%%\n"+rst, wpct)
		renderStatusLine(mockGit, WindowInfo{Pct: wpct, TimeLeft: tl}, 45, "medium", model, cwd, false)
	}

	fmt.Println()
}
