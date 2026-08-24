//go:build windows

package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	_ "image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// Arquivos embutidos no executável.
// O launcher não baixa nada da internet.

//go:embed assets/background.png
var backgroundPNG []byte

//go:embed assets/paper.png
var paperPNG []byte

//go:embed assets/finn.ico
var finnICO []byte

//go:embed assets/Assembly-CSharp-PTBR.dll
var translatedDLL []byte

const (
	appTitle      = "Hora de Aventura - Tradução PT-BR"
	translationID = "Tradução v0.4"

	originalDLLHash = "ee50580504e8e2de7977633b004892f0fb73ed45588a212b4e99e21331a42a2d"
	translatedHash  = "8f40eeac2974c9cb101fc07682003ac8e8409d311c97f4131fc44316f818d081"

	canvasWidth  = 1000
	canvasHeight = 563
)

const (
	wmCreate       = 1
	wmDestroy      = 2
	wmPaint        = 15
	wmCommand      = 0x111
	wmDrawItem     = 0x2B
	wmCtlColorEdit = 0x133
	wmEraseBkgnd   = 0x14
	wmSetIcon      = 0x80
	wmApp          = 0x8000
	wmAsyncDone    = wmApp + 1
	wmDetectDone   = wmApp + 2
)

const (
	wsCaption     = 0x00C00000
	wsSysMenu     = 0x00080000
	wsMinimizeBox = 0x00020000
	wsVisible     = 0x10000000
	wsChild       = 0x40000000
	wsTabStop     = 0x00010000

	wsExClientEdge = 0x200
	esAutoHScroll  = 0x80
	bsOwnerDraw    = 0x0B

	csVRedraw = 1
	csHRedraw = 2

	dtLeft         = 0
	dtCenter       = 1
	dtVCenter      = 4
	dtSingleLine   = 0x20
	dtEndEllipsis  = 0x8000
	transparentBg  = 1
	opaqueBg       = 2
	dibRGBColors   = 0
	srcCopy        = 0x00CC0020
	biRGB          = 0
	mbOK           = 0
	mbInfo         = 0x40
	mbWarn         = 0x30
	mbError        = 0x10
	bifReturnFSDir = 1
	bifNewDialog   = 0x40

	idcArrow       = 32512
	imageIcon      = 1
	lrLoadFromFile = 0x10
	lrDefaultColor = 0

	controlPath    = 1001
	controlBrowse  = 1002
	controlInstall = 1003
	controlRestore = 1004

	fontNormal   = 400
	fontSemiBold = 600
	fontBold     = 700

	driveFixed = 3
)

type point struct{ X, Y int32 }

type rect struct {
	Left, Top, Right, Bottom int32
}

type msg struct {
	Hwnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

type wndClassEx struct {
	CbSize, Style uint32
	WndProc       uintptr
	ClsExtra      int32
	WndExtra      int32
	Instance      uintptr
	Icon          uintptr
	Cursor        uintptr
	Background    uintptr
	MenuName      *uint16
	ClassName     *uint16
	IconSm        uintptr
}

type paintStruct struct {
	Hdc       uintptr
	Erase     int32
	Paint     rect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type rgbQuad struct{ B, G, R, A byte }

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]rgbQuad
}

type drawItemStruct struct {
	CtlType, CtlID, ItemID, ItemAction, ItemState uint32
	Item, DC                                      uintptr
	Rect                                          rect
	Data                                          uintptr
}

type browseInfo struct {
	Owner, Root uintptr
	Display     *uint16
	Title       *uint16
	Flags       uint32
	Callback    uintptr
	LParam      uintptr
	Image       int32
}

type asyncResult struct {
	Title  string
	Msg    string
	Status string
	Icon   uintptr
}

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")
)

var (
	procRegisterClassExW   = user32.NewProc("RegisterClassExW")
	procCreateWindowExW    = user32.NewProc("CreateWindowExW")
	procDefWindowProcW     = user32.NewProc("DefWindowProcW")
	procShowWindow         = user32.NewProc("ShowWindow")
	procUpdateWindow       = user32.NewProc("UpdateWindow")
	procGetMessageW        = user32.NewProc("GetMessageW")
	procTranslateMessage   = user32.NewProc("TranslateMessage")
	procDispatchMessageW   = user32.NewProc("DispatchMessageW")
	procPostQuitMessage    = user32.NewProc("PostQuitMessage")
	procPostMessageW       = user32.NewProc("PostMessageW")
	procBeginPaint         = user32.NewProc("BeginPaint")
	procEndPaint           = user32.NewProc("EndPaint")
	procGetClientRect      = user32.NewProc("GetClientRect")
	procDrawTextW          = user32.NewProc("DrawTextW")
	procFillRect           = user32.NewProc("FillRect")
	procSetWindowTextW     = user32.NewProc("SetWindowTextW")
	procGetWindowTextW     = user32.NewProc("GetWindowTextW")
	procSendMessageW       = user32.NewProc("SendMessageW")
	procInvalidateRect     = user32.NewProc("InvalidateRect")
	procMessageBoxW        = user32.NewProc("MessageBoxW")
	procGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	procAdjustWindowRect   = user32.NewProc("AdjustWindowRect")
	procLoadCursorW        = user32.NewProc("LoadCursorW")
	procLoadImageW         = user32.NewProc("LoadImageW")
	procSetProcessDPIAware = user32.NewProc("SetProcessDPIAware")
	procEnableWindow       = user32.NewProc("EnableWindow")

	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
	procSetBkMode        = gdi32.NewProc("SetBkMode")
	procSetBkColor       = gdi32.NewProc("SetBkColor")
	procSetTextColor     = gdi32.NewProc("SetTextColor")
	procSelectObject     = gdi32.NewProc("SelectObject")
	procCreateFontW      = gdi32.NewProc("CreateFontW")
	procStretchDIBits    = gdi32.NewProc("StretchDIBits")

	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	procGetLogicalDrives = kernel32.NewProc("GetLogicalDrives")
	procGetDriveTypeW    = kernel32.NewProc("GetDriveTypeW")

	procSHBrowseForFolderW  = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDList = shell32.NewProc("SHGetPathFromIDListW")

	procCoTaskMemFree  = ole32.NewProc("CoTaskMemFree")
	procCoInitializeEx = ole32.NewProc("CoInitializeEx")
	procCoUninitialize = ole32.NewProc("CoUninitialize")
)

var (
	mainWindow    uintptr
	pathEdit      uintptr
	browseButton  uintptr
	installButton uintptr
	restoreButton uintptr

	normalFont uintptr
	smallFont  uintptr
	tinyFont   uintptr
	titleFont  uintptr
	buttonFont uintptr
	editBrush  uintptr

	statusText = "Backup automático ao instalar."
	isBusy     bool

	backgroundPixels []byte
	backBufferInfo   bitmapInfo

	pendingResult asyncResult
	resultLock    sync.Mutex
)

func utf16Ptr(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

func rgb(r, g, b byte) uintptr {
	return uintptr(r) | uintptr(g)<<8 | uintptr(b)<<16
}

func lowWord(v uintptr) uint16 {
	return uint16(v & 0xffff)
}

func clampByte(v int) byte {
	switch {
	case v < 0:
		return 0
	case v > 255:
		return 255
	default:
		return byte(v)
	}
}

func imageColor(img image.Image, x, y int) color.NRGBA {
	return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
}

func bilinearSample(img image.Image, srcX, srcY float64) color.NRGBA {
	bounds := img.Bounds()

	x0 := int(math.Floor(srcX))
	y0 := int(math.Floor(srcY))
	x1 := x0 + 1
	y1 := y0 + 1

	if x0 < bounds.Min.X {
		x0 = bounds.Min.X
	}
	if y0 < bounds.Min.Y {
		y0 = bounds.Min.Y
	}
	if x1 >= bounds.Max.X {
		x1 = bounds.Max.X - 1
	}
	if y1 >= bounds.Max.Y {
		y1 = bounds.Max.Y - 1
	}

	fx := srcX - float64(x0)
	fy := srcY - float64(y0)

	c00 := imageColor(img, x0, y0)
	c10 := imageColor(img, x1, y0)
	c01 := imageColor(img, x0, y1)
	c11 := imageColor(img, x1, y1)

	mix := func(a, b, c, d byte) byte {
		value := float64(a)*(1-fx)*(1-fy) +
			float64(b)*fx*(1-fy) +
			float64(c)*(1-fx)*fy +
			float64(d)*fx*fy
		return clampByte(int(value + 0.5))
	}

	return color.NRGBA{
		R: mix(c00.R, c10.R, c01.R, c11.R),
		G: mix(c00.G, c10.G, c01.G, c11.G),
		B: mix(c00.B, c10.B, c01.B, c11.B),
		A: mix(c00.A, c10.A, c01.A, c11.A),
	}
}

func blendPixel(x, y int, r, g, b, a byte) {
	if x < 0 || y < 0 || x >= canvasWidth || y >= canvasHeight || a == 0 {
		return
	}

	index := (y*canvasWidth + x) * 4
	alpha := float64(a) / 255.0

	backgroundPixels[index] = clampByte(int(float64(b)*alpha + float64(backgroundPixels[index])*(1-alpha) + 0.5))
	backgroundPixels[index+1] = clampByte(int(float64(g)*alpha + float64(backgroundPixels[index+1])*(1-alpha) + 0.5))
	backgroundPixels[index+2] = clampByte(int(float64(r)*alpha + float64(backgroundPixels[index+2])*(1-alpha) + 0.5))
	backgroundPixels[index+3] = 255
}

func drawResizedImage(img image.Image, src image.Rectangle, dx, dy, dw, dh int, alphaMultiplier float64) {
	srcWidth := src.Dx()
	srcHeight := src.Dy()

	for y := 0; y < dh; y++ {
		srcY := float64(src.Min.Y) + (float64(y)+0.5)*float64(srcHeight)/float64(dh) - 0.5
		for x := 0; x < dw; x++ {
			srcX := float64(src.Min.X) + (float64(x)+0.5)*float64(srcWidth)/float64(dw) - 0.5
			c := bilinearSample(img, srcX, srcY)
			alpha := clampByte(int(float64(c.A) * alphaMultiplier))
			blendPixel(dx+x, dy+y, c.R, c.G, c.B, alpha)
		}
	}
}

func drawOverlayRect(x, y, w, h int, r, g, b byte, alpha float64) {
	if alpha <= 0 {
		return
	}
	a := clampByte(int(alpha * 255))
	for yy := 0; yy < h; yy++ {
		for xx := 0; xx < w; xx++ {
			blendPixel(x+xx, y+yy, r, g, b, a)
		}
	}
}

func buildBackground() {
	bg, _, err := image.Decode(bytes.NewReader(backgroundPNG))
	if err != nil {
		return
	}

	paper, _, err := image.Decode(bytes.NewReader(paperPNG))
	if err != nil {
		return
	}

	backgroundPixels = make([]byte, canvasWidth*canvasHeight*4)

	drawResizedImage(bg, bg.Bounds(), 0, 0, canvasWidth, canvasHeight, 1.0)

	paperBounds := paper.Bounds()
	paperCrop := image.Rect(
		paperBounds.Min.X+25,
		paperBounds.Min.Y+46,
		paperBounds.Min.X+500,
		paperBounds.Min.Y+190,
	)

	paperX := 395
	paperY := 345
	paperW := 590
	paperH := 220

	shadowLayers := []struct {
		XOffset int
		YOffset int
		Alpha   float64
	}{
		{8, 10, 0.18},
		{5, 8, 0.08},
		{10, 8, 0.06},
		{6, 13, 0.05},
		{11, 13, 0.05},
	}

	for _, layer := range shadowLayers {
		srcWidth := paperCrop.Dx()
		srcHeight := paperCrop.Dy()
		for y := 0; y < paperH; y++ {
			srcY := float64(paperCrop.Min.Y) + (float64(y)+0.5)*float64(srcHeight)/float64(paperH) - 0.5
			for x := 0; x < paperW; x++ {
				srcX := float64(paperCrop.Min.X) + (float64(x)+0.5)*float64(srcWidth)/float64(paperW) - 0.5
				c := bilinearSample(paper, srcX, srcY)
				alpha := clampByte(int(float64(c.A) * layer.Alpha))
				blendPixel(paperX+x+layer.XOffset, paperY+y+layer.YOffset, 0, 0, 0, alpha)
			}
		}
	}

	drawResizedImage(paper, paperCrop, paperX, paperY, paperW, paperH, 1.0)

	// Faixa discreta para status no canto inferior esquerdo.
	drawOverlayRect(18, 535, 320, 20, 0, 0, 0, 0.42)

	backBufferInfo.Header = bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       canvasWidth,
		Height:      -canvasHeight,
		Planes:      1,
		BitCount:    32,
		Compression: biRGB,
	}
}

func loadIconFromBytes(size int) uintptr {
	tempFile, err := os.CreateTemp("", "hora-aventura-*.ico")
	if err != nil {
		return 0
	}

	tempPath := tempFile.Name()
	_, _ = tempFile.Write(finnICO)
	_ = tempFile.Close()
	defer os.Remove(tempPath)

	icon, _, _ := procLoadImageW.Call(
		0,
		uintptr(unsafe.Pointer(utf16Ptr(tempPath))),
		imageIcon,
		uintptr(size),
		uintptr(size),
		lrLoadFromFile|lrDefaultColor,
	)

	return icon
}

func createFont(height int32, weight int32, face string) uintptr {
	handle, _, _ := procCreateFontW.Call(
		uintptr(height), 0, 0, 0,
		uintptr(weight),
		0, 0, 0,
		1, 0, 0, 5, 0,
		uintptr(unsafe.Pointer(utf16Ptr(face))),
	)
	return handle
}

func drawText(dc uintptr, value string, bounds rect, textColor uintptr, fontHandle uintptr, flags uint32) {
	oldFont, _, _ := procSelectObject.Call(dc, fontHandle)
	procSetBkMode.Call(dc, transparentBg)
	procSetTextColor.Call(dc, textColor)

	utf16 := syscall.StringToUTF16(value)
	if len(utf16) > 0 {
		procDrawTextW.Call(
			dc,
			uintptr(unsafe.Pointer(&utf16[0])),
			uintptr(len(utf16)-1),
			uintptr(unsafe.Pointer(&bounds)),
			uintptr(flags),
		)
	}

	procSelectObject.Call(dc, oldFont)
}

func setStatus(text string) {
	statusText = text
	procInvalidateRect.Call(mainWindow, 0, 0)
}

func setPathText(path string) {
	procSetWindowTextW.Call(pathEdit, uintptr(unsafe.Pointer(utf16Ptr(path))))
}

func getPathText() string {
	buffer := make([]uint16, 4096)
	procGetWindowTextW.Call(pathEdit, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	return syscall.UTF16ToString(buffer)
}

func createButton(parent, id uintptr, label string, x, y, width, height uintptr) uintptr {
	handle, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(utf16Ptr("BUTTON"))),
		uintptr(unsafe.Pointer(utf16Ptr(label))),
		wsChild|wsVisible|wsTabStop|bsOwnerDraw,
		x, y, width, height,
		parent,
		id,
		0,
		0,
	)
	procSendMessageW.Call(handle, 0x30, buttonFont, 1)
	return handle
}

func createControls(hwnd uintptr) {
	normalFont = createFont(-17, fontNormal, "Segoe UI")
	smallFont = createFont(-14, fontNormal, "Segoe UI")
	tinyFont = createFont(-13, fontNormal, "Segoe UI")
	titleFont = createFont(-25, fontBold, "Segoe Print")
	buttonFont = createFont(-16, fontSemiBold, "Segoe UI")

	editBrush, _, _ = procCreateSolidBrush.Call(rgb(250, 244, 215))

	pathEdit, _, _ = procCreateWindowExW.Call(
		wsExClientEdge,
		uintptr(unsafe.Pointer(utf16Ptr("EDIT"))),
		uintptr(unsafe.Pointer(utf16Ptr(""))),
		wsChild|wsVisible|wsTabStop|esAutoHScroll,
		455, 456, 360, 29,
		hwnd,
		controlPath,
		0,
		0,
	)
	procSendMessageW.Call(pathEdit, 0x30, normalFont, 1)

	browseButton = createButton(hwnd, controlBrowse, "...", 824, 456, 46, 29)
	installButton = createButton(hwnd, controlInstall, "INSTALAR", 455, 494, 175, 39)
	restoreButton = createButton(hwnd, controlRestore, "RESTAURAR", 640, 494, 185, 39)

	go func() {
		if detected := detectGamePath(); detected != "" {
			resultLock.Lock()
			pendingResult.Status = detected
			resultLock.Unlock()
			procPostMessageW.Call(hwnd, wmDetectDone, 0, 0)
		}
	}()
}

func paintWindow(hwnd uintptr) {
	var ps paintStruct
	dc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	defer procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))

	var client rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client)))
	if len(backgroundPixels) > 0 {
		procStretchDIBits.Call(
			dc,
			0, 0,
			uintptr(client.Right), uintptr(client.Bottom),
			0, 0,
			canvasWidth, canvasHeight,
			uintptr(unsafe.Pointer(&backgroundPixels[0])),
			uintptr(unsafe.Pointer(&backBufferInfo)),
			dibRGBColors,
			srcCopy,
		)
	}

	dark := rgb(54, 50, 43)
	red := rgb(183, 52, 39)
	teal := rgb(25, 103, 111)

	drawText(dc, "PORT PT-BR PARA PC", rect{435, 372, 955, 405}, red, titleFont, dtCenter|dtSingleLine)
	drawText(dc, "MILTRADUÇÕES  •  Tradução v0.4", rect{435, 408, 955, 429}, teal, smallFont, dtCenter|dtSingleLine)
	drawText(dc, "Pasta do jogo:", rect{435, 432, 955, 450}, dark, tinyFont, dtCenter|dtSingleLine)
	drawText(dc, statusText, rect{24, 536, 330, 553}, rgb(255, 255, 255), tinyFont, dtLeft|dtSingleLine|dtEndEllipsis)
}

func drawButton(dis *drawItemStruct) {
	var label string
	var backgroundColor uintptr
	var textColor uintptr

	switch dis.CtlID {
	case controlBrowse:
		label = "..."
		backgroundColor = rgb(31, 114, 122)
		textColor = rgb(255, 249, 221)
	case controlInstall:
		label = "INSTALAR"
		backgroundColor = rgb(211, 71, 50)
		textColor = rgb(255, 249, 221)
	case controlRestore:
		label = "RESTAURAR"
		backgroundColor = rgb(31, 103, 111)
		textColor = rgb(255, 249, 221)
	default:
		return
	}

	if dis.ItemState&1 != 0 {
		if dis.CtlID == controlInstall {
			backgroundColor = rgb(177, 56, 40)
		} else {
			backgroundColor = rgb(24, 83, 90)
		}
	}

	if isBusy {
		backgroundColor = rgb(112, 108, 91)
		textColor = rgb(225, 219, 194)
	}

	brush, _, _ := procCreateSolidBrush.Call(backgroundColor)
	procFillRect.Call(dis.DC, uintptr(unsafe.Pointer(&dis.Rect)), brush)
	procDeleteObject.Call(brush)

	drawText(dis.DC, label, dis.Rect, textColor, buttonFont, dtCenter|dtVCenter|dtSingleLine)
}

func setBusyState(value bool) {
	isBusy = value
	enabled := uintptr(1)
	if value {
		enabled = 0
	}

	for _, handle := range []uintptr{browseButton, installButton, restoreButton, pathEdit} {
		if handle != 0 {
			procEnableWindow.Call(handle, enabled)
		}
	}

	procInvalidateRect.Call(mainWindow, 0, 0)
}

func browseFolder(owner uintptr) string {
	var displayName [260]uint16
	info := browseInfo{
		Owner:   owner,
		Display: &displayName[0],
		Title:   utf16Ptr("Selecione a pasta do jogo, a pasta _Data ou Managed"),
		Flags:   bifReturnFSDir | bifNewDialog,
	}

	pidl, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&info)))
	if pidl == 0 {
		return ""
	}
	defer procCoTaskMemFree.Call(pidl)

	var pathBuffer [1024]uint16
	ok, _, _ := procSHGetPathFromIDList.Call(pidl, uintptr(unsafe.Pointer(&pathBuffer[0])))
	if ok == 0 {
		return ""
	}

	return syscall.UTF16ToString(pathBuffer[:])
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func resolveManagedPath(input string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(strings.Trim(input, "\"")))
	if cleaned == "." || cleaned == "" {
		return "", fmt.Errorf("pasta vazia")
	}

	dataFolder := "Adventure Time Pirates of the Enchiridion_Data"
	candidates := []string{
		cleaned,
		filepath.Join(cleaned, "Managed"),
		filepath.Join(cleaned, dataFolder, "Managed"),
	}

	for _, candidate := range candidates {
		if fileExists(filepath.Join(candidate, "Assembly-CSharp.dll")) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("Assembly-CSharp.dll não encontrado")
}

func listFixedDrives() []string {
	mask, _, _ := procGetLogicalDrives.Call()
	var drives []string

	for i := 0; i < 26; i++ {
		if mask&(1<<i) == 0 {
			continue
		}

		root := fmt.Sprintf("%c:\\", 'A'+i)
		driveType, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(utf16Ptr(root))))
		if driveType == driveFixed {
			drives = append(drives, root)
		}
	}

	return drives
}

func findSteamLibraries() []string {
	seen := make(map[string]bool)
	var roots []string

	if x := os.Getenv("ProgramFiles(x86)"); x != "" {
		roots = append(roots, filepath.Join(x, "Steam"))
	}
	if x := os.Getenv("ProgramFiles"); x != "" {
		roots = append(roots, filepath.Join(x, "Steam"))
	}
	for _, drive := range listFixedDrives() {
		roots = append(roots, filepath.Join(drive, "Steam"), filepath.Join(drive, "SteamLibrary"))
	}

	addLibrary := func(path string, out *[]string) {
		path = filepath.Clean(strings.ReplaceAll(path, `\\`, `\`))
		lower := strings.ToLower(path)
		if path == "" || seen[lower] {
			return
		}
		seen[lower] = true
		*out = append(*out, path)
	}

	regex := regexp.MustCompile(`(?i)"path"\s+"([^"]+)"`)
	var libraries []string

	for _, root := range roots {
		addLibrary(root, &libraries)

		vdfPath := filepath.Join(root, "steamapps", "libraryfolders.vdf")
		data, err := os.ReadFile(vdfPath)
		if err != nil {
			continue
		}

		for _, match := range regex.FindAllStringSubmatch(string(data), -1) {
			if len(match) > 1 {
				addLibrary(match[1], &libraries)
			}
		}
	}

	return libraries
}

func detectGamePath() string {
	relative := filepath.Join(
		"steamapps", "common", "Adventure Time Pirates of the Enchiridion",
		"Adventure Time Pirates of the Enchiridion_Data", "Managed",
	)

	for _, library := range findSteamLibraries() {
		candidate := filepath.Join(library, relative)
		if fileExists(filepath.Join(candidate, "Assembly-CSharp.dll")) {
			return candidate
		}
	}

	return ""
}

func fileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}

	_, err = io.Copy(out, in)
	closeErr := out.Close()

	if err != nil {
		return err
	}
	return closeErr
}

func showMessageBox(title, msg string, icon uintptr) {
	procMessageBoxW.Call(
		mainWindow,
		uintptr(unsafe.Pointer(utf16Ptr(msg))),
		uintptr(unsafe.Pointer(utf16Ptr(title))),
		mbOK|icon,
	)
}

func finishAsync(result asyncResult) {
	resultLock.Lock()
	pendingResult = result
	resultLock.Unlock()
	procPostMessageW.Call(mainWindow, wmAsyncDone, 0, 0)
}

func installTranslation(input string) {
	managedPath, err := resolveManagedPath(input)
	if err != nil {
		finishAsync(asyncResult{
			Title:  "Pasta inválida",
			Msg:    "Não encontrei Assembly-CSharp.dll.\n\nSelecione a pasta do jogo, a pasta *_Data ou Managed.",
			Status: "Pasta inválida. Nada foi alterado.",
			Icon:   mbWarn,
		})
		return
	}

	dllPath := filepath.Join(managedPath, "Assembly-CSharp.dll")
	backupPath := filepath.Join(managedPath, "Assembly-CSharp.original.backup.dll")

	currentHash, err := fileHash(dllPath)
	if err != nil {
		finishAsync(asyncResult{"Erro", err.Error(), "Erro ao verificar o arquivo.", mbError})
		return
	}

	if currentHash == translatedHash {
		finishAsync(asyncResult{
			Title:  "Tudo certo",
			Msg:    "A tradução PT-BR v0.4 já está instalada.",
			Status: "Tradução v0.4 já instalada.",
			Icon:   mbInfo,
		})
		return
	}

	validBackup := false
	if fileExists(backupPath) {
		if backupHash, err := fileHash(backupPath); err == nil && backupHash == originalDLLHash {
			validBackup = true
		}
	}

	if currentHash == originalDLLHash {
		if !fileExists(backupPath) {
			if err := copyFile(dllPath, backupPath); err != nil {
				finishAsync(asyncResult{
					Title:  "Erro ao criar backup",
					Msg:    "Feche o jogo e tente novamente.\n\n" + err.Error(),
					Status: "Falha ao criar o backup.",
					Icon:   mbError,
				})
				return
			}
			validBackup = true
		}
	} else if !validBackup {
		finishAsync(asyncResult{
			Title:  "Arquivo não reconhecido",
			Msg:    "Por segurança, o launcher não vai substituir esta DLL.\n\nVerifique a integridade dos arquivos pela Steam e tente novamente.",
			Status: "Arquivo não reconhecido. Nada foi alterado.",
			Icon:   mbWarn,
		})
		return
	}

	sum := sha256.Sum256(translatedDLL)
	if hex.EncodeToString(sum[:]) != translatedHash {
		finishAsync(asyncResult{
			Title:  "Erro interno",
			Msg:    "A tradução embutida falhou na verificação SHA-256.",
			Status: "Erro interno de integridade.",
			Icon:   mbError,
		})
		return
	}

	if err := os.WriteFile(dllPath, translatedDLL, 0644); err != nil {
		finishAsync(asyncResult{
			Title:  "Erro ao instalar",
			Msg:    "Não consegui gravar o arquivo. Feche o jogo e tente novamente.\n\n" + err.Error(),
			Status: "Falha na instalação.",
			Icon:   mbError,
		})
		return
	}

	finalHash, err := fileHash(dllPath)
	if err != nil || finalHash != translatedHash {
		finishAsync(asyncResult{
			Title:  "Falha na verificação",
			Msg:    "O arquivo foi gravado, mas a verificação final falhou. Restaure o original antes de abrir o jogo.",
			Status: "Falha na verificação final.",
			Icon:   mbError,
		})
		return
	}

	finishAsync(asyncResult{
		Title:  "Tradução instalada!",
		Msg:    "Pronto! A tradução PT-BR v0.4 foi instalada.\n\nAbra o jogo pela Steam e deixe o idioma em INGLÊS.\n\nO backup original foi preservado.",
		Status: "Instalada! Use o jogo em INGLÊS.",
		Icon:   mbInfo,
	})
}

func restoreOriginal(input string) {
	managedPath, err := resolveManagedPath(input)
	if err != nil {
		finishAsync(asyncResult{
			Title:  "Pasta inválida",
			Msg:    "Selecione primeiro a pasta correta do jogo.",
			Status: "Pasta inválida.",
			Icon:   mbWarn,
		})
		return
	}

	dllPath := filepath.Join(managedPath, "Assembly-CSharp.dll")
	backupPath := filepath.Join(managedPath, "Assembly-CSharp.original.backup.dll")

	if !fileExists(backupPath) {
		if currentHash, err := fileHash(dllPath); err == nil && currentHash == originalDLLHash {
			finishAsync(asyncResult{
				Title:  "Tudo certo",
				Msg:    "O arquivo original já está instalado.",
				Status: "O arquivo original já está instalado.",
				Icon:   mbInfo,
			})
			return
		}

		finishAsync(asyncResult{
			Title:  "Backup não encontrado",
			Msg:    "Não encontrei um backup original válido.\n\nUse 'Verificar integridade dos arquivos' na Steam.",
			Status: "Backup original não encontrado.",
			Icon:   mbWarn,
		})
		return
	}

	backupHash, err := fileHash(backupPath)
	if err != nil || backupHash != originalDLLHash {
		finishAsync(asyncResult{
			Title:  "Backup inválido",
			Msg:    "O backup não corresponde ao arquivo original esperado. Por segurança, nada foi alterado.",
			Status: "Backup inválido. Nada foi alterado.",
			Icon:   mbWarn,
		})
		return
	}

	if err := copyFile(backupPath, dllPath); err != nil {
		finishAsync(asyncResult{
			Title:  "Erro ao restaurar",
			Msg:    "Feche o jogo e tente novamente.\n\n" + err.Error(),
			Status: "Falha ao restaurar.",
			Icon:   mbError,
		})
		return
	}

	finishAsync(asyncResult{
		Title:  "Original restaurado",
		Msg:    "Pronto! O arquivo original do jogo foi restaurado.",
		Status: "Original restaurado.",
		Icon:   mbInfo,
	})
}

func windowProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmCreate:
		mainWindow = hwnd
		createControls(hwnd)
		return 0

	case wmEraseBkgnd:
		return 1

	case wmPaint:
		paintWindow(hwnd)
		return 0

	case wmCtlColorEdit:
		procSetTextColor.Call(wParam, rgb(54, 50, 43))
		procSetBkColor.Call(wParam, rgb(250, 244, 215))
		procSetBkMode.Call(wParam, opaqueBg)
		return editBrush

	case wmDrawItem:
		drawButton((*drawItemStruct)(unsafe.Pointer(lParam)))
		return 1

	case wmDetectDone:
		resultLock.Lock()
		detectedPath := pendingResult.Status
		resultLock.Unlock()
		if detectedPath != "" && strings.TrimSpace(getPathText()) == "" {
			setPathText(detectedPath)
			setStatus("Jogo encontrado automaticamente.")
		}
		return 0

	case wmAsyncDone:
		resultLock.Lock()
		result := pendingResult
		resultLock.Unlock()
		setBusyState(false)
		setStatus(result.Status)
		if result.Title != "" {
			showMessageBox(result.Title, result.Msg, result.Icon)
		}
		return 0

	case wmCommand:
		if isBusy {
			return 0
		}

		switch lowWord(wParam) {
		case controlBrowse:
			selectedPath := browseFolder(hwnd)
			if selectedPath == "" {
				return 0
			}

			if managedPath, err := resolveManagedPath(selectedPath); err == nil {
				setPathText(managedPath)
				setStatus("Pasta válida.")
			} else {
				setPathText(selectedPath)
				setStatus("Pasta selecionada.")
			}

		case controlInstall:
			setBusyState(true)
			setStatus("Instalando... aguarde.")
			go installTranslation(getPathText())

		case controlRestore:
			setBusyState(true)
			setStatus("Restaurando... aguarde.")
			go restoreOriginal(getPathText())
		}
		return 0

	case wmDestroy:
		for _, object := range []uintptr{normalFont, smallFont, tinyFont, titleFont, buttonFont, editBrush} {
			if object != 0 {
				procDeleteObject.Call(object)
			}
		}
		procPostQuitMessage.Call(0)
		return 0
	}

	result, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return result
}

func main() {
	runtime.LockOSThread()

	procSetProcessDPIAware.Call()
	procCoInitializeEx.Call(0, 2)
	defer procCoUninitialize.Call()

	buildBackground()

	instance, _, _ := procGetModuleHandleW.Call(0)
	className := utf16Ptr("ATPTBRLauncher")
	iconBig := loadIconFromBytes(48)
	iconSmall := loadIconFromBytes(16)
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)

	wc := wndClassEx{
		CbSize:     uint32(unsafe.Sizeof(wndClassEx{})),
		Style:      csHRedraw | csVRedraw,
		WndProc:    syscall.NewCallback(windowProc),
		Instance:   instance,
		Icon:       iconBig,
		Cursor:     cursor,
		Background: 0,
		ClassName:  className,
		IconSm:     iconSmall,
	}

	if result, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); result == 0 {
		return
	}

	style := uintptr(wsCaption | wsSysMenu | wsMinimizeBox | wsVisible)
	windowRect := rect{0, 0, canvasWidth, canvasHeight}
	procAdjustWindowRect.Call(uintptr(unsafe.Pointer(&windowRect)), style, 0)

	width := windowRect.Right - windowRect.Left
	height := windowRect.Bottom - windowRect.Top
	screenW, _, _ := procGetSystemMetrics.Call(0)
	screenH, _, _ := procGetSystemMetrics.Call(1)
	x := (int32(screenW) - width) / 2
	y := (int32(screenH) - height) / 2

	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr(appTitle))),
		style,
		uintptr(x), uintptr(y),
		uintptr(width), uintptr(height),
		0, 0, instance, 0,
	)
	if hwnd == 0 {
		return
	}

	mainWindow = hwnd
	procSendMessageW.Call(hwnd, wmSetIcon, 1, iconBig)
	procSendMessageW.Call(hwnd, wmSetIcon, 0, iconSmall)
	procShowWindow.Call(hwnd, 5)
	procUpdateWindow.Call(hwnd)

	var message msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
	}
}
