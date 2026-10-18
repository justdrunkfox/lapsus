# lapsus

[English](README.md) | [Русский](README.ru.md)

A wrong-keyboard-layout fixer for Linux/Wayland, built for [niri](https://github.com/niri-wm/niri) — in the spirit of Punto Switcher. Typed `ghbdtn` instead of `привет`? lapsus flips it back: automatically at word boundaries, or on a hotkey. The name comes from Latin *lapsus linguae*, "a slip of the tongue".

## Features

- **Auto-fix daemon** — reads keystrokes straight from `/dev/input` (no grab, no root), keeps the word being typed in a buffer and fixes it when the word ends — but only when the dictionaries are confident: the flipped reading must be a real word, the original must not. After two consecutive fixes to the same language the daemon moves the layout there, so a wrong-layout phrase self-heals after a word or two.
- **Hotkey toggle** — one key press flips the word at the caret to the other layout, unconditionally (an explicit key press needs no dictionary), and the layout follows the word. A second hotkey does the same for the current selection, phrase by phrase.
- **Double-Alt flip** — two quick taps of the left Alt flip the word being typed right now (a pure positional toggle, no dictionary — gibberish flips too) and move the layout along. Off by default (`daemon.double_alt_flip`).
- **Per-application layout memory** — remembers which language you type in each application and restores it when a window regains focus. New windows of a familiar app start in "their" language; windows of unknown apps can get a configured default language (`daemon.default_layout`).
- **Tray icon** — the flag of the active layout, dimmed while paused; menu with toggles (auto-fix / sound / notifications), persisted to the config.
- **Feedback** — desktop notification and a sound on every flip (optional).
- **systemd user service** included (`deploy/lapsus.service`).

## Requirements

- Linux, Wayland; compositor integration is currently [niri](https://github.com/niri-wm/niri)-specific (layout switching and window focus over `niri msg`)
- Go ≥ 1.26 (build only; CGO is not used)
- [wtype](https://github.com/atx/wtype) and [wl-clipboard](https://github.com/bugaevc/wl-clipboard)
- `libnotify-bin` for desktop notifications (optional)
- membership in the `input` group for the daemon (reads `/dev/input` without grab, without root)
- for `inject_method = "uinput"`: write access to `/dev/uinput` (deploy/60-lapsus-uinput.rules grants it to the `input` group)

## Build & install

```sh
go build -o ~/.local/bin/lapsus ./cmd/lapsus
lapsus check                     # self-test: config, dictionaries, analyzer
lapsus convert "Ghbdtn? vbh!"    # → Привет, мир!
```

Cross-compiles cleanly (no CGO):

```sh
GOOS=linux GOARCH=arm64 go build -o dist/lapsus-linux-arm64 ./cmd/lapsus
```

Run the daemon permanently via the included systemd user unit:

```sh
cp deploy/lapsus.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now lapsus
journalctl --user -u lapsus -f
```

## Hotkeys

Bind `lapsus fix` in `~/.config/niri/config.kdl`:

```kdl
binds {
    // On this ThinkPad the bare Fn key arrives as XF86WakeUp.
    // Right Alt is turned into a Compose key (compose:ralt), because a
    // bare modifier key cannot be bound in niri.
    XF86WakeUp      { spawn "lapsus" "fix"; }              // word at the caret
    Ctrl+XF86WakeUp { spawn "lapsus" "fix" "--selection"; } // current selection
}
```

- **`lapsus fix`** — flips the word left of the caret, then switches the layout to the fixed word's language. In GUI apps the selection is grown across the application's word units, so words containing `, . ; ' [ ]` — the letters `б ж э ю х ъ` on ЙЦУКЕН — flip whole; the press that crosses into the previous word is undone. If the daemon just auto-fixed a word (within `daemon.undo_window_ms`, with nothing typed since), the same key **reverts that fix** instead: the word goes back as typed and the fix's layout move is taken back — press once more to toggle as usual.
- **`lapsus fix --selection`** — flips the current selection (mouse or Shift+arrows); a phrase is flipped word by word with whitespace preserved.
- **`lapsus fix --tail`** — flips the words just typed, from the daemon's phrase memory (words since the last Enter/arrows/combo, up to 32). No selection, no clipboard — the same in GUI apps and terminals. Words the auto-fix already corrected stay as they are; only the missed ones flip. Bind it to its own key, e.g. `Ctrl+XF86WakeUp { spawn "lapsus" "fix" "--tail"; }`.
- Debugging: `lapsus fix -v` (step log), `lapsus fix -n` (dry run).

## The daemon

The daemon reads keystrokes directly from `/dev/input` (no grab, no root), keeps the word being typed in a buffer and fixes it at a word boundary:

```
keyboard (evdev, input group) ──► word buffer ──► boundary:
                                       ▲          space (the only boundary)
                                       │
niri event-stream: active layout ──────┘          │
and focused window                          analyze (dictionaries)
                                                    │ confident?
                                                    ▼
                                 BackSpace ×N + type the fixed word
                                                    ▼
                                 niri msg action switch-layout (last)
```

- The word ends on **the space** — the only key that means the same in every layout, so restoring it after a fix is always safe. Punctuation typed inside a word stays in it: on ЙЦУКЕН the keys `, . ; ' [ ]` are the letters `б ж э ю х ъ`, so `hs,f` is `рыба` typed in the wrong layout — the dictionaries fix such a word whole. The fix fires only when the entire flipped reading is a real dictionary word (`ghbdtn,` would flip to `приветб`, which is not a word — it is left alone). Enter, Tab, arrows and Ctrl/Alt combos drop the buffer without fixing (the text may already be gone — an executed command, a completion, a moved caret).
- The replacement is the same everywhere — GUI apps and terminals: BackSpace × word length, then type the fix. No selections, no clipboard involved.
- Two injection backends: **wtype** (default; keysyms through the compositor — layout-independent) and **uinput** (raw keycodes through a virtual kernel keyboard — works compositor-independent and in raw-input apps; the layout is switched to match each typed character).
- Devices are rescanned every 3 s (hotplug); the active layout and focused window are tracked via `niri msg --json event-stream`.
- `--verbose` logs keycodes and every buffered word to stderr (journald keeps those): do not leave it on permanently — the log then contains what you type, including passwords.

### Tray

The icon is rendered in code (no assets): the flag of the active layout, dimmed while auto-fixing is paused. The menu (both mouse buttons open it):

- **Dictionary auto-fix** — pause/resume the daemon (same as SIGUSR1);
- **Sound** and **Notifications** — feedback toggles;
- **Quit** — stop the daemon.

Toggles apply immediately and are saved to the config file.

### Pausing

```kdl
Mod+Shift+P { spawn "pkill" "-USR1" "lapsus"; }   // toggle auto-fix
```

## Configuration

`~/.config/lapsus/config.toml` (all keys optional, defaults are valid):

```toml
[fix]
switch_layout = true     # switch the layout to the fixed word's language
pause_ms      = 50       # delay after synthetic keypresses (wtype)

[feedback]
notify = true            # desktop notification on every flip
                       # (needs: sudo apt install libnotify-bin)
sound  = "bell"        # "bell" (freedesktop), a path to a file, or ""

[daemon]
tray                   = true  # tray icon: layout flag + toggles
inject_method          = "wtype"  # "wtype" (keysyms) | "uinput" (raw
                               # keycodes via /dev/uinput; needs the
                               # udev rule from deploy/)
switch_layout          = true  # after 2 consecutive dictionary fixes to
                               # the same language, move the layout there
switch_after_words     = 2     # 1 = move on every fix; [1, 10]
remember_window_layout = true  # restore the last typed language per app
default_layout         = ""    # language for windows of unknown apps:
                               # "", "en" or "ru" ("" = don't touch)
exclude_app_ids        = []    # app_ids where auto-fix is off (VMs, games)
boundary_pause_ms      = 0     # idle boundary off: a word ends only on a
                               # space (opt-in, [50, 5000] ms)
min_word_len           = 3     # shorter words are left alone by the daemon
double_alt_flip        = false # double-tap of Left Alt flips the word being
                               # typed and moves the layout along with it
double_alt_window_ms   = 1000  # max gap between the two Alt taps, [100, 5000]
undo_window_ms         = 2000  # the fix key reverts a fresh auto-fix instead
                               # of toggling; 0 = off, [200, 10000]

[hotkey]
terminals = ["foot", "kitty", "Alacritty", "wezterm", "ghostty", "st"]

[capture]
method = "clipboard"     # clipboard (primary selection) | cut (Ctrl+X fallback)

[dictionary]
user_dir = "~/.config/lapsus/dicts"   # en_freq.txt / ru_freq.txt
```

User dictionaries: `en_freq.txt` / `ru_freq.txt`, one `word frequency` pair per line; they override the built-in frequencies — add your own terms, nicknames, project names.

## Dictionaries

`dict/dict_data/{en,ru}_freq.txt` — top-20 000 words from [hermitdave/FrequencyWords](https://github.com/hermitdave/FrequencyWords) (OpenSubtitles 2018). Refresh:

```sh
curl -fsSL https://raw.githubusercontent.com/hermitdave/FrequencyWords/master/content/2018/en/en_50k.txt | head -n 20000 > dict/dict_data/en_freq.txt
curl -fsSL https://raw.githubusercontent.com/hermitdave/FrequencyWords/master/content/2018/ru/ru_50k.txt | head -n 20000 > dict/dict_data/ru_freq.txt
```

## How the conversion works

The table in `layout/layout.go` is position-faithful: every character is replaced with the character of the same physical key (Shift included) in the other layout. Consequences:

- `?` → `,` (Shift+/ types `,` on ЙЦУКЕН) and back; `@ # $ ^ &` → `" № ; : ?`;
- `` ` `` ↔ `ё`, `~` ↔ `Ё`, `<` ↔ `Б`, `>` ↔ `Ю`;
- digits and unmapped characters pass through unchanged.

Edge punctuation flips along with the word in the explicit hotkey paths (a selection was flipped) — it was pressed with the same wrong-layout keys: `руддщ,` → `hello?` (RU Shift+/ types `,`, but the user meant `?`). Characters identical in both layouts (brackets, space, `!`) pass unchanged. The daemon completes a word on the space only (restored verbatim); punctuation inside its buffer flips with the word, but the dictionary gate lets a fix through only when the whole flipped reading is a word.

## Known limitations

- Held-down keys (autorepeat) invalidate the daemon's word buffer — that word is skipped, nothing is corrupted.
- The last word before Enter is never auto-fixed: Enter drops the buffer so an already-executed command is not touched. A word closed by punctuation is not auto-fixed either — the sign is layout-dependent, and re-typing it "flipped" would corrupt it.
- Fast typing during the injection itself can interleave with it (nothing to intercept without a grab).
- Moving the caret with the mouse is invisible to the daemon — a word typed before the click is not fixed.

## Development

```sh
go vet ./... && go test ./...
```

The roadmap and development notes live in [TODO.md](TODO.md) (in Russian). The project is a successor of two prototypes: **wksw** (Rust, the end-to-end pipeline and the positional table) and **oshitkeyb** (Go, dictionaries, analyzer, config).

## License

[MIT](LICENSE)
