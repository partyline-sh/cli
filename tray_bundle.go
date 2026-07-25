//go:build darwin

package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// tray_bundle.go — materializes Partyline.app around the shipped ptln-tray binary.
//
// WHY A BUNDLE AT ALL: macOS attributes a notification to the app that POSTED it. A bare executable
// has no bundle identity, so notifications had to go through `osascript` — which posts them as
// SCRIPT EDITOR. That's why the banner wore Script Editor's icon instead of ours, and why clicking it
// activated Script Editor and threw up an open-document dialog. There was never a link to remove:
// the icon and the click were both just "this notification belongs to Script Editor".
//
// Inside a bundle the tray posts as ITSELF: our icon, and a click that does nothing (LSUIElement
// means there's no window to raise).
//
// WHY ASSEMBLE IT HERE instead of in goreleaser: a bundle is a directory, a plist, and an icon
// wrapped around a binary we ALREADY ship and notarize, so building it locally keeps the release
// pipeline untouched — which matters, because pipeline changes have broken releases repeatedly.
//
// BUT THE WRAPPER IS NOT INERT, which is the correction that produced this code. Gatekeeper does not
// evaluate the binary inside a bundle — it evaluates THE BUNDLE. Our binary is signed as a
// standalone Mach-O whose seal declares it has no resources, so dropping it into a Contents/MacOS
// with an Info.plist and an .icns makes the signature inconsistent with what's on disk:
//
//	code has no resources but signature indicates they must be present
//	→ "Partyline.app is damaged and can't be opened."
//
// The assembled bundle therefore has to be RE-SEALED (adHocSign below). The Developer ID signature
// on the shipped binary is what got it past Gatekeeper on download; the ad-hoc seal is what makes
// the locally-built bundle internally consistent. Both are needed, for different moments.

//go:embed internal/trayapp/partyline.icns
var trayIcns []byte

// trayAppPath is where the bundle lives: the user's own Applications folder, so it needs no
// privileges and shows up in Login Items under a name they recognize.
func trayAppPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Applications", "Partyline.app")
}

// trayAppExe is the binary inside the bundle — what must be launched for macOS to treat the process
// as the bundled app. Launching the bare ptln-tray instead would silently lose the identity.
func trayAppExe() string { return filepath.Join(trayAppPath(), "Contents", "MacOS", "ptln-tray") }

// LSUIElement: a menu bar accessory, so no Dock icon and no window. It's also what makes a
// notification click a harmless no-op — there's nothing to bring to the front.
const trayInfoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>Partyline</string>
  <key>CFBundleDisplayName</key><string>Partyline</string>
  <key>CFBundleIdentifier</key><string>sh.partyline.tray</string>
  <key>CFBundleExecutable</key><string>ptln-tray</string>
  <key>CFBundleIconFile</key><string>partyline</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>%s</string>
  <key>CFBundleVersion</key><string>%s</string>
  <key>LSUIElement</key><true/>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
`

// ensureTrayApp (re)builds the bundle around the currently-installed ptln-tray and returns the path
// to the bundled executable. Rebuilt whenever the shipped binary is newer than the bundled copy, so
// an upgrade doesn't leave you running last release's tray out of a stale bundle.
//
// Returns "" when there's no tray binary to wrap — the caller treats that as "no tray on this box".
func ensureTrayApp() string {
	src := trayBinary()
	if src == "" {
		return ""
	}
	exe := trayAppExe()
	if upToDate(src, exe) {
		return exe
	}
	res := filepath.Join(trayAppPath(), "Contents", "Resources")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		return ""
	}
	if err := os.MkdirAll(res, 0o755); err != nil {
		return ""
	}
	plist := fmt.Sprintf(trayInfoPlist, version, version)
	if err := os.WriteFile(filepath.Join(trayAppPath(), "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		return ""
	}
	if err := os.WriteFile(filepath.Join(res, "partyline.icns"), trayIcns, 0o644); err != nil {
		return ""
	}
	// COPY, never symlink: a symlinked executable resolves to its target's location, and macOS would
	// then see the bare binary again rather than the bundle — losing exactly the identity we're here
	// to establish.
	b, err := os.ReadFile(src)
	if err != nil {
		return ""
	}
	if err := os.WriteFile(exe, b, 0o755); err != nil {
		return ""
	}
	// Re-seal, or macOS refuses to open what we just built. See the note at the top of this file.
	if err := adHocSign(trayAppPath()); err != nil {
		return "" // an unopenable bundle is worse than no tray — fall back to no icon
	}
	// Belt and braces: the binary may carry a quarantine flag from the download it arrived in, and it
	// propagates into the copy. A quarantined ad-hoc-signed bundle is refused on first launch.
	_ = exec.Command("xattr", "-dr", "com.apple.quarantine", trayAppPath()).Run()
	return exe
}

// adHocSign seals the bundle with an ad-hoc signature (`-`), making the code signature cover the
// Info.plist and Resources we just wrote. Without this the bundle is "damaged" — the seal inherited
// from the standalone binary says there are no resources, and now there are.
//
// Ad-hoc rather than Developer ID because there is no signing identity on a user's machine, and none
// is needed: the artifact was never downloaded as a bundle, so it isn't quarantined and Gatekeeper
// doesn't demand a notarized bundle for it.
func adHocSign(app string) error {
	out, err := exec.Command("codesign", "--force", "--deep", "--sign", "-", app).CombinedOutput()
	if err != nil {
		return fmt.Errorf("codesign: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// upToDate reports whether the bundled copy exists and is no older than the shipped binary.
func upToDate(src, bundled string) bool {
	si, err := os.Stat(src)
	if err != nil {
		return false
	}
	bi, err := os.Stat(bundled)
	if err != nil {
		return false
	}
	return !bi.ModTime().Before(si.ModTime())
}
