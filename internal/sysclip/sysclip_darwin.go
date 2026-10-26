package sysclip

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>
#include <stdlib.h>
#include <string.h>

static long cf_change_count(void) {
	return (long)[[NSPasteboard generalPasteboard] changeCount];
}

// Returns a malloc'd UTF-8 copy, or NULL when there is no text. Wrapped in an
// autorelease pool because this runs on a Go thread, not the main run loop.
static char *cf_text(void) {
	@autoreleasepool {
		NSString *s = [[NSPasteboard generalPasteboard] stringForType:NSPasteboardTypeString];
		if (s == nil) return NULL;
		const char *u = [s UTF8String];
		return u ? strdup(u) : NULL;
	}
}

static int cf_set_text(const char *u) {
	@autoreleasepool {
		NSPasteboard *pb = [NSPasteboard generalPasteboard];
		[pb clearContents];
		if (u[0] == 0) return 1;
		return [pb setString:[NSString stringWithUTF8String:u] forType:NSPasteboardTypeString] ? 1 : 0;
	}
}

// http://nspasteboard.org: password managers add these marker types.
static int cf_concealed(void) {
	@autoreleasepool {
		NSArray *types = [[NSPasteboard generalPasteboard] types];
		return [types containsObject:@"org.nspasteboard.ConcealedType"] ||
		       [types containsObject:@"org.nspasteboard.TransientType"] ||
		       [types containsObject:@"com.agilebits.onepassword"];
	}
}
*/
import "C"

import (
	"errors"
	"unsafe"
)

func changeCount() int64 { return int64(C.cf_change_count()) }

func text() (string, error) {
	p := C.cf_text()
	if p == nil {
		return "", nil
	}
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p), nil
}

func setText(s string) error {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	if C.cf_set_text(cs) == 0 {
		return errors.New("sysclip: could not write to the clipboard")
	}
	return nil
}

func concealed() bool { return C.cf_concealed() != 0 }
