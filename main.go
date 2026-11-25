package main

import (
	"clankerv2/agent"
	"clankerv2/data"
	"clankerv2/tui"
	"embed"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
)

// Version is injected at build time via -ldflags "-X main.Version=..."
var Version = "dev"

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var icon []byte

func main() {
	// Setup cleanup for managed executors
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Received shutdown signal, cleaning up executors...")
		agent.Cleanup()
		agent.SessionLog().CloseSessionFile()
		os.Exit(0)
	}()
	defer agent.Cleanup()
	defer agent.SessionLog().CloseSessionFile()

	err := data.InitDB()
	if err != nil {
		fmt.Println("Database could not be initialized, goodbye")
		os.Exit(1)
	}

	app := NewApp()

	cliMode := flag.Bool("cli", false, "Start in CLI mode")
	flag.BoolVar(cliMode, "c", false, "Start in CLI mode (shorthand)")
	flag.Parse()

	// Background summary sweep: after 10m of idle time it summarizes any
	// session whose summary is missing or no longer covers the latest
	// message. Started here (not in wails startup) so both UI modes get it.
	// agent.StartSummaryEngine()

	if *cliMode {
		version := fmt.Sprintf("1.0-%s", Version)
		fmt.Printf("Starting Clanker %s in cliMode\n", version)

		// TUI: keep log lines off the terminal so they never corrupt the
		// Bubble Tea rendering; the per-session file is the durable record.
		agent.SessionLog().SetStderr(false)
		log.SetOutput(agent.SessionLog()) // ensure global output is the tee writer
		m := tui.NewModel(version)
		p := tea.NewProgram(m)

		// Async listener is started inside tui.Init()
		time.Sleep(3000 * time.Millisecond)
		if _, err := p.Run(); err != nil {
			fmt.Printf("Error running TUI: %v\n", err)
			os.Exit(1)
		}

		return
	}

	appMenu := menu.NewMenu()
	fileMenu := appMenu.AddSubmenu("File")
	fileMenu.AddText("Open Workspace", nil, func(cd *menu.CallbackData) {
		fmt.Println(cd)
	})
	fileMenu.AddText("New Session", nil, func(cd *menu.CallbackData) {
		fmt.Println(cd)
	})

	err = wails.Run(&options.App{
		Title: fmt.Sprintf("Clanker v2 (%s)", Version),
		Menu:  appMenu,
		Linux: &linux.Options{
			Icon:        icon,
			ProgramName: "Clanker v2",
		},
		Width:  1600,
		Height: 1400,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        app.startup,
		Bind: []any{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
