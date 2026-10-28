package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/harry1453/go-common-file-dialog/cfd"
	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procShowWindow               = user32.NewProc("ShowWindow")
	procIsIconic                 = user32.NewProc("IsIconic")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procSetWindowLongPtrW        = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW          = user32.NewProc("CallWindowProcW")
	procLoadImageW               = user32.NewProc("LoadImageW")
	procSendMessageW             = user32.NewProc("SendMessageW")
	procGetDpiForSystem          = user32.NewProc("GetDpiForSystem")
	procAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")
	procMessageBoxW              = user32.NewProc("MessageBoxW")
	dwmapi                       = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute    = dwmapi.NewProc("DwmSetWindowAttribute")
	shell32                      = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW          = shell32.NewProc("ShellExecuteExW")
)

const (
	swHide        = 0
	swShow        = 5
	swRestore     = 9
	wmClose       = 0x0010
	wmSetIcon     = 0x0080
	wmSettingChg  = 0x001A
	gwlpWndProc   = ^uintptr(3) // -4
	imageIcon     = 1
	lrDefaultSize = 0x40
)

// ---------- main window ----------

type appWindow struct {
	mu       sync.Mutex
	url      string
	dataDir  string
	wv       webview2.WebView
	hwnd     uintptr
	origProc uintptr
}

var mainWindow *appWindow

func newAppWindow(url, dataDir string) *appWindow {
	mainWindow = &appWindow{url: url, dataDir: dataDir}
	return mainWindow
}

// Show opens the window, creating it on first use. Closing it only hides
// it; FolderChain keeps running in the tray.
func (w *appWindow) Show() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.wv != nil {
		w.wv.Dispatch(func() { w.bringToFront() })
		return
	}
	if v, err := webviewloader.GetInstalledVersion(); err != nil || v == "" {
		openURL(w.url) // no WebView2 runtime: fall back to the default browser
		return
	}
	ready := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		scale := 1.0
		if dpi, _, _ := procGetDpiForSystem.Call(); dpi > 0 {
			scale = float64(dpi) / 96
		}
		wv := webview2.NewWithOptions(webview2.WebViewOptions{
			DataPath:  w.dataDir,
			AutoFocus: true,
			WindowOptions: webview2.WindowOptions{
				Title:  "FolderChain",
				Width:  uint(1040 * scale),
				Height: uint(720 * scale),
				Center: true,
			},
		})
		if wv == nil {
			close(ready)
			openURL(w.url)
			return
		}
		w.wv = wv
		w.hwnd = uintptr(wv.Window())
		wv.SetSize(int(720*scale), int(520*scale), webview2.HintMin)
		w.setIcon()
		applyTitleBarTheme(w.hwnd)
		w.origProc, _, _ = procSetWindowLongPtrW.Call(w.hwnd, gwlpWndProc, windows.NewCallback(wndProc))
		wv.Navigate(w.url)
		w.bringToFront()
		close(ready)
		wv.Run()
	}()
	<-ready
}

func (w *appWindow) bringToFront() {
	procShowWindow.Call(w.hwnd, swShow)
	if iconic, _, _ := procIsIconic.Call(w.hwnd); iconic != 0 {
		procShowWindow.Call(w.hwnd, swRestore)
	}
	procSetForegroundWindow.Call(w.hwnd)
	w.wv.Eval("window.__fcHidden=false; window.refresh && refresh()")
}

func (w *appWindow) setIcon() {
	var hinst windows.Handle
	windows.GetModuleHandleEx(0, nil, &hinst)
	name, _ := windows.UTF16PtrFromString("APP")
	for _, big := range []uintptr{0, 1} {
		size := uintptr(16)
		if big == 1 {
			size = 32
		}
		h, _, _ := procLoadImageW.Call(uintptr(hinst), uintptr(unsafe.Pointer(name)), imageIcon, size, size, 0)
		if h != 0 {
			procSendMessageW.Call(w.hwnd, wmSetIcon, big, h)
		}
	}
}

func (w *appWindow) visible() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.hwnd == 0 {
		return false
	}
	v, _, _ := procIsWindowVisible.Call(w.hwnd)
	return v != 0
}

func (w *appWindow) owner() uintptr {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hwnd
}

func wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	w := mainWindow
	switch msg {
	case wmClose:
		procShowWindow.Call(hwnd, swHide)
		w.wv.Eval("window.__fcHidden=true")
		return 0
	case wmSettingChg:
		applyTitleBarTheme(hwnd)
	}
	r, _, _ := procCallWindowProcW.Call(w.origProc, hwnd, msg, wp, lp)
	return r
}

// applyTitleBarTheme makes the native title bar dark when Windows apps use
// dark mode, so it matches the page.
func applyTitleBarTheme(hwnd uintptr) {
	dark := uint32(0)
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE); err == nil {
		if v, _, err := k.GetIntegerValue("AppsUseLightTheme"); err == nil && v == 0 {
			dark = 1
		}
		k.Close()
	}
	const dwmwaUseImmersiveDarkMode = 20
	procDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkMode, uintptr(unsafe.Pointer(&dark)), 4)
}

// ---------- shell helpers ----------

func openURL(u string) {
	windows.ShellExecute(0, windows.StringToUTF16Ptr("open"), windows.StringToUTF16Ptr(u), nil, nil, windows.SW_SHOWNORMAL)
}

func openFolder(path string) error {
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		return fmt.Errorf("folder not found: %s", path)
	}
	return exec.Command("explorer.exe", path).Start()
}

func pickFolder(start string, owner uintptr) (string, error) {
	type result struct {
		path string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
		defer windows.CoUninitialize()
		dlg, err := cfd.NewSelectFolderDialog(cfd.DialogConfig{
			Title:              "Choose the folder to sync",
			Folder:             start,
			ParentWindowHandle: owner,
		})
		if err != nil {
			ch <- result{"", err}
			return
		}
		defer dlg.Release()
		p, err := dlg.ShowAndGetResult()
		if errors.Is(err, cfd.ErrorCancelled) {
			err = nil
		}
		ch <- result{p, err}
	}()
	r := <-ch
	return r.path, r.err
}

func messageBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x40) // MB_ICONINFORMATION
}

// ---------- start with Windows ----------

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func autostartName() string {
	if *flagProfile != "" {
		return "FolderChain-" + *flagProfile
	}
	return "FolderChain"
}

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(autostartName())
	return err == nil
}

func setAutostart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		err := k.DeleteValue(autostartName())
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := fmt.Sprintf(`"%s" --background`, exe)
	if *flagProfile != "" {
		cmd += fmt.Sprintf(` --profile "%s"`, *flagProfile)
	}
	return k.SetStringValue(autostartName(), cmd)
}

// ---------- firewall ----------

type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     windows.Handle
}

// allowThroughFirewall adds an inbound rule for this exe (UAC prompt) and
// waits for it to finish.
func allowThroughFirewall(owner uintptr) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	name := autostartName()
	args := fmt.Sprintf(`/c netsh advfirewall firewall delete rule name="%[1]s" >nul & netsh advfirewall firewall add rule name="%[1]s" dir=in action=allow program="%[2]s" enable=yes profile=any`, name, exe)
	if strings.ContainsAny(exe, `"&|<>^`) {
		return errors.New("FolderChain's path contains characters the firewall tool can't handle")
	}
	info := shellExecuteInfo{
		fMask:        0x40, // SEE_MASK_NOCLOSEPROCESS
		hwnd:         owner,
		lpVerb:       windows.StringToUTF16Ptr("runas"),
		lpFile:       windows.StringToUTF16Ptr("cmd.exe"),
		lpParameters: windows.StringToUTF16Ptr(args),
		nShow:        swHide,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if r, _, err := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return errors.New("cancelled — administrator permission is needed to change the firewall")
		}
		return err
	}
	defer windows.CloseHandle(info.hProcess)
	windows.WaitForSingleObject(info.hProcess, 60000)
	var code uint32
	windows.GetExitCodeProcess(info.hProcess, &code)
	if code != 0 {
		return fmt.Errorf("firewall tool exited with code %d", code)
	}
	return nil
}

// ---------- single instance ----------

func instanceNames() (mutex, event string) {
	suffix := ""
	if *flagProfile != "" {
		suffix = "-" + *flagProfile
	}
	return `Local\FolderChain-Instance` + suffix, `Local\FolderChain-Show` + suffix
}

// claimInstance reports whether this is the only running copy, returning
// the "show window" event to wait on. Otherwise it asks the running copy to
// show itself.
func claimInstance() (windows.Handle, bool) {
	mname, ename := instanceNames()
	_, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(mname))
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if ev, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, windows.StringToUTF16Ptr(ename)); err == nil {
			procAllowSetForegroundWindow.Call(^uintptr(0)) // ASFW_ANY
			windows.SetEvent(ev)
			windows.CloseHandle(ev)
		}
		return 0, false
	}
	ev, _ := windows.CreateEvent(nil, 0, 0, windows.StringToUTF16Ptr(ename))
	return ev, true
}

func waitForShowRequests(ev windows.Handle, show func()) {
	for ev != 0 {
		r, err := windows.WaitForSingleObject(ev, windows.INFINITE)
		if err != nil || r != windows.WAIT_OBJECT_0 {
			return
		}
		show()
	}
}
