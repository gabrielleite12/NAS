//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")

	pRegisterClassExW  = user32.NewProc("RegisterClassExW")
	pCreateWindowExW   = user32.NewProc("CreateWindowExW")
	pDefWindowProcW    = user32.NewProc("DefWindowProcW")
	pGetMessageW       = user32.NewProc("GetMessageW")
	pTranslateMessage  = user32.NewProc("TranslateMessage")
	pDispatchMessageW  = user32.NewProc("DispatchMessageW")
	pPostQuitMessage   = user32.NewProc("PostQuitMessage")
	pShowWindow        = user32.NewProc("ShowWindow")
	pUpdateWindow      = user32.NewProc("UpdateWindow")
	pDestroyWindow     = user32.NewProc("DestroyWindow")
	pSendMessageW      = user32.NewProc("SendMessageW")
	pSetTimer          = user32.NewProc("SetTimer")
	pLoadCursorW       = user32.NewProc("LoadCursorW")
	pGetModuleHandleW  = kernel32.NewProc("GetModuleHandleW")
	pCreateFontW       = gdi32.NewProc("CreateFontW")
	pShellExecuteW     = shell32.NewProc("ShellExecuteW")
	pGetDC             = user32.NewProc("GetDC")
	pReleaseDC         = user32.NewProc("ReleaseDC")
	pSetBkMode         = gdi32.NewProc("SetBkMode")
	pSetTextColor      = gdi32.NewProc("SetTextColor")
	pGetSysColorBrush  = user32.NewProc("GetSysColorBrush")
	pInvalidateRect    = user32.NewProc("InvalidateRect")
	pBeginPaint        = user32.NewProc("BeginPaint")
	pEndPaint          = user32.NewProc("EndPaint")
	pFillRect          = user32.NewProc("FillRect")
	pSelectObject      = gdi32.NewProc("SelectObject")
	pTextOutW          = gdi32.NewProc("TextOutW")
	pDeleteObject      = gdi32.NewProc("DeleteObject")
	pCreateSolidBrush  = gdi32.NewProc("CreateSolidBrush")
	pSetBkColor        = gdi32.NewProc("SetBkColor")
	pGetClientRect     = user32.NewProc("GetClientRect")
)

const (
	WS_OVERLAPPED   = 0x00000000
	WS_CAPTION      = 0x00C00000
	WS_SYSMENU      = 0x00080000
	WS_MINIMIZEBOX  = 0x00020000
	WS_VISIBLE      = 0x10000000
	WS_CHILD        = 0x40000000
	WS_BORDER       = 0x00800000
	WS_EX_CLIENTEDGE = 0x00000200

	WM_DESTROY  = 0x0002
	WM_COMMAND  = 0x0111
	WM_TIMER    = 0x0113
	WM_PAINT    = 0x000F
	WM_CTLCOLORSTATIC = 0x0138
	WM_CTLCOLORBTN   = 0x0135
	WM_SETFONT  = 0x0030

	BS_PUSHBUTTON = 0x00000000
	BS_FLAT       = 0x00008000
	SS_LEFT       = 0x00000000
	SS_CENTER     = 0x00000001

	SW_SHOW       = 5
	CW_USEDEFAULT = 0x80000000
	IDC_ARROW     = 32512

	COLOR_BTNFACE = 15
	TRANSPARENT   = 1
	OPAQUE        = 2

	IDC_BTN_OPEN     = 1001
	IDC_BTN_SHUTDOWN = 1002
	IDC_LBL_URL      = 2001
	IDC_LBL_STATUS   = 2002
	IDC_LBL_DISK     = 2003
	IDC_LBL_FTP      = 2004

	TIMER_UPDATE = 1
)

type WNDCLASSEXW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     syscall.Handle
	HIcon         syscall.Handle
	HCursor       syscall.Handle
	HbrBackground syscall.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       syscall.Handle
}

type MSG struct {
	HWnd    syscall.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type PAINTSTRUCT struct {
	HDC         uintptr
	FErase      int32
	RcPaint     RECT
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}

type RECT struct {
	Left, Top, Right, Bottom int32
}

var (
	hwndMain       syscall.Handle
	hwndLblURL     syscall.Handle
	hwndLblStatus  syscall.Handle
	hwndLblDisk    syscall.Handle
	hwndLblFTP     syscall.Handle
	hwndBtnOpen    syscall.Handle
	hwndBtnShutdown syscall.Handle
	serverURL      string
	hFont          uintptr
	hFontBold      uintptr
	hFontTitle     uintptr
	bgBrush        uintptr
)

func utf16Ptr(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}

func createFont(size int32, bold bool, name string) uintptr {
	weight := int32(400)
	if bold {
		weight = 700
	}
	h, _, _ := pCreateFontW.Call(
		uintptr(size), 0, 0, 0,
		uintptr(weight),
		0, 0, 0, // italic, underline, strikeout
		1,        // charset (DEFAULT_CHARSET)
		0, 0, 4,  // outPrecision, clipPrecision, quality (ANTIALIASED)
		0,
		uintptr(unsafe.Pointer(utf16Ptr(name))),
	)
	return h
}

func setWindowText(hwnd syscall.Handle, text string) {
	pSendMessageW.Call(
		uintptr(hwnd),
		0x000C, // WM_SETTEXT
		0,
		uintptr(unsafe.Pointer(utf16Ptr(text))),
	)
}

func wndProc(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_COMMAND:
		cmdID := int32(wParam & 0xFFFF)
		switch cmdID {
		case IDC_BTN_OPEN:
			pShellExecuteW.Call(0,
				uintptr(unsafe.Pointer(utf16Ptr("open"))),
				uintptr(unsafe.Pointer(utf16Ptr(serverURL))),
				0, 0, 5)
		case IDC_BTN_SHUTDOWN:
			go func() {
				http.Post(serverURL+"/api/shutdown", "application/json", strings.NewReader("{}"))
			}()
			time.Sleep(500 * time.Millisecond)
			pDestroyWindow.Call(uintptr(hwnd))
		}
	case WM_TIMER:
		if wParam == TIMER_UPDATE {
			go updateStatus()
		}
	case WM_CTLCOLORSTATIC:
		pSetBkMode.Call(wParam, TRANSPARENT)
		pSetTextColor.Call(wParam, 0x00333333) // dark gray text
		pSetBkColor.Call(wParam, 0x00F5F5F5) // light bg
		return bgBrush
	case WM_DESTROY:
		pPostQuitMessage.Call(0)
		return 0
	}

	ret, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return ret
}

func updateStatus() {
	// Check FTP status
	resp, err := http.Get(serverURL + "/api/status")
	if err != nil {
		setWindowText(hwndLblStatus, "Status: ❌ Servidor offline")
		setWindowText(hwndLblFTP, "FTP: Erro de conexão")
		return
	}
	defer resp.Body.Close()
	var statusData map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&statusData)

	if s, ok := statusData["status"].(string); ok && s == "online" {
		setWindowText(hwndLblStatus, "Status:  ✅ Online")
		setWindowText(hwndLblFTP, "FTP:  ✅ Conectado")
	} else {
		errMsg := ""
		if e, ok := statusData["error"].(string); ok {
			errMsg = " - " + e
		}
		setWindowText(hwndLblStatus, "Status:  ❌ Offline"+errMsg)
		setWindowText(hwndLblFTP, "FTP:  ❌ Desconectado")
	}

	// Check disk space
	resp2, err2 := http.Get(serverURL + "/api/diskspace")
	if err2 != nil {
		setWindowText(hwndLblDisk, "Disco:  Não disponível")
		return
	}
	defer resp2.Body.Close()
	var diskData map[string]interface{}
	json.NewDecoder(resp2.Body).Decode(&diskData)

	if total, ok := diskData["total"].(float64); ok && total > 0 {
		free := diskData["free"].(float64)
		used := diskData["used"].(float64)
		pct := (used / total) * 100
		setWindowText(hwndLblDisk, fmt.Sprintf("Disco:  %s livre de %s (%.0f%% usado)",
			formatSizeWin(uint64(free)), formatSizeWin(uint64(total)), pct))
	} else {
		setWindowText(hwndLblDisk, "Disco:  Não disponível")
	}
}

func formatSizeWin(b uint64) string {
	if b == 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	fb := float64(b)
	for fb >= 1024 && i < len(units)-1 {
		fb /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", fb, units[i])
}

func createLabel(parent syscall.Handle, text string, x, y, w, h int32, id uintptr, style uint32) syscall.Handle {
	hwnd, _, _ := pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(utf16Ptr("STATIC"))),
		uintptr(unsafe.Pointer(utf16Ptr(text))),
		uintptr(WS_VISIBLE|WS_CHILD|style),
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		uintptr(parent), id, 0, 0,
	)
	pSendMessageW.Call(hwnd, WM_SETFONT, hFont, 1)
	return syscall.Handle(hwnd)
}

func createButton(parent syscall.Handle, text string, x, y, w, h int32, id uintptr) syscall.Handle {
	hwnd, _, _ := pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(utf16Ptr("BUTTON"))),
		uintptr(unsafe.Pointer(utf16Ptr(text))),
		uintptr(WS_VISIBLE|WS_CHILD|BS_PUSHBUTTON),
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		uintptr(parent), id, 0, 0,
	)
	pSendMessageW.Call(hwnd, WM_SETFONT, hFont, 1)
	return syscall.Handle(hwnd)
}

func platformRun(url string) {
	serverURL = url

	hInstance, _, _ := pGetModuleHandleW.Call(0)
	cursor, _, _ := pLoadCursorW.Call(0, IDC_ARROW)

	// Create fonts
	hFont = createFont(-14, false, "Segoe UI")
	hFontBold = createFont(-14, true, "Segoe UI")
	hFontTitle = createFont(-22, true, "Segoe UI")

	// Background brush
	bgBrush, _, _ = pCreateSolidBrush.Call(0x00F5F5F5) // light gray

	className := utf16Ptr("NASLocalWindowClass")

	wc := WNDCLASSEXW{
		CbSize:        uint32(unsafe.Sizeof(WNDCLASSEXW{})),
		Style:         3, // CS_HREDRAW | CS_VREDRAW
		LpfnWndProc:   syscall.NewCallback(wndProc),
		HInstance:     syscall.Handle(hInstance),
		HCursor:       syscall.Handle(cursor),
		HbrBackground: syscall.Handle(bgBrush),
		LpszClassName: className,
	}

	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	winW := int32(480)
	winH := int32(380)

	h, _, _ := pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr("NAS Local - Painel de Controle"))),
		uintptr(WS_OVERLAPPED|WS_CAPTION|WS_SYSMENU|WS_MINIMIZEBOX|WS_VISIBLE),
		uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT),
		uintptr(winW), uintptr(winH),
		0, 0, hInstance, 0,
	)
	hwndMain = syscall.Handle(h)

	// --- Layout ---
	pad := int32(25)
	contentW := winW - pad*2

	// Title
	lblTitle := createLabel(hwndMain, "📁  NAS Local", pad, 15, contentW, 35, 0, SS_LEFT)
	pSendMessageW.Call(uintptr(lblTitle), WM_SETFONT, hFontTitle, 1)

	// Separator line (thin static)
	createLabel(hwndMain, "", pad, 55, contentW, 1, 0, SS_LEFT)

	// URL
	createLabel(hwndMain, "Endereço de acesso:", pad, 70, contentW, 20, 0, SS_LEFT)
	hwndLblURL = createLabel(hwndMain, url, pad, 92, contentW, 25, IDC_LBL_URL, SS_LEFT)
	pSendMessageW.Call(uintptr(hwndLblURL), WM_SETFONT, hFontBold, 1)

	// Status info
	hwndLblStatus = createLabel(hwndMain, "Status:  ⏳ Verificando...", pad, 135, contentW, 22, IDC_LBL_STATUS, SS_LEFT)
	hwndLblFTP = createLabel(hwndMain, "FTP:  ⏳ Verificando...", pad, 162, contentW, 22, IDC_LBL_FTP, SS_LEFT)
	hwndLblDisk = createLabel(hwndMain, "Disco:  ⏳ Verificando...", pad, 189, contentW, 22, IDC_LBL_DISK, SS_LEFT)

	// Buttons
	btnW := int32(200)
	btnH := int32(40)
	btnY := int32(245)
	
	hwndBtnOpen = createButton(hwndMain, "🌐  Abrir no Navegador", pad, btnY, btnW, btnH, IDC_BTN_OPEN)
	hwndBtnShutdown = createButton(hwndMain, "🛑  Desligar Servidor", contentW+pad-btnW, btnY, btnW, btnH, IDC_BTN_SHUTDOWN)

	// Info label at bottom
	createLabel(hwndMain, "O servidor está rodando em segundo plano.\nFeche esta janela ou clique em Desligar para parar.", pad, 300, contentW, 40, 0, SS_LEFT)

	pShowWindow.Call(uintptr(hwndMain), SW_SHOW)
	pUpdateWindow.Call(uintptr(hwndMain))

	// Start update timer (every 5 seconds)
	pSetTimer.Call(uintptr(hwndMain), TIMER_UPDATE, 5000, 0)

	// Initial status check
	go updateStatus()

	// Message loop
	var msg MSG
	for {
		ret, _, _ := pGetMessageW.Call(
			uintptr(unsafe.Pointer(&msg)),
			0, 0, 0,
		)
		if ret == 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	// Cleanup fonts
	pDeleteObject.Call(hFont)
	pDeleteObject.Call(hFontBold)
	pDeleteObject.Call(hFontTitle)
	pDeleteObject.Call(bgBrush)
}
