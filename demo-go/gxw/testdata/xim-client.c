/* Real Xlib consumer: exercises discovery, property transport, negotiation,
 * focus, forwarding, async candidate selection, COMPOUND_TEXT and reset. */
#include <X11/Xlib.h>
#include <X11/Xutil.h>
#include <X11/keysym.h>
#include <X11/extensions/XTest.h>
#include <locale.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

int main(void) {
    alarm(15);
    setlocale(LC_ALL, "");
    XSetLocaleModifiers("");
    Display *d = XOpenDisplay(NULL);
    if (!d) return 2;
    XIM im = XOpenIM(d, NULL, NULL, NULL);
    if (!im) { fprintf(stderr,"XOpenIM failed\n"); return 3; }
    XIMStyles *styles = NULL;
    if (XGetIMValues(im, XNQueryInputStyle, &styles, NULL) || !styles) return 4;
    XFree(styles);
    Window w = XCreateSimpleWindow(d, DefaultRootWindow(d), 0, 0, 300, 100, 0, 0, 0);
    XStoreName(d,w,"XIM test");
    XSelectInput(d,w,KeyPressMask|KeyReleaseMask|StructureNotifyMask);
    XIC ic = XCreateIC(im, XNInputStyle, XIMPreeditNothing|XIMStatusNothing,
                      XNClientWindow,w,XNFocusWindow,w,NULL);
    if (!ic) {fprintf(stderr,"XCreateIC failed\n");return 5;}
    XMapWindow(d,w);
    for (;;) { XEvent e;XNextEvent(d,&e);if(e.type==MapNotify)break; }
    XSetInputFocus(d,w,RevertToParent,CurrentTime);
    XSetICFocus(ic);
    const char *input="a[b[[c[[[ni1]]]\r[[[ABC\033[\001Z";
    for (const char *p=input;*p;p++) {
        if (*p=='\001') {
            /* The WM's Alt+n must still work while it holds a bracket. */
            KeyCode alt=XKeysymToKeycode(d,XK_Alt_L),n=XKeysymToKeycode(d,XK_n);
            XTestFakeKeyEvent(d,alt,True,0);
            XTestFakeKeyEvent(d,n,True,0);XTestFakeKeyEvent(d,n,False,0);
            XTestFakeKeyEvent(d,alt,False,0);
            continue;
        }
        int shift=*p>='A' && *p<='Z';
        KeyCode sh=XKeysymToKeycode(d,XK_Shift_L);
        KeySym sym=shift?*p+32:*p;
        if(*p=='\r')sym=XK_Return;
        if(*p=='\033')sym=XK_Escape;
        KeyCode key=XKeysymToKeycode(d,sym);
        if(shift)XTestFakeKeyEvent(d,sh,True,0);
        XTestFakeKeyEvent(d,key,True,0);XTestFakeKeyEvent(d,key,False,0);
        if(shift)XTestFakeKeyEvent(d,sh,False,0);
    }
    XFlush(d);
    char result[4096]={0};size_t used=0;
    for (;;) {
        XEvent e;XNextEvent(d,&e);
        if(XFilterEvent(&e,None)||e.type!=KeyPress)continue;
        char text[512];KeySym sym;Status status;
        int n=Xutf8LookupString(ic,&e.xkey,text,sizeof(text),&sym,&status);
        if(n>0 && n<(int)sizeof(text) && used+n<sizeof(result)) {
            memcpy(result+used,text,n);used+=n;result[used]=0;
            if(strchr(result,'Z'))break;
        }
    }
    char *reset=Xutf8ResetIC(ic);if(reset)XFree(reset);
    XUnsetICFocus(ic);XDestroyIC(ic);XCloseIM(im);XDestroyWindow(d,w);XCloseDisplay(d);
    puts(result);
    return strcmp(result,"a[b[[c你\rABC[Z") ? 6 : 0;
}
