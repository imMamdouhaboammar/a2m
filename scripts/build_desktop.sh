#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_DIR="${SCRIPT_DIR}/apps/desktop/macos/A2M.app"
MACOS_DIR="${APP_DIR}/Contents/MacOS"
RESOURCES_DIR="${APP_DIR}/Contents/Resources"
DIST_DIR="${SCRIPT_DIR}/dist"

echo "=========================================================="
echo " Building A2M Desktop (Native macOS Cocoa Mach-O App)"
echo "=========================================================="

# 1. Compile A2M microservices
"${SCRIPT_DIR}/build.sh"

# 2. Setup bundle directories
rm -rf "${APP_DIR}"
mkdir -p "${MACOS_DIR}" "${RESOURCES_DIR}"

# Bundle the canonical A2M v2 web UI for the Go gateway to serve locally.
# The gateway resolves this path from the running app executable, with no CDN.
mkdir -p "${RESOURCES_DIR}/UI"
cp -R "${SCRIPT_DIR}/UI/a2m-desktop-v2" "${RESOURCES_DIR}/UI/"
# The app may be installed under a different owner (e.g. /Applications).
# Readable static assets are required for the Go sidecar to serve the UI.
chmod -R a+rX "${RESOURCES_DIR}/UI"

# A2M macOS dock/Finder icon, rendered from the in-app link mark.
cp "${SCRIPT_DIR}/apps/desktop/macos/assets/A2M.icns" "${RESOURCES_DIR}/A2M.icns"

# 3. Write Info.plist
cat << 'EOF' > "${APP_DIR}/Contents/Info.plist"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>A2MApp</string>
    <key>CFBundleIdentifier</key>
    <string>io.github.a2m.desktop</string>
    <key>CFBundleName</key>
    <string>A2M Desktop</string>
    <key>CFBundleIconFile</key>
    <string>A2M.icns</string>
    <key>CFBundleDisplayName</key>
    <string>A2M Desktop</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>1.1.0</string>
    <key>CFBundleVersion</key>
    <string>2</string>
    <key>LSMinimumSystemVersion</key>
    <string>12.0</string>
    <key>NSHighResolutionCapable</key>
    <true/>
    <!-- Allow WKWebView to load http://127.0.0.1 without ATS blocking -->
    <key>NSAppTransportSecurity</key>
    <dict>
        <key>NSAllowsLocalNetworking</key>
        <true/>
    </dict>
</dict>
</plist>
EOF

# 4. Compile Swift Cocoa Binary
echo "--> Compiling Swift native launcher..."
swiftc -O -target arm64-apple-macosx12.0 "${SCRIPT_DIR}/apps/desktop/macos/main.swift" -o "${MACOS_DIR}/A2MApp"

# 5. Embed microservices into app bundle
echo "--> Bundling A2M microservices into .app bundle..."
cp "${SCRIPT_DIR}/bin/a2m-gateway" "${MACOS_DIR}/"
cp "${SCRIPT_DIR}/bin/a2m-fs" "${MACOS_DIR}/"
cp "${SCRIPT_DIR}/bin/a2m-docs" "${MACOS_DIR}/"
cp "${SCRIPT_DIR}/bin/a2m" "${MACOS_DIR}/"

# Backwards compatibility symlinks
(
    cd "${MACOS_DIR}"
    ln -sf a2m-gateway dc-gateway
    ln -sf a2m-fs dc-fs-engine
    ln -sf a2m-docs dc-doc-service
)

chmod -R +x "${MACOS_DIR}"

# Sign after every executable and web resource is inside the bundle.
# Ad-hoc signing supports local development; public distribution still needs
# a Developer ID signature and Apple notarization.
echo "--> Ad-hoc signing complete app bundle..."
codesign --force --deep --sign - --timestamp=none "${APP_DIR}"
codesign --verify --deep --strict "${APP_DIR}"

# 6. Generate DMG Package
mkdir -p "${DIST_DIR}"
DMG_PATH="${DIST_DIR}/A2M-macos-arm64.dmg"
DMG_STAGING="${DIST_DIR}/dmg_staging"

echo "--> Packaging A2M Desktop into DMG installer..."
rm -rf "${DMG_STAGING}" "${DMG_PATH}"
mkdir -p "${DMG_STAGING}"
cp -R "${APP_DIR}" "${DMG_STAGING}/"
ln -s /Applications "${DMG_STAGING}/Applications"

hdiutil create -volname "A2M Desktop" \
    -srcfolder "${DMG_STAGING}" \
    -ov -format UDZO \
    "${DMG_PATH}" > /dev/null

rm -rf "${DMG_STAGING}"

echo "=========================================================="
echo " A2M Desktop Built Successfully!"
echo " App Bundle: ${APP_DIR} ($(du -sh "${APP_DIR}" | awk '{print $1}'))"
echo " DMG Installer: ${DMG_PATH} ($(du -sh "${DMG_PATH}" | awk '{print $1}'))"
echo "=========================================================="
