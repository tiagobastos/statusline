# statusline

Rich status line for [Claude Code](https://claude.ai/code), built in Go. Zero dependencies.

Displays model name, effort level, context window usage, session cost, rate-limit bar, git branch, stash count, and working directory — all rendered as a compact ANSI status line on each turn.

## Installation

### Option A: Download binary (recommended)

Download the latest release for macOS from [Releases](https://github.com/tiagobastos/statusline/releases) and place the binary at `~/.claude/statusline/statusline`.

```bash
# Example for macOS arm64
curl -L https://github.com/tiagobastos/statusline/releases/latest/download/statusline-darwin-arm64 \
  -o ~/.claude/statusline/statusline
chmod +x ~/.claude/statusline/statusline
```

### Option B: Build from source

Requires Go 1.23+.

```bash
go install github.com/tiagobastos/statusline@latest
mv "$(go env GOPATH)/bin/statusline" ~/.claude/statusline/statusline
```

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
| `xhigh` | `▰▰▰ ✦` (red-orange) |
| `maximum` | `▰▰▰ ⚡` (yellow-gold) |

Aliases for `xhigh`: `x-high`, `extra-high`, `extrahigh`.

The `/effort` slash command overrides effort for the current session only.

## Layout

**Git directories** — two-line layout:
```
⎇ main  [2]
sonnet-4-6 ▰▰▱  ░░░░████████░░  $0.12  ▓▓▓▓▓░░░  ~/code/myproject
```

**Non-git directories** — one-line layout:
```
~/code/scripts  sonnet-4-6 ▰▰▱  ░░░░████████░░  $0.12
```

## License

MIT
