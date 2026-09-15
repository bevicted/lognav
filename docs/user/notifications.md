# Notifications

lognav can notify you when all instances in a fetch have settled. It is enabled
by default:

```yaml
core:
  notifyOnFetchDone: true
  notifyStyle: osc9
```

Set `notifyOnFetchDone: false` to disable it. Use `lognav config describe
core.notifyStyle` for the available styles.

## What lognav emits

lognav writes terminal control sequences; it does not call an operating-system
notification service. `bell` writes an audible BEL. The other styles write this
fixed message, `lognav: fetch complete`:

| Style    | Sequence                                                       |
| -------- | -------------------------------------------------------------- |
| `osc9`   | OSC 9 notification                                             |
| `osc777` | OSC 777 `notify` with title `lognav` and body `fetch complete` |
| `osc99`  | OSC 99 notification                                            |

Unsupported OSC sequences are ignored by the terminal. While Logs is active,
its contextual row shows fetch phase and elapsed progress regardless of this
setting.

## Terminal support

Choose a style your terminal documents:

- [iTerm2](https://iterm2.com/documentation-escape-codes.html) supports OSC 9.
- [WezTerm](https://wezterm.org/escape-sequences.html) supports OSC 9 and OSC 777.
- [Ghostty](https://ghostty.org/docs/vt/osc/9) supports OSC 9 and OSC 777. Its
  [OSC 99 delivery is not implemented](https://github.com/ghostty-org/ghostty/issues/5634).
- [kitty](https://sw.kovidgoyal.net/kitty/desktop-notifications/) supports OSC
  99 and legacy OSC 9. Its current
  [notification source](https://github.com/kovidgoyal/kitty/blob/master/kitty/notifications.py)
  also handles OSC 777.
- [foot](https://codeberg.org/dnkl/foot/raw/branch/master/doc/foot-ctlseqs.7.scd)
  documents OSC 9, OSC 777, and OSC 99; OSC 99 arrived in
  [foot 1.18](https://codeberg.org/dnkl/foot/raw/tag/1.18.0/CHANGELOG.md).
- Apple documents an audible
  [Terminal.app bell](https://support.apple.com/guide/terminal/change-profiles-advanced-settings-trmladvn/mac),
  so use `bell` there. Alacritty's
  [supported escape list](https://alacritty.org/misc-alacritty-escapes.html)
  does not list these OSC notification protocols.

## tmux and SSH

Inside tmux, lognav DCS-passthrough-wraps OSC notifications when `$TMUX` is
set. tmux still needs `allow-passthrough on`, which was added in tmux 3.3 and
defaults off:

```sh
tmux set -g allow-passthrough on
```

See the tmux [passthrough FAQ](https://github.com/tmux/tmux/wiki/FAQ#what-is-the-passthrough-escape-sequence-and-how-do-i-use-it)
and [3.3 changes](https://raw.githubusercontent.com/tmux/tmux/3.3/CHANGES).
The bell stays a normal tmux bell and follows tmux bell settings.

Over SSH, an OSC notification is delivered by the terminal that receives the
connection, so it normally appears on the local terminal.
