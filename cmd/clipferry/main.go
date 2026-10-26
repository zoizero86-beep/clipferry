//go:build darwin || windows

// Command clipferry is the desktop app: a menu bar (macOS) or system tray
// (Windows) icon with a small window, wrapped around the Clipferry engine.
package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/bkrdmrcioglu/clipferry/internal/core"
	"github.com/bkrdmrcioglu/clipferry/internal/lan"
	"github.com/bkrdmrcioglu/clipferry/internal/store"
	"github.com/bkrdmrcioglu/clipferry/internal/sysclip"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed icons/tray-template.png
var trayTemplateIcon []byte

//go:embed icons/tray.png
var trayIcon []byte

func dataDir() string {
	if d := os.Getenv("CLIPFERRY_DATA_DIR"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "Clipferry")
}

func main() {
	autostart := flag.String("autostart", "", `"on" or "off": register or remove start at login, then exit`)
	flag.Parse()

	dir := dataDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(dir, "clipferry.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		log.SetOutput(logFile)
	}

	st, err := store.Open(filepath.Join(dir, "clipferry.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	firstRun, _ := st.Setting("launched", "")
	st.SetSetting("launched", "1")

	eng, err := core.New(core.Options{Store: st, Clipboard: sysclip.New(), Discovery: lan.MDNS{}, Logf: log.Printf})
	if err != nil {
		log.Fatal(err)
	}

	port := core.DefaultPort
	if p, err := strconv.Atoi(os.Getenv("CLIPFERRY_PORT")); err == nil {
		port = p
	}
	ln, err := core.Listen(port)
	if err != nil {
		// Something else holds the usual port; MCP clients will need the new
		// address, which the app shows in "Connect an AI tool".
		log.Printf("port %d busy (%v), picking another", port, err)
		if ln, err = core.Listen(0); err != nil {
			log.Fatal(err)
		}
	}
	url := fmt.Sprintf("http://%s/#token=%s", ln.Addr().String(), eng.Token())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	go func() {
		if err := eng.Serve(ctx, ln); err != nil {
			log.Printf("local server: %v", err)
		}
	}()

	var window *application.WebviewWindow
	app := application.New(application.Options{
		Name:        "Clipferry",
		Description: "Shared clipboard for your computers",
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyAccessory,
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.bkrdmrcioglu.clipferry",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				if window != nil {
					window.Show()
					window.Focus()
				}
			},
		},
		OnShutdown: cancel,
	})

	if *autostart != "" {
		var err error
		if *autostart == "on" {
			err = app.Autostart.Enable()
		} else {
			err = app.Autostart.Disable()
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "autostart:", err)
			os.Exit(1)
		}
		return
	}

	window = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main",
		Title:     "Clipferry",
		Width:     520,
		Height:    740,
		MinWidth:  360,
		MinHeight: 420,
		URL:       url,
		Hidden:    firstRun != "", // the first launch opens the window so pairing is obvious
		Windows:   application.WindowsWindow{HiddenOnTaskbar: false},
		Mac: application.MacWindow{
			TitleBar: application.MacTitleBarDefault,
		},
	})
	// Closing the window keeps Clipferry running in the menu bar / tray.
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		window.Hide()
		e.Cancel()
	})

	show := func(path string) {
		if path != "" {
			window.SetURL(fmt.Sprintf("http://%s/%s#token=%s", ln.Addr().String(), path, eng.Token()))
		}
		window.Show()
		window.Focus()
	}

	tray := app.SystemTray.New()
	if runtime.GOOS == "darwin" {
		tray.SetTemplateIcon(trayTemplateIcon)
	} else {
		tray.SetIcon(trayIcon)
	}
	tray.SetTooltip("Clipferry")

	menu := app.NewMenu()
	statusItem := menu.Add("Starting…").SetEnabled(false)
	menu.AddSeparator()
	menu.Add("Open Clipferry").OnClick(func(*application.Context) { show("") })
	menu.Add("Add a device…").OnClick(func(*application.Context) { show("?pair=1") })
	menu.AddSeparator()
	pauseItem := menu.AddCheckbox("Pause syncing", eng.Settings().Paused)
	pauseItem.OnClick(func(c *application.Context) {
		s := eng.Settings()
		s.Paused = c.ClickedMenuItem().Checked()
		eng.UpdateSettings(s)
	})
	eng.SetAutostart(app.Autostart)
	loginEnabled, _ := app.Autostart.IsEnabled()
	loginItem := menu.AddCheckbox("Start at login", loginEnabled)
	loginItem.OnClick(func(c *application.Context) {
		if err := eng.SetAutostartEnabled(c.ClickedMenuItem().Checked()); err != nil {
			log.Printf("autostart: %v", err)
			loginItem.SetChecked(!c.ClickedMenuItem().Checked())
			menu.Update()
		}
	})
	menu.AddSeparator()
	menu.Add("Quit Clipferry").OnClick(func(*application.Context) { app.Quit() })
	tray.SetMenu(menu)

	refresh := func() {
		st := eng.Status()
		set := eng.Settings()
		label := "Not paired — open to add a device"
		switch {
		case st.Paired && set.Paused:
			label = "Syncing paused"
		case st.Paired && st.Connected:
			label = "Syncing"
			if ds, err := eng.Store().Devices(); err == nil && len(ds) > 1 {
				label = fmt.Sprintf("Syncing with %d other device", len(ds)-1)
				if len(ds) > 2 {
					label += "s"
				}
			}
		case st.Paired:
			label = "Offline — will sync when back"
		}
		statusItem.SetLabel(label)
		pauseItem.SetChecked(set.Paused)
		if on := eng.Autostart(); on != nil {
			loginItem.SetChecked(*on)
		}
		menu.Update()
	}
	go func() {
		events, stop := eng.Subscribe()
		defer stop()
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-events:
				switch ev.Type {
				case "status", "devices", "settings", "pair.done", "autostart":
					application.InvokeAsync(refresh)
				}
			}
		}
	}()
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		refresh()
		if firstRun == "" {
			show("") // a menu bar app is never activated on its own
		}
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
