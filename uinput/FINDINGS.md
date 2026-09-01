# Находки эксперимента uinput (2026-08-31 → 2026-09-01, ThinkPad T14, Debian 13, ядро 7.0.13+deb13)

## ИТОГ: uinput РАБОТАЕТ. Инъекция через него на niri проверена живьём.

Требования и найденные грабли:

1. Права: /dev/uinput по умолчанию root-only 600. Правило
   deploy/60-lapsus-uinput.rules (MODE 0660, GROUP input) + разово
   `sudo chgrp input /dev/uinput && sudo chmod 660 /dev/uinput`
   (udevadm trigger на живой узел даёт EINVAL — применять руками).
2. ГЛАВНАЯ ГРАБЛЯ (мой баг): UI_SET_* принимают ЗНАЧЕНИЕ как raw arg,
   а не указатель. Ядро сравнивает arg с максимумом
   (`if (arg > max) EINVAL`) — переданный указатель всегда «больше»,
   отсюда EINVAL на ВСЁ, включая безобидный UI_GET_VERSION.
   В Go: syscall(SYS_IOCTL, fd, req, uintptr(value)) — без &v.
3. UI_DEV_SETUP обязателен перед UI_DEV_CREATE на современных ядрах
   («write device info first» иначе): struct uinput_setup =
   input_id(8) + name[80] + ff_effects_max(4) = 92 байта,
   ioctl 0x405c5503.
4. EV_SYN регистрировать через UI_SET_EVBIT НЕЛЬЗЯ (EINVAL) — он всегда включён.
5. niri#3568 на uinput-устройствах НЕ проявился: группа раскладки
   стабильна до/после инъекции (idx=0 → idx=0).
6. Самоэхо: наша uinput-клавиатура видна в /dev/input — демон-фильтр
   (игнор имени «lapsus») обязателен при переходе на эту инъекцию.

Статус: инъекция кодов проверена (ghbdtn доставлен в xterm и waterfox-
подобное поле ранее не проверялся; раскладка стабильна). Интеграция в
демон (строка→коды, конфиг-флаг, фолбэк wtype) — следующий шаг ветки.
