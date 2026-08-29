# lapsus

Фиксер раскладки RU↔EN для [niri](https://github.com/niri-wm/niri) (Wayland, Linux):
исправляет текст, набранный не в той раскладке — в духе Punto Switcher, но
свой. Название — от лат. *lapsus linguae*, «оговорка».

Преемник двух предыдущих попыток, объединяет их наработки:

- **wksw** (`../wksw`, Rust, март 2026) — рабочий end-to-end прототип:
  конвертация последнего слова по хоткею через `wtype` + `wl-clipboard`.
  Отсюда взят целевой пайплайн и позиционная таблица символов.
- **oshitkeyb** (`../oshitkeyb`, Go, май–июнь 2026) — словари, анализатор,
  конфиг. Отсюда перенесена вся логика (включая 2 коммита из worktree
  `impl-continue`, которые не попали в master).

## Статус

| Компонент | Что делает | Статус |
|---|---|---|
| `layout` | конвертация QWERTY↔ЙЦУКЕН по физическим позициям клавиш | ✅ |
| `dict` | частотные словари: embedded top-20k (OpenSubtitles) + пользовательские | ✅ |
| `analyze` | детект «слово набрано не в той раскладке» по словарям | ✅ |
| `config` | TOML-конфиг с дефолтами и валидацией | ✅ |
| `lapsus check` / `convert` | самопроверка; конвертация текста с авто-направлением | ✅ |
| `lapsus fix` | one-shot фикс последнего слова по хоткею niri | ✅ M1 (проверен вживую в waterfox) |
| `lapsus daemon` | авто-режим (evdev-триггер на границе слова) | 📋 M2 |

План работ — в [TODO.md](TODO.md).

## `lapsus fix`: хоткей в niri (M1)

Бинд в `~/.config/niri/config.kdl`:

```kdl
binds {
    Mod+Shift+S { spawn "lapsus" "fix"; }
}
```

Что происходит при нажатии:

```
niri IPC: focused-window (app_id)  ──►  это терминал? (hotkey.terminals)
   │                                            │
   ▼ нет (GUI)                                  ▼ да (терминал)
wtype Ctrl+Shift+Left                     текст уже выделен мышью:
   │  (выделить слово)                    wl-paste --primary
   ▼                                            │
wl-paste --primary  ◄───────────────────────────┘
   │
analyze (словари)  ──►  не та раскладка?
   │ да
   ▼
терминал: wl-copy + Ctrl+Shift+V      GUI: wtype -- <слово>
   │                                     │
   └──────────────┬──────────────────────┘
                  ▼
   niri msg action switch-layout <индекс>   ← ПОСЛЕДНИМ (см. грабли)
```

Детали:

- **Раскладка после фикса.** niri IPC (`keyboard-layouts`) отдаёт список
  раскладок и текущий индекс — lapsus переключает на раскладку исправленного
  слова по индексу. `niri msg action switch-layout` принимает только
  `next` / `prev` / `<индекс>` (не имя); на старых niri без этого IPC свитч
  просто пропускается. Порядок «сначала инъекция, потом свитч» зафиксирован
  юнит-тестом (niri#3568).
- **Терминалы** не обновляют primary selection по Ctrl+Shift+Left, поэтому
  там текст нужно выделить мышью заранее, фикс идёт через буфер обмена
  (Ctrl+Shift+V) и затирает его. Список терминалов — по `app_id` окна.
- **Отказоустойчивость:** двойное нажатие хоткея не запускает второй
  пайплайн (flock); чтение primary ретраится; мультистрочное/длинное
  выделение не заменяется (отказ без порчи текста); если фикс не нужен —
  выделение схлопывается, каретка на месте.
- Отладка: `lapsus fix -v` (пошаговый лог) и `lapsus fix -n` (dry-run —
  покажет, что зафиксировал бы, ничего не меняя).

Проверено вживую на niri 26.04 (waterfox, адресная строка):
`ghbdtn` → `привет`, раскладка переключилась на Russian. Остальные пункты
ручного чек-листа — в [TODO.md](TODO.md).

Ключевые факты, на которых держится дизайн:

- `wtype` печатает keysyms, а не сканкоды → вставляет кириллицу
  независимо от активной раскладки (доказано wksw).
- `niri msg action switch-layout` — нативное переключение раскладки по IPC.
- libei/EIS для инъекции не подходит: niri не даёт EIS-сервер (поэтому из
  старого плана oshitkeyb он выкинут; CGO в проекте запрещён — нужна чистая
  кросс-компиляция).

## Требования (linux-машина с niri)

- go ≥ 1.26
- [wtype](https://github.com/atx/wtype), [wl-clipboard](https://github.com/bugaevc/wl-clipboard)
- niri (хоткей, `niri msg`)
- для M2 (daemon): членство в группе `input` — чтение `/dev/input/eventX`
  без grab и без root

## Сборка

На linux-машине:

```sh
go build -o ~/.local/bin/lapsus ./cmd/lapsus
lapsus check        # самопроверка: конфиг, словари, анализатор
lapsus convert "Ghbdtn? vbh!"   # → Привет, мир!
```

Кросс-компиляция из macOS (статический бинарь, без CGO):

```sh
GOOS=linux GOARCH=amd64 go build -o dist/lapsus-linux-amd64 ./cmd/lapsus
GOOS=linux GOARCH=arm64 go build -o dist/lapsus-linux-arm64 ./cmd/lapsus
```

## Конфигурация

`~/.config/lapsus/config.toml` (все ключи опциональны, дефолты валидны):

```toml
[hotkey]
source    = "niri"          # niri | evdev
key       = "Ctrl+Alt+K"
terminals = ["foot", "kitty", "Alacritty", "wezterm", "ghostty", "st"]
# ↑ app_id терминалов (niri msg --json focused-window покажет ваш);
#   для них действует отдельный терминальный путь фикса

[capture]
method = "clipboard"     # clipboard (primary selection) | cut (Ctrl+X-фолбэк
                         # для приложений, не обновляющих primary)

[fix]
switch_layout = true     # переключать раскладку на язык исправленного слова
pause_ms      = 50       # пауза после синтетических нажатий (wtype)

[autodetect]
mode = "both"            # hotkey | continuous | both

[dictionary]
user_dir = "~/.config/lapsus/dicts"   # en_freq.txt / ru_freq.txt
```

Пользовательские словари: файлы `en_freq.txt` / `ru_freq.txt`, формат
`слово частота` (по слову в строке), переопределяют встроенные частоты —
туда можно добавлять свои термины, никнеймы, названия проектов.

## Словари

`dict/dict_data/{en,ru}_freq.txt` — top-20 000 слов, [hermitdave/FrequencyWords](https://github.com/hermitdave/FrequencyWords)
(corpus: OpenSubtitles 2018). Обновить:

```sh
curl -fsSL https://raw.githubusercontent.com/hermitdave/FrequencyWords/master/content/2018/en/en_50k.txt | head -n 20000 > dict/dict_data/en_freq.txt
curl -fsSL https://raw.githubusercontent.com/hermitdave/FrequencyWords/master/content/2018/ru/ru_50k.txt | head -n 20000 > dict/dict_data/ru_freq.txt
```

## Конвертация по позициям

Таблица в `layout/layout.go` позиционно-верная: символ заменяется на символ
с той же физической клавиши (с учётом Shift) другой раскладки. Следствия:

- `?` → `,` (Shift+/ на ЙЦУКЕН даёт запятую) и обратно `,` → `?`;
- `@ # $ ^ &` → `" № ; : ?` (сдвиговый ряд цифр);
- `` ` `` ↔ `ё`, `~` ↔ `Ё`, `<` ↔ `Б`, `>` ↔ `Ю`;
- цифры и непомапленные символы проходят как есть.

`analyze` пунктуацию из ядра слова вырезает и не трогает, поэтому
случаи вроде `ghbdtn!` → `привет!` обрабатываются аккуратно.
