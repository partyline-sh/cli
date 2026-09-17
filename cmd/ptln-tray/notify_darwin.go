//go:build darwin && tray

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// postNotification delivers a banner AS THIS APP.
//
// This replaced `osascript -e 'display notification'`, which posts as SCRIPT EDITOR — macOS
// attributes a notification to whichever app posted it. That single fact caused both complaints
// about the old banner: it wore Script Editor's icon rather than ours, and clicking it activated
// Script Editor, which with no document open threw up a file-open dialog that looked like a broken
// link. There was no link; there was a misattributed notification.
//
// Posting from inside Partyline.app means our icon, and a click that lands on an LSUIElement app
// with no window — a harmless no-op, which is exactly what a status banner should do.
//
// NSUserNotification is formally deprecated in favor of UNUserNotificationCenter, but UN requires an
// authorization round-trip and a signed bundle to behave predictably; NSUserNotification needs
// neither and still delivers. Deprecation warnings are silenced rather than papered over elsewhere.
static void postNotification(const char *body) {
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
  NSUserNotification *n = [[NSUserNotification alloc] init];
  n.title = @"Partyline";
  n.informativeText = [NSString stringWithUTF8String:body];
  n.soundName = NSUserNotificationDefaultSoundName;
  // No actionButton and no userInfo: nothing to click through to. The session this is about is
  // already open in a terminal somewhere, and "opening" it would start a SECOND client.
  [[NSUserNotificationCenter defaultUserNotificationCenter] deliverNotification:n];
#pragma clang diagnostic pop
}
*/
import "C"

import "unsafe"

// nativeNotify posts the banner through Cocoa. Silent no-op if the process somehow isn't running
// from the bundle — better a missing banner than a Script Editor one.
func nativeNotify(body string) {
	c := C.CString(body)
	defer C.free(unsafe.Pointer(c))
	C.postNotification(c)
}
