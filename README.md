# statusline

Rich status line for [Claude Code](https://claude.ai/code), built in Go. Zero dependencies.

![statusline](assets/hero.jpeg)

Displays model name, effort level, context window usage, rate-limit bar, git branch, stash count, and working directory — rendered as a compact ANSI status line on each turn.

## Installation

### Option A: Download binary (recommended)

Download the latest release for macOS from [Releases](https://github.com/tiagobastos/statusline/releases) and place the binary at `~/.claude/statusline/statusline`.

```bash
mkdir -p ~/.claude/statusline

# macOS Apple Silicon
curl -L https://github.com/tiagobastos/statusline/releases/latest/download/statusline-darwin-arm64 \
  -o ~/.claude/statusline/statusline
chmod +x ~/.claude/statusline/statusline
```

For macOS Intel, use `statusline-darwin-amd64` instead.

### Option B: Build from source

Requires Go 1.23+.

```bash
go install github.com/tiagobastos/statusline@latest
mv "$(go env GOPATH)/bin/statusline" ~/.claude/statusline/statusline
```

## Updating

To update to a newer release, repeat the installation step — download the new binary and replace the old one. The binary at `~/.claude/statusline/statusline` is self-contained; no other files change.

If you built from source: `go install github.com/tiagobastos/statusline@latest` then move the binary again.

## Claude Code integration

Add to `~/.claude/settings.json`:

```json
{
  "statusLine": {
    "type": "command",
    "command": "~/.claude/statusline/statusline"
  }
}
```

## Configuration

### Effort level

Set `effortLevel` in `~/.claude/settings.json` or a project-level `.claude/settings.json`:

```json
{
  "effortLevel": "medium"
}
```

| Value | Display |
|---|---|
| `low` | `▰▱▱` |
| `medium` | `▰▰▱` |
| `high` | `▰▰▰` |
| `xhigh` | `▰▰▰ ✦` (red-orange, Opus 4.7 only) |
| `maximum` | `▰▰▰ ⚡` (yellow-gold) |

Aliases for `xhigh`: `x-high`, `extra-high`, `extrahigh`.

The `/effort` slash command overrides effort for the current session only.

## Layout

**Git directories** — two-line layout. Model and effort bars on the first line, directory and branch info below the separator.

![git layout](assets/demo-git-effort.jpeg)

**Non-git directories** — single-line layout with directory and model side by side.

![non-git layout](assets/demo-nongit.jpeg)

### Context window bar

The right bar tracks context usage. Color shifts green → yellow → red as it fills. A faint red marker near the right edge shows the auto-compact threshold.

![context window levels](assets/demo-context-levels.jpeg)

### Rate-limit bar

The left bar tracks your 5-hour usage window. Color and icon shift as you approach the limit.

![rate-limit window levels](assets/demo-ratelimit-levels.jpeg)

## License

MIT
