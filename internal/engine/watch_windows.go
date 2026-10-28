package engine

import (
	"time"

	"golang.org/x/sys/windows"
)

// watchLoop asks Windows to tell us when anything in the folder changes and
// triggers a scan. The periodic scan still runs as a safety net.
func (s *session) watchLoop() {
	const filter = windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_DIR_NAME |
		windows.FILE_NOTIFY_CHANGE_SIZE | windows.FILE_NOTIFY_CHANGE_LAST_WRITE
	for s.ctx.Err() == nil {
		h, err := windows.FindFirstChangeNotification(s.root, true, filter)
		if err != nil {
			if !s.sleep(10 * time.Second) {
				return
			}
			continue
		}
		for {
			ev, err := windows.WaitForSingleObject(h, 500)
			if s.ctx.Err() != nil {
				windows.FindCloseChangeNotification(h)
				return
			}
			if err != nil || ev == uint32(windows.WAIT_FAILED) {
				break
			}
			if ev == windows.WAIT_OBJECT_0 {
				s.kick(s.scanReq)
				if windows.FindNextChangeNotification(h) != nil {
					break
				}
			}
		}
		windows.FindCloseChangeNotification(h)
		s.sleep(2 * time.Second)
	}
}

func hideFile(path string) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return
	}
	windows.SetFileAttributes(p, attrs|windows.FILE_ATTRIBUTE_HIDDEN)
}
