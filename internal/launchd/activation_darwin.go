//go:build darwin && cgo

package launchd

/*
#include <stdlib.h>
#include <launch.h>
*/
import "C"

import (
	"fmt"
	"net"
	"os"
	"unsafe"
)

func ActivatedListeners(name string) ([]net.Listener, error) {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	var fds *C.int
	var count C.size_t
	if rc := C.launch_activate_socket(cName, &fds, &count); rc != 0 {
		return nil, fmt.Errorf("launch_activate_socket(%s): %d", name, int(rc))
	}
	defer C.free(unsafe.Pointer(fds))
	listeners := make([]net.Listener, 0, int(count))
	fdSlice := unsafe.Slice(fds, int(count))
	for i, fd := range fdSlice {
		file := os.NewFile(uintptr(fd), fmt.Sprintf("%s-%d", name, i))
		ln, err := net.FileListener(file)
		_ = file.Close()
		if err != nil {
			for _, existing := range listeners {
				_ = existing.Close()
			}
			return nil, err
		}
		listeners = append(listeners, ln)
	}
	return listeners, nil
}
