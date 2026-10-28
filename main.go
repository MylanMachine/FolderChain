// FolderChain keeps one folder in sync across computers on a local network,
// peer to peer and end-to-end encrypted. It lives in the system tray.
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"fyne.io/systray"

	"folderchain/internal/engine"
)

var (
	flagBackground = flag.Bool("background", false, "start minimized to the tray")
	flagProfile    = flag.String("profile", "", "run a separate instance with its own settings (for testing)")
	flagPort       = flag.Int("port", 0, "TCP port for sync connections (default 47124)")
	flagNoWindow   = flag.Bool("no-window", false, "never open the window (testing)")
)

var (
	//go:embed assets/tray.ico
	icoOK []byte
	//go:embed assets/tray-sync.ico
	icoSync []byte
	//go:embed assets/tray-off.ico
	icoOff []byte
	//go:embed assets/tray-warn.ico
	icoWarn []byte
)

func init() { runtime.LockOSThread() } // the tray's message loop runs on the main thread

func main() {
	flag.Parse()
	showEvent, first := claimInstance()
	if !first {
		return // the running copy will show its window
	}

	dataDir := dataDirectory()
	logger := openLog(dataDir)
	logger.Printf("FolderChain %s starting (data: %s)", engine.AppVersion, dataDir)

	eng, err := engine.New(engine.Options{DataDir: dataDir, ListenPort: *flagPort, Log: logger})
	if err != nil {
		messageBox("FolderChain", "FolderChain couldn't start:\n\n"+err.Error())
		os.Exit(1)
	}
	if err := eng.Start(); err != nil {
		logger.Printf("start: %v", err)
	}
	if autostartEnabled() {
		setAutostart(true) // keep the path current if the exe was moved
	}

	srv, url, err := startUIServer(eng)
	if err != nil {
		messageBox("FolderChain", "FolderChain couldn't start its window:\n\n"+err.Error())
		os.Exit(1)
	}
	win := newAppWindow(url, filepath.Join(dataDir, "webview"))
	srv.win = win
	if *flagProfile != "" {
		logger.Printf("ui: %s", url) // test profiles only; the token is per run
	}
	show := win.Show
	if *flagNoWindow {
		show = func() {}
	}
	go waitForShowRequests(showEvent, show)
	if !*flagBackground || !eng.Configured() {
		go show()
	}

	systray.Run(func() { trayReady(eng, show) }, func() {
		eng.Stop()
		logger.Printf("stopped")
	})
}

func dataDirectory() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	name := "FolderChain"
	if *flagProfile != "" {
		name += "-" + *flagProfile
	}
	return filepath.Join(base, name)
}

// openLog writes to folderchain.log in the data folder, keeping one old file.
func openLog(dir string) *log.Logger {
	os.MkdirAll(dir, 0o700)
	p := filepath.Join(dir, "folderchain.log")
	if st, err := os.Stat(p); err == nil && st.Size() > 5<<20 {
		os.Rename(p, p+".old")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return log.New(io.Discard, "", 0)
	}
	return log.New(f, "", log.LstdFlags)
}

func trayReady(eng *engine.Engine, show func()) {
	systray.SetIcon(icoOff)
	systray.SetTooltip("FolderChain")
	systray.SetOnTapped(show)

	mOpen := systray.AddMenuItem("Open FolderChain", "")
	mFolder := systray.AddMenuItem("Open synced folder", "")
	systray.AddSeparator()
	mPause := systray.AddMenuItemCheckbox("Pause syncing", "", false)
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit FolderChain", "Stop syncing and exit")

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				go show()
			case <-mFolder.ClickedCh:
				if f := eng.Folder(); f != "" {
					openFolder(f)
				} else {
					go show()
				}
			case <-mPause.ClickedCh:
				eng.SetPaused(!mPause.Checked())
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()

	go func() {
		var lastIcon *byte
		lastTip := ""
		for {
			st := eng.State()
			ico := icoOK
			switch st.Status {
			case "syncing", "scanning":
				ico = icoSync
			case "waiting", "paused", "setup":
				ico = icoOff
			case "error":
				ico = icoWarn
			}
			if &ico[0] != lastIcon {
				systray.SetIcon(ico)
				lastIcon = &ico[0]
			}
			online := 0
			for _, d := range st.Devices {
				if d.Online {
					online++
				}
			}
			tip := "FolderChain — " + st.StatusText
			if st.Configured && !st.Paused {
				tip += fmt.Sprintf(" · %d device%s online", online, map[bool]string{true: "", false: "s"}[online == 1])
			}
			if tip != lastTip {
				systray.SetTooltip(tip)
				lastTip = tip
			}
			if st.Paused != mPause.Checked() {
				if st.Paused {
					mPause.Check()
				} else {
					mPause.Uncheck()
				}
			}
			if st.Configured {
				mFolder.Enable()
			} else {
				mFolder.Disable()
			}
			time.Sleep(2 * time.Second)
		}
	}()
}
