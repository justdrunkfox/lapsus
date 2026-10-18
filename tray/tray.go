// Package tray draws the lapsus tray icon (the flag of the active
// layout, dimmed when auto-fixing is paused) and serves the toggle
// menu: dictionary auto-fix, sound, notifications.
package tray

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"sync"
	"time"

	"github.com/energye/systray"
	dbus "github.com/godbus/dbus/v5"
	"github.com/voev/lapsus/layout"
)

// Controller is the part of the daemon the tray drives. Implemented by
// *daemon.Daemon (adapted in cmd).
type Controller interface {
	Paused() bool
	SetPaused(bool)
	CurrentLayout() layout.Layout
	Feedback() (notify bool, sound string)
	SetFeedback(notify bool, sound string) error
	RememberLayout() bool
	SetRememberLayout(bool) error
	DefaultLayout() string
	SetDefaultLayout(string) error
	Quit()
}

// Tray holds the menu items that need live updates. The daemon notifies
// it from its own goroutine (UpdateIcon/UpdatePause) while clicks run on
// the systray loop, so the cached settings live under mu.
type Tray struct {
	ctrl Controller

	mAuto     *systray.MenuItem
	mRemember *systray.MenuItem
	mDefOff   *systray.MenuItem
	mDefEn    *systray.MenuItem
	mDefRu    *systray.MenuItem
	mSound    *systray.MenuItem
	mNotify   *systray.MenuItem

	mu        sync.Mutex
	paused    bool
	sound     string
	notify    bool
	lastSound string // last non-off sound choice, restored by the toggle
}

// New builds a Tray over the controller.
func New(ctrl Controller) *Tray {
	t := &Tray{ctrl: ctrl}
	t.paused = ctrl.Paused()
	t.notify, t.sound = ctrl.Feedback()
	if t.sound != "" && t.sound != "bell" {
		t.lastSound = t.sound
	}
	return t
}

// Run registers the tray icon and blocks until ctx is cancelled.
func (t *Tray) Run(ctx context.Context) {
	go func() {
		<-ctx.Done()
		systray.Quit()
	}()
	waitWatcher(ctx)
	systray.Run(t.onReady, nil)
}

// waitWatcher blocks until a StatusNotifierWatcher appears on the bus.
// At session start the daemon races the shell: without this wait the
// one-shot registration fires before the tray host is up and the icon
// never appears (registration is not retried by the systray library).
func waitWatcher(ctx context.Context) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return
	}
	obj := conn.Object("org.freedesktop.DBus", "/org/freedesktop/DBus")
	for {
		call := obj.Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher")
		if call.Err == nil {
			var has bool
			if err := call.Store(&has); err == nil && has {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// cachedNotify returns the cached notification setting (thread-safe).
func (t *Tray) cachedNotify() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.notify
}

func (t *Tray) onReady() {
	t.mu.Lock()
	paused, sound, notify := t.paused, t.sound, t.notify
	t.mu.Unlock()
	systray.SetIcon(flagPNG(t.ctrl.CurrentLayout(), paused))
	systray.SetTitle("lapsus")
	systray.SetTooltip("lapsus — RU/EN layout fixer")

	t.mAuto = systray.AddMenuItemCheckbox("Автопереключение",
		"Авто-фикс слов по словарям (демон)", !paused)
	t.mRemember = systray.AddMenuItemCheckbox("Запоминать язык окон",
		"При фокусе окна возвращать язык, на котором в нём печатали", t.ctrl.RememberLayout())
	mDefault := systray.AddMenuItem("Язык новых окон",
		"Язык приложений, которые демон ещё не выучил")
	t.mDefOff = mDefault.AddSubMenuItemCheckbox("Выкл (не трогать)", "", t.ctrl.DefaultLayout() == "")
	t.mDefEn = mDefault.AddSubMenuItemCheckbox("Английский", "", t.ctrl.DefaultLayout() == "en")
	t.mDefRu = mDefault.AddSubMenuItemCheckbox("Русский", "", t.ctrl.DefaultLayout() == "ru")
	t.mDefOff.Click(func() { t.setDefault("") })
	t.mDefEn.Click(func() { t.setDefault("en") })
	t.mDefRu.Click(func() { t.setDefault("ru") })
	t.mSound = systray.AddMenuItemCheckbox("Звук",
		"Звук при перевороте", sound != "")
	t.mNotify = systray.AddMenuItemCheckbox("Нотификации",
		"Системная нотификация при перевороте", notify)
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Выход", "Остановить lapsus daemon")

	t.mAuto.Click(func() {
		t.ctrl.SetPaused(!t.ctrl.Paused())
	})
	t.mRemember.Click(func() {
		if err := t.ctrl.SetRememberLayout(!t.ctrl.RememberLayout()); err != nil {
			feedbackError(err)
			return
		}
		t.syncMenu()
	})
	t.mSound.Click(func() {
		// Toggle between off and the last used (or default) sound: a
		// custom path from the config survives off/on cycling.
		t.mu.Lock()
		newSound := ""
		if t.sound != "" {
			t.lastSound = t.sound
		} else if t.lastSound != "" {
			newSound = t.lastSound
		} else {
			newSound = "bell"
		}
		t.mu.Unlock()
		if err := t.ctrl.SetFeedback(t.cachedNotify(), newSound); err != nil {
			feedbackError(err)
			return
		}
		t.mu.Lock()
		t.sound = newSound
		t.mu.Unlock()
		t.syncMenu()
	})
	t.mNotify.Click(func() {
		t.mu.Lock()
		notify := !t.notify
		sound := t.sound
		t.mu.Unlock()
		if err := t.ctrl.SetFeedback(notify, sound); err != nil {
			feedbackError(err)
			return
		}
		t.mu.Lock()
		t.notify = notify
		t.mu.Unlock()
		t.syncMenu()
	})
	mQuit.Click(func() {
		t.ctrl.Quit()
	})
}

// feedbackError reports a failed settings persist to stderr (journal);
// the toggle stays in its previous state, which is what syncMenu shows.
func feedbackError(err error) {
	fmt.Fprintln(os.Stderr, "lapsus tray: settings not saved:", err)
}

// syncMenu updates checkbox states from the tray's cached settings.
func (t *Tray) syncMenu() {
	t.mu.Lock()
	paused, sound, notify := t.paused, t.sound, t.notify
	t.mu.Unlock()
	if paused {
		t.mAuto.Uncheck()
	} else {
		t.mAuto.Check()
	}
	if sound != "" {
		t.mSound.Check()
	} else {
		t.mSound.Uncheck()
	}
	if notify {
		t.mNotify.Check()
	} else {
		t.mNotify.Uncheck()
	}
	if t.ctrl.RememberLayout() {
		t.mRemember.Check()
	} else {
		t.mRemember.Uncheck()
	}
	def := t.ctrl.DefaultLayout()
	t.checkItem(t.mDefOff, def == "")
	t.checkItem(t.mDefEn, def == "en")
	t.checkItem(t.mDefRu, def == "ru")
}

// checkItem toggles a menu checkbox conditionally.
func (t *Tray) checkItem(m *systray.MenuItem, on bool) {
	if on {
		m.Check()
	} else {
		m.Uncheck()
	}
}

// setDefault applies and syncs the new-app default language radio.
func (t *Tray) setDefault(v string) {
	if err := t.ctrl.SetDefaultLayout(v); err != nil {
		feedbackError(err)
		return
	}
	t.syncMenu()
}

// UpdateIcon redraws the flag; wire to daemon layout changes.
func (t *Tray) UpdateIcon(l layout.Layout) {
	t.mu.Lock()
	paused := t.paused
	t.mu.Unlock()
	systray.SetIcon(flagPNG(l, paused))
}

// UpdatePause refreshes the icon dimming and the checkbox; wire to
// daemon pause changes.
func (t *Tray) UpdatePause(paused bool) {
	t.mu.Lock()
	t.paused = paused
	t.mu.Unlock()
	t.UpdateIcon(t.ctrl.CurrentLayout())
	t.syncMenu()
}

const (
	iconSize   = 32
	iconRadius = 7
)

// flagPNG renders the layout flag as a 32x32 PNG with rounded corners,
// dimmed when paused.
func flagPNG(l layout.Layout, dimmed bool) []byte {
	img := image.NewRGBA(image.Rect(0, 0, iconSize, iconSize))
	switch l {
	case layout.LayoutRU:
		drawRU(img)
	default:
		drawUS(img)
	}
	if dimmed {
		darken(img)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

// FlagPNGForCheck exposes flagPNG for visual verification tooling.
func FlagPNGForCheck() []byte { return flagPNG(layout.LayoutRU, false) }

// drawRU paints the russian tricolor.
func drawRU(img *image.RGBA) {
	bands := [3]color.RGBA{
		{240, 240, 240, 255},
		{0, 57, 166, 255},
		{213, 43, 30, 255},
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		band := bands[(y-b.Min.Y)*3/b.Dy()]
		for x := b.Min.X; x < b.Max.X; x++ {
			if !inRoundedRect(x, y, b) {
				continue
			}
			img.Set(x, y, band)
		}
	}
}

// drawUS paints a simplified US flag: red stripes on white with a blue
// canton.
func drawUS(img *image.RGBA) {
	white := color.RGBA{245, 245, 245, 255}
	red := color.RGBA{179, 25, 66, 255}
	blue := color.RGBA{10, 49, 97, 255}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		stripe := (y-b.Min.Y)*13/b.Dy()%2 == 0
		for x := b.Min.X; x < b.Max.X; x++ {
			if !inRoundedRect(x, y, b) {
				continue
			}
			c := white
			if stripe {
				c = red
			}
			if (x-b.Min.X)*7/b.Dx() < 3 && (y-b.Min.Y)*7/b.Dy() < 4 {
				c = blue
			}
			img.Set(x, y, c)
		}
	}
}

// inRoundedRect reports whether the pixel belongs to the rounded
// rectangle covering the image bounds (corner circles of iconRadius).
func inRoundedRect(x, y int, b image.Rectangle) bool {
	if x < b.Min.X || y < b.Min.Y || x >= b.Max.X || y >= b.Max.Y {
		return false
	}
	r := iconRadius
	w, h := b.Dx(), b.Dy()
	cx, cy := -1, -1
	if x < r && y < r {
		cx, cy = b.Min.X+r, b.Min.Y+r
	} else if x >= w-r && y < r {
		cx, cy = b.Min.X+w-r-1, b.Min.Y+r
	} else if x < r && y >= h-r {
		cx, cy = b.Min.X+r, b.Min.Y+h-r-1
	} else if x >= w-r && y >= h-r {
		cx, cy = b.Min.X+w-r-1, b.Min.Y+h-r-1
	}
	if cx < 0 {
		return true
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

// darken dims the icon (paused auto-fixing).
func darken(img *image.RGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.RGBAAt(x, y)
			img.SetRGBA(x, y, color.RGBA{c.R * 45 / 100, c.G * 45 / 100, c.B * 45 / 100, c.A})
		}
	}
}
