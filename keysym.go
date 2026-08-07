package main

// X11 keysym values for the keys used in the keymap.
// Latin-1 keysyms equal their character codes (0x20-0xFF).
const (
	// modifier masks
	ShiftMask   = 1 << 0
	LockMask    = 1 << 1
	ControlMask = 1 << 2
	Mod1Mask    = 1 << 3
	Mod2Mask    = 1 << 4
	Mod3Mask    = 1 << 5
	Mod4Mask    = 1 << 6
	Mod5Mask    = 1 << 7

	XKAnyMod = 0x0000 // sentinel: matches any modifiers
	XKNoMod  = 0xFFFF // sentinel: matches no modifiers

	// misc editing keys
	XKBackSpace = 0xFF08
	XKTab       = 0xFF09
	XKReturn    = 0xFF0D
	XKEscape    = 0xFF1B
	XKDelete    = 0xFFFF
	XKInsert    = 0xFF63
	XKHome      = 0xFF50
	XKEnd       = 0xFF57
	XKPrior     = 0xFF55
	XKNext      = 0xFF56
	XKPrint     = 0xFF61
	XKSys_Req   = 0xFF15
	XKBreak     = 0xFF6B
	XKNumLock   = 0xFF7F
	XKScrollLock = 0xFF14

	XKISOLeftTab = 0xFE20

	// arrows
	XKLeft  = 0xFF51
	XKUp    = 0xFF52
	XKRight = 0xFF53
	XKDown  = 0xFF54

	// keypad
	XKKP0      = 0xFFB0
	XKKP1      = 0xFFB1
	XKKP2      = 0xFFB2
	XKKP3      = 0xFFB3
	XKKP4      = 0xFFB4
	XKKP5      = 0xFFB5
	XKKP6      = 0xFFB6
	XKKP7      = 0xFFB7
	XKKP8      = 0xFFB8
	XKKP9      = 0xFFB9
	XKKPDecimal = 0xFFAE
	XKKPSeparator = 0xFFAC
	XKKPMultiply = 0xFFAA
	XKKPAdd     = 0xFFAB
	XKKPSubtract = 0xFFAD
	XKKPDivide  = 0xFFAF
	XKKPEnter   = 0xFF8D
	XKKPHome    = 0xFF95
	XKKPUp      = 0xFF97
	XKKPDown    = 0xFF99
	XKKPPrior   = 0xFF9A
	XKKPLeft    = 0xFF96
	XKKPBegin   = 0xFF9D
	XKKPRight   = 0xFF98
	XKKPNext    = 0xFF9B
	XKKPEnd     = 0xFF9C
	XKKPInsert  = 0xFF9E
	XKKPDelete  = 0xFF9F

	// function keys
	XKF1  = 0xFFBE
	XKF2  = 0xFFBF
	XKF3  = 0xFFC0
	XKF4  = 0xFFC1
	XKF5  = 0xFFC2
	XKF6  = 0xFFC3
	XKF7  = 0xFFC4
	XKF8  = 0xFFC5
	XKF9  = 0xFFC6
	XKF10 = 0xFFC7
	XKF11 = 0xFFC8
	XKF12 = 0xFFC9
	XKF13 = 0xFFCA
	XKF14 = 0xFFCB
	XKF15 = 0xFFCC
	XKF16 = 0xFFCD
	XKF17 = 0xFFCE
	XKF18 = 0xFFCF
	XKF19 = 0xFFD0
	XKF20 = 0xFFD1
	XKF21 = 0xFFD2
	XKF22 = 0xFFD3
	XKF23 = 0xFFD4
	XKF24 = 0xFFD5
	XKF25 = 0xFFD6
	XKF26 = 0xFFD7
	XKF27 = 0xFFD8
	XKF28 = 0xFFD9
	XKF29 = 0xFFDA
	XKF30 = 0xFFDB
	XKF31 = 0xFFDC
	XKF32 = 0xFFDD
	XKF33 = 0xFFDE
	XKF34 = 0xFFDF
	XKF35 = 0xFFE0
)

var keysymByName = map[string]uint{
	"BackSpace": XKBackSpace, "Tab": XKTab, "Return": XKReturn,
	"Escape": XKEscape, "Delete": XKDelete, "Insert": XKInsert,
	"Home": XKHome, "End": XKEnd, "Prior": XKPrior, "Next": XKNext,
	"Print": XKPrint, "Sys_Req": XKSys_Req, "Break": XKBreak,
	"Num_Lock": XKNumLock, "Scroll_Lock": XKScrollLock,
	"ISO_Left_Tab": XKISOLeftTab,
	"Left": XKLeft, "Up": XKUp, "Right": XKRight, "Down": XKDown,
	"KP_0": XKKP0, "KP_1": XKKP1, "KP_2": XKKP2, "KP_3": XKKP3,
	"KP_4": XKKP4, "KP_5": XKKP5, "KP_6": XKKP6, "KP_7": XKKP7,
	"KP_8": XKKP8, "KP_9": XKKP9,
	"KP_Decimal": XKKPDecimal, "KP_Separator": XKKPSeparator,
	"KP_Multiply": XKKPMultiply, "KP_Add": XKKPAdd,
	"KP_Subtract": XKKPSubtract, "KP_Divide": XKKPDivide,
	"KP_Enter": XKKPEnter,
	"KP_Home": XKKPHome, "KP_Up": XKKPUp, "KP_Down": XKKPDown,
	"KP_Prior": XKKPPrior,
	"KP_Left": XKKPLeft, "KP_Begin": XKKPBegin, "KP_Right": XKKPRight,
	"KP_Next": XKKPNext, "KP_End": XKKPEnd,
	"KP_Insert": XKKPInsert, "KP_Delete": XKKPDelete,
}

func init() {
	for i := 0; i < 35; i++ {
		keysymByName["F"+itoa(i+1)] = uint(0xFFBE + i)
	}
	// latin-1 letters
	for c := byte(' '); c <= 0x7e; c++ {
		keysymByName[string(c)] = uint(c)
	}
	// "C", "V", "Y" etc. already covered above
}
