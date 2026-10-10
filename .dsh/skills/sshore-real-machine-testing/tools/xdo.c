// 极简 X11 输入驱动（XTEST），替代 xdotool：list/find/click/dclick/key/type/drag
#include <X11/Xlib.h>
#include <X11/Xutil.h>
#include <X11/keysym.h>
#include <X11/extensions/XTest.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static Display *d;

static void walk(Window w, const char *sub, int listall) {
    Window root, parent, *kids = NULL; unsigned int n = 0;
    char *name = NULL; XClassHint ch;
    if (!XQueryTree(d, w, &root, &parent, &kids, &n)) return;
    for (unsigned int i = 0; i < n; i++) {
        XWindowAttributes a;
        if (XGetWindowAttributes(d, kids[i], &a) && a.map_state == IsViewable) {
            name = NULL; XFetchName(d, kids[i], &name);
            memset(&ch, 0, sizeof ch); XGetClassHint(d, kids[i], &ch);
            const char *cn = ch.res_class ? ch.res_class : "";
            const char *nm = name ? name : "";
            int hit = sub ? (strstr(nm, sub) || strstr(cn, sub)) : 1;
            if (hit) printf("0x%lx\t%s\t%s\t%d,%d\t%dx%d\n", kids[i], cn, nm, a.x, a.y, a.width, a.height);
            if (name) XFree(name);
            if (ch.res_class) XFree(ch.res_class);
        }
        walk(kids[i], sub, listall);
    }
    if (kids) XFree(kids);
}
static void click(int x, int y, int btn, int dbl) {
    XTestFakeMotionEvent(d, -1, x, y, 0); XFlush(d); usleep(60000);
    for (int i = 0; i < (dbl ? 2 : 1); i++) {
        XTestFakeButtonEvent(d, btn, True, 0); XFlush(d); usleep(30000);
        XTestFakeButtonEvent(d, btn, False, 0); XFlush(d);
        if (dbl) usleep(40000);
    }
    XFlush(d); usleep(80000);
}
static void keyname(const char *ks, int shift) {
    KeySym k = XStringToKeysym(ks);
    if (k == NoSymbol) { fprintf(stderr, "bad keysym %s\n", ks); return; }
    KeyCode kc = XKeysymToKeycode(d, k);
    KeyCode sh = XKeysymToKeycode(d, XK_Shift_L);
    if (shift) XTestFakeKeyEvent(d, sh, True, 0);
    XTestFakeKeyEvent(d, kc, True, 0); XFlush(d); usleep(15000);
    XTestFakeKeyEvent(d, kc, False, 0);
    if (shift) XTestFakeKeyEvent(d, sh, False, 0);
    XFlush(d); usleep(25000);
}
static void typetext(const char *s) {
    for (const char *p = s; *p; p++) {
        char nm[2] = {*p, 0}; int shift = 0; const char *ks = nm;
        if (*p >= 'A' && *p <= 'Z') { shift = 1; }
        else if (*p == ':') ks = "colon"; else if (*p == '/') ks = "slash";
        else if (*p == '.') ks = "period"; else if (*p == '-') ks = "minus";
        else if (*p == '_') { ks = "minus"; shift = 1; }
        else if (*p == '$') { ks = "dollar"; } else if (*p == ' ') ks = "space";
        keyname(ks, shift);
    }
}
int main(int argc, char **argv) {
    if (!(d = XOpenDisplay(NULL))) { fprintf(stderr, "no display\n"); return 2; }
    const char *cmd = argc > 1 ? argv[1] : "list";
    if (!strcmp(cmd, "list")) walk(DefaultRootWindow(d), argc > 2 ? argv[2] : NULL, 1);
    else if (!strcmp(cmd, "click")) click(atoi(argv[2]), atoi(argv[3]), argc > 4 ? atoi(argv[4]) : 1, 0);
    else if (!strcmp(cmd, "dclick")) click(atoi(argv[2]), atoi(argv[3]), 1, 1);
    else if (!strcmp(cmd, "rclick")) click(atoi(argv[2]), atoi(argv[3]), 3, 0);
    else if (!strcmp(cmd, "hotkey")) {
        const char *m = argv[2], *k = argv[3];
        if (!strcmp(m, "ctrl")) m = "Control_L"; else if (!strcmp(m, "shift")) m = "Shift_L"; else if (!strcmp(m, "alt")) m = "Alt_L";
        KeyCode mk = XKeysymToKeycode(d, XStringToKeysym(m));
        KeyCode kk = XKeysymToKeycode(d, XStringToKeysym(k));
        XTestFakeKeyEvent(d, mk, True, 0); XTestFakeKeyEvent(d, kk, True, 0); XFlush(d); usleep(25000);
        XTestFakeKeyEvent(d, kk, False, 0); XTestFakeKeyEvent(d, mk, False, 0); XFlush(d); usleep(30000);
    }
    else if (!strcmp(cmd, "focus")) { Window w = strtoul(argv[2], NULL, 0); XSetInputFocus(d, w, RevertToParent, CurrentTime); XRaiseWindow(d, w); XFlush(d); }
    else if (!strcmp(cmd, "slowclick")) {  /* slowclick <x> <y>：按下 → 250ms → 抬起 */
        XTestFakeMotionEvent(d, -1, atoi(argv[2]), atoi(argv[3]), 0); XFlush(d); usleep(80000);
        XTestFakeButtonEvent(d, 1, True, 0); XFlush(d); usleep(250000);
        XTestFakeButtonEvent(d, 1, False, 0); XFlush(d); usleep(80000);
    }
    else if (!strcmp(cmd, "clickmod")) {   /* clickmod <shift|ctrl> <x> <y> */
        const char *m = argv[2];
        if (!strcmp(m, "shift")) m = "Shift_L"; else if (!strcmp(m, "ctrl")) m = "Control_L";
        KeyCode mk = XKeysymToKeycode(d, XStringToKeysym(m));
        XTestFakeMotionEvent(d, -1, atoi(argv[3]), atoi(argv[4]), 0); XFlush(d); usleep(60000);
        XTestFakeKeyEvent(d, mk, True, 0); XFlush(d); usleep(20000);
        XTestFakeButtonEvent(d, 1, True, 0); XFlush(d); usleep(30000);
        XTestFakeButtonEvent(d, 1, False, 0); XFlush(d); usleep(20000);
        XTestFakeKeyEvent(d, mk, False, 0); XFlush(d); usleep(60000);
    }
    else if (!strcmp(cmd, "scroll")) {
        int x = atoi(argv[2]), y = atoi(argv[3]), up = atoi(argv[4]), n = atoi(argv[5]);
        XTestFakeMotionEvent(d, -1, x, y, 0); XFlush(d); usleep(60000);
        for (int i = 0; i < n; i++) {
            XTestFakeButtonEvent(d, up ? 4 : 5, True, 0); XFlush(d); usleep(15000);
            XTestFakeButtonEvent(d, up ? 4 : 5, False, 0); XFlush(d); usleep(45000);
        }
    }
    else if (!strcmp(cmd, "movewin")) { Window w = strtoul(argv[2], NULL, 0); XMoveResizeWindow(d, w, atoi(argv[3]), atoi(argv[4]), atoi(argv[5]), atoi(argv[6])); XFlush(d); }
    else if (!strcmp(cmd, "move")) { XTestFakeMotionEvent(d, -1, atoi(argv[2]), atoi(argv[3]), 0); XFlush(d); }
    else if (!strcmp(cmd, "key")) keyname(argv[2], argc > 3 ? atoi(argv[3]) : 0);
    else if (!strcmp(cmd, "type")) typetext(argv[2]);
    else if (!strcmp(cmd, "drag")) {
        XTestFakeMotionEvent(d, -1, atoi(argv[2]), atoi(argv[3]), 0); XFlush(d); usleep(100000);
        XTestFakeButtonEvent(d, 1, True, 0); XFlush(d); usleep(100000);
        for (int i = 0; i < 12; i++) { int x = atoi(argv[2]) + (atoi(argv[4])-atoi(argv[2]))*i/11; int y = atoi(argv[3]) + (atoi(argv[5])-atoi(argv[3]))*i/11; XTestFakeMotionEvent(d, -1, x, y, 0); XFlush(d); usleep(40000); }
        XTestFakeButtonEvent(d, 1, False, 0); XFlush(d); usleep(150000);
    }
    else { fprintf(stderr, "usage: xdo list|click|dclick|rclick|move|key|type|drag\n"); return 1; }
    XFlush(d); usleep(50000); XCloseDisplay(d); return 0;
}
